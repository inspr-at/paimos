// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
		{"non Actions refusal", "/pulls/7", "Resource not accessible by integration", "github_error", 403, false},
		{"outage", "/actions/runs", "Private server error", "github_error", 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads, revoked atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /app/installations/456":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 456, "app_id": 123, "permissions": map[string]string{"actions": "read", "checks": "read", "contents": "read", "pull_requests": "read", "metadata": "read"}})
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
			err = reader.MetricsRead(t.Context(), reader.App.Config.TenantID, func(get func(string, any) error, source string) error {
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

func TestDeliveryHistoryPartialJobsRetainTransientAndPermissionPriority(t *testing.T) {
	// Risk: a later ordinary error hides a transient failure or Actions refusal,
	// giving failed runs a deadline at which they still cannot be read.
	transient := &crossreview.GitHubHTTPError{StatusCode: 503}
	missing := &crossreview.GitHubHTTPError{StatusCode: 404}
	permission := &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}
	for _, tc := range []struct {
		name, reason string
		failures     []error
	}{
		{"transient then missing", "github_error", []error{transient, missing}},
		{"missing then transient", "github_error", []error{missing, transient}},
		{"transient then permission then missing", "actions_read_missing", []error{transient, permission, missing}},
		{"missing then permission then transient", "actions_read_missing", []error{missing, permission, transient}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, g := newMetricsFixture(t)
			n := len(tc.failures) + 1
			seedBareRuns(t, f, n)
			g.respond = func(path string) (any, error) {
				var id int64
				if _, err := fmt.Sscanf(path, "/actions/runs/%d/attempts/1/jobs?per_page=100&page=1", &id); err != nil {
					t.Fatalf("unexpected jobs read: %s", path)
				}
				if index := 1000 + n - int(id) - 1; index >= 0 {
					return nil, tc.failures[index]
				}
				return githubJobsBody(f.at.Add(-time.Hour), 30, nil), nil
			}
			var step BackfillResult
			f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{}, 200, &step)
			stored := historySource(t, f)
			if step.State != "running" || step.Phase != "jobs" || step.Jobs != 1 || step.Calls != n || step.RetryAt == nil || stored.LastReadError == nil || stored.LastReadError.Reason != tc.reason || !stored.LastReadError.At.Equal(f.at) || stored.RetryAt == nil || !stored.RetryAt.Equal(*step.RetryAt) {
				t.Fatalf("mixed partial retry: step=%+v source=%+v", step, stored)
			}
			if tc.reason == "actions_read_missing" {
				if stored.LastReadError.Permission == nil || *stored.LastReadError.Permission != "actions:read" || !step.RetryAt.Equal(f.at.Add(30*time.Minute)) {
					t.Fatal("mixed failure lost Actions permission priority")
				}
			} else if stored.LastReadError.Permission != nil || step.RetryAt.Sub(f.at) < 30*time.Second || step.RetryAt.Sub(f.at) >= time.Minute {
				t.Fatalf("transient failure did not retain its short retry: %+v", step)
			}
			// A new module has only persisted state to control eligibility.
			fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
			fresh.now = f.m.now
			ctx := tenant.WithPrincipal(t.Context(), f.person)
			calls := len(g.calls)
			f.at = step.RetryAt.Add(-time.Microsecond)
			before, err := fresh.metricBackfillStep(ctx, f.person, f.project, backfillInput{})
			if err != nil || len(g.calls) != calls || before.RetryAt == nil || !before.RetryAt.Equal(*step.RetryAt) {
				t.Fatalf("retried before stored deadline: %+v err=%v calls=%v", before, err, g.calls)
			}
			f.at = *step.RetryAt
			g.respond = func(string) (any, error) { return githubJobsBody(f.at.Add(-time.Hour), 30, nil), nil }
			recovered, err := fresh.metricBackfillStep(ctx, f.person, f.project, backfillInput{})
			if err != nil || recovered.State != "done" || recovered.Jobs != len(tc.failures) || recovered.Calls != len(tc.failures) || recovered.RetryAt != nil {
				t.Fatalf("failed runs ineligible at stored deadline: %+v err=%v", recovered, err)
			}
			if source := historySource(t, f); source.LastReadError != nil || source.RetryAt != nil {
				t.Fatalf("recovery retained a failed source: %+v", source)
			}
			for id := int64(1001); id <= int64(1000+n); id++ {
				if run := readStoredRun(t, f, id, 1); !run.jobsRead || run.failed || run.waitMS == nil || *run.waitMS != 30000 {
					t.Fatalf("recovered run %d lost its facts: %+v", id, run)
				}
			}
		})
	}
}

func TestDeliveryHistoryPartialJobsPersistGrowingBackoffAndReset(t *testing.T) {
	// Risk: partial 404 or malformed responses bypass persisted exponential
	// backoff, or the failed runs remain ineligible at the displayed deadline.
	for _, tc := range []struct {
		name, reason string
		body         any
		failure      error
		base         time.Duration
	}{
		{"missing jobs", "not_installed", nil, &crossreview.GitHubHTTPError{StatusCode: 404}, 30 * time.Minute},
		{"malformed jobs", "github_unavailable", map[string]any{"total_count": "invalid"}, nil, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, g := newMetricsFixture(t)
			ctx := tenant.WithPrincipal(t.Context(), f.person)
			failing := true
			g.respond = func(path string) (any, error) {
				if failing && strings.Contains(path, "/1001/") {
					return tc.body, tc.failure
				}
				return githubJobsBody(f.at.Add(-time.Hour), 30, nil), nil
			}
			want := tc.base
			for attempt := 1; attempt <= 8; attempt++ {
				// Add one healthy run without erasing the previously stored facts,
				// error or deadline; every retry is still a partial failure.
				seedBareRuns(t, f, attempt+1)
				fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
				fresh.now = f.m.now
				calls := len(g.calls)
				step, err := fresh.metricBackfillStep(ctx, f.person, f.project, backfillInput{})
				stored := historySource(t, f)
				if err != nil || step.State != "running" || step.Phase != "jobs" || step.Jobs != 1 || step.Calls != 2 || len(g.calls) != calls+2 || step.RetryAt == nil || !step.RetryAt.Equal(f.at.Add(want)) || stored.LastReadError == nil || stored.LastReadError.Reason != tc.reason || !stored.LastReadError.At.Equal(f.at) || stored.RetryAt == nil || !stored.RetryAt.Equal(*step.RetryAt) {
					t.Fatalf("attempt %d: partial backoff want=%s step=%+v source=%+v err=%v calls=%v", attempt, want, step, stored, err, g.calls)
				}
				if run := readStoredRun(t, f, 1001, 1); run.jobsRead {
					t.Fatal("failed run was reported as filled")
				}
				calls = len(g.calls)
				f.at = step.RetryAt.Add(-time.Microsecond)
				before, err := fresh.metricBackfillStep(ctx, f.person, f.project, backfillInput{})
				if err != nil || len(g.calls) != calls || before.RetryAt == nil || !before.RetryAt.Equal(*step.RetryAt) {
					t.Fatalf("attempt %d retried early: %+v err=%v calls=%v", attempt, before, err, g.calls)
				}
				f.at = *step.RetryAt
				want = min(want*2, time.Hour)
			}
			failing = false
			fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
			fresh.now = f.m.now
			step, err := fresh.metricBackfillStep(ctx, f.person, f.project, backfillInput{})
			if err != nil || step.State != "done" || step.Jobs != 1 || step.Calls != 1 || step.RetryAt != nil {
				t.Fatalf("recovery at deadline: %+v err=%v", step, err)
			}
			if source := historySource(t, f); source.LastReadError != nil || source.RetryAt != nil {
				t.Fatalf("successful read retained backoff: %+v", source)
			}
			if run := readStoredRun(t, f, 1001, 1); !run.jobsRead || run.failed || run.waitMS == nil || *run.waitMS != 30000 {
				t.Fatalf("recovered run facts: %+v", run)
			}
			// A new partial failure after recovery starts at the base again.
			seedBareRuns(t, f, 10)
			f.tx(t, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_runs SET jobs_read_at=NULL,required_jobs='{}' WHERE run_id=1001 AND attempt=1`)
				return err
			})
			failing = true
			step, err = fresh.metricBackfillStep(ctx, f.person, f.project, backfillInput{})
			stored := historySource(t, f)
			if err != nil || step.Jobs != 1 || step.Calls != 2 || step.RetryAt == nil || !step.RetryAt.Equal(f.at.Add(tc.base)) || stored.LastReadError == nil || stored.LastReadError.Reason != tc.reason || stored.RetryAt == nil || !stored.RetryAt.Equal(*step.RetryAt) {
				t.Fatalf("backoff did not reset after success: step=%+v source=%+v err=%v", step, stored, err)
			}
		})
	}
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
				if retry := historySource(t, f).RetryAt; retry != nil && retry.After(f.at) {
					f.at = *retry
				}
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

func TestDeliveryHistoryRefreshDefersRevokedAuthority(t *testing.T) {
	// Risk: finished sources whose linking authority has expired retry on every
	// sweep, locking the tenant and logging forever without reaching GitHub.
	for _, scenario := range []string{"inactive principal", "expired key", "revoked permission"} {
		t.Run(scenario, func(t *testing.T) {
			f, g := newMetricsFixture(t)
			f.tx(t, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT now()`).Scan(&f.at) })
			through := f.at.Add(-metricRefreshInterval - time.Minute)
			raw, err := json.Marshal(backfillCursor{Since: dayStart(through), Phase: "done", ReadThrough: &through})
			if err != nil {
				t.Fatal(err)
			}
			f.tx(t, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_cursor=$2,backfill_done_at=$3,backfill_pending=false WHERE project_id=$1`, f.project, raw, through); err != nil {
					return err
				}
				switch scenario {
				case "inactive principal":
					_, err = tx.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE id=$1`, f.person.ID)
				case "expired key":
					var key string
					if err := tx.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id,expires_at) VALUES($1,$2,'History fixture','history-fixture',decode(repeat('00',32),'hex'),$3,$4,$5) RETURNING id::text`, f.person.TenantID, f.agent.ID, f.agent.Scopes, f.person.ID, f.at.Add(-time.Hour)).Scan(&key); err != nil {
						return err
					}
					_, err = tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_actor_id=$2,backfill_key_id=$3 WHERE project_id=$1`, f.project, f.agent.ID, key)
				case "revoked permission":
					_, err = tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
				}
				return err
			})
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			defer slog.SetDefault(previous)
			for range 2 {
				// A new process has no in-memory cursor that could hide a hot loop.
				fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
				fresh.now = f.m.now
				fresh.sweepMetrics(t.Context())
			}
			if attempts := bytes.Count(output.Bytes(), []byte("delivery history step incomplete")); attempts != 1 {
				t.Errorf("refresh attempts = %d, want one", attempts)
			}
			if g.reads != 0 || len(g.calls) != 0 {
				t.Fatal("revoked authority reached GitHub")
			}
			f.tx(t, func(tx pgx.Tx) error {
				stored, err := loadMetricSourceTx(t.Context(), tx, f.project, false)
				if err != nil {
					return err
				}
				if stored.RetryAt == nil || !stored.RetryAt.Equal(f.at.Add(metricRefreshInterval)) || stored.Pending || stored.Cursor.Phase != "done" || !stored.Cursor.ReadThrough.Equal(through) || stored.LastReadError != nil {
					t.Fatalf("revoked refresh lost its durable delay or changed history: %+v", stored)
				}
				return nil
			})
		})
	}
}

func historyTestApp(t *testing.T, handle func(*http.Request) (*http.Response, error)) AppReader {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "fixture.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body any
		switch r.Method + " " + r.URL.Path {
		case "GET /app/installations/456":
			body = map[string]any{"id": 456, "app_id": 123, "permissions": map[string]string{"actions": "read", "checks": "read", "contents": "read", "pull_requests": "read", "metadata": "read"}}
		case "POST /app/installations/456/access_tokens":
			var in struct {
				Permissions map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			body = map[string]any{"token": "history-test-fixture", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": "example/delivery"}}}
		case "DELETE /installation/token":
			return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		default:
			return handle(r)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	return AppReader{App: &crossreview.GitHubApp{Config: crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Repository: "example/delivery", KeyFile: file}, Client: client}}
}

func TestDeliveryHistoryClassifiesFailuresAndHonoursReset(t *testing.T) {
	// Risk: sanitized transport errors lose their real cause or a header deadline
	// is capped to an hour, including a 403 incorrectly called a missing grant.
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	reset := at.Add(2 * time.Hour)
	for _, tc := range []struct {
		name, reason string
		status       int
		headers      http.Header
		failure      error
		reset        *time.Time
	}{
		{name: "primary", reason: "rate_limited", status: 403, headers: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}}, reset: &reset},
		{name: "retry seconds", reason: "rate_limited", status: 429, headers: http.Header{"Retry-After": {"7200"}}, reset: &reset},
		{name: "retry date", reason: "rate_limited", status: 403, headers: http.Header{"Retry-After": {reset.Format(http.TimeFormat)}}, reset: &reset},
		{name: "timeout", reason: "timed_out", failure: context.DeadlineExceeded},
		{name: "network", reason: "unreachable", failure: &net.DNSError{Err: "fixture-only private transport text", Name: "private.invalid"}},
		{name: "server", reason: "github_error", status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := historyTestApp(t, func(*http.Request) (*http.Response, error) {
				if tc.failure != nil {
					return nil, tc.failure
				}
				return &http.Response{StatusCode: tc.status, Header: tc.headers, Body: io.NopCloser(strings.NewReader(`{"message":"private fixture response"}`))}, nil
			})
			err := reader.MetricsRead(t.Context(), reader.App.Config.TenantID, func(get func(string, any) error, _ string) error { return get("/actions/runs", &map[string]any{}) })
			diagnostic := metricError(err, at)
			if err == nil || diagnostic.Reason != tc.reason || diagnostic.Permission != nil {
				t.Fatalf("reason: %+v err=%v", diagnostic, err)
			}
			if tc.status != 0 && (diagnostic.StatusCode == nil || *diagnostic.StatusCode != tc.status) {
				t.Fatal("status code lost")
			}
			if tc.reset != nil && (diagnostic.ResetAt == nil || !diagnostic.ResetAt.Equal(*tc.reset) || !metricRetry(err, diagnostic, &metricSourceRow{}).Equal(*tc.reset)) {
				t.Fatalf("reset deadline lost: %+v", diagnostic)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("upstream text escaped the boundary")
			}
		})
	}
	if got := metricError(crossreview.ErrTimedOut, at); got.Reason != "timed_out" {
		t.Fatal("body-read timeout lost", got)
	}
}

func TestDeliveryHistoryStopsAtLowRemaining(t *testing.T) {
	// Risk: successful responses exhaust the shared installation budget before
	// the reader notices a 429; revocation must still run when the read stops.
	at := time.Now().UTC()
	reset := at.Add(2 * time.Hour).Truncate(time.Second)
	calls := 0
	reader := historyTestApp(t, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Ratelimit-Remaining": {"99"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	err := reader.MetricsRead(t.Context(), reader.App.Config.TenantID, func(get func(string, any) error, _ string) error {
		if err := get("/actions/runs?page=1", &map[string]any{}); err != nil {
			return err
		}
		return get("/actions/runs?page=2", &map[string]any{})
	})
	diagnostic := metricError(err, at)
	if calls != 1 || diagnostic.Reason != "rate_limited" || diagnostic.ResetAt == nil || !diagnostic.ResetAt.Equal(reset) || !metricRetry(err, diagnostic, &metricSourceRow{}).Equal(reset) {
		t.Fatalf("budget not retained: calls=%d diagnostic=%+v err=%v", calls, diagnostic, err)
	}
}

func TestDeliveryHistoryIncrementalVolumeAndPendingRunsSurviveRestart(t *testing.T) {
	// The 186-run case matches the incident-day CI count; 900 is a stress case.
	for _, storedRuns := range []int64{186, 900} {
		t.Run(strconv.FormatInt(storedRuns, 10), func(t *testing.T) {
			// Risk: a busy-day refresh rereads all stored attempts, loses queued runs
			// behind a watermark, or relies on process memory to retain its position.
			day := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
			cutoff, now := day.Add(12*time.Hour), day.Add(13*time.Hour)
			all := make([]githubRun, 0, storedRuns+4)
			for id := int64(1); id <= storedRuns+4; id++ {
				created := day.Add(time.Duration(id) * 30 * time.Second)
				if id > storedRuns {
					created = cutoff.Add(time.Duration(id-storedRuns) * time.Minute)
				}
				conclusion := "success"
				all = append(all, githubRun{ID: id, Attempt: 2, Name: "CI", Path: defaultCIWorkflow, Event: "push", Branch: "main", Head: strings.Repeat("a", 40), Status: "completed", Conclusion: &conclusion, Created: created, Updated: created.Add(time.Minute)})
			}
			all[storedRuns+3].Created = cutoff.Add(-time.Minute)
			all[storedRuns+3].Status = "in_progress"
			calls, oldAttemptReads := 0, 0
			read := func(endpoint string, out any) error {
				calls++
				u, _ := url.Parse(endpoint)
				var body any
				switch {
				case strings.HasPrefix(u.Path, "/actions/workflows/"):
					from, until := day, now
					if parts := strings.Split(u.Query().Get("created"), ".."); len(parts) == 2 {
						var err error
						from, err = time.Parse(time.RFC3339, parts[0])
						if err != nil {
							t.Fatal(err)
						}
						until, err = time.Parse(time.RFC3339, parts[1])
						if err != nil {
							t.Fatal(err)
						}
					}
					var runs []githubRun
					for i := len(all) - 1; i >= 0; i-- {
						if !all[i].Created.Before(from) && !all[i].Created.After(until) {
							runs = append(runs, all[i])
						}
					}
					page, _ := strconv.Atoi(u.Query().Get("page"))
					start := min((page-1)*100, len(runs))
					end := min(start+100, len(runs))
					body = githubRunsPage{Total: len(runs), Runs: runs[start:end]}
				case strings.HasPrefix(u.Path, "/actions/runs/"):
					var id int64
					var attempt int
					if strings.Contains(u.Path, "/attempts/") {
						if _, err := fmt.Sscanf(u.Path, "/actions/runs/%d/attempts/%d", &id, &attempt); err != nil {
							t.Fatal(err)
						}
						if id <= storedRuns {
							oldAttemptReads++
						}
					} else {
						if _, err := fmt.Sscanf(u.Path, "/actions/runs/%d", &id); err != nil {
							t.Fatal(err)
						}
						attempt = 2
					}
					run := all[id-1]
					run.Attempt = attempt
					body = run
				case u.Path == "":
					body = map[string]string{"full_name": "example/delivery", "default_branch": "main"}
				case u.Path == "/pulls":
					body = []any{}
				default:
					t.Fatalf("unexpected endpoint %s", endpoint)
				}
				raw, err := json.Marshal(body)
				if err != nil {
					return err
				}
				return json.Unmarshal(raw, out)
			}
			finish := func(cursor backfillCursor, clock time.Time, known bool) backfillCursor {
				for step := 0; step < 100; step++ {
					lookup := func(id int64) int {
						if known && id <= storedRuns {
							return 2
						}
						return 0
					}
					next, batch, err := backfillStep(read, "example/delivery", []string{defaultCIWorkflow}, cursor, clock, lookup)
					if err != nil || batch.Calls > backfillCallBudget {
						t.Fatalf("bounded step: calls=%d err=%v", batch.Calls, err)
					}
					raw, err := json.Marshal(next)
					if err != nil {
						t.Fatal(err)
					}
					// Every step is decoded as a restarted server would load it.
					cursor = backfillCursor{}
					if err := json.Unmarshal(raw, &cursor); err != nil {
						t.Fatal(err)
					}
					if cursor.Phase == "done" {
						return cursor
					}
				}
				t.Fatal("fixture did not complete")
				return cursor
			}
			initial := finish(backfillCursor{Since: day, Phase: "runs", Day: day.Format("2006-01-02"), Page: 1, ReadUntil: &cutoff, Workflows: []string{defaultCIWorkflow}}, cutoff, false)
			if len(initial.WaitingRuns) != 1 || initial.WaitingRuns[0] != storedRuns+4 || initial.ReadThrough == nil || !initial.ReadThrough.Equal(cutoff) {
				t.Fatalf("pending run or watermark lost: %+v", initial)
			}
			all[storedRuns+3].Status = "completed"
			calls, oldAttemptReads = 0, 0
			finish(backfillCursor{Since: day, Phase: "runs", Day: day.Format("2006-01-02"), Page: 1}, now, false)
			before := calls
			calls, oldAttemptReads = 0, 0
			final := finish(incrementalCursor(initial, cutoff, now), now, true)
			if calls >= before/10 || oldAttemptReads != 0 || len(final.WaitingRuns) != 0 || final.ReadThrough == nil || !final.ReadThrough.Equal(now) {
				t.Fatalf("incremental refresh: before=%d after=%d old_attempt_reads=%d cursor=%+v", before, calls, oldAttemptReads, final)
			}
			t.Logf("busy-day fixture (%d stored runs, 3 new runs, 1 late completion; two attempts each): requests per refresh %d -> %d", storedRuns, before, calls)

		})
	}
}

func TestDeliveryHistoryPartialProgressAndSettingsRevisionSurviveRestart(t *testing.T) {
	// Risk: a limit on page 2 discards page 1; a cold server rereads it; Check
	// again ignores the reset; background state invalidates the settings editor.
	f, g := newMetricsFixture(t)
	f.tx(t, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT now()`).Scan(&f.at) })
	settings := "/api/projects/" + f.project + "/delivery-settings"
	f.call(t, f.person, "PUT", settings, map[string]any{"deadlines": map[string]int{"ci_green": 45}}, 200, nil)
	var revision string
	f.tx(t, func(tx pgx.Tx) error {
		_, value, err := storedSettingsTx(t.Context(), tx, &f.project)
		revision = value
		return err
	})
	reset := f.at.Add(2 * time.Hour)
	limited := true
	g.respond = func(endpoint string) (any, error) {
		u, _ := url.Parse(endpoint)
		if strings.HasPrefix(u.Path, "/actions/workflows/") && u.Query().Get("page") == "2" && limited {
			return nil, &webhookGitHubError{class: "secondary_limit", reset: reset, cause: &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true}}
		}
		if strings.HasPrefix(u.Path, "/actions/workflows/ci.yml/") && u.Query().Get("page") == "1" && limited {
			runs := make([]githubRun, 100)
			for i := range runs {
				success := "success"
				runs[i] = githubRun{ID: int64(i + 1), Attempt: 1, Name: "CI", Path: defaultCIWorkflow, Event: "push", Head: strings.Repeat("a", 40), Status: "completed", Conclusion: &success, Created: f.at.Add(-time.Hour), Updated: f.at.Add(-time.Minute)}
			}
			return githubRunsPage{Total: 101, Runs: runs}, nil
		}
		return emptyHistory(f.m.config.Repository)(endpoint)
	}
	path := "/api/projects/" + f.project + "/delivery/metrics"
	f.call(t, f.person, "POST", path+"/backfill", map[string]any{"days": 1, "restart": true}, 502, nil)
	f.tx(t, func(tx pgx.Tx) error {
		source, err := loadMetricSourceTx(t.Context(), tx, f.project, false)
		if err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_metric_runs WHERE repository=$1`, f.m.config.Repository).Scan(&count); err != nil {
			return err
		}
		if count != 100 || source.Cursor == nil || source.Cursor.Page != 2 || source.LastReadError.Reason != "rate_limited" || !source.RetryAt.Equal(reset) {
			t.Fatalf("progress lost: count=%d source=%+v", count, source)
		}
		_, after, err := storedSettingsTx(t.Context(), tx, &f.project)
		if after != revision {
			t.Fatal("reader changed the user settings revision")
		}
		return err
	})
	calls := len(g.calls)
	f.call(t, f.person, "POST", path+"/check-again", nil, 200, nil)
	if len(g.calls) != calls {
		t.Fatal("Check again spent budget before reset")
	}
	f.at, limited = reset, false
	fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
	fresh.now = f.m.now
	fresh.sweepMetrics(t.Context())
	if len(g.calls) == calls || !strings.Contains(g.calls[calls], "page=2") {
		t.Fatalf("cold server did not resume its durable page: %v", g.calls[calls:])
	}
	if source := historySource(t, f); source.LastReadError != nil || source.Backfill != "done" {
		t.Fatalf("resume failed: %+v", source)
	}
}

func TestDeliveryMergeWebhookProofUsesOnlyRequiredInstallationGrants(t *testing.T) {
	// Risk: completion asks GitHub to mint checks/statuses/contents grants it
	// never uses; an installation with valid PR access returns 422 and all three
	// closure, destroyed-group and push completion paths keep failing.
	f := newFixture(t)
	sha := strings.Repeat("b", 40)
	reader := historyTestApp(t, func(r *http.Request) (*http.Response, error) {
		var body any
		switch r.URL.Path {
		case "/repos/example/delivery/pulls/7":
			body = map[string]any{"number": 7, "state": "closed", "merged": true, "merge_commit_sha": sha, "merged_at": f.at, "merged_by": map[string]string{"login": "fixture-bot"}, "head": map[string]string{"sha": strings.Repeat("a", 40), "ref": "work/fixture"}, "base": map[string]any{"sha": strings.Repeat("c", 40), "ref": "main", "repo": map[string]string{"full_name": "example/delivery"}}}
		case "/repos/example/delivery/commits/" + sha + "/pulls":
			body = []any{map[string]int{"number": 7}}
		default:
			t.Fatalf("unexpected merge proof read %s", r.URL.Path)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	reader.App.Config.TenantID = f.person.TenantID
	base := reader.App.Client.Transport
	minted := 0
	reader.App.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/access_tokens") {
			var body struct {
				Permissions map[string]string `json:"permissions"`
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
			if err != nil || len(raw) > 4096 {
				t.Fatal("invalid fixture mint")
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Permissions) != 2 || body.Permissions["pull_requests"] != "read" || body.Permissions["metadata"] != "read" {
				return &http.Response{StatusCode: 422, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"fixture installation has PR read only"}`))}, nil
			}
			minted++
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
		}
		return base.RoundTrip(r)
	})
	fact, pending, err := readMergedPull(t.Context(), reader, 7)
	if err != nil || pending || fact == nil || fact.SHA != sha {
		t.Fatalf("closure proof: %+v pending=%v err=%v", fact, pending, err)
	}
	facts, pending, err := f.m.completedGroup(t.Context(), reader, sha)
	if err != nil || pending || len(facts) != 1 || facts[0].SHA != sha {
		t.Fatalf("destroyed-group proof: %+v pending=%v err=%v", facts, pending, err)
	}
	facts, pending, err = readMergedGroup(t.Context(), reader, sha, false)
	if err != nil || pending || len(facts) != 1 || facts[0].SHA != sha || minted != 3 {
		t.Fatalf("push proof: %+v pending=%v err=%v mints=%d", facts, pending, err, minted)
	}
}

func TestDeliveryHistorySchedulesIncrementalRefreshFromStoredWatermark(t *testing.T) {
	// Risk: finished history never recovers missed webhooks, or a restarted
	// reader repeats the whole selected window on every periodic refresh.
	f, g := newMetricsFixture(t)
	f.tx(t, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT now()`).Scan(&f.at) })
	through := f.at.Add(-metricRefreshInterval - time.Minute)
	cursor := backfillCursor{Since: dayStart(f.at.AddDate(0, 0, -30)), Phase: "done", ReadSource: historyActions,
		ReadThrough: &through, ReadUntil: &through, Workflows: fullBackfillWorkflows(&metricSourceRow{CIWorkflow: defaultCIWorkflow, NightlyWorkflow: defaultNightlyWorkflow}), DefaultBranch: "main"}
	raw, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_cursor=$2,backfill_done_at=$3,backfill_pending=false WHERE project_id=$1`, f.project, raw, through)
		return err
	})
	var nextID int64 = 1001
	g.respond = func(endpoint string) (any, error) {
		u, _ := url.Parse(endpoint)
		if strings.HasPrefix(u.Path, "/actions/workflows/") {
			bounds := strings.Split(u.Query().Get("created"), "..")
			if len(bounds) != 2 || bounds[0] != through.UTC().Format(time.RFC3339) || bounds[1] != f.at.UTC().Format(time.RFC3339) {
				t.Fatalf("scheduled refresh lost the stored interval: %s", endpoint)
			}
			if strings.Contains(u.Path, "/ci.yml/") {
				body := githubRunBody(nextID, 1, defaultCIWorkflow, "push", "main", strings.Repeat("a", 40), f.at.Add(-5*time.Minute), time.Second, "success", 0)
				nextID++
				return map[string]any{"total_count": 1, "workflow_runs": []any{body}}, nil
			}
			return githubRunsPage{}, nil
		}
		if u.Path == "/pulls" {
			return []any{}, nil
		}
		t.Fatalf("unexpected scheduled read: %s", endpoint)
		return nil, errRead
	}
	sweep := func() {
		fresh := New(f.d.App, f.m.config, f.m.secret, g, nil)
		fresh.now = f.m.now
		fresh.sweepMetrics(t.Context())
	}
	sweep()
	if len(g.calls) != 4 {
		t.Fatalf("refresh requests: %v", g.calls)
	}
	f.tx(t, func(tx pgx.Tx) error {
		stored, err := loadMetricSourceTx(t.Context(), tx, f.project, false)
		if err != nil {
			return err
		}
		if stored.Pending || stored.Cursor.ReadThrough == nil || !stored.Cursor.ReadThrough.Equal(f.at) {
			t.Fatalf("scheduled watermark not durable: %+v", stored)
		}
		return nil
	})
	through = f.at
	f.at = f.at.Add(metricRefreshInterval - time.Microsecond)
	sweep()
	if len(g.calls) != 4 {
		t.Fatal("restart read history before its refresh interval")
	}
	f.at = f.at.Add(time.Microsecond)
	sweep()
	if len(g.calls) != 8 {
		t.Fatalf("refresh did not resume at its exact interval: %v", g.calls)
	}
}

func TestDeliveryHistoryFailureLogKeepsOnlyClassifiedMetadata(t *testing.T) {
	// Risk: history cannot be diagnosed from its logs, or raw upstream errors
	// leak private request/response text instead of fixed, safe metadata.
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	remaining := 0
	failure := &webhookGitHubError{class: "secondary_limit", reset: at.Add(2 * time.Hour), remaining: &remaining,
		endpoint: "/actions/runs/{run}/attempts/{attempt}/jobs", duration: 1500 * time.Millisecond,
		cause: fmt.Errorf("private fixture response and request URL: %w", &crossreview.GitHubHTTPError{StatusCode: 403, ActionsReadRequired: true})}
	logMetricFailure(t.Context(), failure, metricError(failure, at))
	var fields map[string]any
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"reason": "rate_limited", "status": float64(403), "rate_limit_remaining": float64(0), "rate_limit_reset": failure.reset.Format(time.RFC3339), "timeout": false, "duration_ms": float64(1500), "endpoint": failure.endpoint} {
		if fields[key] != want {
			t.Fatalf("log %s = %v, want %v", key, fields[key], want)
		}
	}
	if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "Authorization") || strings.Contains(output.String(), "token") {
		t.Fatal("private upstream data escaped into history diagnostics")
	}
	output.Reset()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	logMetricFailure(t.Context(), failure, metricError(failure, at))
	for _, field := range []string{"status=403", "rate_limit_remaining=0", "duration_ms=1500", "timeout=false"} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("text diagnostic lost %s", field)
		}
	}
	if strings.Contains(output.String(), "0x") || strings.Contains(output.String(), "private") {
		t.Fatal("text diagnostic contains pointer addresses or private text")
	}
}
