// SPDX-License-Identifier: AGPL-3.0-only
package workorders

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// routineParent finds active budget lineage with a bounded ancestor walk.
// No balance exists before qualified enablement; legacy/off installations keep
// their ordinary order behavior. Truncated ancestry fails closed.
func routineParent(ctx context.Context, tx pgx.Tx, parent *string) (string, error) {
	if parent == nil {
		return "", nil
	}
	// A hidden personal ledger still restricts descendants visible in its
	// output project. This bounded internal decision never returns ledger rows.
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return "", err
	}
	run, err := routineParentInternal(ctx, tx, *parent)
	if err != nil {
		return "", err // caller rolls back every error
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return run, err
}

func routineParentInternal(ctx context.Context, tx pgx.Tx, parent string) (string, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_budget_balances b JOIN routine_runs r ON r.tenant_id=b.tenant_id AND r.id=b.run_id JOIN nodes n ON n.project_id=r.output_project_id WHERE n.id=$1)`, parent).Scan(&enabled); err != nil {
		return "", err
	}
	if !enabled {
		return "", nil
	}
	var run *string
	var truncated bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE ancestors AS (
 SELECT id,parent_id,1 AS depth FROM nodes WHERE id=$1 AND deleted_at IS NULL
 UNION ALL SELECT n.id,n.parent_id,a.depth+1 FROM ancestors a JOIN nodes n ON n.id=a.parent_id WHERE a.depth<64 AND n.deleted_at IS NULL
 ) SELECT (SELECT r.id::text FROM ancestors a JOIN routine_runs r ON r.work_node_id=a.id JOIN routine_budget_balances b ON b.run_id=r.id ORDER BY a.depth LIMIT 1),
 EXISTS(SELECT 1 FROM ancestors WHERE depth=64 AND parent_id IS NOT NULL)`, parent).Scan(&run, &truncated)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", Fail(409, "routine_ancestry_incomplete")
	}
	if run == nil {
		return "", nil
	}
	return *run, nil
}

func requireRoutineChild(ctx context.Context, tx pgx.Tx, p tenant.Principal, in CreateInput) error {
	run, err := routineParent(ctx, tx, in.Parent)
	if err != nil {
		return err
	}
	if run == "" && in.RoutineRunID == "" {
		return nil
	}
	if run == "" || run != in.RoutineRunID {
		return Fail(409, "routine_child_binding_required")
	}
	var project, owner, principal string
	err = tx.QueryRow(ctx, `SELECT r.output_project_id::text,r.owner_principal_id::text,coalesce(r.execution_principal_id::text,'') FROM routine_runs r JOIN routine_budget_balances b ON b.run_id=r.id WHERE r.id=$1`, run).Scan(&project, &owner, &principal)
	if err != nil {
		return err
	}
	if p.ID != owner && p.ID != principal {
		return authz.ErrForbidden
	}
	return authz.RequireTx(ctx, tx, p, "work_orders.write", authz.Scope{ProjectID: project})
}

// BindRoutineOrderTx binds an existing queued order in the budget transaction.
// It does not create a ceiling, change assignment or authorize execution.
func BindRoutineOrderTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID, agentRunID string) error {
	var order, project string
	var parent *string
	err := tx.QueryRow(ctx, `SELECT ar.work_order_id::text,n.parent_id,n.project_id::text FROM agent_runs ar JOIN nodes n ON n.tenant_id=ar.tenant_id AND n.id=ar.work_order_id WHERE ar.id=$1 AND ar.status='queued' AND n.deleted_at IS NULL`, agentRunID).Scan(&order, &parent, &project)
	if err != nil {
		return err
	}
	root, err := routineParent(ctx, tx, parent)
	if err != nil {
		return err
	}
	if root != runID {
		return Fail(409, "routine_order_outside_lineage")
	}
	if err := authz.RequireTx(ctx, tx, p, "work_orders.write", authz.Scope{ProjectID: project}); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO routine_budget_orders(tenant_id,run_id,work_order_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.TenantID, runID, order)
	if err != nil {
		return err
	}
	var bound string
	if err := tx.QueryRow(ctx, `SELECT run_id::text FROM routine_budget_orders WHERE work_order_id=$1`, order).Scan(&bound); err != nil {
		return err
	}
	if bound != runID {
		return Fail(409, "routine_order_binding_conflict")
	}
	return nil
}

// GuardRoutineRunTx is shared by account reservation and final claim. It
// rejects legacy retry/review callers that omit the original shared binding.
// The caller has authenticated the exact run first. The bounded internal read
// must also detect personal routine lineage outside a daemon's node projection;
// it returns only a binding decision and restores visibility before returning.
func GuardRoutineRunTx(ctx context.Context, tx pgx.Tx, agentRunID string) (bool, error) {
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return false, err
	}
	routine, err := guardRoutineRun(ctx, tx, agentRunID)
	if err != nil {
		return false, err // caller must roll back every error
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return routine, err
}

func guardRoutineRun(ctx context.Context, tx pgx.Tx, agentRunID string) (bool, error) {
	var parent *string
	var order string
	err := tx.QueryRow(ctx, `SELECT n.parent_id,n.id::text FROM agent_runs ar JOIN nodes n ON n.tenant_id=ar.tenant_id AND n.id=ar.work_order_id WHERE ar.id=$1`, agentRunID).Scan(&parent, &order)
	if err != nil {
		return false, err
	}
	run, err := routineParent(ctx, tx, parent)
	if err != nil || run == "" {
		return false, err
	}
	var bound string
	err = tx.QueryRow(ctx, `SELECT run_id::text FROM routine_budget_orders WHERE work_order_id=$1`, order).Scan(&bound)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && bound != run {
		return false, Fail(409, "routine_child_binding_required")
	}
	return true, err
}
