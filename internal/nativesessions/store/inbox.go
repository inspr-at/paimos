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
	if len(s.commands) >= 1024 {
		return fail(429, nativecontract.ReasonBufferFull)
	}
	var queued *queuedCommand
	err = s.runtimeTx(ctx, d, b, false, func(ctx context.Context, tx pgx.Tx, session consent.Session, proof receipts.RuntimeProof) error {
		v := session.View
		if v.Mode != "structured" || (v.State != "running" && v.State != "idle") || !v.Capabilities.Input || !v.Capabilities.ChatSupported {
			return nil
		}
		// One live inbox input at a time keeps a stalled provider bounded without
		// blocking an independently queued person stop or interrupt.
		for _, c := range s.commands {
			if c.inbox && !c.done && !c.inboxAcknowledged && c.intent.Receipt.Binding.Equal(b) {
				return nil
			}
		}
		var message, sender, body string
		var cursor int64
		var priority bool
		err := tx.QueryRow(ctx, `SELECT m.id::text,m.sender_principal_id::text,m.body,m.sent_event_id,(`+inbox.LivePrioritySQL+`)
 FROM inbox_messages m WHERE m.tenant_id=$1 AND m.recipient_principal_id=$2 AND m.recipient_session_id=$3
 AND m.content_mode='durable' AND m.chat_thread_id IS NULL AND m.acked_at IS NULL AND m.cancelled_at IS NULL
 AND (m.expires_at IS NULL OR m.expires_at>clock_timestamp())
 AND (m.pickup_lease_until IS NULL OR m.pickup_lease_until<=clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM inbox_receipts r WHERE r.tenant_id=m.tenant_id AND r.message_id=m.id AND r.state='failed')
 AND NOT EXISTS(SELECT 1 FROM harness_deliveries h WHERE h.tenant_id=m.tenant_id AND h.message_id=m.id AND h.session_id=$3)
 ORDER BY (m.idempotency_key LIKE 'desk/%') DESC,m.sent_event_id LIMIT 1 FOR NO KEY UPDATE OF m SKIP LOCKED`, b.TenantID, d.PrincipalID, b.HarnessSessionID).Scan(&message, &sender, &body, &cursor, &priority)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var operation string
		if err := tx.QueryRow(ctx, `INSERT INTO harness_deliveries(tenant_id,session_id,message_id,cursor) VALUES($1,$2,$3,$4) RETURNING id::text`, b.TenantID, b.HarnessSessionID, message, cursor).Scan(&operation); err != nil {
			return err
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
			return err
		}
		intent := nativereceipts.Intent{Receipt: nativecontract.Receipt{RequestID: message, OperationID: operation, Binding: &b, Operation: "input", State: "accepted", InputID: &message, SourceSequence: "0", UpdatedAt: s.now().UTC()}, ActorID: session.Original.ID, PayloadSHA256: digest, ExpiresAt: until, ProcessIdentityDigest: proof.Identity.ProcessIdentityDigest}
		if err := s.production.authority.RegisterInboxOperationTx(ctx, tx, d, intent, input); err != nil {
			return err
		}
		params, _ := json.Marshal(input)
		queued = &queuedCommand{intent: intent, command: nativecontract.DaemonCommand{Kind: "input", Params: params}, actor: session.Original, inbox: true}
		p, _ := tenant.PrincipalFrom(ctx)
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &b.ProjectID, Type: "native_session.receipt_changed", After: nativecontract.AuditEvent{ProjectID: b.ProjectID, OwnedSessionID: b.OwnedSessionID, HarnessSessionID: &b.HarnessSessionID, RuntimeEpoch: &b.RuntimeEpoch, Revision: b.BindingRevision, RequestID: message, OperationID: &operation, State: "accepted"}})
		return err
	})
	if err == nil && queued != nil {
		s.commands[queued.intent.Receipt.OperationID] = *queued
	}
	return err
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
	return nil
}
