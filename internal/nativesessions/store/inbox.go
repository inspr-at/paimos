// SPDX-License-Identifier: AGPL-3.0-only
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativesessions/daemon"
	"github.com/inspr-at/paimos/internal/nativesessions/receipts"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// admitInbox is called only by the live owner's command poll. It neither starts
// nor adopts a process. Offline, terminal, unqualified or unmanaged recipients
// retain their inbox. A durable lease is the P3a operation ID, once per message
// and generation; a restart never reconstructs a possibly written input.
func (s *Store) admitInbox(ctx context.Context, d nativecontract.DaemonActor) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	private, ok := ctx.Value(proofKey{}).(daemon.Proof)
	identity, _, _ := private.Runtime()
	if !ok || identity.RuntimeEpoch == "" {
		return nil
	}
	var raw []byte
	err := dbReadBinding(ctx, s, d, identity.RuntimeEpoch, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var b nativecontract.Binding
	if len(raw) > 4096 || json.Unmarshal(raw, &b) != nil {
		return fail(409, nativecontract.ReasonIdentityMismatch)
	}
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	var queued *queuedCommand
	var retired []queuedCommand
	// The stop-only fence also permits discarding an unclaimed lease after
	// consent lapses. It never admits input without full current consent below.
	err = s.stopTx(ctx, d, b, false, func(ctx context.Context, tx pgx.Tx, session consent.Session, proof receipts.RuntimeProof) error {
		var err error
		retired, err = s.retireUnclaimedInboxTx(ctx, tx, b, session.OwnerPersonID, session.StopOnly)
		if err != nil {
			return err
		}
		queueLength := len(s.commands)
		for _, c := range retired {
			if _, exists := s.commands[c.intent.Receipt.OperationID]; exists {
				queueLength--
			}
		}
		queued, err = s.queueInboxTx(ctx, tx, d, b, session, proof, queueLength)
		if err != nil {
			return err
		}
		// All leases, operations and receipt rows precede the event counter.
		p, _ := tenant.PrincipalFrom(ctx)
		for _, c := range retired {
			if _, err := events.Append(ctx, tx, p, inboxCommandEvent(c.intent.Receipt)); err != nil {
				return err
			}
		}
		if queued != nil {
			_, err = events.Append(ctx, tx, p, inboxCommandEvent(queued.intent.Receipt))
		}
		return err
	})
	if err == nil {
		for _, c := range retired {
			delete(s.commands, c.intent.Receipt.OperationID)
		}
		if queued != nil {
			s.commands[queued.intent.Receipt.OperationID] = *queued
		}
	}
	return err
}

func (s *Store) queueInboxTx(ctx context.Context, tx pgx.Tx, d nativecontract.DaemonActor, b nativecontract.Binding, session consent.Session, proof receipts.RuntimeProof, queueLength int) (*queuedCommand, error) {
	v := session.View
	if session.StopOnly || queueLength >= 1024 || v.Mode != "structured" || (v.State != "running" && v.State != "idle") || !v.Capabilities.Input || !v.Capabilities.ChatSupported {
		return nil, nil
	}
	// One live inbox input at a time keeps a stalled provider bounded without
	// blocking an independently queued person stop or interrupt.
	for _, c := range s.commands {
		if c.inbox && !c.done && !c.inboxAcknowledged && s.now().Before(c.intent.ExpiresAt) && c.intent.Receipt.Binding.Equal(b) {
			return nil, nil
		}
	}
	// Defer inbox admission itself until the observed turn ends. A long turn
	// must not consume a claim window or hide the message behind a lease.
	// Person commands continue through their independent bounded input path.
	busy, err := inboxRuntimeBusyTx(ctx, tx, b, "")
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, nil
	}
	var message, sender, body string
	var cursor int64
	var priority bool
	err = tx.QueryRow(ctx, `SELECT m.id::text,m.sender_principal_id::text,m.body,m.sent_event_id,(`+inbox.LivePrioritySQL+`)
 FROM inbox_messages m WHERE m.tenant_id=$1 AND m.recipient_principal_id=$2 AND m.recipient_session_id=$3
 AND m.content_mode='durable' AND m.chat_thread_id IS NULL AND m.acked_at IS NULL AND m.cancelled_at IS NULL
 AND (m.expires_at IS NULL OR m.expires_at>clock_timestamp())
 AND (m.pickup_lease_until IS NULL OR m.pickup_lease_until<=clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM inbox_receipts r WHERE r.tenant_id=m.tenant_id AND r.message_id=m.id AND r.state='failed')
 AND NOT EXISTS(SELECT 1 FROM harness_deliveries h WHERE h.tenant_id=m.tenant_id AND h.message_id=m.id AND h.session_id=$3)
 ORDER BY (`+inbox.DeskAnswerSQL+`) DESC,m.sent_event_id LIMIT 1 FOR NO KEY UPDATE OF m SKIP LOCKED`, b.TenantID, d.PrincipalID, b.HarnessSessionID).Scan(&message, &sender, &body, &cursor, &priority)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var operation string
	if err := tx.QueryRow(ctx, `INSERT INTO harness_deliveries(tenant_id,session_id,message_id,cursor) VALUES($1,$2,$3,$4) RETURNING id::text`, b.TenantID, b.HarnessSessionID, message, cursor).Scan(&operation); err != nil {
		return nil, err
	}
	until := s.now().Add(5 * time.Minute)
	if session.Policy.ExpiresAt.Before(until) {
		until = session.Policy.ExpiresAt
	}
	mode := "now"
	if !priority {
		mode = "after_turn"
	}
	input := nativecontract.Input{RequestID: message, InputID: message, ExpectedBinding: b, Mode: mode, Text: inbox.LiveMessageText(message, sender, body), ExpiresAt: until}
	digest, err := nativereceipts.Digest(input)
	if err != nil {
		return nil, err
	}
	intent := nativereceipts.Intent{Receipt: nativecontract.Receipt{RequestID: message, OperationID: operation, Binding: &b, Operation: "input", State: "accepted", InputID: &message, SourceSequence: "0", UpdatedAt: s.now().UTC()}, ActorID: session.Original.ID, PayloadSHA256: digest, ExpiresAt: until, ProcessIdentityDigest: proof.Identity.ProcessIdentityDigest}
	if err := s.production.authority.RegisterInboxOperationTx(ctx, tx, d, intent, input); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(input)
	return &queuedCommand{intent: intent, command: nativecontract.DaemonCommand{Kind: "input", Params: params}, actor: session.Original, inbox: true}, nil
}

// A claim durably records accepted before returning command content. Therefore
// absence of a receipt proves that no claim could have crossed the write
// boundary, even after server restart. Claimed/uncertain inputs retain their
// holds; queue loss must never turn a possible write into another injection.
func (s *Store) retireUnclaimedInboxTx(ctx context.Context, tx pgx.Tx, b nativecontract.Binding, owner string, withdrawn bool) ([]queuedCommand, error) {
	rawBinding, _ := json.Marshal(b)
	rows, err := tx.Query(ctx, `SELECT o.intent FROM native_session_operations o
 JOIN harness_deliveries h ON h.tenant_id=o.tenant_id AND h.id=o.operation_id
 WHERE o.tenant_id=$1 AND o.owned_session_id=$2 AND o.intent->'receipt'->'binding'=$3::jsonb
 AND o.intent->'receipt'->>'operation'='input' AND h.session_id=$4 AND h.mode='managed'
 AND h.completed_at IS NULL AND h.released_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM native_session_receipts r WHERE r.tenant_id=o.tenant_id AND r.operation_id=o.operation_id)
 ORDER BY o.operation_id LIMIT 513`, b.TenantID, b.OwnedSessionID, rawBinding, b.HarnessSessionID)
	if err != nil {
		return nil, err
	}
	var candidates []queuedCommand
	for rows.Next() {
		var raw []byte
		var c queuedCommand
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if len(raw) > 16384 || json.Unmarshal(raw, &c.intent) != nil || c.intent.Receipt.Binding == nil || !c.intent.Receipt.Binding.Equal(b) {
			err = fail(409, nativecontract.ReasonIdentityMismatch)
			break
		}
		candidates = append(candidates, c)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates) > nativereceipts.MaxReceipts {
		return nil, fail(429, nativecontract.ReasonBufferFull)
	}
	var retired []queuedCommand
	for _, c := range candidates {
		_, queued := s.commands[c.intent.Receipt.OperationID]
		state, reason := "rejected", nativecontract.ReasonGrantMissing
		if !s.now().Before(c.intent.ExpiresAt) {
			state, reason = "expired", nativecontract.ReasonGrantExpired
		} else if withdrawn {
			reason = nativecontract.ReasonPermissionRevoked
		} else if queued {
			continue
		}
		changed, err := s.retireInboxTx(ctx, tx, &c, owner, state, reason)
		if err != nil {
			return nil, err
		}
		if changed {
			retired = append(retired, c)
		}
	}
	return retired, nil
}

// Caller holds the current runtime/access fence. The receipt absence condition
// is checked again inside the final lease write, serialized with command claims.
func (s *Store) retireInboxTx(ctx context.Context, tx pgx.Tx, c *queuedCommand, owner, state string, reason nativecontract.Reason) (bool, error) {
	b := *c.intent.Receipt.Binding
	tag, err := tx.Exec(ctx, `UPDATE harness_deliveries h SET released_at=$5
 WHERE h.tenant_id=$1 AND h.id=$2 AND h.session_id=$3 AND h.message_id=$4 AND h.mode='managed'
 AND h.released_at IS NULL AND h.completed_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM native_session_receipts r WHERE r.tenant_id=h.tenant_id AND r.operation_id=h.id)`, b.TenantID, c.intent.Receipt.OperationID, b.HarnessSessionID, c.intent.Receipt.RequestID, s.now().UTC())
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	c.intent.Receipt.State, c.intent.Receipt.Reason, c.intent.Receipt.UpdatedAt = state, &reason, s.now().UTC()
	return true, s.recordInboxReceiptTx(ctx, tx, *c, owner)
}

func (s *Store) recordInboxReceiptTx(ctx context.Context, tx pgx.Tx, c queuedCommand, owner string) error {
	r := c.intent.Receipt
	if err := s.validate("NativeReceipt", r); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO native_session_receipts(tenant_id,operation_id,request_id,project_id,owned_session_id,owner_person_id,actor_principal_id,payload_sha256,expires_at,receipt,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11) ON CONFLICT DO NOTHING`, r.Binding.TenantID, r.OperationID, r.RequestID, r.Binding.ProjectID, r.Binding.OwnedSessionID, owner, c.intent.ActorID, c.intent.PayloadSHA256, c.intent.ExpiresAt, raw, r.UpdatedAt)
	return err
}

func inboxCommandEvent(r nativecontract.Receipt) events.Change {
	b := r.Binding
	return events.Change{NodeID: &b.ProjectID, Type: "native_session.receipt_changed", After: nativecontract.AuditEvent{ProjectID: b.ProjectID, OwnedSessionID: b.OwnedSessionID, HarnessSessionID: &b.HarnessSessionID, RuntimeEpoch: &b.RuntimeEpoch, Revision: b.BindingRevision, RequestID: r.RequestID, OperationID: &r.OperationID, State: r.State, Reason: r.Reason}}
}

// An epoch selector only finds the binding; runtimeTx reauthorizes it under the
// access fence before any inbox row or native operation can change.
func dbReadBinding(ctx context.Context, s *Store, d nativecontract.DaemonActor, epoch string, raw *[]byte) error {
	return db.InTenant(ctx, s.pool, d.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT view->'current_binding' FROM native_session_bindings WHERE tenant_id=$1 AND paired_principal_id=$2 AND daemon_id=$3 AND daemon_generation=$4 AND runtime_epoch=$5`, d.TenantID, d.PrincipalID, d.DaemonID, d.DaemonGeneration, epoch).Scan(raw)
	})
}

func inboxCommandTx(ctx context.Context, tx pgx.Tx, d nativecontract.DaemonActor, c queuedCommand) error {
	b := *c.intent.Receipt.Binding
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_deliveries h JOIN inbox_messages m ON m.tenant_id=h.tenant_id AND m.id=h.message_id
 WHERE h.tenant_id=$1 AND h.id=$2 AND h.session_id=$3 AND h.mode='managed' AND h.released_at IS NULL
 AND m.id=$4 AND m.recipient_session_id=$3 AND m.recipient_principal_id=$5 AND m.content_mode='durable' AND m.chat_thread_id IS NULL AND m.cancelled_at IS NULL
 AND ((m.acked_at IS NULL AND (m.expires_at IS NULL OR m.expires_at>clock_timestamp())) OR EXISTS(SELECT 1 FROM native_session_receipts r WHERE r.tenant_id=h.tenant_id AND r.operation_id=h.id AND r.receipt->>'state' IN ('write_acknowledged','turn_started','turn_completed'))))`, b.TenantID, c.intent.Receipt.OperationID, b.HarnessSessionID, c.intent.Receipt.RequestID, d.PrincipalID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return fail(403, nativecontract.ReasonPermissionRevoked)
	}
	// A person input may have won the write boundary since the commands page.
	// Recheck here as well, excluding this exact command's durable claim fence.
	busy, err := inboxRuntimeBusyTx(ctx, tx, b, c.intent.Receipt.OperationID)
	if err != nil {
		return err
	}
	if busy {
		return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
	}
	return nil
}

func inboxRuntimeBusyTx(ctx context.Context, tx pgx.Tx, b nativecontract.Binding, operation string) (bool, error) {
	var busy bool
	rawBinding, _ := json.Marshal(b)
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM native_session_receipts
 WHERE tenant_id=$1 AND owned_session_id=$2 AND receipt->'binding'=$3::jsonb AND operation_id::text<>$4
 AND receipt->>'operation'='input' AND receipt->>'state' IN ('accepted','queued','write_started','write_acknowledged','turn_started','uncertain'))`, b.TenantID, b.OwnedSessionID, rawBinding, operation).Scan(&busy)
	return busy, err
}
