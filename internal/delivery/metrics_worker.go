// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func historyDays(days *int) int {
	if days != nil {
		return *days
	}
	return metricDays
}

func metricError(err error, at time.Time) MetricReadError {
	out := MetricReadError{Reason: "github_unavailable", At: at.UTC()}
	var response *crossreview.GitHubHTTPError
	switch {
	case errors.Is(err, errNotConfigured):
		out.Reason = "not_installed"
	case errors.As(err, &response):
		if response.StatusCode == http.StatusForbidden && response.ActionsReadRequired {
			permission := "actions:read"
			out.Reason, out.Permission = "actions_read_missing", &permission
		} else if response.StatusCode == http.StatusNotFound {
			out.Reason = "not_installed"
		}
	}
	return out
}

func metricRetry(err MetricReadError) time.Time {
	if err.Reason == "actions_read_missing" || err.Reason == "not_installed" {
		return err.At.Add(30 * time.Minute)
	}
	return err.At.Add(5 * time.Minute)
}

func (m *Module) wakeMetrics() {
	select {
	case m.metricsWake <- struct{}{}:
	default:
	}
}

func backfillPosition(source *metricSourceRow, now time.Time) BackfillResult {
	out := BackfillResult{State: "running", Phase: "runs", Since: dayStart(now.AddDate(0, 0, -metricDays)), RetryAt: source.RetryAt}
	if c := source.Cursor; c != nil {
		out.Since, out.Phase, out.Truncated = c.Since, c.Phase, c.Truncated
		if c.Phase == "runs" {
			day := c.Day
			out.Day = &day
		}
		if c.Phase == "done" {
			out.State = "done"
		}
	}
	return out
}

// The durable intent is committed before network I/O. No browser lifetime or
// in-memory goroutine owns progress; replicas and server restarts see the row.
func (m *Module) scheduleMetricBackfill(ctx context.Context, p tenant.Principal, project string, in backfillInput, check bool) error {
	days := historyDays(in.Days)
	if days < 1 || days > 90 {
		return fail(400, "backfill days must be 1 to 90")
	}
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := requireMetrics(ctx, tx, p, "delivery.manage", project); err != nil {
			return err
		}
		source, err := loadMetricSourceTx(ctx, tx, project, true)
		if err != nil {
			return err
		}
		if source == nil {
			return fail(409, "link a repository to this project first")
		}
		var raw []byte
		if source.Cursor == nil || in.Restart {
			since := dayStart(m.now().AddDate(0, 0, -days))
			raw, err = json.Marshal(backfillCursor{Since: since, Phase: "runs", Day: since.Format("2006-01-02"), Page: 1, Workflows: fullBackfillWorkflows(source)})
			if err != nil {
				return err
			}
		}
		if check {
			// Also release deferred job reads, so Check again really reaches GitHub.
			if _, err := tx.Exec(ctx, `UPDATE delivery_metric_runs SET jobs_failed_at=NULL WHERE repository=$1 AND jobs_failed_at IS NOT NULL`, source.Repository); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE delivery_metric_sources SET backfill_pending=true,backfill_actor_id=$2,backfill_key_id=NULLIF($3,'')::uuid,
   backfill_cursor=coalesce($4,backfill_cursor),backfill_since=CASE WHEN $4::jsonb IS NOT NULL THEN ($4::jsonb->>'since')::timestamptz ELSE backfill_since END,
   backfill_done_at=CASE WHEN $4::jsonb IS NULL THEN backfill_done_at END,
   backfill_retry_at=CASE WHEN $4::jsonb IS NULL AND NOT $5 THEN backfill_retry_at END,
   last_read_error=CASE WHEN $4::jsonb IS NULL THEN last_read_error END,backfill_days=$6,backfill_revision=backfill_revision+1
   WHERE project_id=$1`, project, p.ID, p.KeyID, raw, check, days)
		return err
	})
}

func (m *Module) checkMetricSource(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	if err := m.scheduleMetricBackfill(r.Context(), p, project, backfillInput{}, true); err != nil {
		respondError(w, err)
		return
	}
	out, err := m.metricBackfillStep(r.Context(), p, project, backfillInput{})
	m.wakeMetrics()
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) metricReadFailed(ctx context.Context, p tenant.Principal, project string, source *metricSourceRow, failure error) error {
	readError := metricError(failure, m.now())
	raw, err := json.Marshal(readError)
	if err != nil {
		return err
	}
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := requireMetrics(ctx, tx, p, "delivery.manage", project); err != nil {
			return err
		}
		current, err := loadMetricSourceTx(ctx, tx, project, true)
		if err != nil {
			return err
		}
		if current == nil || current.Revision != source.Revision || current.Repository != source.Repository {
			return fail(409, "the backfill moved or the source changed meanwhile; call again to continue")
		}
		_, err = tx.Exec(ctx, `UPDATE delivery_metric_sources SET last_read_error=$2,backfill_retry_at=$3,backfill_pending=true,backfill_revision=backfill_revision+1 WHERE project_id=$1`, project, raw, metricRetry(readError))
		return err
	})
}

func (m *Module) runMetrics(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		m.sweepMetrics(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.metricsWake:
		case <-ticker.C:
		}
	}
}

// A short bounded page and one bounded step per project keep history work from
// monopolising the server. The session lock prevents duplicate replica reads;
// foreground steps still use revision checks and re-authorize their final write.
func (m *Module) sweepMetrics(ctx context.Context) {
	op, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, release, err := db.Acquire(op, m.pool)
	if err != nil {
		return
	}
	defer release()
	var locked bool
	key := "aeon-delivery-history"
	if conn.QueryRow(op, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked) != nil || !locked {
		return
	}
	defer unlock(conn, key)
	op = db.WithConnection(op, m.pool, conn)
	tenants, err := conn.Query(op, `SELECT id::text FROM tenants WHERE id::text>$1 ORDER BY id::text LIMIT 16`, m.metricsTenantCursor)
	if err != nil {
		return
	}
	var ids []string
	for tenants.Next() {
		var id string
		if tenants.Scan(&id) != nil {
			tenants.Close()
			return
		}
		ids = append(ids, id)
	}
	tenants.Close()
	if tenants.Err() != nil {
		return
	}
	if len(ids) == 0 {
		m.metricsTenantCursor = ""
		return
	}
	for _, tid := range ids {
		m.metricsTenantCursor = tid
		service := db.AllProjects(op, "durable delivery history jobs")
		type job struct {
			project, actor, key string
			days                int
		}
		var jobs []job
		err = db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(op, `SELECT s.project_id::text,s.backfill_actor_id::text,coalesce(s.backfill_key_id::text,''),coalesce(s.backfill_days,30)
    FROM delivery_metric_sources s JOIN nodes n ON n.id=s.project_id
    WHERE s.backfill_pending AND s.backfill_actor_id IS NOT NULL AND n.deleted_at IS NULL
    AND (s.backfill_retry_at IS NULL OR s.backfill_retry_at<=$1) ORDER BY s.updated_at,s.project_id LIMIT 16`, m.now())
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var j job
				if err = rows.Scan(&j.project, &j.actor, &j.key, &j.days); err != nil {
					return err
				}
				jobs = append(jobs, j)
			}
			return rows.Err()
		})
		if err != nil {
			continue
		}
		for _, j := range jobs {
			// Only metadata IDs are stored. Reconstruct live key authority, never a
			// serialized scope/role snapshot or a token, and check again after GitHub.
			p := tenant.Principal{TenantID: tid, ID: j.actor, KeyID: j.key, AuthKeyID: j.key}
			err = db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
				if err := tx.QueryRow(op, `SELECT kind FROM principals WHERE id=$1 AND status='active'`, p.ID).Scan(&p.Kind); err != nil {
					return err
				}
				if p.Kind == tenant.Agent {
					if p.KeyID == "" {
						return authz.ErrForbidden
					}
					if err := tx.QueryRow(op, `SELECT scopes,coalesce(full_access,false),coalesce(created_by_principal_id::text,'') FROM agent_keys WHERE id=$1 AND principal_id=$2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())`, p.KeyID, p.ID).Scan(&p.Scopes, &p.FullAccess, &p.KeyCreatorID); err != nil {
						return err
					}
					p.Scopes = authz.ResolveKeyScopes(p.Scopes, p.FullAccess)
				}
				return nil
			})
			if err == nil {
				step := tenant.WithPrincipal(op, p)
				_, err = m.metricBackfillStep(step, p, j.project, backfillInput{Days: &j.days})
			}
			if err != nil && op.Err() == nil {
				slog.WarnContext(op, "delivery history step incomplete", "project_id", j.project)
			}
		}
	}
}
