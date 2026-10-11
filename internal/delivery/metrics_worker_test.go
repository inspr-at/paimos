// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func emptyHistory(repository string) func(string) (any, error) {
	return func(path string) (any, error) {
		if path == "" {
			return map[string]any{"full_name": repository, "default_branch": "main"}, nil
		}
		if strings.HasPrefix(path, "/pulls?") {
			return []any{}, nil
		}
		return map[string]any{"total_count": 0, "workflow_runs": []any{}}, nil
	}
}

func historySource(t *testing.T, f *fixture) *MetricSource {
	t.Helper()
	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	if out.Source == nil {
		t.Fatal("source disappeared")
	}
	return out.Source
}

func TestDeliveryHistoryPermissionRetryCheckAgainAndTenantIsolation(t *testing.T) {
	// Risk: a 403 is lost, a worker hot-loops before retry_at, or a tenant can
	// inspect/change another tenant's diagnostic and webhook timestamps.
	f, g := newMetricsFixture(t)
	path := "/api/projects/" + f.project + "/delivery/metrics"
	g.respond = func(string) (any, error) {
		return nil, &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}
	}
	f.call(t, f.person, "POST", path+"/backfill", map[string]any{"days": 1}, 502, nil)
	source := historySource(t, f)
	if e := source.LastReadError; e == nil || e.Reason != "actions_read_missing" || e.Permission == nil || *e.Permission != "actions:read" || !e.At.Equal(f.at) || source.RetryAt == nil || !source.RetryAt.Equal(f.at.Add(30*time.Minute)) {
		t.Fatalf("permission diagnostic: %+v", source)
	}
	calls := len(g.calls)
	fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
	fresh.now = f.m.now
	fresh.sweepMetrics(t.Context())
	if len(g.calls) != calls {
		t.Fatal("server retried before retry_at")
	}
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	foreign := tenant.WithPrincipal(t.Context(), f.foreign)
	if err := db.InTenant(foreign, f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(last_read_error)+count(last_event_at) FROM delivery_metric_sources WHERE project_id=$1`, f.project).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("cross-tenant diagnostic visible")
		}
		changed, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET last_read_error=NULL,last_event_at=clock_timestamp() WHERE project_id=$1`, f.project)
		if err == nil && changed.RowsAffected() != 0 {
			t.Fatal("cross-tenant diagnostic writable")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A restart at the deadline resumes the same durable job automatically.
	f.at = f.at.Add(30 * time.Minute)
	g.respond = emptyHistory(f.m.config.Repository)
	fresh.metricsTenantCursor = ""
	fresh.sweepMetrics(t.Context())
	source = historySource(t, f)
	if source.Backfill != "done" || source.LastReadError != nil || source.RetryAt != nil || len(g.calls) == calls {
		t.Fatalf("automatic retry did not complete: %+v", source)
	}
	// Check again retries immediately rather than waiting another 30 minutes.
	g.respond = func(string) (any, error) {
		return nil, &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}
	}
	f.call(t, f.person, "POST", path+"/backfill", map[string]any{"restart": true, "days": 1}, 502, nil)
	calls = len(g.calls)
	f.call(t, f.person, "POST", path+"/check-again", nil, 502, nil)
	if len(g.calls) != calls+1 {
		t.Fatal("Check again did not reach GitHub")
	}
	g.respond = emptyHistory(f.m.config.Repository)
	f.call(t, f.person, "POST", path+"/check-again", nil, 200, nil)
	if historySource(t, f).LastReadError != nil {
		t.Fatal("successful check retained the permission failure")
	}
	// Valid signed events update the repository timestamp even without metrics;
	// an unauthenticated request cannot stamp it.
	f.at = f.at.Add(time.Minute)
	f.gh.pulls[7] = f.pull()
	f.webhook(t, "pull_request", "opened", "history-receipt", 204)
	if at := historySource(t, f).LastEventAt; at == nil || !at.Equal(f.at) {
		t.Fatal("accepted webhook timestamp missing")
	}
	// A permission failure among successful job reads still owns the same
	// 30-minute source retry, rather than waiting the ordinary job retry hour.
	seedBareRuns(t, f, 2)
	g.respond = func(path string) (any, error) {
		if strings.Contains(path, "/1002/") {
			return nil, &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}
		}
		return githubJobsBody(f.at.Add(-time.Hour), 30, nil), nil
	}
	var jobs BackfillResult
	f.call(t, f.person, "POST", path+"/backfill", map[string]any{}, 200, &jobs)
	if jobs.Jobs != 1 || jobs.RetryAt == nil || !jobs.RetryAt.Equal(f.at.Add(30*time.Minute)) || historySource(t, f).LastReadError.Reason != "actions_read_missing" {
		t.Fatalf("partial permission retry: %+v", jobs)
	}
	f.at = f.at.Add(30 * time.Minute)
	g.respond = func(string) (any, error) { return githubJobsBody(f.at.Add(-time.Hour), 30, nil), nil }
	f.call(t, f.person, "POST", path+"/backfill", map[string]any{}, 200, &jobs)
	if jobs.State != "done" || jobs.Jobs != 1 {
		t.Fatalf("job permission retry did not run at deadline: %+v", jobs)
	}
}

func TestDeliveryHistorySourceChangeRejectsOldStepAndResumesAfterRestart(t *testing.T) {
	// Risk: an in-flight old source step overwrites a changed source; linking,
	// changing and restarting must all leave durable work for a new server.
	f, g := newMetricsFixture(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	g.respond = func(string) (any, error) {
		close(entered)
		<-release
		return nil, &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}
	}
	go func() {
		_, err := f.m.metricBackfillStep(tenant.WithPrincipal(context.Background(), f.person), f.person, f.project, backfillInput{})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("history read did not enter")
	}
	changed := ".github/workflows/new-ci.yml"
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery/metrics/source", map[string]any{"repository": f.m.config.Repository, "ci_workflow": changed, "days": 1}, 200, nil)
	close(release)
	select {
	case err := <-finished:
		var failure *apiError
		if !errors.As(err, &failure) || failure.status != 409 {
			t.Fatalf("obsolete step returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("obsolete step hung")
	}
	if historySource(t, f).LastReadError != nil {
		t.Fatal("old source failure contaminated new source")
	}
	g.respond = emptyHistory(f.m.config.Repository)
	g.calls = nil
	fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
	fresh.now = f.m.now
	fresh.sweepMetrics(t.Context())
	source := historySource(t, f)
	if source.Backfill != "done" || source.BackfillSince == nil || !source.BackfillSince.Equal(dayStart(f.at.AddDate(0, 0, -1))) {
		t.Fatalf("changed source was not resumed: %+v", source)
	}
	seen := false
	for _, path := range g.calls {
		if strings.Contains(path, "/new-ci.yml/") {
			seen = true
		}
		if strings.Contains(path, "/ci.yml/") {
			t.Fatal("old source workflow read")
		}
	}
	if !seen {
		t.Fatal("new source workflow not read")
	}
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{"restart": true, "days": 7}, 200, nil)
	if source = historySource(t, f); source.BackfillSince == nil || !source.BackfillSince.Equal(dayStart(f.at.AddDate(0, 0, -7))) {
		t.Fatal("restart did not replace the history window")
	}
	var pending bool
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT coalesce(backfill_pending,false) FROM delivery_metric_sources WHERE project_id=$1`, f.project).Scan(&pending)
	})
	if pending {
		t.Fatal("finished restart kept scheduling reads")
	}
}

func TestDeliveryHistoryWorkerRechecksRevokedAuthority(t *testing.T) {
	// Risk: server continuation retains the linker's old grants after revocation.
	f, g := newMetricsFixture(t)
	g.respond = emptyHistory(f.m.config.Repository)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
		return err
	})
	f.m.sweepMetrics(t.Context())
	if len(g.calls) != 0 {
		t.Fatal("revoked principal still read history")
	}
}
