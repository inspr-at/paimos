// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// NativeBrowserRuntimeReader reads bounded trusted qualification evidence in
// the caller's final fenced transaction. No host probe/network work is allowed.
// Nil leaves browser routes unavailable; ordinary coding/manual routes remain
// independent. S13 selects candidates without changing the pinned model family.
type NativeBrowserRuntimeReader func(context.Context, pgx.Tx, agentsetup.RoutineGrant) (agentsetup.NativeBrowserRuntime, error)

func RequireNativeBrowserRouteTx(ctx context.Context, tx pgx.Tx, g agentsetup.RoutineGrant, read NativeBrowserRuntimeReader) error {
	if g.RuntimeCapability != agentsetup.RoutineBrowserCapability {
		return nil
	}
	if read == nil {
		return workorders.Fail(409, "native_browser_launcher_unavailable")
	}
	if g.Validate() != nil {
		return workorders.Fail(409, "native_browser_qualification_unavailable")
	}
	runtime, err := read(ctx, tx, g)
	if err != nil {
		return err
	}
	var now time.Time
	var harness, modelHarness string
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp(),a.harness,m.harness FROM agent_accounts a
 JOIN agent_runs r ON r.tenant_id=a.tenant_id AND r.account_id=a.id
 JOIN model_profiles m ON m.tenant_id=r.tenant_id AND m.id=r.model_profile_id
 WHERE a.id=$1 AND r.id=$2 AND m.enabled`, g.AccountID, g.AgentRunID).Scan(&now, &harness, &modelHarness); err != nil {
		return err
	}
	if harness != "claude" || modelHarness != "claude" || runtime.Require(g, now) != nil {
		return workorders.Fail(409, "native_browser_qualification_unavailable")
	}
	// Reuse the existing live account/enrollment and owner authority predicates.
	// Callers hold the tenant/tree/pairing fence through the final mutation.
	if err := agentpairing.RequireRoutineAccountTx(ctx, tx, g); err != nil {
		return err
	}
	p := tenant.Principal{ID: g.PrincipalID, TenantID: g.TenantID, Kind: tenant.Agent, Scopes: []string{"run.claim"}}
	if err := authz.RequireTx(ctx, tx, p, "run.claim", authz.Scope{ProjectID: g.ProjectID}); err != nil {
		return err
	}
	view, err := agentpairing.UnattendedForPrincipal(ctx, tx, g.PrincipalID)
	if err != nil {
		return err
	}
	if view.Status != "ready" {
		return workorders.Fail(409, view.Reason)
	}
	return nil
}
