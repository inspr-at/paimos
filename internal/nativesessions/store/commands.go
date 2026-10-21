// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativesessions/daemon"
	"github.com/inspr-at/paimos/internal/nativesessions/receipts"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Only the bounded live queue holds command content. SQL retains immutable
// metadata/digests. A restart cannot replay a possible native write from disk.
type queuedCommand struct {
	done           bool
	intent         nativereceipts.Intent
	command        nativecontract.DaemonCommand
	drain          *nativecontract.FinalDrainGrant
	actor          tenant.Principal
	successor      *nativecontract.ClaimResult
	successorProof daemon.Proof
}

func commandValue(c nativecontract.Command) (nativecontract.Binding, string, string, time.Time, any, error) {
	var b nativecontract.Binding
	var request, schema string
	var expires time.Time
	var value any
	switch {
	case c.Kind == "input" && c.Input != nil:
		b, request, schema, expires, value = c.Input.ExpectedBinding, c.Input.RequestID, "NativeInput", c.Input.ExpiresAt, *c.Input
	case c.Kind == "interrupt" && c.Interrupt != nil:
		b, request, schema, expires, value = c.Interrupt.ExpectedBinding, c.Interrupt.RequestID, "NativeInterrupt", c.Interrupt.ExpiresAt, *c.Interrupt
	case c.Kind == "stop" && c.Stop != nil:
		b, request, schema, expires, value = c.Stop.ExpectedBinding, c.Stop.RequestID, "NativeStop", c.Stop.ExpiresAt, *c.Stop
	case c.Kind == "take" && c.Take != nil:
		b, request, schema, expires, value = c.Take.ExpectedBinding, c.Take.RequestID, "NativeTakeIntent", c.Take.ExpiresAt, *c.Take
	case c.Kind == "return" && c.Return != nil:
		b, request, schema, expires, value = c.Return.ExpectedBinding, c.Return.RequestID, "NativeReturnIntent", c.Return.ExpiresAt, *c.Return
	default:
		return b, "", "", expires, nil, fail(400, nativecontract.ReasonUnsupported)
	}
	return b, request, schema, expires, value, nil
}
func (s *Store) Mutate(ctx context.Context, a nativecontract.Actor, c nativecontract.Command) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	if s.production == nil {
		return out, fail(503, nativecontract.ReasonQualificationMissing)
	}
	b, request, schema, expires, value, err := commandValue(c)
	if err != nil {
		return out, err
	}
	if c.Input != nil && len(c.Input.Text) > nativecontract.InputUTF8Bytes || c.Interrupt != nil && len(c.Interrupt.ExpectedTurnRef) > 128 || c.Stop != nil && len(c.Stop.ExpectedProcessIdentityDigest) > 64 {
		return out, fail(413, nativecontract.ReasonBufferFull)
	}
	for _, id := range []string{b.TenantID, b.ProjectID, b.OwnedSessionID, b.HarnessSessionID, b.RuntimeEpoch, request} {
		if len(id) > 36 {
			return out, fail(413, nativecontract.ReasonBufferFull)
		}
	}
	if len(b.DaemonID) > 128 || len(b.DaemonGeneration) > 128 {
		return out, fail(413, nativecontract.ReasonBufferFull)
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > nativecontract.RequestBodyBytes {
		return out, fail(413, nativecontract.ReasonBufferFull)
	}
	if err = s.validate(schema, value); err != nil {
		return out, err
	}
	if c.Stop != nil && (c.Stop.Kind != "graceful" || c.Stop.ForceAuthorizationID != nil) {
		return out, fail(400, nativecontract.ReasonUnsupported)
	}
	p, err := authenticated(ctx, a)
	if err != nil {
		return out, err
	}
	if p.Kind != tenant.Person || p.KeyID != "" || p.AuthKeyID != "" {
		return out, fail(403, nativecontract.ReasonWrongPrincipal)
	}
	digest, err := nativereceipts.Digest(value)
	if err != nil {
		return out, err
	}
	s.mu.Lock()
	process := s.runtimes[b.OwnedSessionID]
	s.mu.Unlock()
	if !nativereceipts.ValidDigest(process) {
		return out, fail(409, nativecontract.ReasonOwnershipUnconfirmed)
	}
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	for id, c := range s.commands {
		if c.done && (c.drain == nil || !s.now().Before(c.drain.ExpiresAt)) {
			delete(s.commands, id)
		}
	}
	if len(s.commands) >= 1024 {
		return out, fail(429, nativecontract.ReasonBufferFull)
	}
	var queued queuedCommand
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		// Only the exact owner's stop survives withdrawn consent; a StopOnly
		// session carries no retention facts, so it issues no final drain.
		authority := s.production.consent.SessionTx
		if c.Stop != nil {
			authority = s.production.consent.StopSessionTx
		}
		session, err := authority(ctx, tx, b)
		if err != nil {
			return err
		}
		control := c.Kind == "take" || c.Kind == "return"
		if p.TenantID != b.TenantID || !control && p.ID != session.Original.ID {
			return fail(403, nativecontract.ReasonWrongPrincipal)
		}
		if err = authz.RequireTx(ctx, tx, p, "harness.control", authz.Scope{ProjectID: b.ProjectID}); err != nil {
			return fail(403, nativecontract.ReasonPermissionRevoked)
		}
		if control {
			if err = s.controlTx(ctx, tx, session, p, c); err != nil {
				return err
			}
		} else if session.View.State != "running" && session.View.State != "idle" {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		if !s.now().Before(expires) || expires.After(session.Policy.ExpiresAt) {
			return fail(403, nativecontract.ReasonGrantExpired)
		}
		if c.Stop != nil && c.Stop.ExpectedProcessIdentityDigest != process {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		created := false
		var prior []byte
		err = tx.QueryRow(ctx, `SELECT intent FROM native_session_operations WHERE tenant_id=$1 AND owned_session_id=$2 AND intent->'receipt'->>'request_id'=$3 LIMIT 1`, p.TenantID, b.OwnedSessionID, request).Scan(&prior)
		if err == nil {
			if len(prior) > 16384 || json.Unmarshal(prior, &queued.intent) != nil || queued.intent.PayloadSHA256 != digest || queued.intent.ActorID != p.ID || queued.intent.Receipt.Operation != c.Kind {
				return fail(409, nativecontract.ReasonIdentityMismatch)
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			created = true
			operation, err := uuid()
			if err != nil {
				return err
			}
			r := nativecontract.Receipt{RequestID: request, OperationID: operation, Binding: &b, Operation: c.Kind, State: "accepted", SourceSequence: "0", UpdatedAt: s.now().UTC()}
			if c.Input != nil {
				id := c.Input.InputID
				r.InputID = &id
			}
			queued.intent = nativereceipts.Intent{Receipt: r, ActorID: p.ID, PayloadSHA256: digest, ExpiresAt: expires, ProcessIdentityDigest: process}
		} else {
			return err
		}
		queued.actor = p
		if control {
			raw, err := json.Marshal(queued.intent)
			if err != nil {
				return err
			}
			var count int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM native_session_operations WHERE tenant_id=$1 AND owned_session_id=$2 AND operation_id<>$3 LIMIT 513) x`, p.TenantID, b.OwnedSessionID, queued.intent.Receipt.OperationID).Scan(&count); err != nil {
				return err
			}
			if count >= nativereceipts.MaxReceipts {
				return fail(429, nativecontract.ReasonBufferFull)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO native_session_operations(tenant_id,operation_id,owned_session_id,project_id,owner_person_id,intent,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, p.TenantID, queued.intent.Receipt.OperationID, b.OwnedSessionID, b.ProjectID, session.OwnerPersonID, raw, s.now()); err != nil {
				return err
			}
		} else if err = s.production.authority.RegisterOperationTx(ctx, tx, queued.intent); err != nil {
			return err
		}
		// An existing separate person-bound chat context and retention decision are
		// required for final publication. Absence never invents a target or approval.
		if session.RetainFinals && !control {
			var retention, conversation, role string
			err = tx.QueryRow(ctx, `SELECT c.grant_id::text,t.id::text,context.role_id::text FROM native_session_consents c JOIN agent_permission_grants g ON g.tenant_id=c.tenant_id AND g.approval_request_id=c.grant_id JOIN chat_session_contexts context ON context.tenant_id=c.tenant_id AND context.session_id=$2 AND context.owner_person_id=c.owner_person_id JOIN chat_threads t ON t.tenant_id=context.tenant_id AND t.role_id=context.role_id AND t.project_id=context.project_id AND t.person_id=context.owner_person_id WHERE c.tenant_id=$1 AND c.project_id=$3 AND c.owner_person_id=$4 AND g.scope=$5 AND g.revoked_at IS NULL AND g.valid_until>$6 AND t.archived_at IS NULL ORDER BY c.created_at DESC,c.grant_id LIMIT 1`, p.TenantID, b.HarnessSessionID, b.ProjectID, session.OwnerPersonID, consent.RetentionScope, s.now()).Scan(&retention, &conversation, &role)
			if err != nil {
				return fail(403, nativecontract.ReasonRetentionUnapproved)
			}
			grant, err := s.production.authority.IssueDrainTx(ctx, tx, b, retention, conversation, role)
			if err != nil {
				return err
			}
			queued.drain = &grant
		}
		queued.command = nativecontract.DaemonCommand{Kind: c.Kind, Params: raw}
		response := queued.intent.Receipt
		if !created {
			var saved []byte
			err := tx.QueryRow(ctx, `SELECT receipt FROM native_session_receipts WHERE tenant_id=$1 AND operation_id=$2`, p.TenantID, response.OperationID).Scan(&saved)
			if err == nil {
				if len(saved) > nativecontract.ReceiptBytes || json.Unmarshal(saved, &response) != nil {
					return fail(409, nativecontract.ReasonIdentityMismatch)
				}
				switch response.State {
				case "turn_completed", "applied", "exit_confirmed", "rejected", "expired":
					queued.done = true
					queued.command.Params = nil
				}
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		out = nativecontract.OperationResult{Receipt: response, Session: &session.View}
		if created {
			operation := queued.intent.Receipt.OperationID
			payload := nativecontract.AuditEvent{ProjectID: b.ProjectID, OwnedSessionID: b.OwnedSessionID, HarnessSessionID: &b.HarnessSessionID, RuntimeEpoch: &b.RuntimeEpoch, Revision: session.View.Revision, RequestID: request, State: "accepted", OperationID: &operation}
			_, err = events.Append(ctx, tx, p, events.Change{NodeID: &b.ProjectID, Type: "native_session.receipt_changed", After: payload})
			return err
		}
		return nil
	})
	if err == nil {
		if previous, exists := s.commands[queued.intent.Receipt.OperationID]; exists {
			queued = previous // Never lose an admitted successor on a duplicate intent.
		}
		s.commands[queued.intent.Receipt.OperationID] = queued
	}
	return out, err
}

func (s *Store) controlTx(ctx context.Context, tx pgx.Tx, session consent.Session, p tenant.Principal, c nativecontract.Command) error {
	b := *session.View.CurrentBinding
	if err := stream.TakeAuthorityTx(ctx, tx, p, session.OwnerPersonID, b.ProjectID); err != nil {
		return err
	}
	var epoch string
	var revision int64
	switch c.Kind {
	case "take":
		if c.Take == nil {
			return fail(400, nativecontract.ReasonInvalidInput)
		}
		epoch, revision = c.Take.ControllerEpoch, c.Take.ExpectedRevision
		if !s.production.consent.Capabilities.Take || !s.production.consent.Capabilities.TakeSupported || !session.View.Capabilities.Take || !session.View.Capabilities.TakeSupported {
			return fail(503, nativecontract.ReasonQualificationMissing)
		}
		if session.View.Mode != "structured" || session.View.State != "running" && session.View.State != "idle" && session.View.State != "requires_action" {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
	case "return":
		if c.Return == nil {
			return fail(400, nativecontract.ReasonInvalidInput)
		}
		epoch, revision = c.Return.ControllerEpoch, c.Return.ExpectedRevision
		if session.View.Mode != "terminal" || session.View.State != "taken" {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
	default:
		return fail(400, nativecontract.ReasonUnsupported)
	}
	if revision != session.View.Revision {
		return fail(409, nativecontract.ReasonStaleRevision)
	}
	return s.production.consent.ControllerTx(ctx, tx, session, p, epoch)
}

func queuedControl(c queuedCommand) (nativecontract.Command, error) {
	out := nativecontract.Command{Kind: c.command.Kind}
	switch out.Kind {
	case "take":
		out.Take = new(nativecontract.TakeIntent)
		if err := json.Unmarshal(c.command.Params, out.Take); err != nil {
			return out, err
		}
	case "return":
		out.Return = new(nativecontract.ReturnIntent)
		if err := json.Unmarshal(c.command.Params, out.Return); err != nil {
			return out, err
		}
	default:
		return out, fail(400, nativecontract.ReasonUnsupported)
	}
	return out, nil
}

func (s *Store) Commands(ctx context.Context, d nativecontract.DaemonActor, q nativecontract.PageQuery) (nativecontract.PendingCommandPage, error) {
	out := nativecontract.PendingCommandPage{Items: []nativecontract.PendingCommand{}}
	if s.production == nil {
		return out, fail(503, nativecontract.ReasonQualificationMissing)
	}
	if q.Limit == 0 {
		q.Limit = 25
	}
	if q.Limit < 1 || q.Limit > 50 || q.After != "" && !workorders.UUID(q.After) {
		return out, fail(400, nativecontract.ReasonInvalidInput)
	}
	s.commandMu.Lock()
	items := make([]queuedCommand, 0, len(s.commands))
	for _, c := range s.commands {
		items = append(items, c)
	}
	s.commandMu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i].intent.Receipt.OperationID < items[j].intent.Receipt.OperationID })
	private, _ := ctx.Value(proofKey{}).(daemon.Proof)
	identity, _, _ := private.Runtime()
	for _, c := range items {
		if c.done || c.successor != nil {
			continue
		}
		if identity.RuntimeEpoch != "" && c.intent.Receipt.Binding.RuntimeEpoch != identity.RuntimeEpoch {
			continue
		}
		r := c.intent.Receipt
		b := *r.Binding
		if b.TenantID != d.TenantID || b.DaemonID != d.DaemonID || b.DaemonGeneration != d.DaemonGeneration || r.OperationID <= q.After || !s.now().Before(c.intent.ExpiresAt) {
			continue
		}
		// Withdrawn consent leaves only the exact owner's stop deliverable; the
		// session's other commands are no longer listed, so they cannot hold it back.
		withdrawn := false
		err := s.stopTx(ctx, d, b, false, func(_ context.Context, _ pgx.Tx, session consent.Session, _ receipts.RuntimeProof) error {
			withdrawn = session.StopOnly
			return nil
		})
		if err != nil {
			return nativecontract.PendingCommandPage{}, err
		}
		if withdrawn && r.Operation != "stop" {
			continue
		}
		if r.Operation == "take" || r.Operation == "return" {
			command, err := queuedControl(c)
			if err != nil {
				return out, err
			}
			err = s.runtimeTx(ctx, d, b, false, func(ctx context.Context, tx pgx.Tx, session consent.Session, _ receipts.RuntimeProof) error {
				return s.controlTx(ctx, tx, session, c.actor, command)
			})
			if err != nil {
				return out, err
			}
		}
		if len(out.Items) == q.Limit {
			out.Truncated = true
			next := out.Items[len(out.Items)-1].OperationID
			out.NextCursor = &next
			break
		}
		out.Items = append(out.Items, nativecontract.PendingCommand{OperationID: r.OperationID, OwnedSessionID: b.OwnedSessionID, RequestID: r.RequestID, Kind: r.Operation, ExpectedBinding: b, ExpiresAt: c.intent.ExpiresAt})
	}
	return out, nil
}

func (s *Store) claimCommand(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Claim) (nativecontract.ClaimResult, error) {
	var out nativecontract.ClaimResult
	s.commandMu.Lock()
	c, ok := s.commands[in.OperationID]
	s.commandMu.Unlock()
	if !ok || c.done || c.intent.Receipt.Binding == nil {
		return out, fail(403, nativecontract.ReasonGrantMissing)
	}
	b := *c.intent.Receipt.Binding
	authority := s.runtimeTx
	if c.command.Kind == "stop" {
		authority = s.stopTx
	}
	err := authority(ctx, d, b, false, func(ctx context.Context, tx pgx.Tx, session consent.Session, proof receipts.RuntimeProof) error {
		if in.DaemonID != d.DaemonID || in.DaemonGeneration != d.DaemonGeneration || in.LedgerGeneration != d.LedgerGeneration || in.CapabilityDigest != d.CapabilityDigest || in.ExpectedRevision != b.BindingRevision || proof.Identity.ProcessIdentityDigest != c.intent.ProcessIdentityDigest || proof.Identity.RuntimeEpoch != b.RuntimeEpoch {
			return fail(409, nativecontract.ReasonIdentityMismatch)
		}
		actor := session.Original
		if c.command.Kind == "take" || c.command.Kind == "return" {
			command, err := queuedControl(c)
			if err != nil {
				return err
			}
			if err = s.controlTx(ctx, tx, session, c.actor, command); err != nil {
				return err
			}
			actor = c.actor
		}
		now := s.now()
		until := now.Add(20 * time.Second)
		if c.intent.ExpiresAt.Before(until) {
			until = c.intent.ExpiresAt
		}
		if !now.Before(until) {
			return fail(403, nativecontract.ReasonGrantExpired)
		}
		out = nativecontract.ClaimResult{OperationID: in.OperationID, OwnedSessionID: b.OwnedSessionID, Intent: session.Intent, Policy: session.Policy, ExpectedBinding: &b, ExpiresAt: until, FinalDrainGrant: c.drain, ClaimKind: "command", Command: &c.command, CommandActor: &nativecontract.CommandActor{TenantID: b.TenantID, PrincipalID: actor.ID, OwnerPersonID: session.OwnerPersonID, GrantID: session.GrantID, GrantRevision: session.Revision}, AuthorizationTTLMS: until.Sub(now).Milliseconds()}
		var launch []byte
		if err := tx.QueryRow(ctx, `SELECT launch_config FROM native_session_intents WHERE owned_session_id=$1`, b.OwnedSessionID).Scan(&launch); err != nil {
			return err
		}
		return json.Unmarshal(launch, &out.LaunchConfig)
	})
	if err == nil {
		err = s.validate("NativeClaimResult", out)
	}
	return out, err
}

func (s *Store) Report(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Receipt) (nativecontract.OperationResult, error) {
	if s.production == nil {
		return nativecontract.OperationResult{}, fail(503, nativecontract.ReasonQualificationMissing)
	}
	if err := s.validate("NativeReceipt", in); err != nil {
		return nativecontract.OperationResult{}, err
	}
	if in.Operation == "take" || in.Operation == "return" {
		return s.reportSuccessor(ctx, d, in)
	}
	if in.Operation == "start" && in.State == "rejected" {
		return s.reportAbandoned(ctx, d, in)
	}
	if in.Operation == "start" {
		return s.reportStarted(ctx, d, in)
	}
	out, err := s.production.receipts.Report(ctx, d, in)
	if err == nil && (in.State == "turn_completed" || in.State == "applied" || in.State == "exit_confirmed") {
		s.commandMu.Lock()
		if c, ok := s.commands[in.OperationID]; ok {
			c.done = true
			c.command.Params = nil
			s.commands[in.OperationID] = c
		}
		s.commandMu.Unlock()
	}
	if err == nil && in.State == "exit_confirmed" && in.Binding != nil {
		b := *in.Binding
		// Only a stop reaches exit_confirmed; its exit settles without consent.
		err = s.stopTx(ctx, d, b, true, func(ctx context.Context, tx pgx.Tx, session consent.Session, proof receipts.RuntimeProof) error {
			if proof.Exit == nil || !proof.Exit.RootExited || !proof.Exit.GroupAbsent {
				return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
			}
			// The receipt above already committed. Only this exact persisted exit may
			// settle the root; API failure before here conservatively retains the hold.
			var state string
			if err := tx.QueryRow(ctx, `SELECT receipt->>'state' FROM native_session_receipts WHERE operation_id=$1 AND owned_session_id=$2`, in.OperationID, b.OwnedSessionID).Scan(&state); err != nil {
				return err
			}
			if state != "exit_confirmed" {
				return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
			}
			if _, err := tx.Exec(ctx, `UPDATE native_session_roots SET released_at=coalesce(released_at,$3),hold_state='released' WHERE owned_session_id=$1 AND runtime_epoch=$2`, b.OwnedSessionID, b.RuntimeEpoch, proof.Exit.ObservedAt); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE harness_sessions SET phase='stopped',stopped_at=coalesce(stopped_at,$2),stop_reason=coalesce(stop_reason,'completed') WHERE id=$1`, b.HarnessSessionID, proof.Exit.ObservedAt); err != nil {
				return err
			}
			if session.View.State == "stopped" {
				return nil
			}
			session.View.State = "stopped"
			session.View.Revision++
			raw, _ := json.Marshal(session.View)
			if _, err := tx.Exec(ctx, `UPDATE native_session_bindings SET view=$2 WHERE id=$1`, b.OwnedSessionID, raw); err != nil {
				return err
			}
			p, _ := tenant.PrincipalFrom(ctx)
			return appendEvent(ctx, tx, p, "native_session.stopped", b.ProjectID, in.OperationID, in.RequestID, session.View)
		})
	}
	return out, err
}

// Final-drain claims only re-read the original non-renewable grant. They never
// issue command, input or launch authority and may follow a confirmed exit.
func (s *Store) claimDrain(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Claim) (nativecontract.ClaimResult, error) {
	var out nativecontract.ClaimResult
	s.commandMu.Lock()
	c, ok := s.commands[in.OperationID]
	s.commandMu.Unlock()
	if !ok || c.drain == nil || c.intent.Receipt.Binding == nil {
		return out, fail(403, nativecontract.ReasonRetentionUnapproved)
	}
	b := *c.intent.Receipt.Binding
	err := s.runtimeTx(ctx, d, b, true, func(ctx context.Context, tx pgx.Tx, session consent.Session, proof receipts.RuntimeProof) error {
		if in.DaemonID != d.DaemonID || in.DaemonGeneration != d.DaemonGeneration || in.LedgerGeneration != d.LedgerGeneration || in.CapabilityDigest != d.CapabilityDigest {
			return fail(403, nativecontract.ReasonWrongGeneration)
		}
		// FinalTx independently validates the separate current retention decision.
		dummy := nativecontract.FinalPublish{ExpectedBinding: b, DrainGrantID: c.drain.GrantID}
		a, err := s.production.authority.FinalTx(ctx, tx, d, dummy)
		if err != nil {
			return err
		}
		now := s.now()
		if !now.Before(a.Grant.ExpiresAt) {
			return fail(403, nativecontract.ReasonPublicationGrantExpired)
		}
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT launch_config FROM native_session_intents WHERE owned_session_id=$1`, b.OwnedSessionID).Scan(&raw); err != nil {
			return err
		}
		var launch nativecontract.LaunchConfig
		if err = json.Unmarshal(raw, &launch); err != nil {
			return err
		}
		out = nativecontract.ClaimResult{OperationID: in.OperationID, OwnedSessionID: b.OwnedSessionID, Intent: session.Intent, Policy: session.Policy, ExpectedBinding: &b, ExpiresAt: a.Grant.ExpiresAt, FinalDrainGrant: &a.Grant, ClaimKind: "final_drain", LaunchConfig: launch, AuthorizationTTLMS: min(20000, a.Grant.ExpiresAt.Sub(now).Milliseconds())}
		return nil
	})
	if err == nil {
		err = s.validate("NativeClaimResult", out)
	}
	return out, err
}
