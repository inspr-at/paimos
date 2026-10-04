// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RunWorkLifecycle survives closed sheets and server restarts. Discovery reads
// are system-scoped; application always re-enters the original person's current
// visibility and permissions. Revocation leaves the intent waiting, never done.
func RunWorkLifecycle(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	m := &Module{pool: pool, events: SQLWriter{}}
	after := "00000000-0000-0000-0000-000000000000"
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pass, cancel := context.WithTimeout(ctx, 20*time.Second)
		rows, err := pool.Query(pass, `SELECT id::text FROM tenants WHERE id>$1::uuid ORDER BY id LIMIT 100`, after)
		if err != nil {
			cancel()
			continue
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err == nil {
			for _, id := range ids {
				if e := m.sweepWorkLifecycleTenant(pass, id); e != nil && pass.Err() == nil {
					slog.Warn("work lifecycle sweep failed", "tenant_id", id, "error", e)
				}
			}
		}
		if len(ids) < 100 {
			after = "00000000-0000-0000-0000-000000000000"
		} else {
			after = ids[len(ids)-1]
		}
		cancel()
	}
}

func (m *Module) sweepWorkLifecycleTenant(ctx context.Context, tenantID string) error {
	type job struct{ id, node, person string }
	jobs := []job{}
	// next_attempt_at is scheduler metadata, not user authority. This bounded
	// claim makes failed/denied intents yield to other jobs instead of starving them.
	err := db.InTenant(db.AllProjects(ctx, "AEON-652 lifecycle scheduling"), m.pool, tenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `WITH due AS(SELECT id FROM work_lifecycle_actions WHERE state='waiting' AND next_attempt_at<=clock_timestamp() ORDER BY next_attempt_at,id LIMIT 20 FOR UPDATE)
 UPDATE work_lifecycle_actions a SET next_attempt_at=clock_timestamp()+interval '10 seconds' FROM due WHERE a.id=due.id RETURNING a.id::text,a.node_id::text,a.requested_by::text`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j job
			if err = rows.Scan(&j.id, &j.node, &j.person); err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, j := range jobs {
		p := tenant.Principal{ID: j.person, TenantID: tenantID, Kind: tenant.Person}
		personCtx := tenant.WithPrincipal(ctx, p)
		err = db.InTransaction(personCtx, m.pool, func(ctx context.Context) error {
			return m.tx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
				if err := db.LockWorkTreeTx(ctx, tx); err != nil {
					return err
				}
				n, err := workPermission(ctx, tx, p, j.node, "nodes.write")
				if err != nil {
					return err
				}
				a, err := loadWorkAction(ctx, tx, j.node, j.id)
				if err != nil {
					return err
				}
				if a.State != "waiting" || a.requester != p.ID {
					return nil
				}
				if _, err = tx.Exec(ctx, `SELECT set_config('aeon.work_lifecycle_action',$1,true)`, a.ID); err != nil {
					return err
				}
				batch := &deferredWorkEvents{}
				worker := *m
				worker.events = batch
				before := len(a.Result)
				if err = worker.finishWorkAction(ctx, tx, p, n, a); err != nil {
					return err
				}
				for _, event := range batch.events {
					if err = m.events.WriteEvent(ctx, tx, event); err != nil {
						return err
					}
				}
				if before != len(a.Result) || a.State == "completed" {
					event := "work.lifecycle_waiting"
					if a.State == "completed" {
						event = "work.lifecycle_completed"
					}
					return m.record(ctx, tx, p.ID, &j.node, event, nil, a)
				}
				return nil
			})
		})
		// Conflict/revocation is durable waiting. A person can review or abandon it;
		// background retries cannot bypass the original target or permission checks.
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}
