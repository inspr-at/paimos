// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// LeadRuntimeChecks is the executor's transaction-only actor reservation seam.
// Prepare resolves the lead role and persists an S09 lead attempt/hold and
// reserved account. Validate repeats deterministic daily/quota/window/CI gates
// (including the prospective root slot); Exit reconciles the exact actor only.
// No collaborator may call a model/network, append events or infer exit.
// An installation without qualified collaborators remains visibly unavailable.
type LeadRuntimeChecks struct {
	Prepare  func(context.Context, pgx.Tx, tenant.Principal, agentsetup.LeadPickup) (agentsetup.LeadLaunch, error)
	Validate func(context.Context, pgx.Tx, tenant.Principal, agentsetup.LeadLaunch) error
	Exit     func(context.Context, pgx.Tx, tenant.Principal, agentsetup.LeadLaunch, ownedprocess.Identity) error
	// Abandon settles a reserved actor that the exact daemon confirms it never
	// attempted. It must release its hold without inventing process/usage proof.
	Abandon func(context.Context, pgx.Tx, tenant.Principal, agentsetup.LeadLaunch) error
}

func (m *Module) WithLeadRuntime(checks LeadRuntimeChecks) *Module {
	m.leadRuntime = checks
	return m
}

// WithLeadAdmission binds the executor's authoritative admission predicates.
// It does not supply execution consent or enable automatic launches.
func (m *Module) WithLeadAdmission(admission LeadAdmission) *Module {
	m.leadAdmission = admission
	return m
}

type leadRuntimeView struct {
	Demand *agentsetup.LeadDemand `json:"demand"`
	State  string                 `json:"state"`
	Reason string                 `json:"reason"`
}

type leadRuntimeBinding struct {
	Demand      json.RawMessage       `json:"demand"`
	Launch      agentsetup.LeadLaunch `json:"launch"`
	WorktreeID  string                `json:"worktree_id"`
	RefDigest   []byte                `json:"ref_digest"`
	LeaseDigest []byte                `json:"lease_digest"`
}

func validLeadPickup(in agentsetup.LeadPickup) bool {
	d := in.Demand
	return workorders.UUID(d.ID) && workorders.UUID(d.ProjectID) && workorders.UUID(d.RoutineRunID) && workorders.UUID(d.OwnerID) && d.Generation >= 0 && d.Revision > 0 &&
		workorders.UUID(in.AccountID) && workorders.UUID(in.ComputerID) && workorders.UUID(in.LedgerGeneration) && routineRuntimeIdentifier(in.DaemonID) && routineRuntimeIdentifier(in.DaemonGeneration) &&
		in.Capability == agentsetup.LeadExecutionCapability && agentsetup.RoutineDigest(in.DaemonDigest) && agentsetup.RoutineDigest(in.CapabilityDigest) && agentsetup.RoutineDigest(in.WorktreeID) &&
		len(in.Ref) >= 32 && len(in.Ref) <= 128 && len(in.Lease) >= 32 && len(in.Lease) <= 128 && in.Ref != in.Lease && routineRuntimeIdentifier(in.Ref) && routineRuntimeIdentifier(in.Lease)
}

func routineRuntimeIdentifier(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}

func (m *Module) readLeadRuntime(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx, project := r.Context(), r.PathValue("projectId")
	if !workorders.UUID(project) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: project}); err != nil {
		return nil, err
	}
	l, err := loadLead(ctx, tx, project, false)
	if err != nil {
		return nil, err
	}
	out := leadRuntimeView{State: "waiting", Reason: "lead_consent_missing"}
	if l.State == "none" {
		return out, nil
	}
	if err = leadOwner(ctx, tx, p, project, l.owner); err != nil {
		return nil, err
	}
	// A live/adoption-selected generation is retained, never reparented or
	// replaced by automatic demand. Unconfirmed historical exits block too.
	if l.SessionID != nil {
		var stopped bool
		if err = tx.QueryRow(ctx, `SELECT aeon_work_session_stopped(stopped_at,stop_reason) FROM harness_sessions WHERE id=$1`, *l.SessionID).Scan(&stopped); err != nil {
			return nil, err
		}
		if !stopped {
			out.State, out.Reason = "reused", "existing_lead"
			return out, nil
		}
	}
	if l.State != "waiting_for_room" || l.Reason == adoptionPending || l.Reason == selectionCleared {
		out.Reason = "explicit_start_required"
		return out, nil
	}
	settings, err := modelregistry.ProjectExecutionTx(ctx, tx, p.TenantID, project, m.routineRuntime)
	if err != nil {
		return nil, err
	}
	if !settings.AutomaticLaunchEnabled {
		out.Reason = settings.WaitReason
		return out, nil
	}
	if m.leadRuntime.Prepare == nil || m.leadRuntime.Validate == nil || m.leadRuntime.Exit == nil || m.leadRuntime.Abandon == nil || m.leadAdmission == nil {
		out.Reason = "lead_runtime_unqualified"
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text,routine_run_id::text FROM routine_launch_intents WHERE project_id=$1 AND owner_principal_id=$2 AND kind='lead' AND lead_generation=$3 AND state='pending' AND exit_observed_at IS NULL ORDER BY created_at,id LIMIT 2`, project, l.owner, l.Generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var d *agentsetup.LeadDemand
	for rows.Next() {
		if d != nil {
			return nil, workorders.Fail(409, "lead_demand_ambiguous")
		}
		next := agentsetup.LeadDemand{ProjectID: project, OwnerID: l.owner, Generation: l.Generation, Revision: l.Revision}
		if err = rows.Scan(&next.ID, &next.RoutineRunID); err != nil {
			return nil, err
		}
		d = &next
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if d == nil {
		out.Reason = "lead_demand_missing"
	} else {
		out.Demand, out.State, out.Reason = d, "pending", ""
	}
	return out, nil
}

func (m *Module) runtimeFence(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	// Pairing uses the shared tenant -> tree -> pairing order before lead rows.
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return err
	}
	if err := leadFence(ctx, tx, project); err != nil {
		return err
	}
	return authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: project})
}

func (m *Module) validateLeadLaunch(ctx context.Context, tx pgx.Tx, p tenant.Principal, in agentsetup.LeadPickup, launch agentsetup.LeadLaunch) error {
	g := launch.Grant
	if g.Validate() != nil || launch.Demand != in.Demand || g.LaunchID != in.Demand.ID || g.ProjectID != in.Demand.ProjectID || g.RoutineRunID != in.Demand.RoutineRunID || g.OwnerID != in.Demand.OwnerID || g.TenantID != p.TenantID || g.PrincipalID != p.ID || g.DaemonID != in.DaemonID || g.DaemonGeneration != in.DaemonGeneration || g.LedgerGeneration != in.LedgerGeneration || g.AccountID != in.AccountID || g.ComputerID != in.ComputerID || g.DaemonDigest != in.DaemonDigest || g.CapabilityDigest != in.CapabilityDigest || launch.Generation != in.Demand.Generation+1 || !workorders.UUID(launch.ProfileID) || launch.Model == "" || len(launch.Model) > 256 || len(launch.Effort) > 32 || !validHarness(launch.Harness) || len(launch.Host) < 1 || len(launch.Host) > 128 || launch.MaxDurationSeconds < 1 || launch.MaxDurationSeconds > 86400 {
		return workorders.Fail(409, "lead_runtime_binding_changed")
	}
	settings, err := modelregistry.ProjectExecutionTx(ctx, tx, p.TenantID, g.ProjectID, m.routineRuntime)
	if err != nil {
		return err
	}
	if !settings.AutomaticLaunchEnabled {
		return workorders.Fail(409, settings.WaitReason)
	}
	if settings.Revision != g.ExecutionRevision || settings.QualificationID == nil || *settings.QualificationID != g.QualificationID {
		return workorders.Fail(409, "lead_consent_changed")
	}
	q, err := modelregistry.LoadRoutineQualificationTx(ctx, tx, g.ProjectID, g.QualificationID)
	if err != nil {
		return err
	}
	if q.OwnerPersonID != g.OwnerID || q.Runtime.DaemonDigest != g.DaemonDigest || q.Runtime.CapabilityDigest != g.CapabilityDigest || q.Runtime.HostMappingDigest != g.HostMappingDigest || !slices.Contains(q.Runtime.Capabilities, agentsetup.LeadExecutionCapability) || !slices.Contains(q.Runtime.Capabilities, g.RuntimeCapability) || !slices.Contains(q.Runtime.BudgetModes, g.BudgetMode) {
		return workorders.Fail(409, "lead_capability_unqualified")
	}
	var recurrence string
	var valid bool
	var now time.Time
	err = tx.QueryRow(ctx, `SELECT recurrence_id::text,owner_principal_id=$2::uuid AND output_project_id=$3::uuid AND execution_principal_id=$4::uuid AND definition_revision=$5 AND consent_revision=$6 AND execute_consent AND assignment_digest=$7 AND state NOT IN ('completed','cancelled','failed'),clock_timestamp() FROM routine_runs WHERE id=$1`, g.RoutineRunID, g.OwnerID, g.ProjectID, g.ExecutionPrincipalID, g.DefinitionRevision, g.ConsentRevision, g.AssignmentDigest).Scan(&recurrence, &valid, &now)
	if err != nil {
		return err
	}
	if !valid {
		return workorders.Fail(409, "routine_assignment_changed")
	}
	if !now.Before(g.ExpiresAt) || g.ExpiresAt.Sub(now) > 2*time.Minute {
		return workorders.Fail(409, "lead_grant_expired")
	}
	principal, err := recurrences.RequireExecutionConsentTx(ctx, tx, p.TenantID, recurrence, g.DefinitionRevision, m.routineRuntime)
	if err != nil {
		return err
	}
	if principal != g.ExecutionPrincipalID {
		return authz.ErrForbidden
	}
	if err = leadOwner(ctx, tx, p, g.ProjectID, g.OwnerID); err != nil {
		return err
	}
	if err = authz.RequireTx(ctx, tx, p, "run.claim", authz.Scope{ProjectID: g.ProjectID}); err != nil {
		return err
	}
	if err = agentpairing.RequireRoutineAccountTx(ctx, tx, g); err != nil {
		return err
	}
	// The actor must already have a real, distinct lead attempt and shared hold.
	// An injected preparer cannot convert a worker's envelope into lead authority.
	err = tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM routine_budget_grants b
 JOIN routine_budget_balances balance ON balance.tenant_id=b.tenant_id AND balance.run_id=b.run_id
 JOIN routine_attempts a ON a.tenant_id=b.tenant_id AND a.id=b.attempt_id AND a.run_id=b.run_id
 JOIN agent_runs r ON r.tenant_id=b.tenant_id AND r.id=b.agent_run_id AND r.id=a.agent_run_id
 JOIN model_profiles m ON m.tenant_id=r.tenant_id AND m.id=r.model_profile_id
 WHERE b.id=$1 AND b.run_id=$2 AND b.agent_run_id=$3 AND b.account_id=$4
 AND b.parent_id IS NULL AND b.state='held' AND a.role='lead' AND a.assignment_digest=$5
 AND a.principal_id=$6 AND r.agent_principal_id=$6 AND r.account_id=$4
 AND r.purpose='managed' AND r.status IN ('queued','starting')
 AND m.id=$7 AND m.enabled AND m.harness=$8 AND m.model=$9 AND m.effort=$10 AND b.harness=$8
 AND ($11::bigint IS NULL OR b.maximum_tokens=$11) AND ($12::bigint IS NULL OR b.maximum_microusd=$12)
 AND balance.mode=$13 AND b.owner_principal_id=$14 AND balance.owner_principal_id=$14
 AND b.maximum_ms >= $15
 AND NOT EXISTS(SELECT 1 FROM harness_sessions hs WHERE hs.run_id=r.id AND hs.id::text<>$16
  AND NOT aeon_work_session_stopped(hs.stopped_at,hs.stop_reason)))`,
		g.BudgetGrantID, g.RoutineRunID, g.AgentRunID, g.AccountID, g.AssignmentDigest, p.ID,
		launch.ProfileID, launch.Harness, launch.Model, launch.Effort, g.RemainingTokens,
		g.RemainingCostMicros, g.BudgetMode, g.OwnerID, launch.MaxDurationSeconds*1000, launch.SessionID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return workorders.Fail(409, "lead_hold_unavailable")
	}
	policy, err := modelregistry.LoadRoutineLeadPolicyTx(ctx, tx, p.TenantID, g.ProjectID)
	if err != nil {
		return err
	}
	if policy.OwnerPersonID == nil || *policy.OwnerPersonID != g.OwnerID || policy.Effective.AllowedAccountIDs != nil && !slices.Contains(*policy.Effective.AllowedAccountIDs, g.AccountID) || policy.Effective.AllowedHostIDs != nil && !slices.Contains(*policy.Effective.AllowedHostIDs, g.ComputerID) {
		return workorders.Fail(409, "lead_owner_policy_changed")
	}
	// Explicit owner selectors are binding even when the injected reserver
	// incorrectly returns a healthy profile from a different role/ladder.
	if policy.ModelSelector != nil && policy.ModelSelector.Cell != nil {
		cell := policy.ModelSelector.Cell
		if cell.Mode == "pinned" && cell.ProfileID != launch.ProfileID || cell.Harness != "" && cell.Harness != launch.Harness || cell.Effort != "" && cell.Effort != launch.Effort {
			return workorders.Fail(409, "lead_model_changed")
		}
	}
	host, err := agentpairing.UnattendedForPrincipal(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if host.Status != "ready" {
		return workorders.Fail(409, "host_"+host.Reason)
	}
	if m.leadRuntime.Validate == nil {
		return workorders.Fail(409, "lead_runtime_unqualified")
	}
	return m.leadRuntime.Validate(ctx, tx, p, launch)
}

func (m *Module) claimLeadRuntime(r *http.Request, tx pgx.Tx, p tenant.Principal) (result any, err error) {
	r, flush := deferControlEvents(r, tx)
	defer flush(&err)
	var in agentsetup.LeadPickup
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !validLeadPickup(in) || in.Demand.ProjectID != r.PathValue("projectId") {
		return nil, workorders.Fail(400, "invalid_lead_pickup")
	}
	ctx := r.Context()
	if err = m.runtimeFence(ctx, tx, p, in.Demand.ProjectID); err != nil {
		return nil, err
	}
	l, err := loadLead(ctx, tx, in.Demand.ProjectID, true)
	if err != nil {
		return nil, err
	}
	var raw []byte
	var state string
	err = tx.QueryRow(ctx, `SELECT CASE WHEN octet_length(binding::text)<=65536 THEN binding END,state FROM routine_launch_intents WHERE id=$1 AND project_id=$2 AND owner_principal_id=$3 AND routine_run_id=$4 AND kind='lead' AND lead_generation=$5 FOR NO KEY UPDATE`, in.Demand.ID, in.Demand.ProjectID, in.Demand.OwnerID, in.Demand.RoutineRunID, in.Demand.Generation).Scan(&raw, &state)
	if err != nil {
		return nil, err
	}
	if raw == nil || len(raw) > 65536 {
		return nil, workorders.Fail(409, "lead_binding_unavailable")
	}
	if state == "claimed" {
		var saved leadRuntimeBinding
		if json.Unmarshal(raw, &saved) != nil || !runtimeProof(saved, in) || saved.Launch.Grant.PrincipalID != p.ID || l.SessionID == nil || *l.SessionID != saved.Launch.SessionID || l.Generation != saved.Launch.Generation {
			return nil, workorders.Fail(409, "lead_writer_conflict")
		}
		// Recovery returns immutable evidence even after expiry/revocation. It
		// never renews the grant; preflight remains required before native start.
		return saved.Launch, nil
	}
	if state != "pending" || l.owner != in.Demand.OwnerID || l.Generation != in.Demand.Generation || l.Revision != in.Demand.Revision || l.State != "waiting_for_room" || l.Reason == adoptionPending || l.Reason == selectionCleared {
		return nil, workorders.Fail(409, "lead_intent_changed")
	}
	if l.SessionID != nil {
		var stopped bool
		if err = tx.QueryRow(ctx, `SELECT aeon_work_session_stopped(stopped_at,stop_reason) FROM harness_sessions WHERE id=$1`, *l.SessionID).Scan(&stopped); err != nil {
			return nil, err
		}
		if !stopped {
			return nil, workorders.Fail(409, "lead_exit_unconfirmed")
		}
	}
	if m.leadRuntime.Prepare == nil || m.leadRuntime.Validate == nil || m.leadRuntime.Exit == nil || m.leadRuntime.Abandon == nil || m.leadAdmission == nil {
		return nil, workorders.Fail(409, "lead_runtime_unqualified")
	}
	settings, e := modelregistry.ProjectExecutionTx(ctx, tx, p.TenantID, l.ProjectID, m.routineRuntime)
	if e != nil {
		return nil, e
	}
	if !settings.AutomaticLaunchEnabled {
		return nil, workorders.Fail(409, settings.WaitReason)
	}
	if err = leadOwner(ctx, tx, p, l.ProjectID, l.owner); err != nil {
		return nil, err
	}
	launch, err := m.leadRuntime.Prepare(ctx, tx, p, in)
	if err != nil {
		return nil, err
	}
	if launch.SessionID != "" {
		return nil, workorders.Fail(409, "fresh_lead_required")
	}
	if err = m.validateLeadLaunch(ctx, tx, p, in, launch); err != nil {
		return nil, err
	}
	candidate := Session{ProjectID: launch.Grant.ProjectID, AgentPrincipalID: p.ID, OwnerPrincipalID: &launch.Grant.OwnerID, RunID: &launch.Grant.AgentRunID, Harness: launch.Harness, Role: "coordinator"}
	reason, err := m.checkLeadAdmission(ctx, tx, p, l.owner, candidate)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, workorders.Fail(409, reason)
	}
	reason, err = m.leadScheduleWait(ctx, tx, p, l, true)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, workorders.Fail(409, reason)
	}
	// Registration is reused for private proof/native-context fences. All its
	// events remain deferred until the generation/claim/actor writes finish.
	var existing bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE ref_digest=$1 OR lease_digest=$2) OR EXISTS(SELECT 1 FROM routine_launch_intents WHERE id<>$3 AND worktree_id=$4 AND exit_observed_at IS NULL)`, digest("ref", in.Ref), digest("lease", in.Lease), in.Demand.ID, in.WorktreeID).Scan(&existing); err != nil {
		return nil, err
	}
	if existing {
		return nil, workorders.Fail(409, "fresh_owned_lead_required")
	}
	var order string
	if err = tx.QueryRow(ctx, `SELECT work_order_id::text FROM agent_runs WHERE id=$1`, launch.Grant.AgentRunID).Scan(&order); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"agent_principal_id": p.ID, "run_id": launch.Grant.AgentRunID, "work_order_id": order, "harness": launch.Harness, "host": launch.Host, "management_mode": "managed", "role": "coordinator", "harness_session_ref": in.Ref, "worker_lease": in.Lease, "model": launch.Model, "reasoning_effort": launch.Effort, "advertised_capabilities": []string{"inbox", "pause", "interrupt", "stop", "owned_stop_v1"}})
	reg := r.Clone(ctx)
	reg.Body = io.NopCloser(bytes.NewReader(body))
	reg.ContentLength = int64(len(body))
	session, err := m.register(reg, tx, p)
	if err != nil {
		return nil, err
	}
	s, ok := session.(Session)
	if !ok || s.ParentID != nil || s.OwnerPrincipalID == nil || *s.OwnerPrincipalID != l.owner {
		return nil, workorders.Fail(409, "owned_root_required")
	}
	launch.SessionID = s.ID
	stored := leadRuntimeBinding{Demand: json.RawMessage(raw), Launch: launch, WorktreeID: in.WorktreeID, RefDigest: digest("ref", in.Ref), LeaseDigest: digest("lease", in.Lease)}
	binding, _ := json.Marshal(stored)
	if _, err = tx.Exec(ctx, `UPDATE project_leads SET session_id=$2,generation=$3,state='starting',reason='',revision=revision+1,dispatch_key_id=nullif($4,'')::uuid,updated_at=clock_timestamp() WHERE project_id=$1`, l.ProjectID, s.ID, launch.Generation, p.AuthKeyID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO project_lead_generations(tenant_id,project_id,generation,session_id) VALUES($1,$2,$3,$4)`, p.TenantID, l.ProjectID, launch.Generation, s.ID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE routine_launch_intents SET binding=$2,state='claimed',agent_run_id=$3,worker_principal_id=$4,worktree_id=$5,claimed_at=clock_timestamp() WHERE id=$1`, in.Demand.ID, binding, launch.Grant.AgentRunID, p.ID, in.WorktreeID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='starting',daemon_id=$2,daemon_generation=$3,routine_launch_id=$4 WHERE id=$1`, launch.Grant.AgentRunID, in.DaemonID, in.DaemonGeneration, in.Demand.ID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES($1,$2,'claimed',$3)`, p.TenantID, in.Demand.ID, binding); err != nil {
		return nil, err
	}
	if err = record(ctx, tx, p, Session{ProjectID: l.ProjectID}, "lead_runtime_claimed", nil, map[string]any{"launch_id": in.Demand.ID, "session_id": s.ID, "generation": launch.Generation}); err != nil {
		return nil, err
	}
	return launch, nil
}

func runtimeProof(saved leadRuntimeBinding, in agentsetup.LeadPickup) bool {
	g := saved.Launch.Grant
	return saved.WorktreeID == in.WorktreeID && saved.Launch.Demand == in.Demand && g.DaemonID == in.DaemonID && g.DaemonGeneration == in.DaemonGeneration && g.LedgerGeneration == in.LedgerGeneration && g.AccountID == in.AccountID && g.ComputerID == in.ComputerID && g.DaemonDigest == in.DaemonDigest && g.CapabilityDigest == in.CapabilityDigest && subtle.ConstantTimeCompare(saved.RefDigest, digest("ref", in.Ref)) == 1 && subtle.ConstantTimeCompare(saved.LeaseDigest, digest("lease", in.Lease)) == 1
}

// Preflight and observed-start/exit share one bounded endpoint. A receipt of
// turn completion is never exit. Exit can be recorded after consent/window
// closes, but only by the exact original daemon with the saved process proof.
func (m *Module) reportLeadRuntime(r *http.Request, tx pgx.Tx, p tenant.Principal) (result any, err error) {
	r, flush := deferControlEvents(r, tx)
	defer flush(&err)
	var in struct {
		Pickup    agentsetup.LeadPickup  `json:"pickup"`
		Kind      string                 `json:"kind"`
		Ownership *ownedprocess.Identity `json:"ownership"`
	}
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !validLeadPickup(in.Pickup) || in.Pickup.Demand.ProjectID != r.PathValue("projectId") || !slices.Contains([]string{"preflight", "started", "exit_observed", "abandon"}, in.Kind) || in.Kind == "abandon" && in.Ownership != nil {
		return nil, workorders.Fail(400, "invalid_lead_report")
	}
	ctx := r.Context()
	pickup := in.Pickup
	if err = m.runtimeFence(ctx, tx, p, pickup.Demand.ProjectID); err != nil {
		return nil, err
	}
	var raw []byte
	var state, worktree string
	if err = tx.QueryRow(ctx, `SELECT CASE WHEN octet_length(binding::text)<=65536 THEN binding END,state,coalesce(worktree_id,'') FROM routine_launch_intents WHERE id=$1 AND project_id=$2 AND owner_principal_id=$3 AND routine_run_id=$4 AND lead_generation=$5 AND kind='lead' FOR NO KEY UPDATE`, pickup.Demand.ID, pickup.Demand.ProjectID, pickup.Demand.OwnerID, pickup.Demand.RoutineRunID, pickup.Demand.Generation).Scan(&raw, &state, &worktree); err != nil {
		return nil, err
	}
	// This explicit, fenced refusal is the only authority for deleting a
	// private receipt whose claim response may have been lost.
	if in.Kind == "abandon" && state == "pending" && worktree == "" {
		return nil, workorders.Fail(409, "lead_claim_uncommitted")
	}
	var saved leadRuntimeBinding
	if len(raw) > 65536 || json.Unmarshal(raw, &saved) != nil || !runtimeProof(saved, pickup) || saved.Launch.Grant.PrincipalID != p.ID || worktree != pickup.WorktreeID {
		return nil, workorders.Fail(409, "lead_writer_conflict")
	}
	launch := saved.Launch
	if _, err = loadLead(ctx, tx, pickup.Demand.ProjectID, true); err != nil {
		return nil, err
	}
	session, err := load(ctx, tx, pickup.Demand.ProjectID, launch.SessionID, true)
	if err != nil {
		return nil, err
	}
	if in.Kind == "abandon" {
		if state == "abandoned" {
			return launch, nil
		}
		var started bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_launch_evidence WHERE launch_id=$1 AND kind='started')`, pickup.Demand.ID).Scan(&started); err != nil {
			return nil, err
		}
		if state != "claimed" || started || session.ProcessOwnership != nil {
			return nil, workorders.Fail(409, "lead_start_already_observed")
		}
		if m.leadRuntime.Abandon == nil {
			return nil, workorders.Fail(409, "lead_runtime_unqualified")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES($1,$2,'abandoned',$3)`, p.TenantID, pickup.Demand.ID, raw); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE routine_launch_intents SET state='abandoned',exit_observed_at=clock_timestamp() WHERE id=$1`, pickup.Demand.ID); err != nil {
			return nil, err
		}
		// Acquire/settle actor locks before status triggers append events.
		if err = m.leadRuntime.Abandon(ctx, tx, p, launch); err != nil {
			return nil, err
		}
		var settled bool
		if err = tx.QueryRow(ctx, `SELECT state='settled' AND used_tokens=0 AND used_microusd=0 AND used_ms=0
 FROM routine_budget_grants WHERE id=$1 AND run_id=$2 AND agent_run_id=$3 AND account_id=$4 AND parent_id IS NULL`, launch.Grant.BudgetGrantID, launch.Grant.RoutineRunID, launch.Grant.AgentRunID, launch.Grant.AccountID).Scan(&settled); err != nil {
			return nil, err
		}
		if !settled {
			return nil, workorders.Fail(409, "lead_abandon_hold_unsettled")
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='cancelled',ended_at=clock_timestamp() WHERE id=$1 AND status='starting'`, launch.Grant.AgentRunID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE harness_sessions SET stopped_at=clock_timestamp(),stop_reason='stopped',phase='stopped',revision=revision+1 WHERE id=$1`, launch.SessionID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE project_leads SET state='waiting_for_room',reason='awaiting_generation',revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND session_id=$2 AND generation=$3`, launch.Grant.ProjectID, launch.SessionID, launch.Generation); err != nil {
			return nil, err
		}
		if err = record(ctx, tx, p, Session{ProjectID: launch.Grant.ProjectID}, "lead_runtime_abandoned", nil, map[string]any{"launch_id": pickup.Demand.ID, "session_id": launch.SessionID}); err != nil {
			return nil, err
		}
		return launch, nil
	}
	if in.Kind == "preflight" {
		if state != "claimed" {
			return nil, workorders.Fail(409, "lead_exit_observed")
		}
		l, e := loadLead(ctx, tx, pickup.Demand.ProjectID, true)
		if e != nil {
			return nil, e
		}
		if l.State != "starting" || l.SessionID == nil || *l.SessionID != launch.SessionID || l.Generation != launch.Generation {
			return nil, workorders.Fail(409, "lead_intent_changed")
		}
		if err = m.validateLeadLaunch(ctx, tx, p, pickup, launch); err != nil {
			return nil, err
		}
		return launch, nil
	}
	identity := in.Ownership
	if identity == nil || identity.DaemonID != pickup.DaemonID || identity.Generation != pickup.DaemonGeneration || !validIdentity(identity.Generation) || !validIdentity(identity.ProcessID) || identity.RootPID < 2 || identity.GroupID != identity.RootPID || identity.StartedAt.IsZero() {
		return nil, workorders.Fail(400, "owned_process_required")
	}
	evidence, _ := json.Marshal(identity)
	var previous []byte
	e := tx.QueryRow(ctx, `SELECT receipt FROM routine_launch_evidence WHERE launch_id=$1 AND kind='started'`, pickup.Demand.ID).Scan(&previous)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if e == nil {
		var prior ownedprocess.Identity
		if json.Unmarshal(previous, &prior) != nil || prior != *identity {
			return nil, workorders.Fail(409, "lead_process_changed")
		}
	}
	if in.Kind == "started" {
		// A lost exit reply may replay started first. Returning its exact existing
		// evidence is harmless and cannot revive an exited generation.
		if e == nil {
			return launch, nil
		}
		if state != "claimed" {
			return nil, workorders.Fail(409, "lead_exit_observed")
		}
		if err = m.reportOwnership(ctx, tx, session, *identity, false); err != nil {
			return nil, err
		}
		// This records an already reserved attempt, even across the window edge.
		// It cannot create/revive a generation or authorize another process.
		if _, err = tx.Exec(ctx, `INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES($1,$2,'started',$3)`, p.TenantID, pickup.Demand.ID, evidence); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='running' WHERE id=$1 AND status='starting'`, launch.Grant.AgentRunID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE project_leads SET state='working',revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND session_id=$2 AND generation=$3 AND state='starting'`, launch.Grant.ProjectID, launch.SessionID, launch.Generation); err != nil {
			return nil, err
		}
	} else {
		if e != nil {
			return nil, workorders.Fail(409, "lead_start_unconfirmed")
		}
		if state == "exit_observed" {
			return launch, nil
		}
		if m.leadRuntime.Exit == nil {
			return nil, workorders.Fail(409, "lead_runtime_unqualified")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES($1,$2,'exit_observed',$3)`, p.TenantID, pickup.Demand.ID, evidence); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE routine_launch_intents SET state='exit_observed',exit_observed_at=clock_timestamp() WHERE id=$1`, pickup.Demand.ID); err != nil {
			return nil, err
		}
		// Make exact exit evidence visible to transactional reservation guards
		// before reconciliation. Unknown usage remains held; callback failure
		// rolls back both evidence and settlement. Acquire its locks before any
		// session-status trigger can append an event.
		if err = m.leadRuntime.Exit(ctx, tx, p, launch, *identity); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE harness_sessions SET stopped_at=clock_timestamp(),stop_reason='process_exited',phase='stopped',revision=revision+1 WHERE id=$1`, launch.SessionID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE project_leads SET state='paused',reason='process_stopped',revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND session_id=$2 AND generation=$3`, launch.Grant.ProjectID, launch.SessionID, launch.Generation); err != nil {
			return nil, err
		}
	}
	if err = record(ctx, tx, p, Session{ProjectID: launch.Grant.ProjectID}, "lead_runtime_"+in.Kind, nil, map[string]any{"launch_id": pickup.Demand.ID, "session_id": launch.SessionID}); err != nil {
		return nil, err
	}
	return launch, nil
}
