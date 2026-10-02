// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// LiveBlocked checks dependency completion, not mere relation presence. Its
// content-free result includes hidden-project blockers while tenant RLS stays
// enforced. The caller holds the tree fence for scheduling or claim decisions.
func LiveBlocked(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	step, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer step.Rollback(ctx)
	var prior string
	if err = step.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&prior); err != nil {
		return false, err
	}
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return false, err
	}
	var blocked bool
	err = step.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_relations r JOIN nodes b ON b.tenant_id=r.tenant_id AND b.id=r.source_node_id WHERE r.target_node_id=$1 AND r.type='blocks' AND b.deleted_at IS NULL AND replace(replace(lower(btrim(b.state)),' ','_'),'-','_') NOT IN ('done','delivered','accepted','cancelled','archived'))`, id).Scan(&blocked)
	if err != nil {
		return false, err
	}
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, prior); err != nil {
		return false, err
	}
	return blocked, step.Commit(ctx)
}
