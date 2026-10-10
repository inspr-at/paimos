// SPDX-License-Identifier: AGPL-3.0-only
package routinebudget

import (
	"context"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// StartCheck returns the final-claim seam for the authenticated daemon. S11
// must connect it alongside daemon/generation/workspace capability checks.
// A reservation replay alone never authorizes launch. Each invocation re-reads
// consent, qualification, exact account, freshness, dial and owner policy.
func (b Broker) StartCheck(p tenant.Principal) agentaccounts.RoutineStartCheck {
	return func(ctx context.Context, tx pgx.Tx, agentRunID, accountID string) error {
		var runID string
		if err := tx.QueryRow(ctx, `SELECT run_id::text FROM routine_budget_grants WHERE agent_run_id=$1 AND account_id=$2 AND parent_id IS NULL`, agentRunID, accountID).Scan(&runID); err != nil {
			return err
		}
		r, err := begin(ctx, tx, p, runID)
		if err != nil {
			return err
		}
		var agent string
		if err := tx.QueryRow(ctx, `SELECT agent_principal_id::text FROM agent_runs WHERE id=$1`, agentRunID).Scan(&agent); err != nil {
			return err
		}
		if p.Kind != tenant.Agent || p.ID != agent {
			return authz.ErrForbidden
		}
		if err := authz.RequireTx(ctx, tx, p, "run.claim", authz.Scope{ProjectID: r.project}); err != nil {
			return err
		}
		// The daemon is not the owner's service identity. The internal checker
		// validates that saved authority independently, never copying credentials.
		owner := tenant.Principal{ID: r.owner, TenantID: p.TenantID, Kind: tenant.Person}
		policy, gate, err := b.authorize(ctx, tx, owner, r)
		if err != nil {
			return err
		}
		var held bool
		if err := tx.QueryRow(ctx, `SELECT state='held' FROM routine_budget_grants WHERE agent_run_id=$1 AND account_id=$2 AND parent_id IS NULL`, agentRunID, accountID).Scan(&held); err != nil {
			return err
		}
		if !held {
			return workorders.Fail(409, "routine_grant_unavailable")
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		harness, billing, host, err := agentaccounts.RequireRoutineCapacityTx(ctx, tx, r.owner, agentRunID, accountID, gate, now)
		if err != nil {
			return err
		}
		if billing != "subscription" && (billing != "api" || !slices.Contains(gate.PaidAccountIDs, accountID)) {
			return workorders.Fail(409, "execution_account_unqualified")
		}
		var originalBilling string
		if err := tx.QueryRow(ctx, `SELECT billing_mode FROM routine_budget_grants WHERE agent_run_id=$1 AND parent_id IS NULL`, agentRunID).Scan(&originalBilling); err != nil {
			return err
		}
		if billing != originalBilling {
			return workorders.Fail(409, "routine_account_changed")
		}
		if policy.Effective.AllowedAccountIDs != nil && !slices.Contains(*policy.Effective.AllowedAccountIDs, accountID) || policy.Effective.AllowedHostIDs != nil && !slices.Contains(*policy.Effective.AllowedHostIDs, host) {
			return workorders.Fail(409, "owner_policy_mismatch")
		}
		return workingSlots(ctx, tx, p, r.owner, harness, agentRunID)
	}
}
