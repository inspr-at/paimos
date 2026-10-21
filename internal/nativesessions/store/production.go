// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativesessions/daemon"
	"github.com/inspr-at/paimos/internal/nativesessions/receipts"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type production struct {
	consent   consent.Authority
	authority receipts.CurrentAuthority
	receipts  *receipts.Repository
	relay     *stream.Relay
}

type proofKey struct{}
type daemonKey struct{}

// BindProduction is a construction-only composition seam. Missing services
// retain qualification_missing; there is no default authority implementation.
func (s *Store) BindProduction(c consent.Authority, r *receipts.Repository, relay *stream.Relay) {
	if r == nil || relay == nil {
		return
	}
	s.production = &production{c, receipts.CurrentAuthority{Consent: c}, r, relay}
	s.commands = map[string]queuedCommand{}
	s.runtimes = map[string]string{}
}

// PrivateObserver accepts only the authenticated transport's request-scoped
// observation. SQL subsequently rechecks the issued key, computer and grants.
type PrivateObserver struct{}

func (PrivateObserver) Observe(ctx context.Context, d nativecontract.DaemonActor, p PendingStart) (Observation, error) {
	proof, ok := ctx.Value(proofKey{}).(daemon.Proof)
	if !ok || len(proof.SessionRef) < 16 || proof.LocalStart == nil || !reflect.DeepEqual(proof.LocalStart.Intent, p.Intent) {
		return Observation{}, fail(403, nativecontract.ReasonGrantMissing)
	}
	ref, lease := sha256.Sum256([]byte(proof.SessionRef)), sha256.Sum256([]byte(proof.WorkerLease))
	return Observation{OperationID: p.OperationID, PrincipalID: d.PrincipalID, ComputerID: d.ComputerID, DaemonID: d.DaemonID, DaemonGeneration: d.DaemonGeneration, LedgerGeneration: d.LedgerGeneration, CapabilityDigest: d.CapabilityDigest, AccountID: p.Intent.AccountID, WorkspaceID: p.Intent.WorkspaceID, LedgerReservationID: proof.ReservationID, LedgerClear: proof.ReservationID != "", SessionRefDigest: ref[:], WorkerLeaseDigest: lease[:], ObservedAt: proof.ObservedAt, ExpiresAt: proof.ExpiresAt}, nil
}

func (s *Store) DaemonRequest(r *http.Request) (context.Context, nativecontract.DaemonActor, error) {
	ctx := r.Context()
	var d nativecontract.DaemonActor
	if s.production == nil {
		return ctx, d, fail(503, nativecontract.ReasonQualificationMissing)
	}
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.Kind != tenant.Agent || p.KeyID == "" {
		return ctx, d, fail(403, nativecontract.ReasonWrongPrincipal)
	}
	proof, err := daemon.Decode(r)
	if err != nil {
		return ctx, d, err
	}
	if proof.Capability != s.production.consent.Capabilities.QualificationDigest {
		return ctx, d, fail(403, nativecontract.ReasonQualificationMissing)
	}
	d.Actor = nativecontract.Actor{TenantID: p.TenantID, PrincipalID: p.ID, PrincipalKind: "agent", KeyCeiling: p.Scopes}
	d.DaemonID, d.DaemonGeneration, d.LedgerGeneration, d.CapabilityDigest = proof.DaemonID, proof.Generation, proof.Ledger, proof.Capability
	err = db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT c.id::text,q.approved_by::text FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.tenant_id=$1 AND c.principal_id=$2 AND c.key_id=$3 AND c.daemon_id=$4 AND c.ledger_generation=$5 AND c.state='connected' AND q.state='redeemed'`, p.TenantID, p.ID, p.KeyID, d.DaemonID, d.LedgerGeneration).Scan(&d.ComputerID, &d.OwnerPersonID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = fail(403, nativecontract.ReasonPermissionRevoked)
	}
	if err != nil {
		return ctx, d, err
	}
	i, e, x := proof.Runtime()
	ctx = context.WithValue(ctx, proofKey{}, proof)
	ctx = context.WithValue(ctx, daemonKey{}, d)
	return receipts.WithRuntimeProof(ctx, receipts.RuntimeProof{WorkerLease: proof.WorkerLease, Identity: i, Evidence: e, Exit: x}), d, nil
}

func (s *Store) Persist(ctx context.Context, in nativecontract.FinalDelivery) (nativecontract.FinalReceipt, error) {
	d, ok := ctx.Value(daemonKey{}).(nativecontract.DaemonActor)
	if !ok || s.production == nil {
		return nativecontract.FinalReceipt{}, fail(403, nativecontract.ReasonGrantMissing)
	}
	return s.production.receipts.Persist(ctx, d, in)
}
func (s *Store) Receipts(ctx context.Context, a nativecontract.Actor, b nativecontract.Binding, q nativecontract.PageQuery) (nativecontract.ReceiptPage, error) {
	if s.production == nil {
		return nativecontract.ReceiptPage{}, fail(503, nativecontract.ReasonQualificationMissing)
	}
	return s.production.receipts.Receipts(ctx, a, b, q)
}

// resolveLocal selects only an existing exact person-issued intent. The local
// request ID is a private lookup selector, never an original-person credential.
func (s *Store) resolveLocal(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Claim) (nativecontract.Claim, error) {
	proof, ok := ctx.Value(proofKey{}).(daemon.Proof)
	if !ok || proof.LocalStart == nil {
		return in, nil
	}
	local := *proof.LocalStart
	if in.OperationID != local.Intent.RequestID || local.Intent.ComputerID != d.ComputerID {
		return in, fail(403, nativecontract.ReasonIdentityMismatch)
	}
	if err := s.validate("NativeLocalStart", local); err != nil {
		return in, err
	}
	err := db.InTenant(ctx, s.pool, d.TenantID, func(tx pgx.Tx) error {
		var op string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM native_session_intents WHERE tenant_id=$1 AND request_id=$2`, d.TenantID, local.Intent.RequestID).Scan(&op); err != nil {
			return err
		}
		r, err := loadPending(ctx, tx, op, false)
		if err != nil {
			return err
		}
		if r.actor.Principal.Kind != tenant.Person || r.pairedPrincipal != d.PrincipalID || r.Intent.ComputerID != d.ComputerID || !reflect.DeepEqual(r.Intent, local.Intent) {
			return fail(403, nativecontract.ReasonPermissionRevoked)
		}
		in.OperationID = op
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = fail(403, nativecontract.ReasonPermissionRevoked)
	}
	return in, err
}

// Ready is represented by an applied start receipt. It requires the private
// lease plus an actual runtime identity; accepted/queued never imply running.
func (s *Store) reportStarted(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Receipt) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	if in.Binding == nil || in.Operation != "start" || in.State != "applied" {
		return out, fail(400, nativecontract.ReasonUnsupported)
	}
	b := *in.Binding
	s.mu.Lock()
	defer s.mu.Unlock()
	var digest string
	err := s.runtimeTx(ctx, d, b, false, func(ctx context.Context, tx pgx.Tx, session consent.Session, proof receipts.RuntimeProof) error {
		var operation, request string
		if err := tx.QueryRow(ctx, `SELECT id::text,request_id::text FROM native_session_intents WHERE owned_session_id=$1`, b.OwnedSessionID).Scan(&operation, &request); err != nil {
			return err
		}
		i := proof.Identity
		if operation != in.OperationID || request != in.RequestID || i.RuntimeEpoch != b.RuntimeEpoch || !nativereceipts.ValidDigest(i.ProcessIdentityDigest) || i.PID <= 1 || i.StartedAt.IsZero() || i.StartedAt.After(s.now()) || i.NativeSessionRef == "" {
			return fail(403, nativecontract.ReasonOwnershipUnconfirmed)
		}
		digest = i.ProcessIdentityDigest
		prior := s.runtimes[b.OwnedSessionID]
		if prior != "" && prior != digest {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		view := session.View
		changed := view.State == "starting"
		if changed {
			view.State = "running"
			view.Revision++
			raw, _ := json.Marshal(view)
			if _, err := tx.Exec(ctx, `UPDATE native_session_bindings SET view=$2 WHERE id=$1`, b.OwnedSessionID, raw); err != nil {
				return err
			}
		}
		if view.State != "running" && view.State != "idle" {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		out = nativecontract.OperationResult{Receipt: in, Session: &view}
		if changed {
			p, _ := tenant.PrincipalFrom(ctx)
			return appendEvent(ctx, tx, p, "native_session.receipt_changed", b.ProjectID, in.OperationID, in.RequestID, view)
		}
		return nil
	})
	if err == nil {
		s.runtimes[b.OwnedSessionID] = digest
		err = s.production.relay.Register(*out.Session)
	}
	return out, err
}

// Verified no-fork abandonment is a rejected start receipt from the exact
// paired owner, sent only before its possible-fork marker. It releases a root
// never reported running and records why. Withdrawn consent is one of its
// causes, so it needs none; it grants no launch, input or final authority.
func (s *Store) reportAbandoned(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Receipt) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	if in.Binding == nil || in.Reason == nil || in.SourceSequence != "0" || in.InputID != nil || in.TurnRef != nil || in.Binding.RunID != nil {
		return out, fail(400, nativecontract.ReasonInvalidInput)
	}
	b := *in.Binding
	reason := *in.Reason
	p, err := authenticated(ctx, d.Actor)
	if err != nil {
		return out, err
	}
	s.mu.Lock()
	started := s.runtimes[b.OwnedSessionID] != ""
	s.mu.Unlock()
	if started {
		return out, fail(409, nativecontract.ReasonOwnershipUnconfirmed)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// The locked binding row serialises this against reportStarted: whichever
	// commits first moves the view out of "starting" and refuses the other.
	err = db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		r, err := loadPending(ctx, tx, in.OperationID, true)
		if err != nil {
			return err
		}
		if r.Intent.RequestID != in.RequestID || r.view.CurrentBinding == nil || !r.view.CurrentBinding.Equal(b) || r.pairedPrincipal != d.PrincipalID || r.daemon != d.DaemonID || r.runID != nil {
			return fail(409, nativecontract.ReasonIdentityMismatch)
		}
		if err = s.production.authority.AbandonTx(ctx, tx, d, b, r.Intent, r.view); err != nil {
			return err
		}
		out = nativecontract.OperationResult{Receipt: in, Session: &r.view}
		if r.view.State == "failed" {
			return nil // A lost reply: the first report already released the root.
		}
		// Only a start never reported running is settled; anything later is a
		// possible owned writer and keeps its hold until its confirmed exit.
		if r.view.State != "starting" {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		// No provider opened: the root's occupancy, its exact quota holds and
		// its period-usage estimate are all returned in this transaction.
		now := s.now()
		if err = agentaccounts.AbandonNativeRootTx(ctx, tx, r.Intent.AccountID, b.OwnedSessionID, b.RuntimeEpoch, now); err != nil {
			var refusal *nativecontract.ValidationError
			if errors.As(err, &refusal) {
				return fail(409, refusal.Reason)
			}
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE harness_sessions SET phase='stopped',stopped_at=coalesce(stopped_at,$2),stop_reason=coalesce(stop_reason,'stopped') WHERE id=$1`, b.HarnessSessionID, now); err != nil {
			return err
		}
		view := r.view
		view.State, view.Reason = "failed", &reason
		view.Revision++
		raw, _ := json.Marshal(view)
		if _, err = tx.Exec(ctx, `UPDATE native_session_bindings SET view=$2 WHERE id=$1`, b.OwnedSessionID, raw); err != nil {
			return err
		}
		out.Session = &view
		return appendEvent(ctx, tx, p, "native_session.stopped", b.ProjectID, in.OperationID, in.RequestID, view)
	})
	return out, err
}

func (s *Store) runtimeTx(ctx context.Context, d nativecontract.DaemonActor, b nativecontract.Binding, draining bool, fn func(context.Context, pgx.Tx, consent.Session, receipts.RuntimeProof) error) error {
	return s.runtime(ctx, d, b, draining, false, fn)
}

// stopTx serves only the exact owner's stop (its listing, claim and exit):
// withdrawn consent passes fn a StopOnly session instead of a refusal, while
// the paired lease and worker permission are still required. fn must refuse
// anything but a stop for a StopOnly session.
func (s *Store) stopTx(ctx context.Context, d nativecontract.DaemonActor, b nativecontract.Binding, draining bool, fn func(context.Context, pgx.Tx, consent.Session, receipts.RuntimeProof) error) error {
	return s.runtime(ctx, d, b, draining, true, fn)
}

func (s *Store) runtime(ctx context.Context, d nativecontract.DaemonActor, b nativecontract.Binding, draining, stop bool, fn func(context.Context, pgx.Tx, consent.Session, receipts.RuntimeProof) error) error {
	if s.production == nil {
		return fail(503, nativecontract.ReasonQualificationMissing)
	}
	p, err := authenticated(ctx, d.Actor)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		authority := s.production.authority.RuntimeTx
		if stop {
			authority = s.production.authority.StopRuntimeTx
		}
		session, proof, err := authority(ctx, tx, d, b, draining)
		if err != nil {
			return err
		}
		if local, ok := ctx.Value(proofKey{}).(daemon.Proof); ok && local.LocalStart != nil {
			if !reflect.DeepEqual(local.LocalStart.Intent, session.Intent) {
				return fail(403, nativecontract.ReasonIdentityMismatch)
			}
			// The local profile guards launch and input; a stop-only session has neither.
			if !session.StopOnly {
				if err := s.production.consent.RequireLocalProfileTx(ctx, tx, session.Actor, session.Intent, session.Policy, nativecontract.LocalProfile{AccountID: session.Intent.AccountID, WorkspaceRoot: local.WorkspaceRoot}, local.LocalStart.Cwd); err != nil {
					return err
				}
			}
		}

		if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: b.ProjectID}); err != nil {
			return fail(403, nativecontract.ReasonPermissionRevoked)
		}
		return fn(ctx, tx, session, proof)
	})
}

// Start uses the HTTP route selector when called by the public module.
func productionProject(ctx context.Context) context.Context {
	if p := nativesessions.RouteProject(ctx); p != "" {
		return WithProject(ctx, p)
	}
	return ctx
}

var _ nativecontract.Store = (*Store)(nil)
var _ nativecontract.FinalSink = (*Store)(nil)
