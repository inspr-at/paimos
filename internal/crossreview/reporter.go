// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

func statusState(v Review) string {
	if v.GitHubStatus == "stale" {
		return "error"
	}
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

// RunStatusReporter runs publication and binding refresh independently. Each
// lane has its own replica lock; dirty retries cannot delay green revocation.
func (m *Module) RunStatusReporter(ctx context.Context) {
	if m.publisher == nil {
		return
	}
	done := make(chan struct{})
	go func() { defer close(done); m.runStatusLane(ctx, true) }()
	m.runStatusLane(ctx, false)
	<-done
}
func (m *Module) runStatusLane(ctx context.Context, refresh bool) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		m.reportStatusLane(ctx, refresh)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reportStatuses is one deterministic sweep of both independently runnable lanes.
func (m *Module) reportStatuses(ctx context.Context) {
	m.reportStatusLane(ctx, false)
	m.reportStatusLane(ctx, true)
}
func (m *Module) reportStatusLane(ctx context.Context, refresh bool) {
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var locked bool
	if conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, reporterLane(refresh)).Scan(&locked) != nil || !locked {
		return
	}
	defer unlockStatus(conn, reporterLane(refresh))
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
	for _, tid := range ids {
		m.reportTenantStatuses(ctx, conn, tid, refresh)
	}
}

func reporterLane(refresh bool) string {
	if refresh {
		return "aeon-review-binding-refresh"
	}
	return "aeon-review-status-reporter"
}

func unlockStatus(conn *pgxpool.Conn, key string) {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key); err != nil {
		_ = conn.Conn().Close(cleanup)
	}
}

// Only reviews capable of publishing may supersede a status owner.
const latestStatusOwner = `NOT EXISTS(SELECT 1 FROM work_order_reviews newer
 WHERE newer.tenant_id=v.tenant_id AND newer.repository=v.repository AND newer.head_sha=v.head_sha
 AND newer.pull_request IS NOT NULL AND newer.github_status<>'unconfigured'
 AND (newer.created_at,newer.work_order_id)>(v.created_at,v.work_order_id))`

func (m *Module) reportTenantStatuses(ctx context.Context, conn *pgxpool.Conn, tid string, refresh bool) {
	service := db.AllProjects(ctx, "cross-family review status reporter")
	var beforeTime *time.Time
	var beforeID *string
	for ctx.Err() == nil {
		reviews := []Review{}
		pageSize := 0
		err := db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT v.work_order_id::text,v.created_at,v.github_reported_state
                FROM work_order_reviews v WHERE v.pull_request IS NOT NULL AND v.github_status<>'unconfigured' AND (v.github_status<>'stale' OR v.github_reported_state<>'error')
                AND ($1::timestamptz IS NULL OR (v.created_at,v.work_order_id)<($1,$2::uuid))
                AND `+latestStatusOwner+`
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
				if !refresh && v.GitHubStatus != "stale" && (e.previous != state || v.GitHubStatus == "error") ||
					refresh && (e.previous == "success" && state == "success" || v.GitHubStatus == "stale" && e.previous != "error") {
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
			_ = m.publishReview(op, conn, tid, v, nil)
			cancel()
		}
		if pageSize < 100 {
			return
		}
	}
}

// publishReview serializes a head across replicas and webhook/reporter lanes.
// Re-read after the lock: a queued snapshot must not revive a revoked status.
// Session locks span network I/O, but transactions and row locks never do.
func (m *Module) publishReview(ctx context.Context, conn *pgxpool.Conn, tid string, snapshot Review, changed *pullChange) error {
	key := "aeon-review-head:" + tid + ":" + snapshot.Repository + ":" + snapshot.HeadSHA
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, key); err != nil {
		return err
	}
	defer unlockStatus(conn, key)
	service := db.AllProjects(ctx, "cross-family review status publication")
	var v Review
	var previous string
	skip := false
	err := db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
		var latest bool
		if err := tx.QueryRow(ctx, `SELECT `+latestStatusOwner+`,v.github_reported_state FROM work_order_reviews v WHERE v.work_order_id=$1`, snapshot.OrderID).Scan(&latest, &previous); err != nil {
			return err
		}
		var err error
		v, err = load(ctx, tx, snapshot.OrderID)
		if err != nil {
			return err
		}
		if !latest || v.PullRequest == nil || v.GitHubStatus == "unconfigured" || v.GitHubStatus == "stale" && previous == "error" {
			skip = true
			return nil
		}
		if changed != nil {
			if *v.PullRequest != changed.Number || v.HeadSHA != changed.Head.SHA || v.BaseSHA == changed.Base.SHA {
				skip = true
				return nil
			}
			// Commit the invalidation before attempting the external post. A
			// failed post stays stale and retryable even if the base is restored.
			if _, err = tx.Exec(ctx, `UPDATE work_order_reviews SET github_status='stale' WHERE work_order_id=$1`, v.OrderID); err != nil {
				return err
			}
			v.GitHubStatus, v.GateOpen = "stale", false
		}
		return nil
	})
	if err != nil || skip {
		return err
	}
	state := statusState(v)
	published, publishErr := m.publisher.Publish(ctx, tid, v, state)
	if v.GitHubStatus == "stale" {
		published = "stale"
	}
	if publishErr != nil && published != "stale" {
		published = "error"
	}
	recorded := previous
	if publishErr == nil {
		recorded = state
		if published == "stale" {
			recorded = "error"
		} // Publish confirms the revocation post.
	}
	writeErr := db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE work_order_reviews SET github_status=$2,github_reported_state=$3 WHERE work_order_id=$1`, v.OrderID, published, recorded)
		return err
	})
	if writeErr != nil {
		return writeErr
	}
	return publishErr
}
