// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// RoutineLaunchChecks is wired by the routine executor, never by an HTTP body.
// Admission revalidates the S09 persisted hold and current remaining grant,
// mandatory dial/quota/window facts and the S13 generation/guardrail decision.
// It is bounded transactional work, with no external effects or event append.
// Nil checks refuse routines without changing ordinary/manual runs.
// Start rechecks a budget-held launch's grant for the claiming daemon when
// its reserved capacity is validated; without it such claims keep waiting.
type RoutineLaunchChecks struct {
	Runtime        modelregistry.ExecutionRuntimeReader
	Admission      func(context.Context, pgx.Tx, tenant.Principal, agentsetup.RoutineGrant) error
	Start          func(tenant.Principal) agentaccounts.RoutineStartCheck
	BrowserRuntime workqueue.NativeBrowserRuntimeReader
}

// BindRoutineLaunch connects internal executor collaborators. It is not a
// feature flag: S04 person consent and exact qualification remain mandatory.
func BindRoutineLaunch(boundary httpapi.Module, checks RoutineLaunchChecks) error {
	m, ok := boundary.(*module)
	if !ok || checks.Runtime == nil || checks.Admission == nil {
		return errors.New("routine launch collaborators unavailable")
	}
	m.routines = checks
	return nil
}

func routineTrace(v Run) (*agentsetup.RoutineGrant, error) {
	var trace struct {
		Grant        *agentsetup.RoutineGrant `json:"routine_launch"`
		RoutineRunID string                   `json:"routine_run_id"`
	}
	if len(v.Trace) > 262144 {
		return nil, workorders.Fail(409, "routine_trace_unavailable")
	}
	if len(v.Trace) > 0 {
		if err := json.Unmarshal(v.Trace, &trace); err != nil {
			return nil, err
		}
	}
	if trace.RoutineRunID != "" && trace.Grant == nil {
		return nil, workorders.Fail(409, "routine_intent_required")
	}
	if (v.RoutineLaunchID == nil) != (trace.Grant == nil) {
		return nil, workorders.Fail(409, "routine_binding_unavailable")
	}
	if trace.Grant != nil && (*v.RoutineLaunchID != trace.Grant.LaunchID || trace.Grant.AgentRunID != v.ID || trace.RoutineRunID != trace.Grant.RoutineRunID) {
		return nil, workorders.Fail(409, "routine_binding_changed")
	}
	return trace.Grant, nil
}

// PrepareRoutineLaunchTx persists a qualified launch intent alongside S13's
// assignment. It never routes/reserves/forks. Caller appends events LAST.
func PrepareRoutineLaunchTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, g agentsetup.RoutineGrant, checks RoutineLaunchChecks) error {
	if err := g.Validate(); err != nil {
		return workorders.Fail(409, "routine_binding_unavailable")
	}
	if g.TenantID != p.TenantID {
		return authz.ErrForbidden
	}
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, p, "run.create", authz.Scope{ProjectID: g.ProjectID}); err != nil {
		return err
	}
	v, o, err := lockRun(ctx, tx, g.AgentRunID)
	if err != nil {
		return err
	}
	if v.Status != "queued" || v.AgentID != g.PrincipalID || v.AccountID == nil || *v.AccountID != g.AccountID {
		return workorders.Fail(409, "routine_assignment_changed")
	}
	if err = validateRoutineGrantTx(ctx, tx, p, g, o, checks); err != nil {
		return err
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT binding FROM routine_launch_intents WHERE id=$1 FOR NO KEY UPDATE`, g.LaunchID).Scan(&previous)
	if err == nil {
		var old agentsetup.RoutineGrant
		if json.Unmarshal(previous, &old) != nil {
			return workorders.Fail(409, "routine_intent_unavailable")
		}
		oldRaw, _ := json.Marshal(old)
		if string(oldRaw) != string(raw) {
			return workorders.Fail(409, "routine_intent_conflict")
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if v.RoutineLaunchID != nil {
		return workorders.Fail(409, "routine_intent_conflict")
	}
	_, err = tx.Exec(ctx, `INSERT INTO routine_launch_intents(tenant_id,id,routine_run_id,agent_run_id,project_id,owner_principal_id,worker_principal_id,binding) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.TenantID, g.LaunchID, g.RoutineRunID, g.AgentRunID, g.ProjectID, g.OwnerID, g.PrincipalID, raw)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_runs SET routine_launch_id=$2,trace=jsonb_set(jsonb_set(coalesce(trace,'{}'::jsonb),'{routine_launch}',$3::jsonb),'{routine_run_id}',to_jsonb($4::text)) WHERE id=$1`, g.AgentRunID, g.LaunchID, raw, g.RoutineRunID)
	return err
}

func validateRoutineGrantTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, g agentsetup.RoutineGrant, o workorders.Order, checks RoutineLaunchChecks) error {
	if checks.Runtime == nil || checks.Admission == nil {
		return workorders.Fail(409, "routine_runtime_unavailable")
	}
	if g.TenantID != p.TenantID || g.Validate() != nil {
		return workorders.Fail(409, "routine_binding_changed")
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if !now.Before(g.ExpiresAt) || g.ExpiresAt.Sub(now) > 2*time.Minute {
		return workorders.Fail(409, "routine_grant_expired")
	}
	var matches bool
	var recurrenceID string
	err := tx.QueryRow(ctx, `SELECT r.owner_principal_id=$2::uuid AND r.output_project_id=$3::uuid AND (r.agent_run_id=$4::uuid OR ($12 AND EXISTS(SELECT 1 FROM routine_attempts a JOIN routine_budget_orders b ON b.tenant_id=a.tenant_id AND b.run_id=a.run_id AND b.work_order_id=$9 WHERE a.run_id=r.id AND a.agent_run_id=$4 AND a.assignment_digest=r.assignment_digest AND a.role=$13)))
 AND r.assignment_digest=$5 AND r.definition_revision=$6 AND r.consent_revision=$7 AND r.execute_consent
 AND d.owner_principal_id=r.owner_principal_id AND d.output_project_id=r.output_project_id
 AND d.execute_consent AND d.consent_revision=r.consent_revision AND d.assignment=r.assignment
 AND c.revision=r.definition_revision AND r.consent_revision=r.definition_revision AND c.retired_at IS NULL AND NOT c.paused
 AND d.consent_qualification_id=$8::uuid AND (r.work_order_id=$9::uuid OR $12)
 AND r.assignment->'budget'->>'mode'=$10 AND r.execution_principal_id=$11::uuid
 AND r.assignment->'runtime_requirements'->>'needs_native_host'='true'
 AND (($14='routine_native_browser_v1' AND r.assignment->'runtime_requirements'->>'needs_browser'='true'
       AND r.assignment->'runtime_requirements'->>'runtime_class'='native_browser')
   OR ($14='routine_native_coding_v1' AND r.assignment->'runtime_requirements'->>'needs_browser'='false'
       AND r.assignment->'runtime_requirements'->>'runtime_class'='native_coding'))
, r.recurrence_id::text
 FROM routine_runs r JOIN recurrence_definitions d ON d.tenant_id=r.tenant_id AND d.recurrence_id=r.recurrence_id
 JOIN recurrences c ON c.tenant_id=d.tenant_id AND c.id=d.recurrence_id WHERE r.id=$1`, g.RoutineRunID, g.OwnerID, g.ProjectID, g.AgentRunID, g.AssignmentDigest, g.DefinitionRevision, g.ConsentRevision, g.QualificationID, o.NodeID, g.BudgetMode, g.ExecutionPrincipalID, g.Lineage != nil, lineageRole(g), g.RuntimeCapability).Scan(&matches, &recurrenceID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !matches {
		return workorders.Fail(409, "routine_assignment_changed")
	}
	if err != nil {
		return err
	}
	executionPrincipal, err := recurrences.RequireExecutionConsentTx(ctx, tx, p.TenantID, recurrenceID, g.DefinitionRevision, checks.Runtime)
	if err != nil {
		return err
	}
	if executionPrincipal != g.ExecutionPrincipalID {
		return workorders.Fail(409, "routine_execution_identity_changed")
	}
	owner := tenant.Principal{ID: g.OwnerID, TenantID: p.TenantID, Kind: tenant.Person}
	if err := authz.RequireTx(ctx, tx, owner, "nodes.write", authz.Scope{ProjectID: g.ProjectID}); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, owner, "run.create", authz.Scope{ProjectID: g.ProjectID}); err != nil {
		return err
	}
	settings, err := modelregistry.ProjectExecutionTx(ctx, tx, p.TenantID, g.ProjectID, checks.Runtime)
	if err != nil {
		return err
	}
	if !settings.AutomaticLaunchEnabled {
		return workorders.Fail(409, settings.WaitReason)
	}
	if settings.Revision != g.ExecutionRevision || settings.QualificationID == nil || *settings.QualificationID != g.QualificationID {
		return workorders.Fail(409, "routine_consent_changed")
	}
	q, err := modelregistry.LoadRoutineQualificationTx(ctx, tx, g.ProjectID, g.QualificationID)
	if err != nil {
		return err
	}
	if q.Runtime.DaemonDigest != g.DaemonDigest || q.Runtime.CapabilityDigest != g.CapabilityDigest || q.Runtime.HostMappingDigest != g.HostMappingDigest || !slices.Contains(q.Runtime.Capabilities, g.RuntimeCapability) || !slices.Contains(q.Runtime.BudgetModes, g.BudgetMode) {
		return workorders.Fail(409, "routine_capability_changed")
	}
	if err := agentpairing.RequireRoutineAccountTx(ctx, tx, g); err != nil {
		return err
	}
	if err := workqueue.RequireNativeBrowserRouteTx(ctx, tx, g, checks.BrowserRuntime); err != nil {
		return err
	}
	return checks.Admission(ctx, tx, p, g)
}

func lineageRole(g agentsetup.RoutineGrant) string {
	if g.Lineage != nil {
		return g.Lineage.Role
	}
	return "build"
}

// routineInternal runs a bounded decision on one exact routine launch with
// system visibility: a personal routine's ledger is hidden from its assigned
// daemon's ordinary projection. fn returns only a decision, never rows.
func routineInternal(ctx context.Context, tx pgx.Tx, fn func() error) error {
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return err
	}
	err := fn()
	_, restoreErr := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	if err != nil {
		return err // caller rolls back every error
	}
	return restoreErr
}

func (m *module) validateRoutinePickup(ctx context.Context, tx pgx.Tx, p tenant.Principal, v Run, o workorders.Order, in *agentsetup.RoutinePickup, daemon, generation string) error {
	g, err := routineTrace(v)
	if err != nil {
		return err
	}
	if g == nil {
		if in != nil {
			return workorders.Fail(409, "run_has_no_routine_intent")
		}
		// An S05 run is routine demand even before S13 has assigned a grant.
		var pending bool
		if err := routineInternal(ctx, tx, func() error {
			return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_runs WHERE agent_run_id=$1)`, v.ID).Scan(&pending)
		}); err != nil {
			return err
		}
		if pending {
			return workorders.Fail(409, "routine_intent_required")
		}
		return nil
	}
	if in == nil || in.LaunchID != g.LaunchID || in.Capability != agentsetup.RoutineExecutionCapability || in.DaemonDigest != g.DaemonDigest || in.CapabilityDigest != g.CapabilityDigest || in.ComputerID != g.ComputerID || !agentsetup.RoutineDigest(in.WorktreeID) {
		return workorders.Fail(409, "routine_daemon_update_required")
	}
	if p.ID != g.PrincipalID || daemon != g.DaemonID || generation != g.DaemonGeneration || v.AccountID == nil || *v.AccountID != g.AccountID {
		return workorders.Fail(409, "routine_daemon_binding_changed")
	}
	if err := authz.RequireTx(ctx, tx, p, "run.claim", authz.Scope{ProjectID: g.ProjectID}); err != nil {
		return err
	}
	// The daemon is bound to this exact grant above; the owner's personal
	// ledger it executes is checked internally rather than through its view.
	return routineInternal(ctx, tx, func() error {
		if err := validateRoutineGrantTx(ctx, tx, p, *g, o, m.routines); err != nil {
			return err
		}
		var raw []byte
		var worktree *string
		var state string
		if err := tx.QueryRow(ctx, `SELECT binding,worktree_id,state FROM routine_launch_intents WHERE id=$1 FOR NO KEY UPDATE`, g.LaunchID).Scan(&raw, &worktree, &state); err != nil {
			return err
		}
		var stored agentsetup.RoutineGrant
		if json.Unmarshal(raw, &stored) != nil {
			return workorders.Fail(409, "routine_intent_unavailable")
		}
		a, _ := json.Marshal(stored)
		b, _ := json.Marshal(g)
		if string(a) != string(b) || state != "pending" && state != "claimed" || worktree != nil && *worktree != in.WorktreeID {
			return workorders.Fail(409, "routine_writer_conflict")
		}
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_launch_intents WHERE id<>$1 AND worktree_id=$2 AND exit_observed_at IS NULL)`, g.LaunchID, in.WorktreeID).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return workorders.Fail(409, "routine_worktree_exit_unconfirmed")
		}
		return nil
	})
}

func acceptRoutinePickup(ctx context.Context, tx pgx.Tx, v Run, in *agentsetup.RoutinePickup) error {
	if v.RoutineLaunchID == nil {
		return nil
	}
	raw, _ := json.Marshal(in)
	return routineInternal(ctx, tx, func() error {
		if _, err := tx.Exec(ctx, `INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES(current_setting('aeon.tenant_id')::uuid,$1,'claimed',$2) ON CONFLICT DO NOTHING`, *v.RoutineLaunchID, raw); err != nil {
			return err
		}
		return updateRoutineIntent(ctx, tx, `UPDATE routine_launch_intents SET state='claimed',worktree_id=$2,claimed_at=coalesce(claimed_at,clock_timestamp()) WHERE id=$1`, *v.RoutineLaunchID, in.WorktreeID)
	})
}

// A launch intent write that matches no row is a refusal, never a success.
func updateRoutineIntent(ctx context.Context, tx pgx.Tx, query string, args ...any) error {
	tag, err := tx.Exec(ctx, query, args...)
	if err == nil && tag.RowsAffected() != 1 {
		err = workorders.Fail(409, "routine_intent_unavailable")
	}
	return err
}

func reportRoutineExit(ctx context.Context, tx pgx.Tx, v Run, t Telemetry, status string) error {
	if v.RoutineLaunchID == nil {
		return nil
	}
	if terminal(status) && (t.Kind != "finished" || t.ProcessState == "" || t.ProcessState == "unconfirmed" && status != "ownership_lost") {
		return workorders.Fail(409, "routine completion requires local process evidence")
	}
	if t.Kind == "started" {
		return routineInternal(ctx, tx, func() error {
			_, err := tx.Exec(ctx, `INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES(current_setting('aeon.tenant_id')::uuid,$1,'launched','{}') ON CONFLICT DO NOTHING`, *v.RoutineLaunchID)
			return err
		})
	}
	if t.Kind != "finished" || t.ProcessState == "unconfirmed" {
		return nil
	}
	return routineInternal(ctx, tx, func() error {
		if t.ProcessState == "not_attempted" {
			var launched bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_launch_evidence WHERE launch_id=$1 AND kind='launched')`, *v.RoutineLaunchID).Scan(&launched); err != nil {
				return err
			}
			if launched {
				return workorders.Fail(409, "routine launch conflicts with no-launch evidence")
			}
		}
		raw, _ := json.Marshal(map[string]any{"process_state": t.ProcessState, "sequence": t.Sequence, "daemon_id": v.DaemonID, "daemon_generation": v.Generation})
		if _, err := tx.Exec(ctx, `INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES(current_setting('aeon.tenant_id')::uuid,$1,'exit_observed',$2) ON CONFLICT DO NOTHING`, *v.RoutineLaunchID, raw); err != nil {
			return err
		}
		return updateRoutineIntent(ctx, tx, `UPDATE routine_launch_intents SET state='exit_observed',exit_observed_at=coalesce(exit_observed_at,clock_timestamp()) WHERE id=$1`, *v.RoutineLaunchID)
	})
}
