// SPDX-License-Identifier: AGPL-3.0-only

// Package routineexec consumes durable routine demand. It assigns and writes
// launch outboxes; it never forks a process, calls a model or enables a project.
package routineexec

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/engineadmission"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/routinebudget"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const batchSize = 32
const recoveryInterval = 30 * time.Second

// Options are internal collaborators installed before serving. Missing exact
// runtime, qualified gate ages or CI readings means WAIT. No HTTP setting can
// replace a collaborator or turn an advisory verdict into authority.
type Options struct {
	Reviews       *crossreview.Module
	LeadRuntime   harness.LeadRuntimeChecks
	Runtime       modelregistry.ExecutionRuntimeReader
	Gates         routinebudget.GateReader
	LeadAdmission harness.LeadAdmission
	Facts         engineadmission.RoutineFactsReader
}
type Executor struct {
	pool    *pgxpool.Pool
	options Options
}

func New(pool *pgxpool.Pool, options Options) *Executor {
	return &Executor{pool: pool, options: options}
}

// Bind installs the same executor for leased coordinator pickup and scheduler
// recovery, with fail-closed final-claim checks even before runtime qualification.
func (e *Executor) Bind(module httpapi.Module) error {
	if err := agentruns.BindRoutineCompletion(module, e.recordCompletion); err != nil {
		return err
	}
	if err := agentruns.BindRoutineQueue(module, e.DispatchTx); err != nil {
		return err
	}
	checks := e.LaunchChecks()
	if checks.Runtime == nil {
		checks.Runtime = func(context.Context, pgx.Tx, string) (modelregistry.ExecutionRuntime, error) {
			return modelregistry.ExecutionRuntime{}, errors.New("routine runtime unavailable")
		}
	}
	return agentruns.BindRoutineLaunch(module, checks)
}

// BindHarness keeps daemon pickup under the same executor/qualification owners.
// An absent qualified actor reserver remains an explicit runtime wait. This
// seam never enables projects or treats worker launch checks as lead authority.
func (e *Executor) BindHarness(module *harness.Module) {
	checks := e.options.LeadRuntime
	checks.Consent = recurrences.RequireExecutionConsentTx
	module.WithRoutineExecutionRuntime(e.options.Runtime).WithLeadAdmission(e.options.LeadAdmission).WithLeadRuntime(checks)
}

// Pass reports partial work explicitly; a capped pass is never a complete scan.
type Pass struct {
	Scanned    int
	Dispatched int
	Waiting    int
	Partial    bool
}

// Run combines transactional S05 notifications with bounded polling recovery.
// One coalesced wake is enough: the durable outbox, rather than the notification
// payload, is the work source. Replicas serialize through the normal fences.
func (e *Executor) Run(ctx context.Context) {
	wakes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { defer close(done); e.listen(ctx, wakes) }()
	defer func() { <-done }()
	ticker := time.NewTicker(recoveryInterval)
	defer ticker.Stop()
	cursor := ""
	for ctx.Err() == nil {
		passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		rows, err := e.pool.Query(passCtx, `SELECT id::text FROM tenants WHERE id::text>$1 ORDER BY id LIMIT 101`, cursor)
		ids := []string{}
		if err == nil {
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					break
				}
				ids = append(ids, id)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		if err == nil {
			more := len(ids) > 100
			if more {
				ids = ids[:100]
			}
			for _, id := range ids {
				p, stepErr := e.RunTenant(passCtx, id)
				if stepErr != nil && ctx.Err() == nil {
					slog.Error("routine dispatch pass unavailable", "tenant_id", id)
				}
				if p.Partial {
					slog.Warn("routine dispatch scan partial", "tenant_id", id, "scanned", p.Scanned)
				}
				cursor = id
			}
			if !more {
				cursor = ""
			} else {
				slog.Warn("routine tenant scan partial", "scanned", len(ids))
			}
		} else if ctx.Err() == nil {
			slog.Error("routine dispatch scan unavailable")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wakes:
		}
	}
}

func (e *Executor) listen(ctx context.Context, wakes chan<- struct{}) {
	for ctx.Err() == nil {
		step, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := pgx.ConnectConfig(step, e.pool.Config().ConnConfig.Copy())
		if err == nil {
			_, err = conn.Exec(step, `LISTEN aeon_routine_intents`)
		}
		cancel()
		if err == nil {
			for ctx.Err() == nil {
				n, waitErr := conn.WaitForNotification(ctx)
				if waitErr != nil {
					break
				}
				if len(n.Payload) > 1024 {
					continue
				}
				var wake struct {
					TenantID string `json:"tenant_id"`
					RunID    string `json:"run_id"`
				}
				if json.Unmarshal([]byte(n.Payload), &wake) != nil || !workorders.UUID(wake.TenantID) || !workorders.UUID(wake.RunID) {
					continue
				}
				select {
				case wakes <- struct{}{}:
				default:
				}
			}
		}
		if conn != nil {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = conn.Close(closeCtx)
			closeCancel()
		}
		timer := time.NewTimer(recoveryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// RunTenant scans <=33 IDs, then commits <=32 independent final transactions.
// Disabled projects are excluded before any write: merging changes no legacy
// recurrence, queue, lead or delivery behavior. Waiting entries move behind due
// work via reconcile_after; there is no unbounded/always-first recovery scan.
func (e *Executor) RunTenant(ctx context.Context, tenantID string) (Pass, error) {
	out := Pass{}
	ctx, cancel := context.WithTimeout(db.AllProjects(ctx, "routine intent recovery"), 25*time.Second)
	defer cancel()
	ids := []string{}
	err := db.InTenant(ctx, e.pool, tenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT o.run_id::text FROM routine_effect_outbox o
 JOIN routine_runs r ON r.tenant_id=o.tenant_id AND r.id=o.run_id
 JOIN project_routine_execution_settings s ON s.tenant_id=r.tenant_id AND s.project_id=r.output_project_id
 WHERE s.automatic_launch_enabled AND o.effect_key='execute' AND o.kind='routine.execute' AND o.state='pending'
 AND r.state IN ('created','pending') AND o.reconcile_after<=clock_timestamp()
 ORDER BY o.reconcile_after,o.id LIMIT 33`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	if len(ids) > batchSize {
		out.Partial = true
		ids = ids[:batchSize]
	}
	var failures []error
	for _, id := range ids {
		out.Scanned++
		var result agentruns.RoutineQueueResult
		err := db.InTenant(ctx, e.pool, tenantID, func(tx pgx.Tx) error {
			var err error
			result, err = e.DispatchTx(ctx, tx, tenantID, id, "", 0)
			return err
		})
		if err != nil {
			failures = append(failures, err)
		} else if result.Routed {
			out.Dispatched++
		} else {
			out.Waiting++
		}
	}
	if ctx.Err() != nil {
		out.Partial = true
	}
	continued, err := e.continueTenant(ctx, tenantID)
	out.Scanned += continued.Scanned
	out.Dispatched += continued.Dispatched
	out.Waiting += continued.Waiting
	out.Partial = out.Partial || continued.Partial
	if err != nil {
		failures = append(failures, err)
	}
	return out, errors.Join(failures...)
}

type intent struct {
	id, recurrence, project, owner, principal, work, run, digest string
	revision, consentRevision                                    int64
	consent                                                      bool
	state                                                        string
	assignment                                                   []byte
}

func loadIntent(ctx context.Context, tx pgx.Tx, id string) (intent, error) {
	var r intent
	err := tx.QueryRow(ctx, `SELECT id::text,recurrence_id::text,output_project_id::text,owner_principal_id::text,
 coalesce(execution_principal_id::text,''),work_node_id::text,coalesce(agent_run_id::text,''),assignment_digest,
 definition_revision,consent_revision,execute_consent,state,
 CASE WHEN octet_length(assignment::text)<=131072 THEN assignment END FROM routine_runs WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&r.id, &r.recurrence, &r.project, &r.owner, &r.principal, &r.work, &r.run, &r.digest, &r.revision, &r.consentRevision, &r.consent, &r.state, &r.assignment)
	return r, err
}

// DispatchTx is also the coordinator callback. The scheduler supplies no
// generation override; the coordinator supplies the identity it acted on.
// Entry order: tenant -> tree -> pairing/account-use -> definition/lead/work/
// run/account/budget/intent rows -> event counter LAST. Admission and all writes
// are in a savepoint so WAIT retains no provisional route/hold/turn. The only
// later write is the already-fenced execute outbox's redacted result.
func (e *Executor) DispatchTx(ctx context.Context, tx pgx.Tx, tenantID, id, session string, generation int64) (agentruns.RoutineQueueResult, error) {
	out := agentruns.RoutineQueueResult{}
	if !workorders.UUID(tenantID) || !workorders.UUID(id) || session != "" && (!workorders.UUID(session) || generation < 1) {
		return out, workorders.Fail(400, "invalid_dispatch_binding")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return out, err
	}
	if err := accountuse.LockShared(ctx, tx); err != nil {
		return out, err
	}
	if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
		return out, err
	}
	var sameTenant bool
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')=$1`, tenantID).Scan(&sameTenant); err != nil {
		return out, err
	}
	if !sameTenant {
		return out, authz.ErrForbidden
	}
	// Personal demand remains hidden from ordinary coordinator projections. This
	// internal decision checks the exact owner's live rights and exposes no row.
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return out, err
	}
	// Every error requires caller rollback. Restore successful decisions below.
	r, err := loadIntent(ctx, tx, id)
	if err != nil {
		return out, err
	}
	// A disabled installation is read-only, including its pending receipts.
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT automatic_launch_enabled FROM project_routine_execution_settings WHERE project_id=$1),false)`, r.project).Scan(&enabled); err != nil {
		return out, err
	}
	if !enabled {
		out.WaitReason = "automatic_launch_disabled"
		_, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
		return out, err
	}
	// This outbox is the durable dispatch receipt, locked BEFORE routing events.
	var outbox string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM routine_effect_outbox WHERE run_id=$1 AND effect_key='execute' AND kind='routine.execute' FOR NO KEY UPDATE`, id).Scan(&outbox); err != nil {
		return out, err
	}
	step, err := tx.Begin(ctx)
	if err != nil {
		return out, err
	}
	out, err = e.dispatch(ctx, step, tenantID, r, session, generation, false)
	if err != nil || !out.Routed && out.WaitReason != "lead_demand_pending" {
		if rollbackErr := step.Rollback(ctx); rollbackErr != nil {
			return out, rollbackErr
		}
		if err != nil {
			var refusal *workorders.Error
			var pairingRefusal *agentpairing.Error
			var useRefusal *accountuse.Error
			switch {
			case errors.As(err, &refusal) && refusal.Status == 409:
				out.WaitReason = refusal.Message
			case errors.As(err, &pairingRefusal) && (pairingRefusal.Status == 409 || pairingRefusal.Status == 410):
				out.WaitReason = pairingRefusal.Code
			case errors.As(err, &useRefusal) && useRefusal.Status == 409:
				out.WaitReason = useRefusal.Message
			case errors.Is(err, authz.ErrForbidden), errors.Is(err, db.ErrKeyAuthorityChanged):
				out.WaitReason = "owner_or_dispatch_authority_revoked"
			default:
				return out, err
			}
		}
	} else if err = step.Commit(ctx); err != nil {
		return out, err
	}
	state := "pending"
	if out.Routed {
		state = "succeeded" // assignment persisted; this says nothing about launch
	}
	raw, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `UPDATE routine_effect_outbox SET state=$2,result=$3,reconcile_after=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp() WHERE id=$1`, outbox, state, raw)
	if err == nil {
		_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	}
	return out, err
}

func (e *Executor) dispatch(ctx context.Context, tx pgx.Tx, tenantID string, r intent, expectedSession string, expectedGeneration int64, probe bool) (agentruns.RoutineQueueResult, error) {
	out := agentruns.RoutineQueueResult{RunID: r.run}
	principal, err := recurrences.RequireExecutionConsentTx(ctx, tx, tenantID, r.recurrence, r.revision, e.options.Runtime)
	if err != nil {
		return out, err
	}
	if !r.consent || r.consentRevision != r.revision || principal != r.principal || r.assignment == nil {
		return out, workorders.Fail(409, "routine_execution_consent_required")
	}
	owner := tenant.Principal{ID: r.owner, TenantID: tenantID, Kind: tenant.Person}
	for _, permission := range []string{"nodes.read", "nodes.write", "run.create", "work_orders.write"} {
		if err := authz.RequireTx(ctx, tx, owner, permission, authz.Scope{ProjectID: r.project}); err != nil {
			return out, err
		}
	}
	if r.run == "" {
		return out, workorders.Fail(409, "routine_work_not_queued")
	}
	// Shared routing/fairness can materialize account windows. Enforce S09's
	// exact input bounds before entering those helpers, rather than after work.
	if err := routingBounds(ctx, tx); err != nil {
		return out, err
	}
	var session *string
	var generation int64
	var leadOwner, state string
	err = tx.QueryRow(ctx, `SELECT session_id::text,generation,owner_principal_id::text,state FROM project_leads WHERE project_id=$1 FOR NO KEY UPDATE`, r.project).Scan(&session, &generation, &leadOwner, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, workorders.Fail(409, "lead_execution_intent_required")
	}
	if err != nil {
		return out, err
	}
	if leadOwner != r.owner {
		return out, workorders.Fail(409, "lead_owner_changed")
	}
	if expectedSession != "" && (session == nil || *session != expectedSession || generation != expectedGeneration) {
		return out, workorders.Fail(409, "stale_lead_generation")
	}
	if session == nil && state == "waiting_for_room" {
		if probe {
			out.WaitReason = "lead_demand_pending"
			return out, nil
		}
		return e.leadDemand(ctx, tx, owner, r, generation)
	}
	if session == nil || state != "working" {
		return out, workorders.Fail(409, "lead_generation_unavailable")
	}
	p, err := harness.RequireRoutineLeadDispatchTx(ctx, tx, tenantID, r.id, r.project, r.owner, *session, generation, e.options.LeadAdmission)
	if err != nil {
		return out, err
	}
	if r.state != "created" && r.state != "pending" && r.state != "dispatched" {
		return out, workorders.Fail(409, "routine_run_unavailable")
	}
	var a recurrences.Assignment
	if json.Unmarshal(r.assignment, &a) != nil {
		return out, workorders.Fail(409, "assignment_unavailable")
	}
	if r.state == "created" || r.state == "pending" {
		prep, err := agentruns.PrepareRoutineWorkTx(ctx, tx, owner, agentruns.RoutinePreparationInput{RecurrenceID: r.recurrence, DefinitionRevision: r.revision}, e.options.Runtime)
		if err != nil {
			return out, err
		}
		if prep.Partial || prep.Candidate == nil || prep.WaitReason != "" {
			return out, workorders.Fail(409, prep.WaitReason)
		}
		if prep.Candidate.RoutineRunID != r.id {
			return out, workorders.Fail(409, "queue_order_wait")
		}
	}
	lead, _ := json.Marshal(map[string]any{"session_id": *session, "generation": generation})
	opts := agentruns.RoutineQueueOptions{Probe: probe}
	if !probe {
		opts.EarlierReady = func(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
			return e.earlierRoutineReady(ctx, tx, tenantID, id, *session, generation)
		}
	}
	return agentruns.RouteRoutineQueueTx(ctx, tx, p, r.run, a.Role, lead, e.options.LeadAdmission, func(ctx context.Context, tx pgx.Tx, run agentruns.Run, target workqueue.RouteTarget) ([]events.Change, error) {
		return e.finish(ctx, tx, owner, r, a, run, target)
	}, opts)
}

// An earlier routine retains its place only when the same complete dispatch
// would succeed. Check once per earlier entry (no recursive order scan), under
// the existing fences, and roll back every provisional write. Probe never
// appends events or advances a turn. Unknown operational errors remain errors.
func (e *Executor) earlierRoutineReady(ctx context.Context, tx pgx.Tx, tenantID, id, session string, generation int64) (bool, error) {
	step, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = step.Rollback(ctx) }()
	r, err := loadIntent(ctx, step, id)
	if err == nil {
		var hasIntent bool
		err = step.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_effect_outbox WHERE run_id=$1 AND effect_key='execute' AND kind='routine.execute')`, id).Scan(&hasIntent)
		if err == nil && !hasIntent {
			return false, nil
		}
	}
	var out agentruns.RoutineQueueResult
	if err == nil {
		out, err = e.dispatch(ctx, step, tenantID, r, session, generation, true)
	}
	if rollbackErr := step.Rollback(ctx); rollbackErr != nil {
		return false, rollbackErr
	}
	if err != nil {
		var refusal *workorders.Error
		var pairingRefusal *agentpairing.Error
		var useRefusal *accountuse.Error
		switch {
		case errors.As(err, &refusal) && refusal.Status == 409,
			errors.As(err, &pairingRefusal) && (pairingRefusal.Status == 409 || pairingRefusal.Status == 410),
			errors.As(err, &useRefusal) && useRefusal.Status == 409,
			errors.Is(err, authz.ErrForbidden), errors.Is(err, db.ErrKeyAuthorityChanged):
			return false, nil
		default:
			return false, err
		}
	}
	return out.Routed, nil
}

// Lead demand references the person's existing project_leads intent and S04
// consent. It is deliberately NOT a RoutineGrant: S14 must qualify its route,
// slot/budget and observed prior exit before creating a lead generation.
func (e *Executor) leadDemand(ctx context.Context, tx pgx.Tx, owner tenant.Principal, r intent, generation int64) (agentruns.RoutineQueueResult, error) {
	out := agentruns.RoutineQueueResult{RunID: r.run}
	settings, err := modelregistry.ProjectExecutionTx(ctx, tx, owner.TenantID, r.project, e.options.Runtime)
	if err != nil {
		return out, err
	}
	if !settings.AutomaticLaunchEnabled || settings.QualificationID == nil {
		return out, workorders.Fail(409, settings.WaitReason)
	}
	// Only one pending demand for this project/generation, even with many runs.
	var existing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_launch_intents WHERE project_id=$1 AND kind='lead' AND lead_generation=$2 AND exit_observed_at IS NULL)`, r.project, generation).Scan(&existing); err != nil {
		return out, err
	}
	if !existing {
		binding, _ := json.Marshal(map[string]any{"routine_run_id": r.id, "recurrence_id": r.recurrence, "definition_revision": r.revision, "consent_revision": r.consentRevision, "owner_principal_id": r.owner, "execution_principal_id": r.principal, "project_id": r.project, "execution_revision": settings.Revision, "qualification_id": *settings.QualificationID, "expected_lead_generation": generation})
		_, err = tx.Exec(ctx, `INSERT INTO routine_launch_intents(tenant_id,id,routine_run_id,project_id,owner_principal_id,worker_principal_id,kind,lead_generation,binding) VALUES($1,gen_random_uuid(),$2,$3,$4,$5,'lead',$6,$7)`, owner.TenantID, r.id, r.project, r.owner, r.principal, generation, binding)
		if err != nil {
			return out, err
		}
		if _, err := events.Append(ctx, tx, owner, events.Change{NodeID: &r.project, Type: "routine.lead_demand_created", After: map[string]any{"routine_run_id": r.id, "lead_generation": generation}}); err != nil {
			return out, err
		}
	}
	out.WaitReason = "lead_demand_pending"
	return out, nil
}

func (e *Executor) finish(ctx context.Context, tx pgx.Tx, owner tenant.Principal, r intent, a recurrences.Assignment, run agentruns.Run, target workqueue.RouteTarget) ([]events.Change, error) {
	return e.finishActor(ctx, tx, owner, r, a, run, target, "dispatch", "build", nil)
}

func (e *Executor) finishActor(ctx context.Context, tx pgx.Tx, owner tenant.Principal, r intent, a recurrences.Assignment, run agentruns.Run, target workqueue.RouteTarget, key, role string, lineage *agentsetup.RoutineLineage) ([]events.Change, error) {
	var account, computer, daemon, daemonGeneration, harnessName string
	var ledger *string
	// Exact account/host selection stays bounded. Multiple accounts require the
	// current registry/person route to select one; never guess an account.
	rows, err := tx.Query(ctx, `SELECT a.id::text,c.id::text,a.daemon_id,a.last_daemon_generation,c.ledger_generation::text,m.harness
 FROM agent_accounts a JOIN model_profiles m ON m.tenant_id=a.tenant_id AND m.id=$1 AND m.harness=a.harness
 JOIN agent_pairing_enrollments en ON en.tenant_id=a.tenant_id AND en.account_id=a.id
 JOIN agent_pairing_computers c ON c.tenant_id=en.tenant_id AND c.id=en.computer_id
 WHERE a.registered_by_principal_id=$2 AND a.owner_person_id=$3 AND a.state='available'
 AND c.principal_id=$2 AND c.state='connected' AND en.state='connected' AND en.ongoing_approved_at IS NOT NULL
 AND ($4::uuid IS NULL OR a.id=$4) AND aeon_account_allows_profile(a.harness,a.allowed_model_profile_ids,m.id)
 AND aeon_account_use_allowed_for_project(a.id,$5) ORDER BY a.id LIMIT 2`, target.Profile, target.Agent, owner.ID, target.Account, r.project)
	if err != nil {
		return nil, err
	}
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&account, &computer, &daemon, &daemonGeneration, &ledger, &harnessName); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, workorders.Fail(409, "routine_account_selection_required")
	}
	// The launch binds the computer's ledger generation; an unenrolled daemon
	// waits with its reason rather than failing the whole pass.
	if ledger == nil {
		return nil, workorders.Fail(409, "routine_ledger_enrollment_required")
	}
	var attempt, action string
	if err = tx.QueryRow(ctx, `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,agent_run_id,principal_id,assignment_digest) VALUES($1,$2,$6,$7,$3,$4,$5) RETURNING id::text`, owner.TenantID, r.id, run.ID, target.Agent, r.digest, key, role).Scan(&attempt); err != nil {
		return nil, err
	}
	worker := tenant.Principal{ID: target.Agent, TenantID: owner.TenantID, Kind: tenant.Agent, Scopes: []string{"run.claim"}}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET requested_account_id=$2 WHERE id=$1`, run.ID, account); err != nil {
		return nil, err
	}
	_, change, err := agentaccounts.ReserveRoutineAccountTx(ctx, tx, worker, r.id, run.ID, account, role)
	if err != nil {
		return nil, err
	}
	var estimate float64
	if err = tx.QueryRow(ctx, `SELECT (fields->>'estimate_hours')::float8 FROM nodes WHERE id=$1`, r.work).Scan(&estimate); err != nil {
		return nil, err
	}
	if err = engineadmission.RequireRoutineTx(ctx, tx, owner, r.id, run.ID, harnessName, estimate, e.options.Facts); err != nil {
		return nil, err
	}
	// The occurrence action is the immutable lineage root; local coding emits
	// no outward action. S15 must admit every future typed effect separately.
	if err = tx.QueryRow(ctx, `SELECT id::text FROM routine_actions WHERE run_id=$1 AND action_key='occurrence' AND state='succeeded'`, r.id).Scan(&action); err != nil {
		return nil, err
	}
	if estimate <= 0 || estimate > 168 || math.IsNaN(estimate) || math.IsInf(estimate, 0) {
		return nil, workorders.Fail(409, "recovery_budget_unavailable")
	}
	maximum := routinebudget.Amount{RecoveryMS: int64(math.Ceil(estimate * 3600000))}
	if role == "review" {
		maximum.RecoveryMS = 1800000
	}
	if a.Budget.TokenCeiling != nil {
		maximum.Tokens = *a.Budget.TokenCeiling
	}
	var billing string
	if err := tx.QueryRow(ctx, `SELECT billing_mode FROM agent_accounts WHERE id=$1`, account).Scan(&billing); err != nil {
		return nil, err
	}
	// Continuations reuse the frozen run balance. Settled usage is counted once;
	// every outstanding actor/evaluator hold stays unavailable to the next actor.
	if lineage != nil {
		var token, money *int64
		var usedTokens, heldTokens, usedMoney, heldMoney int64
		if err := tx.QueryRow(ctx, `SELECT token_ceiling,money_ceiling_microusd,settled_tokens,held_tokens,settled_microusd,held_microusd FROM routine_budget_balances WHERE run_id=$1`, r.id).Scan(&token, &money, &usedTokens, &heldTokens, &usedMoney, &heldMoney); err != nil {
			return nil, err
		}
		if token != nil {
			remaining := *token - usedTokens - heldTokens
			if remaining <= 0 {
				return nil, workorders.Fail(409, "token_budget_exhausted")
			}
			a.Budget.TokenCeiling = &remaining
			maximum.Tokens = remaining
		}
		if money != nil {
			remaining := *money - usedMoney - heldMoney
			if remaining <= 0 {
				return nil, workorders.Fail(409, "money_budget_exhausted")
			}
			a.Budget.MoneyCeilingMicroUSD = &remaining
		}
	}
	if billing == "api" && a.Budget.MoneyCeilingMicroUSD != nil {
		maximum.PaidMicroUSD = *a.Budget.MoneyCeilingMicroUSD
	}
	broker := routinebudget.Broker{Runtime: e.options.Runtime, Gates: e.options.Gates}
	held, err := broker.ReserveTx(ctx, tx, owner, routinebudget.Reservation{RunID: r.id, AttemptID: attempt, ActionID: action, GrantKey: key, AgentRunID: run.ID, AccountID: account, Maximum: maximum})
	if err != nil {
		return nil, err
	}
	settings, err := modelregistry.ProjectExecutionTx(ctx, tx, owner.TenantID, r.project, e.options.Runtime)
	if err != nil {
		return nil, err
	}
	if !settings.AutomaticLaunchEnabled || settings.QualificationID == nil {
		return nil, workorders.Fail(409, settings.WaitReason)
	}
	q, err := modelregistry.LoadRoutineQualificationTx(ctx, tx, r.project, *settings.QualificationID)
	if err != nil {
		return nil, err
	}
	var launch string
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text,clock_timestamp()`).Scan(&launch, &now); err != nil {
		return nil, err
	}
	capability := "routine_native_coding_v1"
	if a.RuntimeRequirements.NeedsBrowser {
		capability = "routine_native_browser_v1"
	}
	g := agentsetup.RoutineGrant{LedgerGeneration: *ledger, LaunchID: launch, TenantID: owner.TenantID, OwnerID: owner.ID, ProjectID: r.project, RoutineRunID: r.id, AgentRunID: run.ID, ExecutionPrincipalID: r.principal, PrincipalID: target.Agent, AccountID: account, ComputerID: computer, DaemonID: daemon, DaemonGeneration: daemonGeneration, AssignmentDigest: r.digest, DefinitionRevision: r.revision, ConsentRevision: r.consentRevision, ExecutionRevision: settings.Revision, QualificationID: q.ID, DaemonDigest: q.Runtime.DaemonDigest, CapabilityDigest: q.Runtime.CapabilityDigest, HostMappingDigest: q.Runtime.HostMappingDigest, Capability: agentsetup.RoutineExecutionCapability, RuntimeCapability: capability, BudgetMode: a.Budget.Mode, BudgetGrantID: held.ID, RemainingTokens: a.Budget.TokenCeiling, RemainingCostMicros: a.Budget.MoneyCeilingMicroUSD, ExpiresAt: now.Add(90 * time.Second)}
	g.Lineage = lineage
	if err := agentruns.PrepareRoutineLaunchTx(ctx, tx, owner, g, e.LaunchChecks()); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE routine_runs SET state='dispatched',updated_at=clock_timestamp() WHERE id=$1`, r.id); err != nil {
		return nil, err
	}
	return []events.Change{change}, nil
}

// LaunchChecks repeats all mandatory predicates at daemon claim; the outbox's
// previous dispatch result is not a start grant. Unknown exit/usage keeps holds.
func (e *Executor) LaunchChecks() agentruns.RoutineLaunchChecks {
	starts := routinebudget.Broker{Runtime: e.options.Runtime, Gates: e.options.Gates}
	return agentruns.RoutineLaunchChecks{Runtime: e.options.Runtime, Start: starts.StartCheck, Admission: func(ctx context.Context, tx pgx.Tx, p tenant.Principal, g agentsetup.RoutineGrant) error {
		if g.Lineage != nil {
			if err := e.validateContinuation(ctx, tx, g); err != nil {
				return err
			}
		}
		worker := tenant.Principal{ID: g.PrincipalID, TenantID: p.TenantID, Kind: tenant.Agent, Scopes: []string{"run.claim"}}
		var bound bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_budget_grants WHERE id=$1 AND run_id=$2 AND agent_run_id=$3 AND account_id=$4 AND parent_id IS NULL AND state='held' AND ($5::bigint IS NULL OR maximum_tokens=$5) AND CASE WHEN billing_mode='subscription' THEN maximum_microusd=0 ELSE ($6::bigint IS NULL OR maximum_microusd=$6) END)`, g.BudgetGrantID, g.RoutineRunID, g.AgentRunID, g.AccountID, g.RemainingTokens, g.RemainingCostMicros).Scan(&bound); err != nil {
			return err
		}
		if !bound {
			return workorders.Fail(409, "routine_grant_unavailable")
		}
		broker := routinebudget.Broker{Runtime: e.options.Runtime, Gates: e.options.Gates}
		if err := broker.StartCheck(worker)(ctx, tx, g.AgentRunID, g.AccountID); err != nil {
			return err
		}
		host, err := agentpairing.UnattendedForPrincipal(ctx, tx, g.PrincipalID)
		if err != nil {
			return err
		}
		if host.Status != "ready" {
			return workorders.Fail(409, host.Reason)
		}
		var harnessName string
		var estimate float64
		if err := tx.QueryRow(ctx, `SELECT m.harness,(n.fields->>'estimate_hours')::float8 FROM routine_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_node_id JOIN agent_runs ar ON ar.tenant_id=r.tenant_id AND ar.id=$2 JOIN model_profiles m ON m.tenant_id=ar.tenant_id AND m.id=ar.model_profile_id WHERE r.id=$1`, g.RoutineRunID, g.AgentRunID).Scan(&harnessName, &estimate); err != nil {
			return err
		}
		return engineadmission.RequireRoutineTx(ctx, tx, tenant.Principal{ID: g.OwnerID, TenantID: p.TenantID, Kind: tenant.Person}, g.RoutineRunID, g.AgentRunID, harnessName, estimate, e.options.Facts)
	}}
}
