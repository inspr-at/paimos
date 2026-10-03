// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-280: every message to an agent is delivered, or its sender is told
// loudly that it was not. A queued receipt carries deliver_by (migration 0926);
// past it, the sweeper fails the message. Ending a session fails its
// undelivered session-bound messages at once. A failure is terminal: the
// delivery goes dead, the receipt fails, the message leaves every read path and
// one system notice reaches the sender (plus the recipient session's
// coordinator). A late acknowledgement cannot move a failed receipt back.

// Failure reasons written by the delivery guarantee. Adapter reasons written by
// the routine dispatcher (http_error, transport_error, ...) remain valid too.
const (
	ReasonDeadline     = "deadline"
	ReasonAttempts     = "attempts"
	ReasonSessionEnded = "session_ended"
	ReasonNoListener   = "no_listener"
	// Managed hand-off failures (AEON-282), mapped by managedFailureReason.
	ReasonUnavailable    = "unavailable"
	ReasonTransportError = "transport_error"
)

// Seen channels recorded on a harness session when it pulls its inbox.
const (
	SeenHook     = "hook"
	SeenDrain    = "drain"
	SeenLongPoll = "long_poll"
	SeenStream   = "stream"
	SeenAck      = "ack"
)

// ErrNotDelivered answers an acknowledgement or completion of a message whose
// delivery already failed.
var ErrNotDelivered = &httpError{409, "not_delivered", "This message was not delivered."}

// receiptFailed reports a terminal failure. Call it under the message row lock.
func receiptFailed(ctx context.Context, tx pgx.Tx, messageID string) (bool, error) {
	var failed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_receipts WHERE message_id=$1::uuid AND state='failed')`, messageID).Scan(&failed)
	return failed, err
}

// MessageNotDelivered is receiptFailed for the harness: true when the message's
// delivery failed and it must not be acknowledged or completed.
func MessageNotDelivered(ctx context.Context, tx pgx.Tx, messageID string) (bool, error) {
	return receiptFailed(ctx, tx, messageID)
}

// DeliverySettings are the tenant's deadlines and adapter attempt cap.
type DeliverySettings struct {
	SessionDeadlineSeconds int `json:"session_deadline_seconds"`
	UnboundDeadlineSeconds int `json:"unbound_deadline_seconds"`
	MaxAttempts            int `json:"max_attempts"`
}

// DefaultDeliverySettings apply until a tenant chooses otherwise.
var DefaultDeliverySettings = DeliverySettings{SessionDeadlineSeconds: 300, UnboundDeadlineSeconds: 1800, MaxAttempts: 8}

func loadDeliverySettings(ctx context.Context, tx pgx.Tx) (DeliverySettings, error) {
	s := DefaultDeliverySettings
	err := tx.QueryRow(ctx, `SELECT session_deadline_seconds,unbound_deadline_seconds,max_attempts FROM inbox_delivery_settings`).Scan(&s.SessionDeadlineSeconds, &s.UnboundDeadlineSeconds, &s.MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultDeliverySettings, nil
	}
	return s, err
}

// receiptDeadlineSQL computes deliver_by for message $2. Only agents have a
// delivery path to guarantee; a person reads messages in the app, so a message
// to a person has no deadline. A sender's expires_at shortens the deadline.
const receiptDeadlineSQL = `(SELECT CASE WHEN rp.kind<>'agent' THEN NULL ELSE LEAST(m.expires_at, m.created_at + make_interval(secs => CASE WHEN m.recipient_session_id IS NOT NULL THEN coalesce(s.session_deadline_seconds,300) ELSE coalesce(s.unbound_deadline_seconds,1800) END)) END
 FROM inbox_messages m JOIN principals rp ON rp.tenant_id=m.tenant_id AND rp.id=m.recipient_principal_id
 LEFT JOIN inbox_delivery_settings s ON s.tenant_id=m.tenant_id WHERE m.id=$2::uuid)`

// systemActor returns the tenant's System principal, creating it on first use.
func systemActor(ctx context.Context, tx pgx.Tx, tenantID string) (tenant.Principal, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT aeon_authz_system_actor($1::uuid)::text`, tenantID).Scan(&id); err != nil {
		return tenant.Principal{}, err
	}
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Agent, Name: "System"}, nil
}

// systemEvent appends one System-authored inbox event through the narrow SQL
// path of migration 0926. Unlike events.Append it works inside a caller that
// sees only some projects: such a caller could not read a node-less System
// event back, and the INSERT ... RETURNING would roll its transaction back.
func systemEvent(ctx context.Context, tx pgx.Tx, tenantID, kind string, after any) (int64, error) {
	raw, err := json.Marshal(after)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT aeon_inbox_system_event($1::uuid,$2,$3::jsonb)`, tenantID, kind, raw).Scan(&id)
	return id, err
}

// MarkSessionSeen records that this generation pulled its inbox. Writes are
// throttled to one per five seconds per channel so long polls and streams do
// not rewrite the row on every wake. An acknowledgement is not a pull: it can
// fill inbox_seen_via only when nothing has pulled yet, and on a later beat it
// refreshes inbox_seen_at without replacing the pull path. A hook pull therefore
// wins over the ack that follows it on the same beat (AEON-307).
func MarkSessionSeen(ctx context.Context, tx pgx.Tx, sessionID, via string) error {
	if sessionID == "" || via == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE harness_sessions SET
 inbox_seen_at=clock_timestamp(),
 inbox_seen_via=CASE WHEN $2='ack' AND inbox_seen_via IN ('hook','drain','long_poll','stream') THEN inbox_seen_via ELSE $2 END
 WHERE id=$1::uuid AND (
  inbox_seen_at IS NULL
  OR inbox_seen_at<clock_timestamp()-interval '5 seconds'
  OR ($2='ack' AND inbox_seen_via IN ('hook','drain','long_poll','stream')) IS NOT TRUE AND inbox_seen_via IS DISTINCT FROM $2
 )`, sessionID, via)
	return err
}

// Locking protocol (AEON-280), the same on every path that reads, hands over,
// acknowledges, completes or fails a message: the bound harness_sessions row
// first (close takes FOR UPDATE, listening paths FOR NO KEY UPDATE, failure FOR
// KEY SHARE), then the inbox_messages row, then its delivery
// (inbox_message_deliveries or harness_deliveries), then the receipt. A failure sets expires_at under that row lock, so a reader that
// locks and re-checks the row can never hand over a failed message.

// handOver is the read side of that protocol. It locks the candidate rows in id
// order, keeps only those still deliverable to p (unacknowledged, not expired,
// not failed), stamps a first hand-over (Delivered to the sender) with one
// content-free inbox.message_fetched event each, and returns the survivors.
func handOver(ctx context.Context, tx pgx.Tx, p tenant.Principal, via string, ids []string) (map[string]bool, error) {
	alive := map[string]bool{}
	if len(ids) == 0 {
		return alive, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text,sender_principal_id::text,fetched_at IS NULL FROM inbox_messages
 WHERE content_mode='durable' AND id=ANY($1::uuid[]) AND recipient_principal_id=$2::uuid AND acked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())
 ORDER BY id FOR NO KEY UPDATE`, ids, p.ID)
	if err != nil {
		return nil, err
	}
	type fetched struct{ id, sender string }
	var first []fetched
	for rows.Next() {
		var f fetched
		var fresh bool
		if err := rows.Scan(&f.id, &f.sender, &fresh); err != nil {
			rows.Close()
			return nil, err
		}
		alive[f.id] = true
		if fresh {
			first = append(first, f)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, f := range first {
		if _, err := tx.Exec(ctx, `UPDATE inbox_messages SET fetched_at=clock_timestamp() WHERE id=$1::uuid AND fetched_at IS NULL`, f.id); err != nil {
			return nil, err
		}
		after := map[string]any{"message_id": f.id, "sender_principal_id": f.sender, "recipient_principal_id": p.ID}
		if via != "" {
			after["via"] = via
		}
		if _, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.message_fetched", After: after}); err != nil {
			return nil, err
		}
	}
	return alive, nil
}

// MarkFetched hands messages to their recipient p after the caller locked them
// (the harness drain); see handOver.
func MarkFetched(ctx context.Context, tx pgx.Tx, p tenant.Principal, via string, ids ...string) error {
	_, err := handOver(ctx, tx, p, via, ids)
	return err
}

// FailSessionMessages fails every undelivered message bound to this ended
// generation with session_ended. The harness calls it in the transaction that
// stops or archives the session, before it releases the generation's leases.
// Messages accepted before deadlines existed keep their legacy behaviour.
func FailSessionMessages(ctx context.Context, tx pgx.Tx, tenantID, sessionID string) error {
	rows, err := tx.Query(ctx, `SELECT m.id::text FROM inbox_messages m JOIN inbox_receipts r ON r.tenant_id=m.tenant_id AND r.message_id=m.id
 WHERE m.recipient_session_id=$1::uuid AND m.acked_at IS NULL AND r.state='queued' AND r.deliver_by IS NOT NULL ORDER BY m.id`, sessionID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := failMessage(ctx, tx, id, ReasonSessionEnded, false); err != nil {
			return err
		}
	}
	return nil
}

// FailMessage is the one terminal "not delivered" transition, for every caller
// (the sweeper, session end, the attempt cap, managed delivery that gives up).
// In the caller's transaction it locks the message, and when it is still
// unacknowledged with a queued receipt that has a deadline it: marks the
// delivery dead with reason, fails the receipt, hides the message from every
// read path, releases drain leases, appends inbox.receipt_failed and
// inbox.delivery_failed (System), and writes the System notice to the sender
// plus the coordinator copy. It returns false and changes nothing otherwise
// (already terminal, acknowledged, or a legacy message without a deadline),
// so every caller is idempotent. The caller must not set acked_at first.
// reason is deadline, attempts, session_ended, no_listener or an adapter
// reason allowed by inbox_message_deliveries_reason_check.
func FailMessage(ctx context.Context, tx pgx.Tx, messageID, reason string) (bool, error) {
	return failMessage(ctx, tx, messageID, reason, false)
}

type failingMessage struct {
	tenant, id, sender, recipient, recipientName string
	recipientSession, senderSession              *string
	sessionLabel                                 *string
	parentSession, parentPrincipal               *string
	body                                         string
	createdAt                                    time.Time
}

// liveLeaseSQL: an adapter or drain holds the message right now and may have
// injected it already. The sweeper waits; the lease expiry is its deadline.
const liveLeaseSQL = `SELECT EXISTS(SELECT 1 FROM inbox_message_deliveries d JOIN inbox_compat_messages c ON c.tenant_id=d.tenant_id AND c.id=d.message_id
 WHERE c.inbox_message_id=$1::uuid AND d.state='pending' AND d.lease_until>clock_timestamp())
 OR EXISTS(SELECT 1 FROM harness_deliveries h WHERE h.message_id=$1::uuid AND h.completed_at IS NULL AND h.released_at IS NULL AND h.leased_at>clock_timestamp()-interval '2 minutes')`

func failMessage(ctx context.Context, tx pgx.Tx, messageID, reason string, respectLease bool) (bool, error) {
	// Lock order (AEON-280): the bound session before the message. KEY SHARE
	// waits only for a close (FOR UPDATE) and never blocks a pull or an ack.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM harness_sessions WHERE id=(SELECT recipient_session_id FROM inbox_messages WHERE id=$1::uuid) FOR KEY SHARE`, messageID); err != nil {
		return false, err
	}
	var f failingMessage
	var acked *time.Time
	// Only the coordinator of the recipient's own project counts as its parent.
	err := tx.QueryRow(ctx, `SELECT m.tenant_id::text,m.id::text,m.sender_principal_id::text,m.recipient_principal_id::text,rp.name,m.recipient_session_id::text,m.sender_session_id::text,rs.display_label,ps.id::text,ps.agent_principal_id::text,m.body,m.created_at,m.acked_at
 FROM inbox_messages m JOIN principals rp ON rp.tenant_id=m.tenant_id AND rp.id=m.recipient_principal_id
 LEFT JOIN harness_sessions rs ON rs.tenant_id=m.tenant_id AND rs.id=m.recipient_session_id
 LEFT JOIN harness_sessions ps ON ps.tenant_id=rs.tenant_id AND ps.id=rs.parent_id AND ps.role='coordinator' AND ps.project_id=rs.project_id
 WHERE m.id=$1::uuid AND m.content_mode='durable' FOR UPDATE OF m`, messageID).Scan(&f.tenant, &f.id, &f.sender, &f.recipient, &f.recipientName, &f.recipientSession, &f.senderSession, &f.sessionLabel, &f.parentSession, &f.parentPrincipal, &f.body, &f.createdAt, &acked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || acked != nil {
		return false, err
	}
	var state string
	var deadline *time.Time
	err = tx.QueryRow(ctx, `SELECT state,deliver_by FROM inbox_receipts WHERE message_id=$1::uuid`, messageID).Scan(&state, &deadline)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (state != "queued" || deadline == nil)) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if respectLease {
		var leased bool
		if err := tx.QueryRow(ctx, liveLeaseSQL, messageID).Scan(&leased); err != nil || leased {
			return false, err
		}
	}
	var deliveryID *string
	err = tx.QueryRow(ctx, `UPDATE inbox_message_deliveries d SET state='dead',reason=$2,lease_token=NULL,lease_until=NULL
 FROM inbox_compat_messages c WHERE c.tenant_id=d.tenant_id AND c.id=d.message_id AND c.inbox_message_id=$1::uuid AND d.state IN ('pending','blocked')
 RETURNING d.id::text`, messageID, reason).Scan(&deliveryID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if deliveryID == nil {
		// Already dead (the routine dispatcher's terminal failure) or no delivery row.
		_ = tx.QueryRow(ctx, `SELECT d.id::text FROM inbox_message_deliveries d JOIN inbox_compat_messages c ON c.tenant_id=d.tenant_id AND c.id=d.message_id WHERE c.inbox_message_id=$1::uuid`, messageID).Scan(&deliveryID)
	}
	if _, err := tx.Exec(ctx, `UPDATE harness_deliveries SET released_at=clock_timestamp() WHERE message_id=$1::uuid AND completed_at IS NULL AND released_at IS NULL`, messageID); err != nil {
		return false, err
	}
	// Leave every read path: a message the sender was told failed must not
	// surface later as a surprise duplicate.
	if _, err := tx.Exec(ctx, `UPDATE inbox_messages SET expires_at=LEAST(coalesce(expires_at,clock_timestamp()),clock_timestamp()) WHERE id=$1::uuid`, messageID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inbox_receipts SET state='failed',failure_reason=$2,handed_off_at=NULL WHERE message_id=$1::uuid AND state='queued'`, messageID, reason); err != nil {
		return false, err
	}
	if _, err := systemEvent(ctx, tx, f.tenant, "inbox.receipt_failed", map[string]any{"message_id": messageID, "state": "failed", "failure_reason": reason}); err != nil {
		return false, err
	}
	after := map[string]any{"message_id": messageID, "reason": reason, "sender_principal_id": f.sender, "recipient_principal_id": f.recipient}
	if deliveryID != nil {
		after["delivery_id"] = *deliveryID
	}
	if f.recipientSession != nil {
		after["recipient_session_id"] = *f.recipientSession
	}
	if _, err := systemEvent(ctx, tx, f.tenant, "inbox.delivery_failed", after); err != nil {
		return false, err
	}
	sys, err := systemActor(ctx, tx, f.tenant)
	if err != nil {
		return false, err
	}
	return true, notifyFailure(ctx, tx, sys, f, reason)
}

// notifyFailure writes one durable system message to the sender, and a copy to
// the principal of the recipient session's coordinator. Both are keyed by the
// failed message, so a replay writes nothing new. Notices have no receipt and
// no deadline: a notice can never fail into another notice. The copy quotes the
// message only when its reader could already read it (the coordinator runs as
// the recipient principal); otherwise it says that, not what, was lost, so a
// private session-bound message never leaks.
func notifyFailure(ctx context.Context, tx pgx.Tx, sys tenant.Principal, f failingMessage, reason string) error {
	if f.sender == sys.ID {
		return nil
	}
	target := f.recipientName
	if f.sessionLabel != nil && strings.TrimSpace(*f.sessionLabel) != "" {
		target = *f.sessionLabel
	}
	why := failureText(reason)
	excerpt := excerptOf(f.body, 160)
	sent := f.createdAt.UTC().Format("15:04 UTC")
	body := fmt.Sprintf("Not delivered: your message to %s was not delivered (%s). Sent %s, message %s. It will not be delivered later; send it again when the recipient is listening.\n\n> %s", target, why, sent, f.id, excerpt)
	if err := postNotice(ctx, tx, sys, f.sender, f.senderSession, "delivery-failed/"+f.id, body); err != nil {
		return err
	}
	if f.parentPrincipal == nil || *f.parentPrincipal == f.sender {
		return nil
	}
	var senderName string
	if err := tx.QueryRow(ctx, `SELECT name FROM principals WHERE id=$1::uuid`, f.sender).Scan(&senderName); err != nil {
		return err
	}
	copyBody := fmt.Sprintf("Not delivered to your worker %s: a message from %s (%s). Sent %s, message %s.", target, senderName, why, sent, f.id)
	if *f.parentPrincipal == f.recipient {
		copyBody += "\n\n> " + excerpt
	}
	return postNotice(ctx, tx, sys, *f.parentPrincipal, f.parentSession, "delivery-failed/"+f.id+"/coordinator", copyBody)
}

func postNotice(ctx context.Context, tx pgx.Tx, sys tenant.Principal, recipient string, session *string, key, body string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE sender_principal_id=$1::uuid AND idempotency_key=$2)`, sys.ID, key).Scan(&exists); err != nil || exists {
		return err
	}
	// Bind the notice to that session only while it runs; otherwise it waits in
	// the principal inbox for the next generation.
	var bound *string
	if session != nil {
		var active bool
		err := tx.QueryRow(ctx, `SELECT stopped_at IS NULL AND archived_at IS NULL FROM harness_sessions WHERE id=$1::uuid AND agent_principal_id=$2::uuid`, *session, recipient).Scan(&active)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if active {
			bound = session
		}
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return err
	}
	eventID, err := systemEvent(ctx, tx, sys.TenantID, "inbox.sent", messageMeta{ID: id, SenderPrincipalID: sys.ID, RecipientPrincipalID: recipient, IdempotencyKey: key})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key,recipient_session_id,sender_label) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8::uuid,'System')`, sys.TenantID, id, sys.ID, recipient, eventID, body, key, bound); err != nil {
		return err
	}
	msg := Message{ID: id, SenderPrincipalID: sys.ID, RecipientPrincipalID: recipient, SentEventID: eventID, RecipientSessionID: bound}
	return enqueueWakesWith(ctx, tx, sys.TenantID, msg, func(c events.Change) error {
		_, err := systemEvent(ctx, tx, sys.TenantID, c.Type, c.After)
		return err
	})
}

func failureText(reason string) string {
	switch reason {
	case ReasonDeadline:
		return "it was not confirmed before the delivery deadline"
	case ReasonAttempts:
		return "every delivery attempt failed"
	case ReasonSessionEnded:
		return "the session ended before it picked the message up"
	case ReasonNoListener:
		return "nothing was listening for it"
	case ReasonUnavailable:
		return "the managed session could not take it"
	case ReasonTransportError:
		return "the hand-off to the session was not confirmed"
	case "":
		return "delivery failed"
	}
	return "delivery failed: " + strings.ReplaceAll(reason, "_", " ")
}

func excerptOf(body string, max int) string {
	s := strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max])) + "…"
}

// confirmReceived is the receiver's confirmation for a message it acknowledged:
// a direct ack or a harness drain completion. Plain messages record handed_off
// as before. A compat message is delivered too, bound or not, so an acked
// unbound compat message no longer stays queued forever; a delivery an adapter
// already completed, or that already failed, is left alone.
func confirmReceived(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID string) error {
	var compat bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_compat_messages WHERE inbox_message_id=$1::uuid)`, messageID).Scan(&compat); err != nil {
		return err
	}
	if !compat {
		return advanceReceipt(ctx, tx, p, messageID, "handed_off", "", "", receiptTarget{})
	}
	if _, err := tx.Exec(ctx, `UPDATE inbox_message_deliveries d SET state='delivered',reason='',effective_level='simple',lease_token=NULL,lease_until=NULL FROM inbox_compat_messages c
 WHERE c.tenant_id=d.tenant_id AND c.id=d.message_id AND c.inbox_message_id=$1::uuid AND d.state IN ('pending','blocked')`, messageID); err != nil {
		return err
	}
	return advanceReceipt(ctx, tx, p, messageID, "handed_off", "simple", "", receiptTarget{})
}
