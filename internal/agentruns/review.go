// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// QueueReview is the narrow cross-principal dispatch path. It requires an
// immutable review binding and an already selected, approved account. Public
// RunCreate continues to restrict agents to their own ordinary runs.
func QueueReview(ctx context.Context, tx pgx.Tx, p tenant.Principal, o workorders.Order, agentID, profileID, accountID string, personID *string, residency string, trace json.RawMessage) (Run, error) {
	if o.Kind != "review" || o.Review == nil || o.Review.ProfileID == nil || *o.Review.ProfileID != profileID || o.Assignee == nil || *o.Assignee != agentID || o.Review.ReviewerFamily == nil || *o.Review.ReviewerFamily == o.Review.AuthorFamily || !reviewgate.ValidFamily(o.Review.AuthorFamily) {
		return Run{}, workorders.Fail(409, "independent review binding required")
	}
	if err := dispatchable(ctx, tx, o); err != nil {
		return Run{}, err
	}
	var model, harness, family string
	err := tx.QueryRow(ctx, `SELECT p.model,p.harness,p.family FROM model_profiles p JOIN agent_accounts a ON a.tenant_id=p.tenant_id AND a.harness=p.harness
        WHERE p.id=$1 AND a.id=$2 AND a.registered_by_principal_id=$3 AND p.enabled AND p.family=$4
        AND (a.allowed_model_profile_ids IS NULL OR p.id=ANY(a.allowed_model_profile_ids))`, profileID, accountID, agentID, *o.Review.ReviewerFamily).Scan(&model, &harness, &family)
	if err != nil {
		return Run{}, err
	}
	if !harnesslaunch.FamilyMatches(harness, model, family) {
		return Run{}, workorders.Fail(409, "review profile family does not match its provider")
	}
	v, err := scan(tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,requested_account_id,residency,prefs_person_id,trace)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+columns, p.TenantID, o.NodeID, agentID, profileID, model, accountID, modelprefs.Stamp(residency), personID, trace))
	if err != nil {
		return v, err
	}
	if _, err = tx.Exec(ctx, `UPDATE work_order_reviews SET run_id=$2 WHERE work_order_id=$1 AND run_id IS NULL`, o.NodeID, v.ID); err != nil {
		return v, err
	}
	return v, workorders.Record(ctx, tx, p, o.NodeID, "run.created", nil, v)
}
