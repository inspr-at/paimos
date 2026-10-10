// SPDX-License-Identifier: AGPL-3.0-only
package routinebudget

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// GateReader reads local accepted gate evidence, never caller-provided ages.
// No production reader is installed by this slice. S26 must qualify its binding.
type GateReader func(context.Context, pgx.Tx, string) (agentaccounts.RoutineGateContract, error)
type Broker struct {
	Runtime modelregistry.ExecutionRuntimeReader
	Gates   GateReader
}

type runBinding struct {
	id, recurrence, project, owner, principal, work string
	revision                                        int64
	assignment                                      []byte
}

// Every entry takes tenant -> tree -> pairing before any record/account rows.
// The caller must use a bounded db.InTenant transaction, roll back every error,
// and append its audit events last. There are no external effects here.
func begin(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID string) (runBinding, error) {
	var r runBinding
	if !workorders.UUID(runID) || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
		return r, workorders.Fail(400, "invalid_budget_binding")
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')=$1`, p.TenantID).Scan(&same); err != nil {
		return r, err
	}
	if !same {
		return r, authz.ErrForbidden
	}
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return r, err
	}
	err := tx.QueryRow(ctx, `SELECT id::text,recurrence_id::text,output_project_id::text,owner_principal_id::text,coalesce(execution_principal_id::text,''),work_node_id::text,definition_revision,assignment FROM routine_runs WHERE id=$1`, runID).Scan(&r.id, &r.recurrence, &r.project, &r.owner, &r.principal, &r.work, &r.revision, &r.assignment)
	return r, err
}

func (b Broker) authorize(ctx context.Context, tx pgx.Tx, p tenant.Principal, r runBinding) (modelregistry.LeadSettings, agentaccounts.RoutineGateContract, error) {
	var policy modelregistry.LeadSettings
	var gate agentaccounts.RoutineGateContract
	principal, err := recurrences.RequireExecutionConsentTx(ctx, tx, p.TenantID, r.recurrence, r.revision, b.Runtime)
	if err != nil {
		return policy, gate, err
	}
	if principal != r.principal || p.ID != principal && p.ID != r.owner {
		return policy, gate, authz.ErrForbidden
	}
	for _, permission := range []string{"run.create", "work_orders.write"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: r.project}); err != nil {
			return policy, gate, err
		}
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT execute_consent AND consent_revision=definition_revision AND state NOT IN ('completed','cancelled','failed') FROM routine_runs WHERE id=$1`, r.id).Scan(&valid); err != nil {
		return policy, gate, err
	}
	if !valid {
		return policy, gate, workorders.Fail(409, "routine_run_unavailable")
	}
	policy, err = modelregistry.LoadRoutineLeadPolicyTx(ctx, tx, p.TenantID, r.project)
	if err != nil {
		return policy, gate, err
	}
	settings, err := modelregistry.ProjectExecutionTx(ctx, tx, p.TenantID, r.project, b.Runtime)
	if err != nil {
		return policy, gate, err
	}
	if !settings.AutomaticLaunchEnabled || settings.QualificationID == nil {
		return policy, gate, workorders.Fail(409, "automatic_launch_disabled")
	}
	if b.Gates == nil {
		return policy, gate, workorders.Fail(409, "admission_contract_unqualified")
	}
	gate, err = b.Gates(ctx, tx, *settings.QualificationID)
	if err != nil {
		return policy, gate, workorders.Fail(409, "admission_contract_unavailable")
	}
	q, err := modelregistry.LoadRoutineQualificationTx(ctx, tx, r.project, *settings.QualificationID)
	if err != nil {
		return policy, gate, err
	}
	if !gate.Valid() || !pin(gate.CapabilityDigest) || gate.QualificationID != q.ID || gate.CapabilityDigest != q.Runtime.CapabilityDigest {
		return policy, gate, workorders.Fail(409, "admission_contract_unqualified")
	}
	return policy, gate, nil
}

func loadBalance(ctx context.Context, tx pgx.Tx, id string) (Balance, error) {
	var b Balance
	err := tx.QueryRow(ctx, `SELECT run_id::text,mode,token_ceiling,money_ceiling_microusd,recovery_ceiling_ms,settled_tokens,settled_microusd,settled_ms,held_tokens,held_microusd,held_ms FROM routine_budget_balances WHERE run_id=$1 FOR NO KEY UPDATE`, id).Scan(&b.RunID, &b.Mode, &b.TokenCeiling, &b.MoneyCeiling, &b.RecoveryMS, &b.Settled.Tokens, &b.Settled.PaidMicroUSD, &b.Settled.RecoveryMS, &b.Held.Tokens, &b.Held.PaidMicroUSD, &b.Held.RecoveryMS)
	return b, err
}
func writeBalance(ctx context.Context, tx pgx.Tx, b Balance) error {
	_, err := tx.Exec(ctx, `UPDATE routine_budget_balances SET settled_tokens=$2,settled_microusd=$3,settled_ms=$4,held_tokens=$5,held_microusd=$6,held_ms=$7 WHERE run_id=$1`, b.RunID, b.Settled.Tokens, b.Settled.PaidMicroUSD, b.Settled.RecoveryMS, b.Held.Tokens, b.Held.PaidMicroUSD, b.Held.RecoveryMS)
	return err
}
func ensureBalance(ctx context.Context, tx pgx.Tx, p tenant.Principal, r runBinding, policy modelregistry.LeadSettings) (Balance, error) {
	if len(r.assignment) > 131072 {
		return Balance{}, workorders.Fail(409, "assignment_unavailable")
	}
	var a recurrences.Assignment
	if json.Unmarshal(r.assignment, &a) != nil {
		return Balance{}, workorders.Fail(409, "assignment_unavailable")
	}
	recovery := policy.Effective.Recovery
	if recovery == nil || recovery.AgentHours <= 0 || recovery.AgentHours > 10000 || math.IsNaN(recovery.AgentHours) || math.IsInf(recovery.AgentHours, 0) {
		return Balance{}, workorders.Fail(409, "recovery_budget_unavailable")
	}
	limit := int64(math.Floor(recovery.AgentHours * 3600000))
	budget := a.Budget
	if budget.Mode != "off" && budget.Mode != "tokens" && budget.Mode != "money" && budget.Mode != "both" {
		return Balance{}, workorders.Fail(409, "invalid_budget_mode")
	}
	_, err := tx.Exec(ctx, `INSERT INTO routine_budget_balances(tenant_id,run_id,owner_principal_id,mode,token_ceiling,money_ceiling_microusd,recovery_ceiling_ms) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, p.TenantID, r.id, r.owner, budget.Mode, budget.TokenCeiling, budget.MoneyCeilingMicroUSD, limit)
	if err != nil {
		return Balance{}, err
	}
	return loadBalance(ctx, tx, r.id)
}

const grantColumns = `id::text,run_id::text,attempt_id::text,action_id::text,grant_key,parent_id::text,agent_run_id::text,account_id::text,maximum_tokens,maximum_microusd,maximum_ms,state,used_tokens,used_microusd,used_ms,billing_mode`

func scanGrant(row pgx.Row) (Grant, error) {
	var g Grant
	err := row.Scan(&g.ID, &g.RunID, &g.AttemptID, &g.ActionID, &g.GrantKey, &g.ParentID, &g.AgentRunID, &g.AccountID, &g.Maximum.Tokens, &g.Maximum.PaidMicroUSD, &g.Maximum.RecoveryMS, &g.State, &g.Usage.Tokens, &g.Usage.PaidMicroUSD, &g.Usage.RecoveryMS, &g.BillingMode)
	return g, err
}
func replay(ctx context.Context, tx pgx.Tx, in Reservation, parent *string) (Grant, bool, error) {
	g, err := scanGrant(tx.QueryRow(ctx, `SELECT `+grantColumns+` FROM routine_budget_grants WHERE run_id=$1 AND attempt_id=$2 AND action_id=$3 AND grant_key=$4`, in.RunID, in.AttemptID, in.ActionID, in.GrantKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return g, false, nil
	}
	if err != nil {
		return g, false, err
	}
	if g.Maximum != in.Maximum || g.AgentRunID != in.AgentRunID || g.AccountID != in.AccountID || (g.ParentID == nil) != (parent == nil) || parent != nil && *g.ParentID != *parent {
		return g, false, workorders.Fail(409, "grant_replay_conflict")
	}
	return g, true, nil
}

// ReserveTx reserves one actor's build/lead/evaluate/review/fix envelope and
// slot. Provider calls must consume subgrants; an envelope is not billable work.
func (b Broker) ReserveTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Reservation) (Grant, error) {
	if !in.valid() {
		return Grant{}, workorders.Fail(400, "invalid_budget_reservation")
	}
	r, err := begin(ctx, tx, p, in.RunID)
	if err != nil {
		return Grant{}, err
	}
	policy, gate, err := b.authorize(ctx, tx, p, r)
	if err != nil {
		return Grant{}, err
	}
	if prior, ok, err := replay(ctx, tx, in, nil); err != nil || ok {
		return prior, err
	}
	var role string
	var valid bool
	err = tx.QueryRow(ctx, `SELECT a.role,a.agent_run_id=$3::uuid AND a.assignment_digest=r.assignment_digest AND a.state='pending'
 AND (x.attempt_id IS NULL OR x.attempt_id=a.id) AND ar.status='queued' AND ar.purpose='managed'
 AND ar.account_id=$5::uuid AND (a.principal_id IS NULL OR a.principal_id=ar.agent_principal_id)
 AND n.project_id=r.output_project_id AND n.deleted_at IS NULL
 FROM routine_attempts a JOIN routine_runs r ON r.tenant_id=a.tenant_id AND r.id=a.run_id
 JOIN routine_actions x ON x.tenant_id=a.tenant_id AND x.run_id=a.run_id AND x.id=$4
 JOIN agent_runs ar ON ar.tenant_id=a.tenant_id AND ar.id=a.agent_run_id JOIN nodes n ON n.tenant_id=ar.tenant_id AND n.id=ar.work_order_id
 WHERE a.run_id=$1 AND a.id=$2`, in.RunID, in.AttemptID, in.AgentRunID, in.ActionID, in.AccountID).Scan(&role, &valid)
	if err != nil {
		return Grant{}, err
	}
	if !valid || !slices.Contains([]string{"lead", "evaluate", "build", "review", "fix"}, role) {
		return Grant{}, workorders.Fail(409, "attempt_action_unbound")
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return Grant{}, err
	}
	harness, billing, host, err := agentaccounts.RequireRoutineCapacityTx(ctx, tx, r.owner, in.AgentRunID, in.AccountID, gate, now)
	if err != nil {
		return Grant{}, err
	}
	if policy.Effective.AllowedAccountIDs != nil && !slices.Contains(*policy.Effective.AllowedAccountIDs, in.AccountID) || policy.Effective.AllowedHostIDs != nil && !slices.Contains(*policy.Effective.AllowedHostIDs, host) {
		return Grant{}, workorders.Fail(409, "owner_policy_mismatch")
	}
	// The accepted execution-account restriction remains subscription-only.
	// A paid API path requires a separate approved context and qualified adapter.
	if billing != "subscription" && (billing != "api" || !slices.Contains(gate.PaidAccountIDs, in.AccountID)) {
		return Grant{}, workorders.Fail(409, "execution_account_unqualified")
	}
	if billing == "subscription" && in.Maximum.PaidMicroUSD != 0 {
		return Grant{}, workorders.Fail(409, "subscription_paid_charge_invalid")
	}
	if err := workingSlots(ctx, tx, p, r.owner, harness); err != nil {
		return Grant{}, err
	}
	balance, err := ensureBalance(ctx, tx, p, r, policy)
	if err != nil {
		return Grant{}, err
	}
	// Every actor and retry consumes the same finite run-wide attempt count.
	// Switching from build to review/fix cannot reset the recovery allowance.
	var attempts int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM routine_budget_grants WHERE run_id=$1 AND parent_id IS NULL`, r.id).Scan(&attempts)
	if err != nil {
		return Grant{}, err
	}
	if policy.Effective.Recovery == nil || attempts >= policy.Effective.Recovery.MaxAttempts {
		return Grant{}, workorders.Fail(409, "recovery_attempts_exhausted")
	}
	balance, err = balance.reserve(in.Maximum)
	if err != nil {
		return Grant{}, err
	}
	if err := workorders.BindRoutineOrderTx(ctx, tx, p, r.id, in.AgentRunID); err != nil {
		return Grant{}, err
	}
	if err := writeBalance(ctx, tx, balance); err != nil {
		return Grant{}, err
	}
	return insertGrant(ctx, tx, p, r, in, nil, harness, billing)
}

func insertGrant(ctx context.Context, tx pgx.Tx, p tenant.Principal, r runBinding, in Reservation, parent *string, harness, billing string) (Grant, error) {
	return scanGrant(tx.QueryRow(ctx, `INSERT INTO routine_budget_grants(tenant_id,run_id,owner_principal_id,attempt_id,action_id,grant_key,parent_id,agent_run_id,account_id,harness,billing_mode,maximum_tokens,maximum_microusd,maximum_ms)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING `+grantColumns, p.TenantID, in.RunID, r.owner, in.AttemptID, in.ActionID, in.GrantKey, parent, in.AgentRunID, in.AccountID, harness, billing, in.Maximum.Tokens, in.Maximum.PaidMicroUSD, in.Maximum.RecoveryMS))
}

// workingSlots uses the existing dial/harness predicate, counting all still
// unconfirmed routine roots and reporting sessions once per agent run.
func workingSlots(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, harness string, exclude ...string) error {
	person := tenant.Principal{ID: owner, TenantID: p.TenantID, Kind: tenant.Person}
	snapshot, err := agentplan.ReadTx(ctx, tx, person)
	if err != nil {
		return err
	}
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return err
	}
	var omitted any
	if len(exclude) == 1 {
		omitted = exclude[0]
	}
	rows, err := tx.Query(ctx, `SELECT g.harness,count(*) FROM routine_budget_grants g WHERE g.owner_principal_id=$1 AND g.parent_id IS NULL AND g.state<>'settled' AND ($2::uuid IS NULL OR g.agent_run_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM harness_sessions s WHERE s.run_id=g.agent_run_id AND s.stopped_at IS NULL AND s.archived_at IS NULL AND s.phase<>'stopped') GROUP BY g.harness`, owner, omitted)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h string
		var n int
		if err = rows.Scan(&h, &n); err != nil {
			break
		}
		snapshot.Running[h] += n
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system); err != nil {
		return err
	}
	// Daily policy was checked for the exact account above; this call shares the
	// concurrency calculation without interpreting unknown quota percentages.
	if ok, reason := agentplan.CanStart(snapshot.Plan, snapshot.Running, harness, agentplan.DailyDecision{}); !ok {
		return workorders.Fail(409, "dial_"+reason)
	}
	return nil
}
