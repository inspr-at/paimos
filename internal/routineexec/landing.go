// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/routineactions"
	"github.com/jackc/pgx/v5"
)

// landTenant recovers only persisted catalogue intents on enabled projects.
// It makes no worker merge/deploy call and writes no node status. The broker
// rechecks current consent/evaluation/owner inside each final-write fence.
func (e *Executor) landTenant(ctx context.Context, tid string) (Pass, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := Pass{}
	if e.options.Actions == nil {
		return out, nil
	}
	type candidate struct{ id, kind string }
	var candidates []candidate
	err := db.InTenant(db.AllProjects(ctx, "routine pipeline recovery"), e.pool, tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.id::text,a.kind FROM routine_actions a JOIN routine_runs r ON r.id=a.run_id JOIN project_routine_execution_settings s ON s.project_id=r.output_project_id JOIN routine_effect_outbox o ON o.action_id=a.id
 WHERE s.automatic_launch_enabled AND r.state IN ('running','awaiting_pipeline') AND a.kind IN ('pr.open','pipeline.request') AND a.state<>'succeeded' AND o.state<>'succeeded'
 ORDER BY a.created_at,a.id LIMIT 33`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.kind); err != nil {
				return err
			}
			candidates = append(candidates, c)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	if len(candidates) > batchSize {
		out.Partial = true
		candidates = candidates[:batchSize]
	}
	var failures []error
	for _, c := range candidates {
		out.Scanned++
		var r routineactions.Receipt
		if c.kind == "pr.open" {
			r, err = e.options.Actions.ReconcilePR(ctx, tid, c.id)
		} else {
			r, err = e.options.Actions.ReconcilePipeline(ctx, tid, c.id)
		}
		if err != nil {
			failures = append(failures, err)
			out.Waiting++
		} else if r.State == "succeeded" {
			out.Dispatched++
		} else {
			out.Waiting++
		}
		if ctx.Err() != nil {
			out.Partial = true
			break
		}
	}
	return out, errors.Join(failures...)
}

// nonCodeLandingTx requires an independently authorized, durable non-code
// pipeline action. Missing range alone cannot declare a code result complete.
func nonCodeLandingTx(ctx context.Context, tx pgx.Tx, run string) (bool, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT o.payload FROM routine_effect_outbox o JOIN routine_actions a ON a.id=o.action_id WHERE o.run_id=$1 AND o.kind='pipeline.request' AND a.evaluation_id IS NOT NULL AND a.state='pending' ORDER BY a.created_at,a.id LIMIT 1`, run).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var v struct {
		Request routineactions.Request `json:"request"`
	}
	if len(raw) > routineactions.MaxBodyBytes || json.Unmarshal(raw, &v) != nil || v.Request.Validate() != nil {
		return false, nil
	}
	return v.Request.PipelineRequest != nil && v.Request.PipelineRequest.NonCode, nil
}
