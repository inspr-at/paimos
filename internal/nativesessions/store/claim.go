// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	privateDaemon "github.com/inspr-at/paimos/internal/nativesessions/daemon"
	"github.com/inspr-at/paimos/internal/nativesessions/receipts"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// Expose the closed native reason to transport error mapping while retaining
// Failure's precise HTTP status (including indistinguishable read 404s).
func (e *Failure) Unwrap() error {
	return &nativecontract.ValidationError{Reason: e.Reason, Path: "$"}
}

type pendingRecord struct {
	PendingStart
	actor                   actorRecord
	policy                  nativecontract.Policy
	launch                  nativecontract.LaunchConfig
	view                    nativecontract.SessionView
	pairedPrincipal, daemon string
	runID                   *string
	claimDigest             []byte
	claimExpires            *time.Time
}

func loadPending(ctx context.Context, tx pgx.Tx, operation string, lock bool) (pendingRecord, error) {
	var r pendingRecord
	var actorRaw, intentRaw, policyRaw, launchRaw, viewRaw []byte
	query := `SELECT i.id::text,i.owned_session_id::text,i.project_id::text,i.owner_person_id::text,i.actor_authority,i.intent,i.policy,i.launch_config,
 b.view,b.paired_principal_id::text,b.daemon_id,i.claim_digest,i.claim_expires_at,b.run_id::text FROM native_session_intents i
 JOIN native_session_bindings b ON b.tenant_id=i.tenant_id AND b.id=i.owned_session_id WHERE i.id=$1`
	if lock {
		query += ` FOR NO KEY UPDATE OF i,b`
	}
	err := tx.QueryRow(ctx, query, operation).Scan(&r.OperationID, &r.OwnedSessionID, &r.ProjectID, &r.OwnerPersonID, &actorRaw, &intentRaw, &policyRaw, &launchRaw, &viewRaw, &r.pairedPrincipal, &r.daemon, &r.claimDigest, &r.claimExpires, &r.runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fail(404, nativecontract.ReasonInvalidInput)
	}
	if err != nil {
		return r, err
	}
	for _, v := range []struct {
		raw []byte
		dst any
	}{{actorRaw, &r.actor}, {intentRaw, &r.Intent}, {policyRaw, &r.policy}, {launchRaw, &r.launch}, {viewRaw, &r.view}} {
		if len(v.raw) > 131072 {
			return r, fail(413, nativecontract.ReasonBufferFull)
		}
		if err = json.Unmarshal(v.raw, v.dst); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (s *Store) Claim(ctx context.Context, daemon nativecontract.DaemonActor, in nativecontract.Claim) (nativecontract.ClaimResult, error) {
	var out nativecontract.ClaimResult
	if len(in.RequestID) > 36 || len(in.OperationID) > 36 || len(in.DaemonID) > 128 || len(in.DaemonGeneration) > 128 || len(in.LedgerGeneration) > 128 || len(in.CapabilityDigest) > 64 || len(in.ClaimKind) > 32 {
		return out, fail(413, nativecontract.ReasonBufferFull)
	}
	if err := s.validate("NativeClaim", in); err != nil {
		return out, err
	}
	if s.production != nil && in.ClaimKind == "final_drain" {
		return s.claimDrain(ctx, daemon, in)
	}
	if s.production != nil && in.ClaimKind == "command" {
		return s.claimCommand(ctx, daemon, in)
	}
	if s.production != nil && in.ClaimKind == "resume" {
		return s.claimSuccessor(ctx, daemon, in)
	}
	if s.production != nil && in.ClaimKind == "start" {
		var err error
		in, err = s.resolveLocal(ctx, daemon, in)
		if err != nil {
			return out, err
		}
	}
	if in.ClaimKind != "start" {
		return out, fail(400, nativecontract.ReasonUnsupported)
	}
	p, err := authenticated(ctx, daemon.Actor)
	if err != nil {
		return out, err
	}
	if p.Kind != tenant.Agent {
		return out, fail(403, nativecontract.ReasonWrongPrincipal)
	}
	if in.DaemonID != daemon.DaemonID || in.DaemonGeneration != daemon.DaemonGeneration || in.LedgerGeneration != daemon.LedgerGeneration || in.CapabilityDigest != daemon.CapabilityDigest {
		return out, fail(403, nativecontract.ReasonWrongGeneration)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var before pendingRecord
	err = db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		var e error
		before, e = loadPending(ctx, tx, in.OperationID, false)
		if e != nil {
			return e
		}
		if before.pairedPrincipal != p.ID || before.Intent.ComputerID != daemon.ComputerID || before.daemon != daemon.DaemonID {
			return fail(403, nativecontract.ReasonWrongPrincipal)
		}
		if e = authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: before.ProjectID}); e != nil {
			return fail(403, nativecontract.ReasonPermissionRevoked)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	// Local observation/network reads never hold a final mutation transaction.
	observed, err := s.observer.Observe(ctx, daemon, before.PendingStart)
	if err != nil {
		return out, err
	}
	if observed.TerminalViewerURL != nil {
		if err = s.viewerHosts.Validate(*observed.TerminalViewerURL); err != nil {
			return out, err
		}
	}
	claimRaw, _ := json.Marshal(in)
	proofRaw, _ := json.Marshal(struct {
		Principal, Computer, Reservation string
		Ref, Lease                       []byte
		Viewer                           *string
	}{p.ID, daemon.ComputerID, observed.LedgerReservationID, observed.SessionRefDigest, observed.WorkerLeaseDigest, observed.TerminalViewerURL})
	sum := sha256.Sum256(append(claimRaw, proofRaw...))
	digest := sum[:]
	// Include the original delegated key in the admission batch BEFORE tenant
	// locks. RequireTx then re-enters its existing fence after target locking.
	if before.actor.KeyID != "" {
		ctx = db.WithKeyScopeUse(ctx, p.TenantID, before.actor.KeyID)
	}
	finalCtx, finalCancel := context.WithTimeout(ctx, 3*time.Second)
	defer finalCancel()
	err = db.InTenant(finalCtx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := finalCtx
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		r, err := loadPending(ctx, tx, in.OperationID, true)
		if err != nil {
			return err
		}
		if r.actor.KeyID != before.actor.KeyID {
			return fail(409, nativecontract.ReasonIdentityMismatch)
		}
		if r.pairedPrincipal != p.ID || r.Intent.ComputerID != daemon.ComputerID || r.daemon != daemon.DaemonID {
			return fail(403, nativecontract.ReasonWrongPrincipal)
		}
		if err = authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: r.ProjectID}); err != nil {
			return fail(403, nativecontract.ReasonPermissionRevoked)
		}
		// The paired daemon executes with its ordinary runtime ceiling. Control
		// and the native grant belong to the original actor, re-read below under
		// the same access fence; they are never delegated to this runtime key.
		if err = requireStart(ctx, tx, r.actor.principal(), r.ProjectID); err != nil {
			return err
		}
		grant, err := s.authorize(ctx, tx, r.actor.actor(), r.ProjectID, r.Intent)
		if err != nil {
			return err
		}
		if s.production != nil {
			if proof, ok := ctx.Value(proofKey{}).(privateDaemon.Proof); ok && proof.LocalStart != nil && proof.WorkspaceRoot != "" {
				if err = s.production.consent.RequireLocalProfileTx(ctx, tx, r.actor.actor(), r.Intent, r.policy, nativecontract.LocalProfile{AccountID: r.Intent.AccountID, WorkspaceRoot: proof.WorkspaceRoot}, proof.LocalStart.Cwd); err != nil {
					return err
				}
			}
		}
		owner, err := workqueue.ResolveRunPersonTx(ctx, tx, p, "native_intent", r.OperationID)
		if err != nil {
			return err
		}
		if owner != grant.OwnerPersonID {
			return fail(409, nativecontract.ReasonIdentityMismatch)
		}
		if err := authz.RequireTx(ctx, tx, tenant.Principal{TenantID: p.TenantID, ID: owner, Kind: tenant.Person}, "nodes.read", authz.Scope{ProjectID: r.ProjectID}); err != nil {
			return fail(403, nativecontract.ReasonPermissionRevoked)
		}
		if grant.RunID != nil {
			var runPerson *string
			if err := tx.QueryRow(ctx, `SELECT acting_person_id::text FROM agent_runs WHERE tenant_id=$1 AND id=$2`, p.TenantID, *grant.RunID).Scan(&runPerson); err != nil {
				return err
			}
			if runPerson != nil && *runPerson != owner {
				return fail(409, nativecontract.ReasonIdentityMismatch)
			}

			if err = workqueue.RequireRunPersonTx(ctx, tx, p, *grant.RunID); err != nil {
				if errors.Is(err, authz.ErrForbidden) {
					return fail(403, nativecontract.ReasonPermissionRevoked)
				}
				return err
			}
		}
		if grant.OwnerPersonID != r.OwnerPersonID || !samePolicy(grant.Policy, r.policy) || !reflect.DeepEqual(grant.Launch, r.launch) || !reflect.DeepEqual(grant.RunID, r.runID) {
			return fail(409, nativecontract.ReasonIdentityMismatch)
		}
		if err = targetStart(ctx, tx, r.ProjectID, r.Intent, true); err != nil {
			return err
		}
		if err = agentpairing.RequireLedgerWork(ctx, tx, p, daemon.LedgerGeneration); err != nil {
			return fail(409, nativecontract.ReasonWrongGeneration)
		}
		now := s.now()
		if err = validateObservation(observed, daemon, r.PendingStart, now); err != nil {
			return err
		}
		// Revalidate paired generations even on an exact replay. Never transfer
		// launch authority to a different daemon, account or local ledger.
		if err = currentDaemon(ctx, tx, daemon, r); err != nil {
			return err
		}
		admit := agentaccounts.NativeAdmission{TenantID: p.TenantID, ProjectID: r.ProjectID, OwnerPersonID: r.OwnerPersonID, PrincipalID: p.ID, OwnedSessionID: r.OwnedSessionID, AccountID: r.Intent.AccountID, ComputerID: r.Intent.ComputerID, Harness: r.Intent.Harness, ModelProfileID: r.Intent.ModelProfileID, DaemonID: daemon.DaemonID, DaemonGeneration: daemon.DaemonGeneration, LedgerGeneration: daemon.LedgerGeneration, WorkID: r.Intent.WorkID, RunID: grant.RunID}
		if len(r.claimDigest) > 0 {
			if !bytes.Equal(r.claimDigest, digest) {
				return fail(409, nativecontract.ReasonIdentityMismatch)
			}
			if r.claimExpires == nil || !now.Before(*r.claimExpires) {
				return fail(409, nativecontract.ReasonGrantExpired)
			}
			if r.view.CurrentBinding == nil || r.view.Revision != in.ExpectedRevision+1 {
				return fail(409, nativecontract.ReasonStaleRevision)
			}
			if !reflect.DeepEqual(r.view.CurrentBinding.RunID, grant.RunID) {
				return fail(409, nativecontract.ReasonIdentityMismatch)
			}
			if _, err = nativeCapacity(ctx, tx, admit, r.OwnedSessionID, now); err != nil {
				return err
			}
			out = claimResult(r, in, *r.claimExpires, now)
			return rootID(ctx, tx, &out)
		}
		if r.view.Revision != in.ExpectedRevision || r.view.CurrentBinding != nil {
			return fail(409, nativecontract.ReasonStaleRevision)
		}
		budget, err := nativeCapacity(ctx, tx, admit, "", now)
		if err != nil {
			return err
		}
		harnessID, err := uuid()
		if err != nil {
			return err
		}
		epoch, err := uuid()
		if err != nil {
			return err
		}
		binding := nativecontract.Binding{TenantID: p.TenantID, ProjectID: r.ProjectID, OwnedSessionID: r.OwnedSessionID, HarnessSessionID: harnessID, RuntimeEpoch: epoch, DaemonID: daemon.DaemonID, DaemonGeneration: daemon.DaemonGeneration, BindingRevision: 2, RunID: grant.RunID}
		var workOrder *string
		if grant.RunID != nil {
			if err = tx.QueryRow(ctx, `SELECT work_order_id::text FROM agent_runs WHERE id=$1`, *grant.RunID).Scan(&workOrder); err != nil {
				return err
			}
			tag, e := tx.Exec(ctx, `UPDATE agent_runs SET status='starting',daemon_id=$2,daemon_generation=$3,started_at=clock_timestamp() WHERE id=$1 AND status='queued'`, *grant.RunID, daemon.DaemonID, daemon.DaemonGeneration)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return fail(409, nativecontract.ReasonStaleRevision)
			}
		}
		root, err := agentaccounts.ReserveNativeRootTx(ctx, tx, admit, r.OwnedSessionID, epoch, budget)
		if err != nil {
			return err
		}
		shape := "unknown"
		if r.Intent.Role == "worker" {
			shape = "ship"
		}
		// The immutable harness generation is registered as the paired computer
		// principal, not the earlier person/CLI registration principal.
		_, err = tx.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,run_id,ticket_node_id,work_order_id,parent_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,owner_principal_id,model_profile_id,model,reasoning_effort,started_by_principal_id)
 VALUES($1,$2,$3,$4,$5,$6,$20,$7,$8,$9,'managed',$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, p.TenantID, harnessID, r.ProjectID, p.ID, grant.RunID, r.Intent.WorkID, r.Intent.ParentHarnessSessionID, r.Intent.Harness, daemon.ComputerID, r.Intent.Role, shape, []string{"interrupt", "stop", "chat"}, observed.SessionRefDigest, observed.WorkerLeaseDigest, r.OwnerPersonID, r.Intent.ModelProfileID, r.launch.Model, r.launch.Effort, r.actor.Principal.ID, workOrder)
		if err != nil {
			return err
		}
		r.view.CurrentBinding = &binding
		r.view.Revision = 2
		r.view.State = "starting"
		r.view.Mode = "structured"
		r.view.Capabilities = grant.Capabilities
		viewRaw, _ := json.Marshal(r.view)
		_, err = tx.Exec(ctx, `UPDATE native_session_bindings SET run_id=$2,current_harness_session_id=$3,runtime_epoch=$4,daemon_generation=$5,ledger_generation=$6,binding_revision=2,view=$7,terminal_viewer_url=$8 WHERE id=$1`, r.OwnedSessionID, grant.RunID, harnessID, epoch, daemon.DaemonGeneration, daemon.LedgerGeneration, viewRaw, observed.TerminalViewerURL)
		if err != nil {
			return err
		}
		expires := now.Add(20 * time.Second)
		if r.policy.ExpiresAt.Before(expires) {
			expires = r.policy.ExpiresAt
		}
		if observed.ExpiresAt.Before(expires) {
			expires = observed.ExpiresAt
		}
		_, err = tx.Exec(ctx, `UPDATE native_session_intents SET claim_request_id=$2,claim_digest=$3,claim_expires_at=$4 WHERE id=$1`, r.OperationID, in.RequestID, digest, expires)
		if err != nil {
			return err
		}
		out = claimResult(r, in, expires, now)
		out.RootReservationID = root
		if err = s.validate("NativeClaimResult", out); err != nil {
			return err
		}
		return appendEvent(ctx, tx, p, "native_session.receipt_changed", r.ProjectID, r.OperationID, in.RequestID, r.view)
	})
	if err != nil {
		return nativecontract.ClaimResult{}, err
	}
	return out, nil
}

func samePolicy(a, b nativecontract.Policy) bool {
	expiresA, expiresB := a.ExpiresAt, b.ExpiresAt
	a.ExpiresAt = time.Time{}
	b.ExpiresAt = time.Time{}
	return a == b && expiresA.Equal(expiresB)
}

func validateObservation(o Observation, d nativecontract.DaemonActor, p PendingStart, now time.Time) error {
	if o.OperationID != p.OperationID || o.PrincipalID != d.PrincipalID || o.ComputerID != d.ComputerID || o.AccountID != p.Intent.AccountID || o.WorkspaceID != p.Intent.WorkspaceID {
		return fail(403, nativecontract.ReasonIdentityMismatch)
	}
	if o.DaemonID != d.DaemonID || o.DaemonGeneration != d.DaemonGeneration || o.LedgerGeneration != d.LedgerGeneration || o.CapabilityDigest != d.CapabilityDigest {
		return fail(409, nativecontract.ReasonWrongGeneration)
	}
	if !o.LedgerClear || !workorders.UUID(o.LedgerReservationID) {
		return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
	}
	if o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > 20*time.Second || !now.Before(o.ExpiresAt) {
		return fail(409, nativecontract.ReasonGrantExpired)
	}
	if len(o.SessionRefDigest) != 32 || len(o.WorkerLeaseDigest) != 32 || bytes.Equal(o.SessionRefDigest, o.WorkerLeaseDigest) {
		return fail(403, nativecontract.ReasonGrantMissing)
	}
	return nil
}

func nativeCapacity(ctx context.Context, tx pgx.Tx, admit agentaccounts.NativeAdmission, except string, now time.Time) (agentaccounts.NativeBudget, error) {
	// Occupancy includes sibling projects without exposing private facts.
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return agentaccounts.NativeBudget{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return agentaccounts.NativeBudget{}, err
	}
	budget, err := agentaccounts.AdmitNativeCapacityTx(ctx, tx, admit, except, now)
	if err != nil {
		return agentaccounts.NativeBudget{}, err
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return budget, err
}

func currentDaemon(ctx context.Context, tx pgx.Tx, d nativecontract.DaemonActor, r pendingRecord) error {
	var principal, daemon, generation, ledger string
	err := tx.QueryRow(ctx, `SELECT c.principal_id::text,c.daemon_id,coalesce(a.last_daemon_generation,''),coalesce(c.ledger_generation,'')
 FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id
 JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
 WHERE c.id=$1 AND a.id=$2 AND c.key_id=$3 AND c.state='connected' AND q.state='redeemed' AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL
 FOR NO KEY UPDATE OF c`, d.ComputerID, r.Intent.AccountID, rDaemonKey(ctx)).Scan(&principal, &daemon, &generation, &ledger)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(403, nativecontract.ReasonPermissionRevoked)
	}
	if err != nil {
		return err
	}
	if principal != d.PrincipalID || daemon != d.DaemonID {
		return fail(403, nativecontract.ReasonWrongPrincipal)
	}
	if generation != d.DaemonGeneration || ledger == "" || ledger != d.LedgerGeneration {
		return fail(409, nativecontract.ReasonWrongGeneration)
	}
	return nil
}

func rDaemonKey(ctx context.Context) string { p, _ := tenant.PrincipalFrom(ctx); return p.KeyID }

func claimResult(r pendingRecord, in nativecontract.Claim, expires, now time.Time) nativecontract.ClaimResult {
	return nativecontract.ClaimResult{OperationID: r.OperationID, OwnedSessionID: r.OwnedSessionID, Intent: r.Intent, Policy: r.policy, ExpectedBinding: r.view.CurrentBinding, ExpiresAt: expires, ClaimKind: in.ClaimKind, LaunchAuthorized: true, LaunchConfig: r.launch, AuthorizationTTLMS: max(1, expires.Sub(now).Milliseconds())}
}

func rootID(ctx context.Context, tx pgx.Tx, out *nativecontract.ClaimResult) error {
	if out.ExpectedBinding.RunID != nil {
		return nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM native_session_roots WHERE owned_session_id=$1 AND runtime_epoch=$2 AND released_at IS NULL AND run_id IS NULL`, out.OwnedSessionID, out.ExpectedBinding.RuntimeEpoch).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err != nil {
		return err
	}
	out.RootReservationID = &id
	return nil
}

// claimSuccessor exchanges one already person-authorized transition for a fresh
// epoch only after the owning daemon proves the exact predecessor's group exit.
// The existing root and its aggregate budget remain held across the switch.
// No native I/O occurs in this transaction. A server restart loses the live
// command and refuses it; persisted metadata cannot grant another fork.
func (s *Store) claimSuccessor(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Claim) (nativecontract.ClaimResult, error) {
	var out nativecontract.ClaimResult
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	c, ok := s.commands[in.OperationID]
	if !ok {
		proof, _ := ctx.Value(proofKey{}).(privateDaemon.Proof)
		identity, _, _ := proof.Runtime()
		for operation, candidate := range s.commands {
			if candidate.command.Kind == "resume" && candidate.intent.Receipt.RequestID == in.OperationID && candidate.intent.Receipt.Binding != nil && candidate.intent.Receipt.Binding.TenantID == d.TenantID && candidate.intent.Receipt.Binding.RuntimeEpoch == identity.RuntimeEpoch {
				in.OperationID = operation
				c, ok = candidate, true
				break
			}
		}
	}
	if !ok || c.done || c.intent.Receipt.Binding == nil {
		return out, fail(403, nativecontract.ReasonGrantMissing)
	}
	command, err := queuedControl(c)
	if err != nil {
		return out, err
	}
	b := *c.intent.Receipt.Binding
	next, ok := ctx.Value(successorProofKey{}).(privateDaemon.Proof)
	if !ok || in.ExpectedRevision != b.BindingRevision || in.DaemonID != d.DaemonID || in.DaemonGeneration != d.DaemonGeneration || in.LedgerGeneration != d.LedgerGeneration || in.CapabilityDigest != d.CapabilityDigest {
		return out, fail(403, nativecontract.ReasonGrantMissing)
	}
	now := s.now()
	if !now.Before(c.intent.ExpiresAt) || !now.Before(next.ExpiresAt) || next.ObservedAt.After(now) || now.Sub(next.ObservedAt) > 20*time.Second {
		return out, fail(403, nativecontract.ReasonGrantExpired)
	}
	if c.successor != nil {
		if !reflect.DeepEqual(next, c.successorProof) || !now.Before(c.successor.ExpiresAt) {
			return out, fail(409, nativecontract.ReasonIdentityMismatch)
		}
		// Lost admission reply: only this exact successor lease can reread it.
		ctx = receipts.WithRuntimeProof(ctx, receipts.RuntimeProof{WorkerLease: next.WorkerLease})
		err = s.runtimeTx(ctx, d, *c.successor.ExpectedBinding, false, func(ctx context.Context, tx pgx.Tx, session consent.Session, _ receipts.RuntimeProof) error {
			if session.View.State != "starting" {
				return fail(409, nativecontract.ReasonStaleRevision)
			}
			return stream.TakeAuthorityTx(ctx, tx, c.actor, session.OwnerPersonID, b.ProjectID)
		})
		out = *c.successor
		out.AuthorizationTTLMS = max(0, out.ExpiresAt.Sub(now).Milliseconds())
		return out, err
	}
	err = s.runtimeTx(ctx, d, b, true, func(ctx context.Context, tx pgx.Tx, session consent.Session, proof receipts.RuntimeProof) error {
		if err := s.controlTx(ctx, tx, session, c.actor, command); err != nil {
			return err
		}
		i, x := proof.Identity, proof.Exit
		if i.RuntimeEpoch != b.RuntimeEpoch || i.ProcessIdentityDigest != c.intent.ProcessIdentityDigest || x == nil || !x.RootExited || !x.GroupAbsent || x.ProcessIdentityDigest != i.ProcessIdentityDigest || x.ObservedAt.IsZero() || x.ObservedAt.After(now) || now.Sub(x.ObservedAt) > 20*time.Second {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		var operation string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM native_session_intents WHERE owned_session_id=$1`, b.OwnedSessionID).Scan(&operation); err != nil {
			return err
		}
		r, err := loadPending(ctx, tx, operation, true)
		if err != nil {
			return err
		}
		if err = currentDaemon(ctx, tx, d, r); err != nil {
			return err
		}
		epoch, err := uuid()
		if err != nil {
			return err
		}
		harness, err := uuid()
		if err != nil {
			return err
		}
		fresh := b
		fresh.RuntimeEpoch, fresh.HarnessSessionID = epoch, harness
		fresh.BindingRevision++
		if fresh.BindingRevision > 9007199254740991 || session.View.Generation >= 9007199254740991 || session.View.Revision >= 9007199254740990 {
			return fail(409, nativecontract.ReasonStaleRevision)
		}
		ref, lease := sha256.Sum256([]byte(next.SessionRef)), sha256.Sum256([]byte(next.WorkerLease))
		_, err = tx.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,run_id,ticket_node_id,work_order_id,parent_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,owner_principal_id,model_profile_id,model,reasoning_effort,started_by_principal_id)
 SELECT tenant_id,$2,project_id,agent_principal_id,run_id,ticket_node_id,work_order_id,parent_id,harness,host,management,role,work_shape,capabilities,$3,$4,owner_principal_id,model_profile_id,model,reasoning_effort,started_by_principal_id FROM harness_sessions WHERE tenant_id=$1 AND id=$5`, b.TenantID, harness, ref[:], lease[:], b.HarnessSessionID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE harness_sessions SET phase='stopped',stopped_at=$2,stop_reason='stopped' WHERE id=$1`, b.HarnessSessionID, x.ObservedAt); err != nil {
			return err
		}
		var root string
		err = tx.QueryRow(ctx, `UPDATE native_session_roots SET runtime_epoch=$3 WHERE owned_session_id=$1 AND runtime_epoch=$2 AND released_at IS NULL AND run_id IS NULL RETURNING id::text`, b.OwnedSessionID, b.RuntimeEpoch, epoch).Scan(&root)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		if err != nil {
			return err
		}
		view := session.View
		view.CurrentBinding = &fresh
		view.Revision++
		view.Generation++
		view.State = "starting"
		view.Mode = "structured"
		if command.Kind == "take" {
			view.Mode = "terminal"
		}
		view.Controller = nil
		view.TerminalViewerURL = nil
		raw, _ := json.Marshal(view)
		if _, err = tx.Exec(ctx, `UPDATE native_session_bindings SET current_harness_session_id=$2,runtime_epoch=$3,binding_revision=$4,view=$5,terminal_viewer_url=NULL WHERE id=$1`, b.OwnedSessionID, harness, epoch, fresh.BindingRevision, raw); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE native_session_controllers SET expires_at=$2 WHERE owned_session_id=$1`, b.OwnedSessionID, now); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE native_session_viewers SET detached_at=coalesce(detached_at,$2) WHERE owned_session_id=$1`, b.OwnedSessionID, now); err != nil {
			return err
		}
		expires := now.Add(20 * time.Second)
		for _, until := range []time.Time{c.intent.ExpiresAt, next.ExpiresAt, session.Policy.ExpiresAt} {
			if until.Before(expires) {
				expires = until
			}
		}
		out = nativecontract.ClaimResult{OperationID: in.OperationID, OwnedSessionID: b.OwnedSessionID, Intent: session.Intent, Policy: session.Policy, ExpectedBinding: &fresh, ExpiresAt: expires, ClaimKind: "resume", LaunchAuthorized: true, LaunchConfig: r.launch, AuthorizationTTLMS: expires.Sub(now).Milliseconds(), RootReservationID: &root}
		if err = s.validate("NativeClaimResult", out); err != nil {
			return err
		}
		p, _ := tenant.PrincipalFrom(ctx)
		return appendEvent(ctx, tx, p, "native_session.receipt_changed", b.ProjectID, in.OperationID, c.intent.Receipt.RequestID, view)
	})
	if err == nil {
		c.successor = &out
		c.successorProof = next
		s.commands[in.OperationID] = c
	}
	return out, err
}

// reportSuccessor records actual owned runtime readiness, then emits exactly
// one mode-change audit line attributed to the person who requested the switch.
func (s *Store) reportSuccessor(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Receipt) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	if in.Binding == nil || in.State != "applied" || in.SourceSequence != "0" || in.InputID != nil || in.TurnRef != nil || in.Reason != nil {
		return out, fail(400, nativecontract.ReasonInvalidInput)
	}
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	c, ok := s.commands[in.OperationID]
	if !ok || c.successor == nil || c.successor.ExpectedBinding == nil || !c.successor.ExpectedBinding.Equal(*in.Binding) || c.intent.Receipt.RequestID != in.RequestID || c.intent.Receipt.Operation != in.Operation {
		return out, fail(409, nativecontract.ReasonIdentityMismatch)
	}
	err := s.runtimeTx(ctx, d, *in.Binding, false, func(ctx context.Context, tx pgx.Tx, session consent.Session, proof receipts.RuntimeProof) error {
		if err := stream.TakeAuthorityTx(ctx, tx, c.actor, session.OwnerPersonID, in.Binding.ProjectID); err != nil {
			return err
		}
		i := proof.Identity
		if i.RuntimeEpoch != in.Binding.RuntimeEpoch || i.PID <= 1 || i.StartedAt.IsZero() || i.StartedAt.After(s.now()) || !nativereceipts.ValidDigest(i.ProcessIdentityDigest) || i.ProcessIdentityDigest == c.intent.ProcessIdentityDigest || i.NativeSessionRef == "" {
			return fail(409, nativecontract.ReasonOwnershipUnconfirmed)
		}
		view := session.View
		out = nativecontract.OperationResult{Receipt: in, Session: &view}
		if c.done {
			return nil
		}
		if !s.now().Before(c.successor.ExpiresAt) || view.State != "starting" {
			return fail(409, nativecontract.ReasonStaleRevision)
		}
		view.State = "idle"
		if in.Operation == "take" {
			view.State = "taken"
		}
		view.Revision++
		raw, _ := json.Marshal(view)
		if _, err := tx.Exec(ctx, `UPDATE native_session_bindings SET view=$2 WHERE id=$1`, in.Binding.OwnedSessionID, raw); err != nil {
			return err
		}
		out.Session = &view
		kind := "native_session.mode_changed"
		if in.Operation == "resume" {
			kind = "native_session.recovered"
		}
		return appendEvent(ctx, tx, c.actor, kind, in.Binding.ProjectID, in.OperationID, in.RequestID, view)
	})
	if err == nil {
		s.mu.Lock()
		s.runtimes[in.Binding.OwnedSessionID] = ctxRuntimeIdentity(ctx)
		s.mu.Unlock()
		c.done = true
		c.command.Params = nil
		s.commands[in.OperationID] = c
		err = s.production.relay.Register(*out.Session)
	}
	return out, err
}

func ctxRuntimeIdentity(ctx context.Context) string {
	proof, _ := ctx.Value(proofKey{}).(privateDaemon.Proof)
	i, _, _ := proof.Runtime()
	return i.ProcessIdentityDigest
}
