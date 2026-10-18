// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ReadDailyPolicyTx reads policy for an already-authorized model/dispatch
// target. It grants no plan-read or account authority and publishes no quota.
// Mutation callers must retain their tenant fence through the final write.
func ReadDailyPolicyTx(ctx context.Context, tx pgx.Tx, tenantID, owner string, now time.Time) (agentplan.Snapshot, error) {
	canonical, err := agentplan.OwnerTx(ctx, tx, tenantID, owner)
	if err != nil {
		return agentplan.Snapshot{}, err
	}
	raw, _, err := agentplan.ReadPreferenceTx(ctx, tx, tenantID, canonical)
	if err != nil {
		return agentplan.Snapshot{}, err
	}
	plan, _, err := agentplan.Decode(raw)
	if err != nil {
		return agentplan.Snapshot{}, err
	}
	out := agentplan.Snapshot{Plan: plan, PrincipalID: canonical}
	err = PopulateDailyTx(ctx, tx, tenant.Principal{ID: canonical, TenantID: tenantID, Kind: tenant.Person}, &out, now)
	return out, err
}

// dailyAccountWaitTx shares the dial calculation without calling routing
// advice. Run-now and recovery permits do not bypass the daily ceiling.
func dailyAccountWaitTx(ctx context.Context, tx pgx.Tx, a Account, now time.Time) (*CapacityWait, error) {
	unknown := waitFor("daily_limit_unknown")
	// The daily ceiling is the owner's saved plan. An unlinked account has
	// no such plan; its session and quota windows stay on the existing gate.
	if a.OwnerPersonID == nil {
		return nil, nil
	}
	var tenantID string
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&tenantID); err != nil {
		return nil, err
	}
	owner, err := agentplan.OwnerTx(ctx, tx, tenantID, *a.OwnerPersonID)
	if err != nil {
		return unknown, err
	}
	raw, _, err := agentplan.ReadPreferenceTx(ctx, tx, tenantID, owner)
	if err != nil {
		return unknown, err
	}
	plan, _, err := agentplan.Decode(raw)
	if err != nil {
		return unknown, nil
	}
	zone, points, start, end, err := DailyContextTx(ctx, tx, owner, now)
	if err != nil {
		return unknown, err
	}
	privacy, err := accountprivacy.Load(ctx, tx, tenant.Principal{ID: owner, TenantID: tenantID, Kind: tenant.Person}, []string{a.ID})
	if err != nil {
		return nil, err
	}
	// A withheld projection has no usable percentage. It is not exhaustion
	// and must not block the quota gate, including when a private sibling
	// shares a resource.
	if !privacy[a.ID] {
		return nil, nil
	}
	d, explicit := plan.Daily[a.Harness]
	if !explicit {
		d = agentplan.DefaultDaily()
	}
	item, _, err := dailyAccountTx(ctx, tx, a, agentplan.DailyAccount{AccountID: a.ID, Freshness: "unknown"}, d, explicit, tenantID, owner, points, start, end, now)
	if err != nil {
		return unknown, err
	}
	reason := agentplan.DailyAccountReason(item, now)
	if reason == "" {
		return nil, nil
	}
	wait := &CapacityWait{Code: reason, Timezone: zone}
	if reason == "daily_limit" {
		wait.Until = &end
	}
	return wait, nil
}

// dailyStartAccountTx turns malformed/unreadable policy into a bounded refusal
// using a savepoint, so a failed SQL read cannot poison the outer transaction.
func dailyStartAccountTx(ctx context.Context, tx pgx.Tx, a Account, now time.Time) (*CapacityWait, error) {
	input, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	wait, err := dailyAccountWaitTx(ctx, input, a, now)
	if err != nil {
		if rollbackErr := input.Rollback(ctx); rollbackErr != nil {
			return nil, rollbackErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return waitFor("daily_limit_unknown"), nil
	}
	if err := input.Commit(ctx); err != nil {
		return nil, err
	}
	return wait, nil
}
