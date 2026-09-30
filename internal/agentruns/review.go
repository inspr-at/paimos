// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// QueueReview is the narrow cross-principal dispatch path. It requires an
// immutable review binding and an already selected, approved account. Public
// RunCreate continues to restrict agents to their own ordinary runs.
func QueueReview(ctx context.Context, tx pgx.Tx, p tenant.Principal, o workorders.Order, agentID, profileID, accountID string) (Run, error) {
	if o.Kind != "review" || o.Review == nil || o.Review.ProfileID == nil || *o.Review.ProfileID != profileID || o.Assignee == nil || *o.Assignee != agentID || o.Review.ReviewerFamily == nil || *o.Review.ReviewerFamily == o.Review.AuthorFamily {
		return Run{}, workorders.Fail(409, "independent review binding required")
	}
	if err := dispatchable(ctx, tx, o); err != nil {
		return Run{}, err
	}
	var model string
	err := tx.QueryRow(ctx, `SELECT p.model FROM model_profiles p JOIN agent_accounts a ON a.tenant_id=p.tenant_id AND a.harness=p.harness
        WHERE p.id=$1 AND a.id=$2 AND a.registered_by_principal_id=$3 AND p.enabled AND p.family=$4
        AND (a.allowed_model_profile_ids IS NULL OR p.id=ANY(a.allowed_model_profile_ids))`, profileID, accountID, agentID, *o.Review.ReviewerFamily).Scan(&model)
	if err != nil {
		return Run{}, err
	}
	v, err := scan(tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,requested_account_id)
        VALUES($1,$2,$3,$4,$5,$6) RETURNING `+columns, p.TenantID, o.NodeID, agentID, profileID, model, accountID))
	if err != nil {
		return v, err
	}
	if _, err = tx.Exec(ctx, `UPDATE work_order_reviews SET run_id=$2 WHERE work_order_id=$1 AND run_id IS NULL`, o.NodeID, v.ID); err != nil {
		return v, err
	}
	return v, workorders.Record(ctx, tx, p, o.NodeID, "run.created", nil, v)
}
