// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestDeliveryHistoryActionsRefusalFromGitHub(t *testing.T) {
	// Risk: GitHub omits its permission header, hiding an Actions refusal and
	// sending history into the generic short retry path. No response text leaks.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "fixture.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, body, reason string
		status                   int
		permission               bool
	}{
		{"runs", "/actions/runs", "Resource not accessible by integration", "actions_read_missing", 403, true},
		{"workflow runs", "/actions/workflows/ci.yml/runs", "Resource not accessible by integration", "actions_read_missing", 403, true},
		{"jobs", "/actions/runs/7/attempts/1/jobs", "Resource not accessible by integration", "actions_read_missing", 403, true},
		{"other Actions refusal", "/actions/runs/7/jobs", "Other private response", "actions_read_missing", 403, true},
		{"missing", "/actions/runs", "Not Found", "not_installed", 404, false},
		{"non Actions refusal", "/pulls/7", "Resource not accessible by integration", "github_unavailable", 403, false},
		{"outage", "/actions/runs", "Private server error", "github_unavailable", 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads, revoked atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/456/access_tokens":
					var in struct {
						Permissions map[string]string `json:"permissions"`
					}
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Permissions["actions"] != "read" {
						t.Error("fixture did not request actions:read")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"token": "fixture-only-token", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": "example/delivery"}}})
				case "DELETE /installation/token":
					revoked.Add(1)
					w.WriteHeader(204)
				case "GET /repos/example/delivery" + tc.path:
					reads.Add(1)
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": tc.body})
				default:
					t.Errorf("unexpected stub request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			stubURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			client := server.Client()
			transport := client.Transport
			client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				copy := r.Clone(r.Context())
				copy.URL.Scheme, copy.URL.Host = stubURL.Scheme, stubURL.Host
				return transport.RoundTrip(copy)
			})
			reader := AppReader{App: &crossreview.GitHubApp{Config: crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Repository: "example/delivery", KeyFile: keyFile}, Client: client}}
			err = reader.MetricsRead(t.Context(), reader.App.Config.TenantID, func(get func(string, any) error) error {
				return get(tc.path, &map[string]any{})
			})
			at := time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC)
			diagnostic := metricError(err, at)
			if err == nil || diagnostic.Reason != tc.reason || (diagnostic.Permission != nil) != tc.permission || reads.Load() != 1 || revoked.Load() != 1 {
				t.Fatalf("Actions refusal classification: %+v err=%v reads=%d revoked=%d", diagnostic, err, reads.Load(), revoked.Load())
			}
			if tc.permission && (*diagnostic.Permission != "actions:read" || !metricRetry(err, diagnostic, &metricSourceRow{}).Equal(at.Add(30*time.Minute))) {
				t.Fatal("Actions refusal lost the permission or long retry")
			}
			if strings.Contains(err.Error(), tc.body) {
				t.Fatal("GitHub response text escaped the error boundary")
			}
		})
	}
}

func TestDeliveryHistoryRetryBackoffGrowsAndCaps(t *testing.T) {
	// Risk: permanent refusals hot-loop after restarts or never cap, while
	// timeouts/server outages wait like missing permissions. Clocks are injected.
	for _, tc := range []struct {
		name      string
		failure   error
		base      time.Duration
		transient bool
	}{
		{"unknown read failure", errRead, time.Minute, false},
		{"unauthorized", &crossreview.GitHubHTTPError{StatusCode: 401}, time.Minute, false},
		{"Actions forbidden", &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}, 30 * time.Minute, false},
		{"not found", &crossreview.GitHubHTTPError{StatusCode: 404}, 30 * time.Minute, false},
		{"canceled", context.Canceled, time.Minute, false},
		{"server outage", &crossreview.GitHubHTTPError{StatusCode: 503}, 0, true},
		{"timeout", context.DeadlineExceeded, 0, true},
		{"connection reset", syscall.ECONNRESET, 0, true},
		{"redacted transport", crossreview.ErrTransient, 0, true},
		{"partial transport", io.ErrUnexpectedEOF, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC)
			source := &metricSourceRow{}
			want := tc.base
			for attempt := int64(1); attempt <= 10; attempt++ {
				diagnostic := metricError(tc.failure, at)
				next := metricRetry(tc.failure, diagnostic, source)
				delay := next.Sub(at)
				receiptDelay := webhookErrorBackoff(attempt, tc.failure)
				if tc.transient {
					if delay < 30*time.Second || delay >= time.Minute || receiptDelay < 30*time.Second || receiptDelay >= time.Minute {
						t.Fatalf("transient retry outside jitter bounds: source=%s receipt=%s", delay, receiptDelay)
					}
				} else if delay != want || receiptDelay != want {
					t.Fatalf("attempt %d: source=%s receipt=%s want %s", attempt, delay, receiptDelay, want)
				}
				// Only persisted fields carry the prior delay into the next call.
				source = &metricSourceRow{LastReadError: &diagnostic, RetryAt: &next}
				at = next
				want = min(want*2, time.Hour)
			}
			if !tc.transient {
				diagnostic := metricError(tc.failure, at)
				if got := metricRetry(tc.failure, diagnostic, &metricSourceRow{}).Sub(at); got != tc.base {
					t.Fatalf("successful read did not reset backoff: %s", got)
				}
				if got := webhookErrorBackoff(1<<62, tc.failure); got != time.Hour {
					t.Fatalf("large attempt overflowed cap: %s", got)
				}
			}
		})
	}
}

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

func TestDeliveryHistoryFirstReadFailurePreservesSelectedWindow(t *testing.T) {
	// Risk: no cursor is committed on the first failed automatic read, so a
	// manual retry or continuation silently replaces the link's 90-day window.
	for _, action := range []string{"check-again", "backfill"} {
		t.Run(action, func(t *testing.T) {
			f, g := newMetricsFixture(t)
			path := "/api/projects/" + f.project + "/delivery/metrics"
			f.call(t, f.person, "PUT", path+"/source", map[string]any{"repository": f.m.config.Repository, "days": 90}, 200, nil)
			g.respond = func(string) (any, error) {
				return nil, &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}
			}
			fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
			fresh.now = f.m.now
			fresh.sweepMetrics(t.Context())
			f.tx(t, func(tx pgx.Tx) error {
				var days int
				var cursorMissing bool
				if err := tx.QueryRow(t.Context(), `SELECT backfill_days,backfill_cursor IS NULL FROM delivery_metric_sources WHERE project_id=$1`, f.project).Scan(&days, &cursorMissing); err != nil {
					return err
				}
				if days != 90 || !cursorMissing {
					t.Fatalf("first-read failure did not retain the window without a cursor: days=%d cursorMissing=%t", days, cursorMissing)
				}
				return nil
			})
			if source := historySource(t, f); source.LastReadError == nil || source.LastReadError.Reason != "actions_read_missing" || source.RetryAt == nil || !source.RetryAt.Equal(f.at.Add(30*time.Minute)) || len(g.calls) != 1 {
				t.Fatalf("first automatic read did not fail as expected: %+v calls=%v", source, g.calls)
			}
			deferred, err := f.m.metricBackfillStep(tenant.WithPrincipal(t.Context(), f.person), f.person, f.project, backfillInput{})
			if err != nil || !deferred.Since.Equal(dayStart(f.at.AddDate(0, 0, -90))) || deferred.RetryAt == nil || !deferred.RetryAt.Equal(f.at.Add(30*time.Minute)) || len(g.calls) != 1 {
				t.Fatalf("deferred read lost its selected window or retried early: %+v err=%v calls=%v", deferred, err, g.calls)
			}
			// Ordinary continuation waits for retry_at; Check again clears it.
			if action == "backfill" {
				f.call(t, f.person, "POST", path+"/backfill", map[string]any{}, 200, nil)
				if len(g.calls) != 1 {
					t.Fatal("continuation erased the first-read retry deadline")
				}
				f.at = f.at.Add(30 * time.Minute)
			}
			wantSince := dayStart(f.at.AddDate(0, 0, -90))
			f.call(t, f.person, "POST", path+"/"+action, map[string]any{}, 502, nil)
			f.tx(t, func(tx pgx.Tx) error {
				var days int
				if err := tx.QueryRow(t.Context(), `SELECT backfill_days FROM delivery_metric_sources WHERE project_id=$1`, f.project).Scan(&days); err != nil {
					return err
				}
				if days != 90 {
					t.Fatalf("%s changed the durable window to %d days", action, days)
				}
				return nil
			})
			if source := historySource(t, f); source.BackfillSince == nil || !source.BackfillSince.Equal(wantSince) || source.LastReadError == nil || source.LastReadError.Reason != "actions_read_missing" || len(g.calls) != 2 {
				t.Fatalf("%s lost the selected window or did not retry: %+v calls=%v", action, source, g.calls)
			}
			// Once a cursor exists, continuation must retain its days even if a
			// caller supplies another window without explicitly restarting.
			g.respond = emptyHistory(f.m.config.Repository)
			second := historySource(t, f)
			wantDelay := 30 * time.Minute
			if action == "backfill" {
				wantDelay = time.Hour
			}
			if second.RetryAt == nil || !second.RetryAt.Equal(f.at.Add(wantDelay)) {
				t.Fatalf("repeated permission refusal did not grow the stored backoff: %+v", second)
			}
			f.at = *second.RetryAt
			var out BackfillResult
			f.call(t, f.person, "POST", path+"/backfill", map[string]any{"days": 7}, 200, &out)
			if !out.Since.Equal(wantSince) {
				t.Fatalf("continuation moved the cursor: %+v", out)
			}
			f.tx(t, func(tx pgx.Tx) error {
				var days int
				if err := tx.QueryRow(t.Context(), `SELECT backfill_days FROM delivery_metric_sources WHERE project_id=$1`, f.project).Scan(&days); err != nil {
					return err
				}
				if days != 90 {
					t.Fatalf("continuation overwrote the durable window with %d days", days)
				}
				return nil
			})
			// Each bounded read handles 40 calls; the 90-day window needs seven
			// steps across three workflows. Tenant-page wrap takes a sweep too.
			for i := 0; i < 20 && historySource(t, f).Backfill != "done"; i++ {
				fresh.sweepMetrics(t.Context())
			}
			if source := historySource(t, f); source.Backfill != "done" || source.BackfillSince == nil || !source.BackfillSince.Equal(wantSince) || source.LastReadError != nil || source.RetryAt != nil {
				t.Fatalf("server continuation did not finish the selected window: %+v", source)
			}
		})
	}
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
	seedBareRuns(t, f, 3)
	g.respond = func(path string) (any, error) {
		if strings.Contains(path, "/1002/") {
			return nil, &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}
		}
		if strings.Contains(path, "/1001/") {
			return nil, &crossreview.GitHubHTTPError{StatusCode: 503}
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
	if jobs.State != "done" || jobs.Jobs != 2 {
		t.Fatalf("job permission retry did not run at deadline: %+v", jobs)
	}
	// Partial transient failures use the same short stored retry and remain
	// eligible at that deadline, rather than waiting the per-run retry hour.
	seedBareRuns(t, f, 5)
	g.respond = func(path string) (any, error) {
		if strings.Contains(path, "/1005/") {
			return nil, &crossreview.GitHubHTTPError{StatusCode: 503}
		}
		return githubJobsBody(f.at.Add(-time.Hour), 30, nil), nil
	}
	f.call(t, f.person, "POST", path+"/backfill", map[string]any{}, 200, &jobs)
	if jobs.Jobs != 1 || jobs.RetryAt == nil || jobs.RetryAt.Sub(f.at) < 30*time.Second || jobs.RetryAt.Sub(f.at) >= time.Minute {
		t.Fatalf("partial transient failure did not get a short retry: %+v", jobs)
	}
	stored := historySource(t, f)
	if stored.RetryAt == nil || !stored.RetryAt.Equal(*jobs.RetryAt) {
		t.Fatal("UI retry deadline differs from the job schedule")
	}
	f.at = *jobs.RetryAt
	g.respond = func(string) (any, error) { return githubJobsBody(f.at.Add(-time.Hour), 30, nil), nil }
	f.call(t, f.person, "POST", path+"/backfill", map[string]any{}, 200, &jobs)
	if jobs.State != "done" || jobs.Jobs != 1 {
		t.Fatalf("transient job was ineligible at its stored deadline: %+v", jobs)
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
