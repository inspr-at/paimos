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
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

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
		if err = authz.RequireTx(ctx, tx, p, "harness.control", authz.Scope{ProjectID: r.ProjectID}); err != nil {
			return fail(403, nativecontract.ReasonPermissionRevoked)
		}
		if err = requireStart(ctx, tx, r.actor.principal(), r.ProjectID); err != nil {
			return err
		}
		grant, err := s.authorize(ctx, tx, r.actor.actor(), r.ProjectID, r.Intent)
		if err != nil {
			return err
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
 VALUES($1,$2,$3,$4,$5,$6,$20,$7,$8,$9,'managed',$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, p.TenantID, harnessID, r.ProjectID, p.ID, grant.RunID, r.Intent.WorkID, r.Intent.ParentHarnessSessionID, r.Intent.Harness, daemon.ComputerID, r.Intent.Role, shape, []string{"interrupt", "stop"}, observed.SessionRefDigest, observed.WorkerLeaseDigest, r.OwnerPersonID, r.Intent.ModelProfileID, r.launch.Model, r.launch.Effort, r.actor.Principal.ID, workOrder)
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
