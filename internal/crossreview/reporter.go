// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

func statusState(v Review) string {
	if v.GateOpen {
		return "success"
	}
	if v.Result.Verdict == "changes" && v.Status == "completed" {
		if v.modelEvidence != "vendor_reported" || v.Model == nil || v.EffectiveModel == nil || !reviewgate.ModelMatches(*v.Model, *v.EffectiveModel) {
			return "error"
		}
		return "failure"
	}
	switch v.Status {
	case "queued", "starting", "running", "waiting":
		return "pending"
	default:
		return "error"
	}
}

// RunStatusReporter is optional. A session advisory lock admits one reporter
// across server replicas. Network I/O holds no transaction or row lock.
func (m *Module) RunStatusReporter(ctx context.Context) {
	if m.publisher == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		m.reportStatuses(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Module) reportStatuses(ctx context.Context) {
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var locked bool
	if conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('aeon-review-status-reporter',0))`).Scan(&locked) != nil || !locked {
		return
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended('aeon-review-status-reporter',0))`)
		if err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	rows, err := conn.Query(ctx, `SELECT id::text FROM tenants`)
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if rows.Err() != nil {
		return
	}
	// Drain changes/retries before spending network time refreshing green
	// bindings. Both lanes traverse every latest head with bounded pages.
	for _, refresh := range []bool{false, true} {
		for _, tid := range ids {
			m.reportTenantStatuses(ctx, tid, refresh)
		}
	}
}

func (m *Module) reportTenantStatuses(ctx context.Context, tid string, refresh bool) {
	service := db.AllProjects(ctx, "cross-family review status reporter")
	var beforeTime *time.Time
	var beforeID *string
	for ctx.Err() == nil {
		reviews := []Review{}
		pageSize := 0
		err := db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT v.work_order_id::text,v.created_at,v.github_reported_state
                FROM work_order_reviews v WHERE v.pull_request IS NOT NULL AND v.github_status NOT IN ('unconfigured','stale')
                AND ($1::timestamptz IS NULL OR (v.created_at,v.work_order_id)<($1,$2::uuid))
                AND NOT EXISTS(SELECT 1 FROM work_order_reviews newer WHERE newer.tenant_id=v.tenant_id AND newer.repository=v.repository AND newer.head_sha=v.head_sha AND (newer.created_at,newer.work_order_id)>(v.created_at,v.work_order_id))
                ORDER BY v.created_at DESC,v.work_order_id DESC LIMIT 100`, beforeTime, beforeID)
			if err != nil {
				return err
			}
			type entry struct {
				id, previous string
				created      time.Time
			}
			orders := []entry{}
			for rows.Next() {
				var e entry
				if err = rows.Scan(&e.id, &e.created, &e.previous); err != nil {
					rows.Close()
					return err
				}
				orders = append(orders, e)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			pageSize = len(orders)
			for _, e := range orders {
				v, err := load(ctx, tx, e.id)
				if err != nil {
					return err
				}
				state := statusState(v)
				if !refresh && e.previous != state || refresh && e.previous == "success" && state == "success" {
					reviews = append(reviews, v)
				}
			}
			if pageSize > 0 {
				last := orders[pageSize-1]
				beforeTime, beforeID = &last.created, &last.id
			}
			return nil
		})
		if err != nil {
			return
		}
		for _, v := range reviews {
			op, cancel := context.WithTimeout(ctx, 45*time.Second)
			state := statusState(v)
			published, err := m.publisher.Publish(op, tid, v, state)
			if err != nil {
				published = "error"
			}
			cancel()
			_ = db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
				recorded := ""
				if err == nil {
					recorded = state
				}
				_, writeErr := tx.Exec(ctx, `UPDATE work_order_reviews SET github_status=$2,github_reported_state=$3 WHERE work_order_id=$1`, v.OrderID, published, recorded)
				return writeErr
			})
		}
		if pageSize < 100 {
			return
		}
	}
}
