// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// withFullOrder widens only project visibility for a bounded internal operation.
// Tenant RLS stays enforced. The caller has checked queue management rights and
// holds the tree lock. A savepoint restores visibility even on failure; no full
// partition snapshots escape into the caller's response.
func withFullOrder(ctx context.Context, tx pgx.Tx, fn func(pgx.Tx) error) error {
	step, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = step.Rollback(ctx) }()
	var prior string
	if err = step.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&prior); err != nil {
		return err
	}
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return err
	}
	if err = fn(step); err != nil {
		return err
	}
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, prior); err != nil {
		return err
	}
	return step.Commit(ctx)
}

// AppendRank reads the full shared partition so arrivals append after hidden
// work too. A nil rank preserves priority/FIFO when manual ordering is off.
func AppendRank(ctx context.Context, tx pgx.Tx) (*int, error) {
	var rank *int
	err := withFullOrder(ctx, tx, func(step pgx.Tx) error {
		return step.QueryRow(ctx, CTE+`SELECT CASE WHEN count(queue_rank)>0 THEN coalesce(max(queue_rank),0)::int+1 END FROM queue_ordered WHERE queue_target_agent_id IS NULL`).Scan(&rank)
	})
	return rank, err
}

// ReorderShared renumbers the complete partition, replacing only the slots
// occupied by the caller's visible runs. Hidden work retains both its place and
// relative order; a move to the same visible position is globally a no-op.
func ReorderShared(ctx context.Context, tx pgx.Tx, visible []string) error {
	return withFullOrder(ctx, tx, func(step pgx.Tx) error {
		rows, err := step.Query(ctx, CTE+`SELECT id::text FROM queue_ordered WHERE queue_target_agent_id IS NULL ORDER BY queue_position`)
		if err != nil {
			return err
		}
		full := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			full = append(full, id)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		seen := make(map[string]bool, len(visible))
		for _, id := range visible {
			seen[id] = true
		}
		next := 0
		for i, id := range full {
			if seen[id] {
				full[i] = visible[next]
				next++
			}
		}
		if next != len(visible) {
			return fmt.Errorf("visible queue changed while reordering")
		}
		_, err = step.Exec(ctx, `UPDATE agent_runs r SET queue_rank=q.rank FROM unnest($1::uuid[]) WITH ORDINALITY q(id,rank) WHERE r.id=q.id AND r.status='queued' AND r.queue_target_agent_id IS NULL`, full)
		return err
	})
}

// ResetShared clears manual ranks across the same complete untargeted
// partition. Permission checks and response projection stay caller-visible.
func ResetShared(ctx context.Context, tx pgx.Tx) error {
	return withFullOrder(ctx, tx, func(step pgx.Tx) error {
		_, err := step.Exec(ctx, CTE+`UPDATE agent_runs r SET queue_rank=NULL FROM queue_ordered q WHERE r.id=q.id AND q.queue_target_agent_id IS NULL AND r.queue_rank IS NOT NULL`)
		return err
	})
}
