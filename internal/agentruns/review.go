// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"encoding/json"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// BindRoutineCompletion records durable continuation demand from accepted
// telemetry. It grants the reporting worker no review or retry authority.
func BindRoutineCompletion(boundary httpapi.Module, completion UsageRecorder) error {
	m, ok := boundary.(*module)
	if !ok || completion == nil {
		return workorders.Fail(409, "routine_completion_unavailable")
	}
	m.routineCompletion = completion
	return nil
}

// QueueRoutineFixTx retains the previous writer's order and checkout identity.
// Its caller resolves the current registry route and persists the shared budget
// hold and launch before committing. Ordinary HTTP clients cannot use it.
func QueueRoutineFixTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, routine, previous, profile, account string, trace json.RawMessage) (Run, error) {
	var order, agent, project, owner string
	err := tx.QueryRow(ctx, `SELECT ar.work_order_id::text,ar.agent_principal_id::text,r.output_project_id::text,r.owner_principal_id::text FROM agent_runs ar JOIN routine_attempts a ON a.agent_run_id=ar.id JOIN routine_runs r ON r.id=a.run_id WHERE ar.id=$1 AND r.id=$2 AND a.role IN ('build','fix') AND ar.status IN ('completed','failed') AND ar.account_id=$3`, previous, routine, account).Scan(&order, &agent, &project, &owner)
	if err != nil {
		return Run{}, err
	}
	if p.Kind != tenant.Person || p.ID != owner {
		return Run{}, authz.ErrForbidden
	}
	for _, perm := range []string{"run.create", "work_orders.write"} {
		if err := authz.RequireTx(ctx, tx, p, perm, authz.Scope{ProjectID: project}); err != nil {
			return Run{}, err
		}
	}
	if err := accountuse.RequireProject(ctx, tx, account, project); err != nil {
		return Run{}, err
	}
	var model string
	if err := tx.QueryRow(ctx, `SELECT m.model FROM model_profiles m JOIN agent_accounts a ON a.harness=m.harness WHERE m.id=$1 AND m.enabled AND a.id=$2 AND a.registered_by_principal_id=$3 AND aeon_account_allows_profile(a.harness,a.allowed_model_profile_ids,m.id)`, profile, account, agent).Scan(&model); err != nil {
		return Run{}, err
	}
	return scan(tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,requested_account_id,prefs_person_id,retry_of_run_id,trace) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+columns, p.TenantID, order, agent, profile, model, account, p.ID, previous, trace))
}

// QueueReview is the narrow cross-principal dispatch path. It requires an
// immutable review binding and an already selected, approved account. Public
// RunCreate continues to restrict agents to their own ordinary runs.
func QueueReview(ctx context.Context, tx pgx.Tx, p tenant.Principal, o workorders.Order, agentID, profileID, accountID string, personID *string, residency string, trace json.RawMessage) (Run, error) {
	var pending []events.Change
	run, err := QueueReviewDeferred(ctx, tx, p, o, agentID, profileID, accountID, personID, residency, trace, &pending)
	if err != nil {
		return run, err
	}
	for _, change := range pending {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return Run{}, err
		}
	}
	return run, nil
}

// QueueReviewDeferred contributes resources and snapshots to its caller's batch.
func QueueReviewDeferred(ctx context.Context, tx pgx.Tx, p tenant.Principal, o workorders.Order, agentID, profileID, accountID string, personID *string, residency string, trace json.RawMessage, pending *[]events.Change) (Run, error) {
	if o.Kind != "review" || o.Review == nil || o.Review.ProfileID == nil || *o.Review.ProfileID != profileID || o.Assignee == nil || *o.Assignee != agentID || o.Review.ReviewerFamily == nil || *o.Review.ReviewerFamily == o.Review.AuthorFamily || !reviewgate.ValidFamily(o.Review.AuthorFamily) {
		return Run{}, workorders.Fail(409, "independent review binding required")
	}
	if err := dispatchable(ctx, tx, o); err != nil {
		return Run{}, err
	}
	var project *string
	if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1`, o.NodeID).Scan(&project); err != nil {
		return Run{}, err
	}
	projectID := ""
	if project != nil {
		projectID = *project
	}
	if err := accountuse.RequireProject(ctx, tx, accountID, projectID); err != nil {
		return Run{}, err
	}
	var model, harness, family string
	err := tx.QueryRow(ctx, `SELECT p.model,p.harness,p.family FROM model_profiles p JOIN agent_accounts a ON a.tenant_id=p.tenant_id AND a.harness=p.harness
        WHERE p.id=$1 AND a.id=$2 AND a.registered_by_principal_id=$3 AND p.enabled AND p.family=$4
        AND aeon_account_allows_profile(a.harness,a.allowed_model_profile_ids,p.id)`, profileID, accountID, agentID, *o.Review.ReviewerFamily).Scan(&model, &harness, &family)
	if err != nil {
		return Run{}, err
	}
	if !harnesslaunch.FamilyMatches(harness, model, family) {
		return Run{}, workorders.Fail(409, "review profile family does not match its provider")
	}
	var placement modelregistry.WorkPlacement
	if err := json.Unmarshal(trace, &placement.PreferenceTrace); err != nil {
		return Run{}, err
	}
	placement.PlannedProfileID = &profileID
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(trace, &merged); err != nil {
		return Run{}, err
	}
	if merged == nil {
		merged = map[string]json.RawMessage{}
	}
	raw, err := placement.JSON()
	if err != nil {
		return Run{}, err
	}
	merged["work_placement"] = raw
	trace, err = json.Marshal(merged)
	if err != nil {
		return Run{}, err
	}
	v, err := scan(tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,requested_account_id,residency,prefs_person_id,trace)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+columns, p.TenantID, o.NodeID, agentID, profileID, model, accountID, modelprefs.Stamp(residency), personID, trace))
	if err != nil {
		return v, err
	}
	if _, err = tx.Exec(ctx, `UPDATE work_order_reviews SET run_id=$2 WHERE work_order_id=$1 AND run_id IS NULL`, o.NodeID, v.ID); err != nil {
		return v, err
	}
	*pending = append(*pending, events.Change{NodeID: &o.NodeID, Type: "run.created", After: v})
	return v, nil
}
