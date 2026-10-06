// SPDX-License-Identifier: AGPL-3.0-only
package attachedmsg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const OfferBudget = 2500 * time.Millisecond

// Receipt contains no text. Epoch is captured with the offer and compared under
// the same database fence as revocation, never in a daemon check-then-settle.
type Receipt struct {
	DeliveryID string `json:"delivery_id"`
	MessageID  string `json:"message_id"`
	Nonce      string `json:"nonce"`
	Outcome    string `json:"outcome"`
	Epoch      string `json:"epoch"`
}
type Exchange struct {
	State       string     `json:"state"`
	Offer       *Offer     `json:"offer,omitempty"`
	ShownAt     *time.Time `json:"shown_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func nonceDigest(b Binding, grant, delivery, message, nonce, epoch string) string {
	raw, _ := json.Marshal(struct {
		Binding                                Binding
		Grant, Delivery, Message, Nonce, Epoch string
	}{b, grant, delivery, message, nonce, epoch})
	sum := sha256.Sum256(append([]byte("aeon.attached.offer.v1\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

func lockSession(ctx context.Context, tx pgx.Tx, b Binding) error {
	var live bool
	err := tx.QueryRow(ctx, `SELECT stopped_at IS NULL AND archived_at IS NULL AND project_id=$2::uuid
 FROM harness_sessions WHERE id=$1::uuid FOR SHARE`, b.SessionID, b.ProjectID).Scan(&live)
	if err != nil {
		return err
	}
	if !live {
		return Fail(409, "attachment_ended")
	}
	return nil
}

// Claim commits the sole attempt, not a reusable lease. The caller MUST commit
// before Release. Failure/ambiguity after this transaction never retries Claim
// for this note. Even a crash before Release will become uncertain.
func (s *Service) Claim(ctx context.Context, tx pgx.Tx, b Binding, epoch string) (*Offer, error) {
	if err := Lock(ctx, tx); err != nil {
		return nil, err
	}
	if len(epoch) == 0 || len(epoch) > 128 {
		return nil, Fail(400, "invalid_hook_epoch")
	}
	g, err := s.ValidateGrant(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE attached_message_grants SET hook_epoch=$2 WHERE id=$1::uuid AND (hook_epoch IS NULL OR hook_epoch=$2)`, g.ID, epoch)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, Fail(409, "hook_epoch_changed")
	}
	if err = lockSession(ctx, tx, b); err != nil {
		return nil, err
	}
	var inflight bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_deliveries WHERE session_id=$1::uuid AND mode='attached_hook' AND terminal_outcome IS NULL)`, b.SessionID).Scan(&inflight)
	if err != nil || inflight {
		return nil, err
	}
	o := &Offer{Binding: b, GrantID: g.ID, DeliveryID: UUID(), Epoch: epoch}
	var cursor int64
	err = tx.QueryRow(ctx, `SELECT m.id::text,m.sent_event_id,m.sender_label,m.created_at
 FROM inbox_messages m JOIN harness_sessions h ON h.tenant_id=m.tenant_id AND h.id=m.recipient_session_id
 WHERE m.content_mode='attached_volatile' AND m.recipient_session_id=$1::uuid
 AND m.recipient_principal_id=h.agent_principal_id AND h.project_id=$2::uuid
 AND m.message_grant_id=$3::uuid AND m.recipient_message_generation=$4::uuid
 AND m.payload_epoch=$5::uuid AND m.attached_outcome='queued' AND m.sender_principal_id=$6::uuid
 AND NOT EXISTS(SELECT 1 FROM harness_deliveries d WHERE d.tenant_id=m.tenant_id AND d.message_id=m.id)
 ORDER BY m.sent_event_id LIMIT 1 FOR UPDATE OF m`, b.SessionID, b.ProjectID, g.ID, b.Generation, b.ServiceEpoch, b.OwnerID).Scan(&o.MessageID, &cursor, &o.Owner, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var expired bool
	err = tx.QueryRow(ctx, `SELECT message_deadline<=clock_timestamp() FROM inbox_messages WHERE id=$1::uuid`, o.MessageID).Scan(&expired)
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, Terminalize(ctx, tx, o.MessageID, "deadline")
	}
	present, published := s.available(b, g.ID, o.MessageID)
	if !present {
		return nil, Terminalize(ctx, tx, o.MessageID, "content_lost")
	}
	// Acceptance publishes immediately after commit. A concurrent hook may see
	// the reservation in that tiny window; leave it queued for its first attempt.
	if !published {
		return nil, nil
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	o.Nonce = hex.EncodeToString(nonce[:])
	err = tx.QueryRow(ctx, `INSERT INTO harness_deliveries(tenant_id,id,session_id,message_id,cursor,mode,message_grant_id,message_generation,daemon_epoch,nonce_digest,hook_epoch,offer_deadline)
 SELECT $1,$2,$3,$4,$5,'attached_hook',$6,$7,$8,$9,$11,least(clock_timestamp()+interval '2500 milliseconds',m.message_deadline,a.lease_until)
 FROM inbox_messages m JOIN harness_attach_requests a ON a.tenant_id=m.tenant_id AND a.id=$10::uuid WHERE m.id=$4::uuid
 RETURNING offer_deadline`, b.TenantID, o.DeliveryID, b.SessionID, o.MessageID, cursor, g.ID, b.Generation, b.DaemonEpoch, nonceDigest(b, g.ID, o.DeliveryID, o.MessageID, o.Nonce, epoch), b.AttachRequestID, epoch).Scan(&o.Deadline)
	if err != nil {
		return nil, err
	}
	if err = projectOutcome(ctx, tx, o.MessageID, "offered"); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) available(b Binding, grant, id string) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.entries[b.TenantID+"/"+id]
	if p == nil || p.binding != b || p.grant != grant || !time.Now().Before(p.deadline) {
		return false, false
	}
	return true, p.published
}
func projectOutcome(ctx context.Context, tx pgx.Tx, id, state string) error {
	if _, err := tx.Exec(ctx, `UPDATE inbox_messages SET attached_outcome=$2 WHERE id=$1::uuid AND content_mode='attached_volatile'`, id, state); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE inbox_compat_messages SET attached_outcome=$2 WHERE inbox_message_id=$1::uuid AND content_mode='attached_volatile'`, id, state)
	return err
}

type attempt struct {
	grant, state, digest, receipt string
	released                      bool
	expired                       bool
	shown, completed              *time.Time
}

// attemptFor first checks the immutable association without locking a message
// ahead of its grant/session. All callers already hold the pairing fence.
func attemptFor(ctx context.Context, tx pgx.Tx, b Binding, r Receipt) (attempt, error) {
	var a attempt
	var stored Binding
	var epoch string
	var currentEpoch bool
	err := tx.QueryRow(ctx, `SELECT g.binding,d.message_grant_id::text,coalesce(d.terminal_outcome,''),d.nonce_digest,
 coalesce(d.receipt_outcome,''),d.body_released_at IS NOT NULL,d.offer_deadline<=clock_timestamp() OR m.message_deadline<=clock_timestamp(),d.shown_at,d.completed_at,d.hook_epoch,d.hook_epoch=g.hook_epoch
 FROM harness_deliveries d JOIN attached_message_grants g ON g.tenant_id=d.tenant_id AND g.id=d.message_grant_id
 JOIN inbox_messages m ON m.tenant_id=d.tenant_id AND m.id=d.message_id
 WHERE d.id=$1::uuid AND d.message_id=$2::uuid AND d.session_id=$3::uuid AND d.mode='attached_hook'
 AND d.message_generation=$4::uuid AND d.daemon_epoch=$5`, r.DeliveryID, r.MessageID, b.SessionID, b.Generation, b.DaemonEpoch).Scan(&stored, &a.grant, &a.state, &a.digest, &a.receipt, &a.released, &a.expired, &a.shown, &a.completed, &epoch, &currentEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, Fail(403, "offer_binding_mismatch")
	}
	if err != nil {
		return a, err
	}
	if stored != b || r.Epoch != epoch || !hashPattern.MatchString(r.Nonce) || subtle.ConstantTimeCompare([]byte(a.digest), []byte(nonceDigest(b, a.grant, r.DeliveryID, r.MessageID, r.Nonce, epoch))) != 1 {
		return a, Fail(403, "offer_binding_mismatch")
	}
	a.expired = a.expired || !currentEpoch
	return a, nil
}
func lockAttempt(ctx context.Context, tx pgx.Tx, b Binding, r Receipt) (bool, error) {
	// Stop uses the session row rather than the pairing fence. Check lifecycle
	// under that row lock, after waiting, so a concurrent stop cannot slip between
	// ValidateGrant and release/settlement.
	var live bool
	if err := tx.QueryRow(ctx, `SELECT stopped_at IS NULL AND archived_at IS NULL FROM harness_sessions WHERE id=$1::uuid FOR SHARE`, b.SessionID).Scan(&live); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM inbox_messages WHERE id=$1::uuid FOR UPDATE`, r.MessageID); err != nil {
		return false, err
	}
	_, err := tx.Exec(ctx, `SELECT 1 FROM harness_deliveries WHERE id=$1::uuid FOR UPDATE`, r.DeliveryID)
	return live, err
}
func offerReceipt(o *Offer) Receipt {
	return Receipt{DeliveryID: o.DeliveryID, MessageID: o.MessageID, Nonce: o.Nonce, Epoch: o.Epoch}
}

// Release is a second transaction after Claim commits. Revocation can win
// between the two; the database fence is held through the one destructive Take.
// The handler sends bytes only after this transaction also commits. A rollback
// or lost response consumes the attempt and cannot restore the volatile text.
func (s *Service) Release(ctx context.Context, tx pgx.Tx, o *Offer) (Exchange, error) {
	out := Exchange{State: "uncertain"}
	if err := Lock(ctx, tx); err != nil {
		return out, err
	}
	r := offerReceipt(o)
	a, err := attemptFor(ctx, tx, o.Binding, r)
	if err != nil {
		return out, err
	}
	_, authority := s.ValidateGrant(ctx, tx, o.Binding)
	live, err := lockAttempt(ctx, tx, o.Binding, r)
	if err != nil {
		return out, err
	}
	if a.state != "" {
		s.Discard(o.Binding.TenantID, o.MessageID)
		out.State = a.state
		return out, nil
	}
	if authority != nil || a.expired || !live {
		s.Discard(o.Binding.TenantID, o.MessageID)
		return out, Terminalize(ctx, tx, o.MessageID, "offer_invalidated")
	}
	if a.released {
		return out, Fail(409, "offer_already_released")
	}
	// Write the release metadata before consuming memory; no payload reaches SQL.
	tag, err := tx.Exec(ctx, `UPDATE harness_deliveries SET body_released_at=clock_timestamp() WHERE id=$1::uuid AND body_released_at IS NULL AND offer_deadline>clock_timestamp()`, o.DeliveryID)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() != 1 {
		s.Discard(o.Binding.TenantID, o.MessageID)
		return out, Terminalize(ctx, tx, o.MessageID, "offer_deadline")
	}
	body, ok := s.Take(o.Binding, o.GrantID, o.MessageID, TakeTransaction{Context: ctx, Tx: tx})
	if !ok {
		return out, Terminalize(ctx, tx, o.MessageID, "content_lost")
	}
	o.Body = body
	out.State = "offered"
	out.Offer = o
	return out, nil
}

// Settle atomically compares the complete binding/generation, live authority,
// deadline and one-use nonce, then records shown + completed in one commit.
// There is no crash window where shown can be replayed to release the body.
func (s *Service) Settle(ctx context.Context, tx pgx.Tx, b Binding, r Receipt) (Exchange, error) {
	out := Exchange{}
	if r.Outcome != "shown" && r.Outcome != "uncertain" {
		return out, Fail(400, "invalid_receipt_outcome")
	}
	if err := Lock(ctx, tx); err != nil {
		return out, err
	}
	a, err := attemptFor(ctx, tx, b, r)
	if err != nil {
		return out, err
	}
	_, authority := s.ValidateGrant(ctx, tx, b)
	live, err := lockAttempt(ctx, tx, b, r)
	if err != nil {
		return out, err
	}
	if a.state != "" {
		if authority != nil || a.expired || !live {
			return out, Fail(409, "receipt_authority_expired")
		}
		if a.receipt != r.Outcome {
			return out, Fail(409, "receipt_already_terminal")
		}
		return Exchange{State: a.state, ShownAt: a.shown, CompletedAt: a.completed}, nil
	}
	if authority != nil || a.expired || !live || !a.released {
		out.State = "uncertain"
		return out, Terminalize(ctx, tx, r.MessageID, "receipt_invalidated")
	}
	if r.Outcome == "uncertain" {
		// Set outcome in the same terminal write so duplicate identical receipts can
		// read metadata, while late shown receipts remain closed.
		if _, err = tx.Exec(ctx, `UPDATE harness_deliveries SET receipt_outcome='uncertain' WHERE id=$1::uuid`, r.DeliveryID); err != nil {
			return out, err
		}
		out.State = "uncertain"
		return out, Terminalize(ctx, tx, r.MessageID, "hook_uncertain")
	}
	err = tx.QueryRow(ctx, `UPDATE harness_deliveries SET shown_at=clock_timestamp(),completed_at=clock_timestamp(),terminal_outcome='completed',receipt_outcome='shown'
 WHERE id=$1::uuid AND offer_deadline>clock_timestamp() RETURNING shown_at,completed_at`, r.DeliveryID).Scan(&out.ShownAt, &out.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		out.State = "uncertain"
		return out, Terminalize(ctx, tx, r.MessageID, "offer_deadline")
	}
	if err != nil {
		return out, err
	}
	if err = projectOutcome(ctx, tx, r.MessageID, "completed"); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE inbox_message_deliveries SET state='delivered',reason='',attempts=1,lease_token=NULL,lease_until=NULL WHERE message_id=$1::uuid`, r.MessageID); err != nil {
		return out, err
	}
	// Shared receipt remains compatible, but attached-aware projections suppress
	// legacy Read/Delivered labels and expose the qualified timestamps instead.
	_, err = tx.Exec(ctx, `UPDATE inbox_receipts SET state='handed_off',handed_off_at=clock_timestamp(),failure_reason='' WHERE message_id=$1::uuid AND state='queued'`, r.MessageID)
	out.State = "completed"
	return out, err
}

// ValidateDisclosure is a fresh, content-free consent check after the daemon
// receives an offer and immediately before it discloses text to the hook.
func (s *Service) ValidateDisclosure(ctx context.Context, tx pgx.Tx, b Binding, r Receipt) (Exchange, error) {
	out := Exchange{State: "uncertain"}
	if err := Lock(ctx, tx); err != nil {
		return out, err
	}
	a, err := attemptFor(ctx, tx, b, r)
	if err != nil {
		return out, err
	}
	_, authority := s.ValidateGrant(ctx, tx, b)
	live, err := lockAttempt(ctx, tx, b, r)
	if err != nil {
		return out, err
	}
	if authority != nil || !live || a.expired || !a.released || a.state != "" {
		return out, nil
	}
	out.State = "offered"
	return out, nil
}
