// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func writeMetricJSON(out any, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestDeliveryMetricsFix2KeepsIntermediateAttempts(t *testing.T) {
	// Risk: a third attempt hides the success in between, so flake, first
	// green and queue-attempt counts are computed from attempt 1 and the latest only.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	day := dayStart(now.AddDate(0, 0, -1))
	head, queueHead := strings.Repeat("c", 40), strings.Repeat("b", 40)
	queueBranchName := "gh-readonly-queue/main/pr-9-" + strings.Repeat("a", 40)
	list := "/actions/workflows/ci.yml/runs?created=" + day.Format("2006-01-02") + "&status=completed&per_page=100&page=1"
	body := func(id int64, attempt int, event, branch, sha string, created time.Time, wall time.Duration, conclusion string) map[string]any {
		return githubRunBody(id, attempt, defaultCIWorkflow, event, branch, sha, created, wall, conclusion, 0)
	}
	paths := map[string]int{}
	get := func(path string, out any) error {
		paths[path]++
		switch {
		case path == list:
			return writeMetricJSON(out, map[string]any{"total_count": 2, "workflow_runs": []any{
				body(501, 3, "pull_request", "work/a", head, day.Add(2*time.Hour), 10*time.Minute, "failure"),
				body(502, 3, "merge_group", queueBranchName, queueHead, day.Add(3*time.Hour), 12*time.Minute, "success"),
			}})
		case path == "/actions/runs/501/attempts/1":
			return writeMetricJSON(out, body(501, 1, "pull_request", "work/a", head, day.Add(time.Hour), 8*time.Minute, "failure"))
		case path == "/actions/runs/501/attempts/2":
			return writeMetricJSON(out, body(501, 2, "pull_request", "work/a", head, day.Add(90*time.Minute), 9*time.Minute, "success"))
		case path == "/actions/runs/502/attempts/1", path == "/actions/runs/502/attempts/2":
			n := 1
			if strings.HasSuffix(path, "/2") {
				n = 2
			}
			return writeMetricJSON(out, body(502, n, "merge_group", queueBranchName, queueHead, day.Add(time.Duration(n)*time.Hour), 11*time.Minute, "success"))
		case strings.HasPrefix(path, "/actions/workflows/"):
			return writeMetricJSON(out, map[string]any{"total_count": 0, "workflow_runs": []any{}})
		case path == "":
			return writeMetricJSON(out, map[string]any{"full_name": "example/delivery", "default_branch": "main"})
		case strings.HasPrefix(path, "/pulls"):
			return writeMetricJSON(out, []any{})
		default:
			return fmt.Errorf("unexpected GitHub read %s", path)
		}
	}
	cursor := backfillCursor{Since: day, Phase: "runs", Day: day.Format("2006-01-02"), Page: 1}
	var runs []metricRun
	for steps := 0; cursor.Phase == "runs"; steps++ {
		if steps > 4 {
			t.Fatal("attempt backfill did not leave the runs phase")
		}
		next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Calls > backfillCallBudget {
			t.Fatalf("step used %d calls", batch.Calls)
		}
		runs = append(runs, batch.Runs...)
		cursor = next
	}
	got := map[string]bool{}
	for _, run := range runs {
		got[fmt.Sprintf("%d:%d:%s", run.ID, run.Attempt, run.Conclusion)] = true
	}
	for _, want := range []string{"501:1:failure", "501:2:success", "501:3:failure", "502:1:success", "502:2:success", "502:3:success"} {
		if !got[want] {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
	if paths["/actions/runs/501/attempts/2"] != 1 || paths["/actions/runs/502/attempts/2"] != 1 {
		t.Fatalf("intermediate attempts were not read: %v", paths)
	}
	// The same read is what a check-suite webhook uses.
	one, err := runWithFirstAttempt(get, githubRun{ID: 501, Attempt: 3, Name: "CI", Path: defaultCIWorkflow, Event: "pull_request", Branch: "work/a", Head: head, Status: "completed", Conclusion: strPtr("failure"), Created: day.Add(2 * time.Hour), Updated: day.Add(2*time.Hour + 11*time.Minute)})
	if err != nil || len(one) != 3 {
		t.Fatalf("webhook attempts: %+v %v", one, err)
	}
	covered := now.AddDate(0, 0, -60)
	// PR 9 merged, so its three queue attempts stay in the merged-PR count.
	merged := day.Add(4 * time.Hour)
	metrics := computeMetrics(metricInput{CIWorkflow: defaultCIWorkflow, NightlyWorkflow: defaultNightlyWorkflow, Covered: &covered, Runs: runs,
		Pulls: []metricPull{{Number: 9, Opened: day, Merged: &merged}}}, now)
	flaky := metricByKey(t, metrics, "flaked_failures")
	wantWindow(t, flaky, 30, "no_data", 1, nil, nil, nil)
	wantCounts(t, flaky, 30, map[string]int{"confirmed": 0, "workflow_rescue": 1})
	wantWindow(t, metricByKey(t, metrics, "time_to_first_green"), 30, "ok", 1, f64(40), f64(40), f64(40))
	wantWindow(t, metricByKey(t, metrics, "queue_runs_per_pr"), 30, "ok", 1, f64(3), f64(3), f64(3))
}

func strPtr(s string) *string { return &s }

func TestDeliveryMetricsFix2AttemptReadsResumeInsideTheBudget(t *testing.T) {
	// Risk: reading every attempt of one run either exceeds the step budget
	// or resumes by skipping the attempt it stopped on.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	day := dayStart(now)
	head := strings.Repeat("d", 40)
	list := "/actions/workflows/ci.yml/runs?created=" + day.Format("2006-01-02") + "&status=completed&per_page=100&page=1"
	get := func(path string, out any) error {
		switch {
		case path == list:
			return writeMetricJSON(out, map[string]any{"total_count": 1, "workflow_runs": []any{
				githubRunBody(800, 45, defaultCIWorkflow, "pull_request", "work/a", head, day.Add(time.Hour), 8*time.Minute, "failure", 0),
			}})
		case strings.HasPrefix(path, "/actions/runs/800/attempts/"):
			n, err := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
			if err != nil || n < 1 || n >= 45 {
				return fmt.Errorf("unexpected attempt %s", path)
			}
			conclusion := "failure"
			if n == 2 {
				conclusion = "success"
			}
			return writeMetricJSON(out, githubRunBody(800, n, defaultCIWorkflow, "pull_request", "work/a", head, day.Add(time.Duration(n)*time.Minute), 8*time.Minute, conclusion, 0))
		case path == "":
			return writeMetricJSON(out, map[string]any{"full_name": "example/delivery", "default_branch": "main"})
		case strings.HasPrefix(path, "/pulls"), strings.HasPrefix(path, "/actions/workflows/"):
			if strings.HasPrefix(path, "/pulls") {
				return writeMetricJSON(out, []any{})
			}
			return writeMetricJSON(out, map[string]any{"total_count": 0, "workflow_runs": []any{}})
		default:
			return fmt.Errorf("unexpected GitHub read %s", path)
		}
	}
	cursor := backfillCursor{Since: day, Phase: "runs", Day: day.Format("2006-01-02"), Page: 1}
	next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Calls != backfillCallBudget || next.AttemptRun != 800 || next.Attempt != 40 || next.Day != day.Format("2006-01-02") {
		t.Fatalf("first step did not resume inside the budget: cursor %+v calls %d", next, batch.Calls)
	}
	seen := map[int]string{}
	for _, run := range batch.Runs {
		seen[run.Attempt] = run.Conclusion
	}
	if seen[1] != "failure" || seen[2] != "success" || seen[40] != "" {
		t.Fatalf("first step attempts: %v", seen)
	}
	for steps := 0; len(seen) < 45 && steps < 4; steps++ {
		cursor = next
		next, batch, err = backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Calls > backfillCallBudget {
			t.Fatalf("resumed step used %d calls", batch.Calls)
		}
		for _, run := range batch.Runs {
			if run.ID == 800 {
				seen[run.Attempt] = run.Conclusion
			}
		}
	}
	if len(seen) != 45 || seen[44] == "" {
		t.Fatalf("resumed attempts: %d has 44=%v", len(seen), seen[44] != "")
	}
}

func TestDeliveryMetricsFix2PullPageCapStaysOpen(t *testing.T) {
	// Risk: the last GitHub pulls page is reported done, so missing merged
	// pulls still produce an ok metric.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	since := dayStart(now.AddDate(0, 0, -30))
	pulls := make([]any, 100)
	for i := range pulls {
		updated := now.Add(-time.Duration(i) * time.Minute)
		pulls[i] = map[string]any{"number": i + 1, "created_at": updated.Add(-time.Hour), "updated_at": updated, "merged_at": updated, "closed_at": updated, "head": map[string]any{"ref": "work/x"}, "base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "example/delivery"}}}
	}
	get := func(path string, out any) error {
		if path != "/pulls?state=all&sort=updated&direction=desc&per_page=100&page=100" {
			return fmt.Errorf("unexpected GitHub read %s", path)
		}
		return writeMetricJSON(out, pulls)
	}
	cursor := backfillCursor{Since: since, Phase: "pulls", Page: 100, DefaultBranch: "main"}
	next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
	if err != nil {
		t.Fatal(err)
	}
	if next.Phase != "done" || !next.Truncated || len(batch.Pulls) != 100 {
		t.Fatalf("full last page: phase %s truncated %v pulls %d", next.Phase, next.Truncated, len(batch.Pulls))
	}
	get = func(path string, out any) error {
		if path != "/pulls?state=all&sort=updated&direction=desc&per_page=100&page=100" {
			return fmt.Errorf("unexpected GitHub read %s", path)
		}
		return writeMetricJSON(out, pulls[:40])
	}
	next, batch, err = backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
	if err != nil {
		t.Fatal(err)
	}
	if next.Phase != "done" || next.Truncated || len(batch.Pulls) != 40 {
		t.Fatalf("short last page: phase %s truncated %v pulls %d", next.Phase, next.Truncated, len(batch.Pulls))
	}
}

func TestDeliveryMetricsFix2BackfillUsesDefaultBranch(t *testing.T) {
	// Risk: backfill counts pulls into every base, while webhook ingestion
	// and the Arion script count only the default branch.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	since := dayStart(now.AddDate(0, 0, -30))
	pull := func(n int, base string) map[string]any {
		updated := now.Add(-time.Hour)
		return map[string]any{"number": n, "created_at": updated.Add(-2 * time.Hour), "updated_at": updated, "merged_at": updated, "closed_at": updated, "head": map[string]any{"ref": "work/x"}, "base": map[string]any{"ref": base, "repo": map[string]any{"full_name": "example/delivery", "default_branch": "trunk"}}}
	}
	get := func(path string, out any) error {
		switch path {
		case "":
			return writeMetricJSON(out, map[string]any{"full_name": "example/delivery", "default_branch": "trunk"})
		case "/pulls?state=all&sort=updated&direction=desc&per_page=100&page=1":
			return writeMetricJSON(out, []any{pull(7, "trunk"), pull(8, "main"), pull(9, "release/1.2")})
		default:
			return fmt.Errorf("unexpected GitHub read %s", path)
		}
	}
	cursor := backfillCursor{Since: since, Phase: "pulls", Page: 1}
	next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
	if err != nil {
		t.Fatal(err)
	}
	if next.Phase != "done" || next.Truncated || len(batch.Pulls) != 1 || batch.Pulls[0].Number != 7 {
		t.Fatalf("mixed bases: phase %s truncated %v pulls %+v", next.Phase, next.Truncated, batch.Pulls)
	}
}

func TestDeliveryMetricsFix2ReadBindsTenantAndActionsScope(t *testing.T) {
	// Risk: metrics reads use another tenant's installation, or a private
	// repository refuses workflow-run reads because actions:read was not requested.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "fixture.pem")
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	tenantID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	minted := 0
	var got map[string]string
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body any
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/456/access_tokens":
			var in struct {
				Permissions  map[string]string `json:"permissions"`
				Repositories []string          `json:"repositories"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			got = in.Permissions
			if len(in.Repositories) != 1 || in.Repositories[0] != "delivery" {
				t.Fatal("metrics token repository drift")
			}
			for name, level := range in.Permissions {
				if level != "read" {
					t.Fatalf("metrics read requested %s:%s", name, level)
				}
			}
			body = map[string]any{"token": "delivery-test-fixture", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": "example/delivery"}}}
			minted++
		case r.Method == "DELETE" && r.URL.Path == "/installation/token":
			body = map[string]any{}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery":
			body = map[string]string{"full_name": "example/delivery"}
		default:
			t.Fatalf("unexpected metrics read %s %s", r.Method, r.URL.Path)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	reader := AppReader{App: &crossreview.GitHubApp{Config: crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: tenantID, Repository: "example/delivery", KeyFile: keyFile}, Client: client}}
	if err = reader.MetricsRead(t.Context(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", func(func(string, any) error) error {
		t.Fatal("another tenant read the installation")
		return nil
	}); err == nil || minted != 0 {
		t.Fatalf("another tenant minted a token: %v calls %d", err, minted)
	}
	if err = reader.MetricsRead(t.Context(), tenantID, func(get func(string, any) error) error {
		var repo struct {
			FullName string `json:"full_name"`
		}
		return get("", &repo)
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"actions": "read", "pull_requests": "read", "checks": "read", "contents": "read", "metadata": "read", "statuses": "read"}
	if minted != 1 || len(got) != len(want) {
		t.Fatalf("scopes %#v minted %d", got, minted)
	}
	for name, level := range want {
		if got[name] != level {
			t.Fatalf("scopes %#v", got)
		}
	}
}

func TestDeliveryMetricsFix2AnotherTenantCannotReadInstallation(t *testing.T) {
	// Risk: a person in another tenant backfills through this workspace's
	// GitHub App and stores the repository's metadata in their tenant.
	f, g := newMetricsFixture(t)
	var project string
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'AEON-9932',id,'Foreign metrics' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.foreign.TenantID).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	var source MetricSource
	f.call(t, f.foreign, "PUT", "/api/projects/"+project+"/delivery/metrics/source", map[string]any{"repository": f.m.config.Repository}, 200, &source)
	if source.AppConnected {
		t.Fatal("another tenant sees this installation as connected")
	}
	before := len(g.calls)
	f.call(t, f.foreign, "POST", "/api/projects/"+project+"/delivery/metrics/backfill", map[string]any{"days": 1}, 409, nil)
	if len(g.calls) != before {
		t.Fatalf("another tenant triggered installation reads: %v", g.calls[before:])
	}
}

func TestDeliveryMetricsFix2DisconnectKeepsHistoryPartial(t *testing.T) {
	// Risk: after the App disconnects, a finished backfill still reports ok
	// across the time nobody is observing.
	f, _ := newMetricsFixture(t)
	completed := f.at.Add(-2 * time.Hour)
	started := completed.Add(-10 * time.Minute)
	done := f.at.Add(-time.Hour)
	since := f.at.AddDate(0, 0, -60)
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_since=$2, backfill_done_at=$3 WHERE project_id=$1`, f.project, since, done); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_runs(tenant_id,repository,run_id,attempt,workflow_path,workflow_name,event,head_branch,head_sha,created_at,started_at,completed_at,conclusion,source,recorded_at)
			VALUES($1,$2,1,1,$3,'CI','pull_request','work/a',$4,$5,$6,$7,'success','backfill',$8)`,
			f.person.TenantID, f.m.config.Repository, defaultCIWorkflow, strings.Repeat("a", 40), completed.Add(-time.Minute), started, completed, f.at)
		return err
	})
	var stored map[string]int
	// Marks older than the load window (30 days plus 7 of slack) are stored but
	// not read back, so this one sits inside that slack and before the 30-day
	// window. Its own coverage then covers the window.
	reportedAt := f.at.Add(-metricDays*24*time.Hour - metricSlack + time.Hour)
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/facts", map[string]any{"facts": []any{
		map[string]any{"kind": "release", "key": "old", "started_at": reportedAt.Add(-time.Hour), "at": reportedAt, "outcome": "live"},
		map[string]any{"kind": "release", "key": "new", "started_at": f.at.Add(-50 * time.Minute), "at": f.at.Add(-10 * time.Minute), "outcome": "live"},
	}}, 200, &stored)
	var before DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &before)
	wall := metricByKey(t, before.Metrics, "pr_ci_wall")
	wantWindow(t, wall, 30, "ok", 1, f64(10), f64(10), f64(10))
	release := metricByKey(t, before.Metrics, "release_queue_to_live")
	if release.Status != "ok" || release.Latest == nil || release.Latest.Value != 40 {
		t.Fatalf("reported release before disconnect: %+v", release)
	}
	f.m.config.InstallationID = ""
	var after DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &after)
	if after.Source == nil || after.Source.AppConnected || after.Source.Backfill != "done" {
		t.Fatalf("source after disconnect: %+v", after.Source)
	}
	wall = metricByKey(t, after.Metrics, "pr_ci_wall")
	wantWindow(t, wall, 30, "partial", 1, f64(10), f64(10), f64(10))
	if wall.Status != "partial" || wall.Reason == nil || !strings.Contains(*wall.Reason, "does not receive events") || !strings.Contains(*wall.Reason, "2026-10-06 23:00") {
		t.Fatalf("disconnected GitHub metric: status %s reason %v", wall.Status, wall.Reason)
	}
	var day *MetricPoint
	for i := range wall.Daily {
		if wall.Daily[i].Date == "2026-10-06" {
			day = &wall.Daily[i]
		}
	}
	if day == nil || day.Status != "partial" || day.N != 1 || day.Value == nil || *day.Value != 10 {
		t.Fatalf("retained day: %+v", day)
	}
	release = metricByKey(t, after.Metrics, "release_queue_to_live")
	if release.Status != "ok" || release.Latest == nil || release.Latest.Value != 40 {
		t.Fatalf("reported release after disconnect: %+v", release)
	}
}

func TestDeliveryPreflightDisconnectEndsObservation(t *testing.T) {
	// Risk: after the App disconnects, preflight still reports a full span, so
	// later chart days look observed. The window-status fallback alone is not enough.
	f, _ := newMetricsFixture(t)
	since := f.at.AddDate(0, 0, -40)
	completed := time.Date(2026, 10, 5, 22, 0, 0, 0, time.UTC)
	created := completed.Add(-6 * time.Minute)
	cursor, err := json.Marshal(backfillCursor{Since: since, Phase: "done", Workflows: []string{defaultCIWorkflow, defaultNightlyWorkflow, defaultPreflightWorkflow}})
	if err != nil {
		t.Fatal(err)
	}
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_cursor=$2,backfill_since=$3,backfill_done_at=$4 WHERE project_id=$1`, f.project, cursor, since, completed); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_runs(tenant_id,repository,run_id,attempt,workflow_path,workflow_name,event,head_branch,head_sha,created_at,started_at,completed_at,conclusion,source,recorded_at)
			VALUES($1,$2,720,1,$3,'preflight','workflow_dispatch','main',$4,$5,$6,$7,'failure','backfill',$8)`,
			f.person.TenantID, f.m.config.Repository, defaultPreflightWorkflow, strings.Repeat("b", 40), created, created.Add(time.Minute), completed, f.at)
		return err
	})
	var before DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &before)
	connected := metricByKey(t, before.Metrics, "preflight_red_rate")
	wantWindow(t, connected, 7, "ok", 1, f64(100), nil, nil)
	if !metricWindow(t, connected, 7).Coverage.Full {
		t.Fatalf("connected preflight coverage: %+v", metricWindow(t, connected, 7).Coverage)
	}
	f.m.config.InstallationID = ""
	var after DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &after)
	stopped := metricByKey(t, after.Metrics, "preflight_red_rate")
	window := metricWindow(t, stopped, 7)
	if window.Coverage.Full {
		t.Fatalf("preflight coverage stayed full after disconnect: %+v", window.Coverage)
	}
	var sampleDay *MetricPoint
	for i := range stopped.Daily {
		if stopped.Daily[i].Date == "2026-10-05" {
			sampleDay = &stopped.Daily[i]
		}
	}
	if sampleDay == nil || sampleDay.Status != "partial" || sampleDay.N != 1 {
		t.Fatalf("retained preflight day: %+v", sampleDay)
	}
	if stopped.Reason == nil || !strings.Contains(*stopped.Reason, "complete only through") {
		t.Fatalf("preflight disconnect reason: %v", stopped.Reason)
	}
}
