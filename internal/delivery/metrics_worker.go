// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
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
	var upstream *webhookGitHubError
	var network net.Error
	if errors.As(err, &response) {
		out.StatusCode = &response.StatusCode
	}
	switch {
	case errors.Is(err, errNotConfigured):
		out.Reason = "not_installed"
	case metricRateLimited(err):
		out.Reason = "rate_limited"
		if errors.As(err, &upstream) {
			reset := maxTime(upstream.reset, at.Add(upstream.retryAfter))
			if reset.After(at) {
				out.ResetAt = &reset
			}
		}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, crossreview.ErrTimedOut), errors.As(err, &network) && network.Timeout(), errors.As(err, &upstream) && upstream.timedOut:
		out.Reason = "timed_out"
	case errors.As(err, &response):
		out.Reason = "github_error"
		if response.StatusCode == http.StatusForbidden && response.ActionsReadRequired {
			permission := "actions:read"
			out.Reason, out.Permission = "actions_read_missing", &permission
		} else if response.StatusCode == http.StatusNotFound {
			out.Reason = "not_installed"
		}
	case errors.As(err, &upstream) && upstream.class == "network", errors.Is(err, crossreview.ErrTransient), errors.As(err, &network):
		out.Reason = "unreachable"
	}
	return out
}

func metricRetryBase(reason string) time.Duration {
	if reason == "actions_read_missing" || reason == "not_installed" {
		return 30 * time.Minute
	}
	return time.Minute
}

func shortMetricRetry() time.Duration {
	return (30 + time.Duration(rand.Int64N(30))) * time.Second
}

func metricRateLimited(failure error) bool {
	var upstream *webhookGitHubError
	var response *crossreview.GitHubHTTPError
	return errors.As(failure, &upstream) && webhookLimited(upstream.class) || errors.As(failure, &response) && response.StatusCode == http.StatusTooManyRequests
}

func metricRetry(failure error, readError MetricReadError, source *metricSourceRow) time.Time {
	if metricRateLimited(failure) {
		if readError.ResetAt != nil && readError.ResetAt.After(readError.At) {
			return *readError.ResetAt
		}
		return readError.At.Add(webhookLimitBackoff(failure, readError.At))
	}
	if crossreview.IsTransientGitHubError(failure) {
		return readError.At.Add(shortMetricRetry())
	}
	delay := metricRetryBase(readError.Reason)
	// The stored diagnostic and due time retain the previous delay across
	// replicas/restarts. A successful read clears both and resets the backoff.
	if source.LastReadError != nil && source.RetryAt != nil {
		previous := min(source.RetryAt.Sub(source.LastReadError.At), time.Hour)
		delay = max(delay, previous*2)
	}
	return readError.At.Add(min(delay, time.Hour))
}

func (m *Module) wakeMetrics() {
	select {
	case m.metricsWake <- struct{}{}:
	default:
	}
}

func backfillPosition(source *metricSourceRow, now time.Time) BackfillResult {
	out := BackfillResult{State: "running", Phase: "runs", Since: dayStart(now.AddDate(0, 0, -source.BackfillDays)), RetryAt: source.RetryAt}
	if c := source.Cursor; c != nil {
		out.Since, out.Phase, out.Truncated = c.Since, c.Phase, c.Truncated
		if c.Phase == "runs" && c.ReadSource != historyChecks {
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
		// A failed first read may not have committed a cursor yet. Continue the
		// persisted selection; only an explicit fresh window replaces it.
		if !in.Restart && (in.Days == nil || source.Cursor != nil) {
			days = source.BackfillDays
		}
		var raw []byte
		now := m.now().UTC()
		if source.Cursor == nil || in.Restart {
			since := dayStart(now.AddDate(0, 0, -days))
			raw, err = json.Marshal(backfillCursor{Since: since, Phase: "runs", Day: since.Format("2006-01-02"), Page: 1, ReadUntil: &now, Workflows: fullBackfillWorkflows(source)})
			if err != nil {
				return err
			}
		} else if check && source.Cursor.Phase == "done" && source.DoneAt != nil && !needsPreflightCatchup(source.Cursor) {
			// Fill already stored runs first. A history refresh then starts at its
			// persisted watermark, including after a different server takes over.
			pending, err := jobWorklistTx(ctx, tx, source, now, 1)
			if err != nil {
				return err
			}
			if len(pending) == 0 {
				fresh := incrementalCursor(*source.Cursor, *source.DoneAt, now)
				raw, err = json.Marshal(fresh)
				if err != nil {
					return err
				}
			}
		}
		if check {
			// Also release deferred job reads, so Check again really reaches GitHub.
			if _, err := tx.Exec(ctx, `UPDATE delivery_metric_runs SET jobs_failed_at=NULL WHERE repository=$1 AND jobs_failed_at IS NOT NULL`, source.Repository); err != nil {
				return err
			}
		}
		limited := source.LastReadError != nil && source.LastReadError.Reason == "rate_limited" && source.RetryAt != nil && source.RetryAt.After(now)
		_, err = tx.Exec(ctx, `UPDATE delivery_metric_sources SET backfill_pending=true,backfill_actor_id=$2,backfill_key_id=NULLIF($3,'')::uuid,
   backfill_cursor=coalesce($4,backfill_cursor),backfill_since=CASE WHEN $4::jsonb IS NOT NULL THEN ($4::jsonb->>'since')::timestamptz ELSE backfill_since END,
   backfill_done_at=CASE WHEN $4::jsonb IS NULL OR $4::jsonb->'refresh_since' IS NOT NULL THEN backfill_done_at END,
   backfill_retry_at=CASE WHEN $8 OR (NOT $5 AND NOT $7) THEN backfill_retry_at END,
   last_read_error=CASE WHEN $8 OR NOT $7 THEN last_read_error END,backfill_days=$6,backfill_revision=backfill_revision+1
   WHERE project_id=$1`, project, p.ID, p.KeyID, raw, check, days, in.Restart, limited)
		return err
	})
}

func incrementalCursor(previous backfillCursor, fallback, now time.Time) backfillCursor {
	from := fallback
	if previous.ReadThrough != nil {
		from = *previous.ReadThrough
	}
	if from.After(now) {
		from = now
	}
	return backfillCursor{Since: previous.Since, Phase: "runs", Day: dayStart(from).Format("2006-01-02"), Page: 1,
		ReadSource: previous.ReadSource, ReadUntil: &now, ReadThrough: previous.ReadThrough, RefreshSince: &from,
		PendingRuns: previous.WaitingRuns, Workflows: previous.Workflows, PreflightAbsent: previous.PreflightAbsent,
		Truncated: previous.Truncated, DefaultBranch: previous.DefaultBranch}
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
	// Read cancellation must not lose the diagnostic or leave a durable job
	// hot-looping. Keep identity/connection context, but bound this final write.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	readError := metricError(failure, m.now())
	logMetricFailure(ctx, failure, readError)
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
		_, err = tx.Exec(ctx, `UPDATE delivery_metric_sources SET last_read_error=$2,backfill_retry_at=$3,backfill_pending=true,backfill_revision=backfill_revision+1 WHERE project_id=$1`, project, raw, metricRetry(failure, readError, current))
		return err
	})
}

// Only fixed classifications and bounded response metadata cross the log
// boundary. Never log an error string, request URL, headers or response body.
func logMetricFailure(ctx context.Context, failure error, diagnostic MetricReadError) {
	var upstream *webhookGitHubError
	endpoint, duration := "history", time.Duration(0)
	var remaining *int
	reset := diagnostic.ResetAt
	if errors.As(failure, &upstream) {
		if upstream.endpoint != "" {
			endpoint = upstream.endpoint
		}
		duration, remaining = upstream.duration, upstream.remaining
		if reset == nil && !upstream.reset.IsZero() {
			reset = &upstream.reset
		}
	}
	// Dereference optional values before handing them to slog: TextHandler
	// otherwise prints pointer addresses where operators need status/counts.
	var status, budget, resetAt any
	if diagnostic.StatusCode != nil {
		status = *diagnostic.StatusCode
	}
	if remaining != nil {
		budget = *remaining
	}
	if reset != nil {
		resetAt = reset.UTC()
	}
	slog.WarnContext(ctx, "delivery history read failed", "reason", diagnostic.Reason,
		"endpoint", endpoint, "status", status, "rate_limit_remaining", budget,
		"rate_limit_reset", resetAt, "timeout", diagnostic.Reason == "timed_out", "duration_ms", duration.Milliseconds())
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

const metricRefreshInterval = 15 * time.Minute

func (m *Module) deferMetricRefresh(ctx context.Context, p tenant.Principal, project string, revision int64) error {
	// Service maintenance only postpones an unchanged finished source. It
	// grants no read/write authority to the actor whose refresh was refused.
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_metric_sources SET backfill_retry_at=$5,backfill_revision=backfill_revision+1
    WHERE project_id=$1 AND backfill_actor_id=$2 AND backfill_key_id IS NOT DISTINCT FROM NULLIF($3,'')::uuid
    AND backfill_revision=$4 AND NOT coalesce(backfill_pending,false) AND backfill_cursor->>'phase'='done'`,
			project, p.ID, p.KeyID, revision, m.now().UTC().Add(metricRefreshInterval))
		return err
	})
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
		previousTenant := m.metricsTenantCursor
		m.metricsTenantCursor = tid
		projectAfter := ""
		if m.metricsProjectTenant == tid {
			projectAfter = m.metricsProjectCursor
		}
		service := db.AllProjects(op, "durable delivery history jobs")
		type job struct {
			project, actor, key string
			refresh             bool
			revision            int64
		}
		var jobs []job
		err = db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(op, `SELECT s.project_id::text,s.backfill_actor_id::text,coalesce(s.backfill_key_id::text,''),NOT coalesce(s.backfill_pending,false),s.backfill_revision
    FROM delivery_metric_sources s JOIN nodes n ON n.id=s.project_id
    WHERE (s.backfill_pending OR (s.backfill_cursor->>'phase'='done'
      AND coalesce((s.backfill_cursor->>'read_through')::timestamptz,s.backfill_done_at)<=$3))
    AND s.backfill_actor_id IS NOT NULL AND n.deleted_at IS NULL
    AND (s.backfill_retry_at IS NULL OR s.backfill_retry_at<=$1) AND s.project_id::text>$2 ORDER BY s.project_id::text LIMIT 16`, m.now(), projectAfter, m.now().Add(-metricRefreshInterval))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var j job
				if err = rows.Scan(&j.project, &j.actor, &j.key, &j.refresh, &j.revision); err != nil {
					return err
				}
				jobs = append(jobs, j)
			}
			return rows.Err()
		})
		if err != nil {
			continue
		}
		if len(jobs) == 16 {
			m.metricsTenantCursor = previousTenant
			m.metricsProjectTenant, m.metricsProjectCursor = tid, jobs[len(jobs)-1].project
		} else {
			m.metricsProjectTenant, m.metricsProjectCursor = "", ""
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
			step := tenant.WithPrincipal(op, p)
			if err == nil && j.refresh {
				err = m.scheduleMetricBackfill(step, p, j.project, backfillInput{}, true)
			}
			if err != nil && j.refresh {
				if deferErr := m.deferMetricRefresh(service, p, j.project, j.revision); deferErr != nil {
					err = deferErr
				}
			} else if err == nil {
				_, err = m.metricBackfillStep(step, p, j.project, backfillInput{})
			}
			if err != nil && op.Err() == nil {
				slog.WarnContext(op, "delivery history step incomplete", "project_id", j.project)
			}
		}
		if len(jobs) == 16 {
			return
		}
	}
}
