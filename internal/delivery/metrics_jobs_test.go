// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Content-free recorded webhook shapes are shifted onto the fixture clock;
// no fixed event date is compared with the database's real clock.
func jobAttemptFixture(t *testing.T, name string, day time.Time) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	var shift func(map[string]any)
	shift = func(object map[string]any) {
		for key, value := range object {
			if nested, ok := value.(map[string]any); ok {
				shift(nested)
			} else if text, ok := value.(string); ok && strings.HasSuffix(key, "_at") {
				if stamp, err := time.Parse(time.RFC3339, text); err == nil {
					object[key] = day.Add(stamp.Sub(base)).Format(time.RFC3339)
				}
			}
		}
	}
	shift(body)
	return body
}

func TestDeliveryGitHubJobAttemptsProjection(t *testing.T) {
	// Risk: live attempts disappear, one completed job is mistaken for the
	// whole run, late job timing is lost, retries duplicate facts, or failed
	// best-effort reads never wake a finished history worker.
	f, g := newMetricsFixture(t)
	f.tx(t, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT now()`).Scan(&f.at) })
	day := dayStart(f.at).AddDate(0, 0, -1)
	seedBareRuns(t, f, 0)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_pending=false`)
		return err
	})
	green := jobAttemptFixture(t, "workflow_run.first_green.json", day)
	rerun := jobAttemptFixture(t, "workflow_run.rerun_green.json", day)
	ejected := jobAttemptFixture(t, "workflow_run.queue_ejection.json", day)
	runs := map[int64]map[string]any{501: green["workflow_run"].(map[string]any), 502: rerun["workflow_run"].(map[string]any), 503: ejected["workflow_run"].(map[string]any)}
	firstRed := githubRunBody(502, 1, defaultCIWorkflow, "pull_request", "work/job-rerun", strings.Repeat("b", 40), day.Add(6*time.Hour+30*time.Minute), 10*time.Minute, "failure", 902)
	jobsUnavailable := true
	g.respond = func(endpoint string) (any, error) {
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(u.Path, "/actions/runs/") {
			parts := strings.Split(u.Path, "/")
			id, _ := strconv.ParseInt(parts[3], 10, 64)
			if strings.HasSuffix(u.Path, "/jobs") {
				if jobsUnavailable {
					return nil, errRead
				}
				conclusions := map[string]string{}
				if id == 502 || id == 503 {
					conclusions["web"] = "failure"
				}
				body := githubJobsBody(day.Add(6*time.Hour), 30, conclusions)
				for i, entry := range body["jobs"].([]any) {
					job := entry.(map[string]any)
					job["id"] = id*10 + int64(i+1)
					if id == 501 && i < 2 {
						delete(job, "created_at") // REST need not provide queue times
					}
				}
				return body, nil
			}
			if id == 502 && strings.HasSuffix(u.Path, "/attempts/1") {
				return firstRed, nil
			}
			if run, ok := runs[id]; ok {
				return run, nil
			}
		}
		if u.Path == "/check-suites/901" {
			return map[string]any{"id": 901, "head_sha": strings.Repeat("a", 40), "status": "completed"}, nil
		}
		if u.Path == "/actions/runs" {
			return map[string]any{"total_count": 1, "workflow_runs": []any{runs[501]}}, nil
		}
		return nil, fmt.Errorf("unexpected attempt fixture read %s", endpoint)
	}
	jobBody := jobAttemptFixture(t, "workflow_job.completed.json", day)
	job := jobBody["workflow_job"].(map[string]any)
	jobBody["action"], job["status"] = "queued", "queued"
	started := job["started_at"]
	job["started_at"], job["conclusion"] = nil, nil
	auditSend(t, f, "workflow_job", "job-queued", jobBody, 204)
	jobBody["action"], job["status"], job["started_at"] = "in_progress", "in_progress", started
	auditSend(t, f, "workflow_job", "job-started", jobBody, 204)
	if len(g.calls) != 0 {
		t.Fatal("an unfinished job read GitHub or pretended the workflow finished")
	}
	auditSend(t, f, "workflow_run", "first-green", green, 204)
	if readStoredRun(t, f, 501, 1).jobsRead {
		t.Fatal("failed jobs read was reported complete")
	}
	var pending bool
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT backfill_pending FROM delivery_metric_sources WHERE project_id=$1`, f.project).Scan(&pending)
	})
	if !pending {
		t.Fatal("a live gap left the completed backfill asleep")
	}
	jobsUnavailable = false
	f.m.sweepMetrics(t.Context())
	if stored := readStoredRun(t, f, 501, 1); !stored.jobsRead || stored.waitMS != nil {
		t.Fatalf("one unknown job wait must withhold the worst wait: %+v", stored)
	}
	// The other job arrives after completion, and repairs the same attempt.
	jobBody["action"], job["status"], job["conclusion"] = "completed", "completed", "success"
	job["id"], job["name"], job["started_at"] = 5012, "web", day.Add(6*time.Hour+3*time.Minute)
	auditSend(t, f, "workflow_job", "late-job", jobBody, 204)
	if stored := readStoredRun(t, f, 501, 1); stored.waitMS == nil || *stored.waitMS != 120000 {
		t.Fatalf("early and late job timestamps did not converge: %+v", stored)
	}
	auditSend(t, f, "workflow_run", "rerun-green", rerun, 204)
	auditSend(t, f, "workflow_run", "queue-ejection", ejected, 204)
	queueGreen := githubRunBody(504, 1, defaultCIWorkflow, "merge_group", "gh-readonly-queue/main/pr-7-"+strings.Repeat("a", 40), strings.Repeat("d", 40), day.Add(8*time.Hour+30*time.Minute), 10*time.Minute, "success", 904)
	runs[504] = queueGreen
	auditSend(t, f, "workflow_run", "queue-green", map[string]any{"action": "completed", "workflow_run": queueGreen}, 204)
	// Completion from the check-run channel converges on the same run identity.
	check := map[string]any{"action": "completed", "check_run": map[string]any{"head_sha": strings.Repeat("a", 40), "name": "go", "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "check_suite": map[string]any{"id": 901}, "pull_requests": []any{}}}
	auditSend(t, f, "check_run", "attempt-check", check, 204)
	calls := len(g.calls)
	auditSend(t, f, "workflow_run", "rerun-green", rerun, 204)
	if len(g.calls) != calls {
		t.Fatal("redelivery repeated an already checkpointed read")
	}
	if count, _, _ := metricCounts(t, f); count != 5 {
		t.Fatalf("attempts were lost or duplicated: %d", count)
	}
	f.tx(t, func(tx pgx.Tx) error {
		merged := day.Add(9 * time.Hour)
		return upsertPullsTx(t.Context(), tx, f.person.TenantID, f.m.config.Repository, "webhook", []metricPull{{Number: 7, Branch: "work/job-first", Opened: day, Merged: &merged}}, f.at)
	})
	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	wantWindow(t, metricByKey(t, out.Metrics, "first_attempt_green"), 7, "ok", 2, f64(50), nil, nil)
	wantWindow(t, metricByKey(t, out.Metrics, "required_checks_green"), 7, "ok", 2, f64(50), nil, nil)
	wantWindow(t, metricByKey(t, out.Metrics, "queue_runs_per_pr"), 7, "ok", 1, f64(2), f64(2), f64(2))
	wantWindow(t, metricByKey(t, out.Metrics, "queue_ejections"), 7, "ok", 1, f64(100), nil, nil)
	for _, key := range []string{"time_to_first_green", "time_to_first_green_commit", "runner_wait"} {
		if window := metricWindow(t, metricByKey(t, out.Metrics, key), 7); window.N != 2 || window.Value == nil {
			t.Fatalf("%s did not read both projected branches: %+v", key, window)
		}
	}
	flakes := metricWindow(t, metricByKey(t, out.Metrics, "flaked_failures"), 7)
	if flakes.Value != nil || flakes.Counts["workflow_rescue"] != 1 {
		t.Fatalf("a rescued workflow was falsely called a confirmed flaky execution: %+v", flakes)
	}
	// Authentication and installation binding apply equally to job events.
	bad := jobAttemptFixture(t, "workflow_job.completed.json", day)
	bad["workflow_job"].(map[string]any)["run_attempt"] = 0
	auditSend(t, f, "workflow_job", "bad-job-attempt", bad, 400)
}

func TestDeliverySevenDayJobBackfillIsIdempotentAndBounded(t *testing.T) {
	// Risk: a seven-day repair reports done before jobs, exceeds the request
	// budget on paginated jobs, ignores GitHub's retry hint, or duplicates an
	// attempt when the same history window is restarted.
	f, g := newMetricsFixture(t)
	f.tx(t, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT now()`).Scan(&f.at) })
	day := dayStart(f.at).AddDate(0, 0, -1)
	green := jobAttemptFixture(t, "workflow_run.first_green.json", day)["workflow_run"]
	limited := false
	g.respond = func(endpoint string) (any, error) {
		u, _ := url.Parse(endpoint)
		switch {
		case u.Path == "":
			return map[string]any{"full_name": f.m.config.Repository, "default_branch": "main"}, nil
		case strings.HasPrefix(u.Path, "/actions/workflows/"):
			runs := []any{}
			if u.Path == "/actions/workflows/ci.yml/runs" && u.Query().Get("created") == day.Format("2006-01-02") {
				runs = append(runs, green)
			}
			return map[string]any{"total_count": len(runs), "workflow_runs": runs}, nil
		case strings.HasSuffix(u.Path, "/jobs"):
			if limited {
				return nil, &webhookGitHubError{class: "http_429", retryAfter: 2 * time.Minute, cause: &crossreview.GitHubHTTPError{StatusCode: 429}}
			}
			return githubJobsBody(day.Add(6*time.Hour), 30, nil), nil
		case u.Path == "/pulls":
			return []any{}, nil
		}
		return nil, fmt.Errorf("unexpected seven-day history read %s", endpoint)
	}
	path := "/api/projects/" + f.project + "/delivery/metrics/backfill"
	var step BackfillResult
	f.call(t, f.person, "POST", path, map[string]any{"days": 7, "restart": true}, 200, &step)
	if step.State != "running" || step.Phase != "jobs" || step.Calls > backfillCallBudget || !step.Since.Equal(dayStart(f.at.AddDate(0, 0, -7))) {
		t.Fatalf("seven-day runs did not enter the jobs pass: %+v", step)
	}
	limited = true
	f.call(t, f.person, "POST", path, map[string]any{}, 502, nil)
	source := historySource(t, f)
	if source.RetryAt == nil || !source.RetryAt.Equal(f.at.Add(2*time.Minute)) {
		t.Fatalf("GitHub's rate limit was not persisted: %+v", source)
	}
	calls := len(g.calls)
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if len(g.calls) != calls || step.State != "running" {
		t.Fatal("backfill ignored the persisted retry deadline")
	}
	f.at, limited = *source.RetryAt, false
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.State != "done" || step.Jobs != 1 || step.Calls > jobFillBudget {
		t.Fatalf("jobs did not finish the window: %+v", step)
	}
	calls = len(g.calls)
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.State != "done" || len(g.calls) != calls {
		t.Fatal("finished history reread complete jobs")
	}
	f.call(t, f.person, "POST", path, map[string]any{"days": 7, "restart": true}, 200, &step)
	if step.State != "done" {
		t.Fatalf("restarting the same window lost its complete job facts: %+v", step)
	}
	if count, _, _ := metricCounts(t, f); count != 1 {
		t.Fatalf("restarted history duplicated the attempt: %d", count)
	}
	// After 39 small reads the next attempt needs three pages. Its second
	// page must not exceed the budget, and the next step must retry it whole.
	seedBareRuns(t, f, 45)
	g.respond = func(endpoint string) (any, error) {
		u, _ := url.Parse(endpoint)
		parts := strings.Split(u.Path, "/")
		id, _ := strconv.ParseInt(parts[3], 10, 64)
		if id != 1006 {
			return githubJobsBody(day.Add(6*time.Hour), 30, nil), nil
		}
		page, _ := strconv.Atoi(u.Query().Get("page"))
		count := 100
		if page == 3 {
			count = 1
		}
		jobs := make([]any, count)
		for i := range jobs {
			jobs[i] = map[string]any{"id": page*100 + i, "name": fmt.Sprintf("matrix-%d-%d", page, i), "conclusion": "success", "created_at": day, "started_at": day.Add(time.Second)}
		}
		return map[string]any{"total_count": 201, "jobs": jobs}, nil
	}
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.Calls != jobFillBudget || step.Jobs != 39 || step.Phase != "jobs" || step.RetryAt == nil || !step.RetryAt.Equal(f.at.Add(time.Minute)) {
		t.Fatalf("pagination exceeded the budget or stored an incomplete map: %+v", step)
	}
	if stored := readStoredRun(t, f, 1006, 1); stored.jobsRead || stored.failed {
		t.Fatalf("budget exhaustion was confused with a GitHub failure: %+v", stored)
	}
	f.at = *step.RetryAt
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.State != "done" || step.Jobs != 6 || step.Calls != 8 {
		t.Fatalf("the whole paginated attempt did not resume: %+v", step)
	}
}

type storedRun struct {
	tree       *string
	jobsRead   bool
	failed     bool
	waitMS     *int
	requiredWe *string
}

func readStoredRun(t *testing.T, f *fixture, id int64, attempt int) storedRun {
	t.Helper()
	var s storedRun
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT head_tree,jobs_read_at IS NOT NULL,jobs_failed_at IS NOT NULL,worst_job_wait_ms,required_jobs->'conclusions'->>'web' FROM delivery_metric_runs WHERE run_id=$1 AND attempt=$2`, id, attempt).
			Scan(&s.tree, &s.jobsRead, &s.failed, &s.waitMS, &s.requiredWe)
	})
	return s
}

func TestDeliveryMetricsWebhookStoresJobFactsAndHeadTree(t *testing.T) {
	// Risk: the numbers that need the jobs (required checks, runner wait) or the
	// head tree (suspects) read facts the webhook never stored, a later attempt
	// erases the first attempt's job facts, or a failed jobs read loses the run
	// instead of leaving a visible gap.
	f, g := newMetricsFixture(t)
	head := strings.Repeat("d", 40)
	tree := strings.Repeat("e", 40)
	created := f.at.Add(-2 * time.Hour)
	suite := map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": head, "head_branch": "work/aeon-1016", "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}
	latest := githubRunBody(501, 2, defaultCIWorkflow, "pull_request", "work/aeon-1016", head, created.Add(30*time.Minute), 12*time.Minute, "success", 900)
	first := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", "work/aeon-1016", head, created, 10*time.Minute, "failure", 900)
	for _, run := range []map[string]any{latest, first} {
		run["head_commit"] = map[string]any{"tree_id": tree, "message": "never stored"}
	}
	jobsFail := false
	g.respond = func(path string) (any, error) {
		switch path {
		case "/actions/runs?check_suite_id=900&head_sha=" + head + "&per_page=2":
			return map[string]any{"total_count": 1, "workflow_runs": []any{latest}}, nil
		case "/actions/runs/501/attempts/1":
			return first, nil
		case "/actions/runs/501/attempts/1/jobs?per_page=100&page=1":
			if jobsFail {
				return nil, errRead
			}
			return githubJobsBody(created, 90, map[string]string{"web": "failure", "e2e": "-"}), nil
		}
		return nil, errRead
	}
	auditSend(t, f, "check_suite", "jobs-suite", suite, 204)
	one, two := readStoredRun(t, f, 501, 1), readStoredRun(t, f, 501, 2)
	if one.tree == nil || *one.tree != tree || two.tree == nil || *two.tree != tree {
		t.Fatalf("head trees: %+v %+v", one, two)
	}
	if !one.jobsRead || one.waitMS == nil || *one.waitMS != 90000 || one.requiredWe == nil || *one.requiredWe != "failure" {
		t.Fatalf("attempt 1 job facts: %+v", one)
	}
	if two.jobsRead {
		t.Fatalf("a later attempt has no job facts of its own: %+v", two)
	}
	var raw []byte
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT required_jobs FROM delivery_metric_runs WHERE run_id=501 AND attempt=1`).Scan(&raw)
	})
	var stored storedJobFacts
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	// Every job is kept, not only the requesting project's checks. A required
	// name with no job is absent here and counts as missing when the map is complete.
	if !stored.Complete || stored.Conclusions["go"] != "success" || stored.Conclusions["web"] != "failure" || stored.Conclusions["e2e"] != "" || stored.Conclusions["go-test (1)"] != "skipped" {
		t.Fatalf("job conclusions: %s", raw)
	}
	// A redelivery converges; the first attempt keeps its facts.
	auditSend(t, f, "check_suite", "jobs-suite", suite, 204)
	if again := readStoredRun(t, f, 501, 1); !again.jobsRead || again.waitMS == nil || *again.waitMS != 90000 {
		t.Fatalf("redelivery: %+v", again)
	}

	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	wantWindow(t, metricByKey(t, out.Metrics, "required_checks_green"), 30, "partial", 1, f64(0), nil, nil)
	wantWindow(t, metricByKey(t, out.Metrics, "runner_wait"), 30, "partial", 1, f64(1.5), f64(1.5), f64(1.5))

	// A jobs read that fails keeps the run and leaves the gap visible.
	jobsFail = true
	otherHead := strings.Repeat("c", 40)
	other := githubRunBody(502, 1, defaultCIWorkflow, "pull_request", "work/aeon-1017", otherHead, created.Add(time.Hour), 9*time.Minute, "success", 901)
	g.respond = func(path string) (any, error) {
		if path == "/actions/runs?check_suite_id=901&head_sha="+otherHead+"&per_page=2" {
			return map[string]any{"total_count": 1, "workflow_runs": []any{other}}, nil
		}
		return nil, errRead
	}
	auditSend(t, f, "check_suite", "jobs-suite-2", map[string]any{"action": "completed", "check_suite": map[string]any{"id": 901, "head_sha": otherHead, "head_branch": "work/aeon-1017", "status": "completed", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}, 204)
	if gap := readStoredRun(t, f, 502, 1); gap.jobsRead || gap.tree != nil {
		t.Fatalf("a run whose jobs could not be read: %+v", gap)
	}
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	wait := metricByKey(t, out.Metrics, "runner_wait")
	if wait.Reason == nil || !strings.Contains(*wait.Reason, "1 run without job facts yet") {
		t.Fatalf("the gap must be named: %+v", wait.Reason)
	}
	wantWindow(t, wait, 30, "partial", 1, f64(1.5), f64(1.5), f64(1.5))
}

// seedBareRuns stores attempt-1 pull-request runs of the CI workflow without job
// facts, newest id first, and marks the backfill finished, as a deployment
// that predates the jobs pass would have it.
func seedBareRuns(t *testing.T, f *fixture, n int) {
	t.Helper()
	runs := make([]metricRun, n)
	for i := range runs {
		completed := f.at.Add(-time.Duration(i+1) * time.Minute)
		runs[i] = metricRun{ID: int64(1000 + n - i), Attempt: 1, Workflow: defaultCIWorkflow, Name: "CI", Event: "pull_request", Branch: fmt.Sprintf("work/b%d", i), Head: strings.Repeat("a", 40),
			Created: completed.Add(-10 * time.Minute), Started: completed.Add(-9 * time.Minute), Completed: completed, Conclusion: "success"}
	}
	f.tx(t, func(tx pgx.Tx) error {
		if err := upsertRunsTx(t.Context(), tx, f.person.TenantID, f.m.config.Repository, "backfill", runs, f.at); err != nil {
			return err
		}
		since := f.at.AddDate(0, 0, -30)
		cursor, err := json.Marshal(backfillCursor{Since: since, Phase: "done", Workflows: fullBackfillWorkflows(&metricSourceRow{CIWorkflow: defaultCIWorkflow, NightlyWorkflow: defaultNightlyWorkflow})})
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_cursor=$2,backfill_since=$3,backfill_done_at=$4 WHERE project_id=$1`,
			f.project, cursor, since, f.at)
		return err
	})
}

func TestDeliveryMetricsJobsPassFillsRunsWithoutJobFacts(t *testing.T) {
	// Risk: the jobs pass reads without a bound, never finishes, lets one run
	// whose jobs GitHub cannot return block all the others, or reports success
	// when GitHub could not be read at all.
	f, g := newMetricsFixture(t)
	seedBareRuns(t, f, 45)
	path := "/api/projects/" + f.project + "/delivery/metrics/backfill"
	newest := int64(1000 + 45) // the first run of the worklist
	broken := map[int64]bool{newest: true}
	g.respond = func(p string) (any, error) {
		var id int64
		if _, err := fmt.Sscanf(p, "/actions/runs/%d/attempts/1/jobs?per_page=100&page=1", &id); err != nil || broken[id] {
			return nil, errRead
		}
		return githubJobsBody(f.at.Add(-time.Hour), 30, nil), nil
	}
	var step BackfillResult
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.State != "running" || step.Phase != "jobs" || step.Calls != jobFillBudget || step.Jobs != jobFillBudget-1 || step.RetryAt == nil || !step.RetryAt.Equal(f.at.Add(time.Minute)) {
		t.Fatalf("first jobs step (40 tried, the newest one broken): %+v", step)
	}
	// The persisted source deadline bounds retries, even with more runs pending.
	callsBeforeRetry := len(g.calls)
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.Phase != "jobs" || step.Jobs != 0 || step.Calls != 0 || step.RetryAt == nil || !step.RetryAt.Equal(f.at.Add(time.Minute)) || len(g.calls) != callsBeforeRetry {
		t.Fatalf("jobs step before deadline: %+v", step)
	}
	f.at = *step.RetryAt
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.Phase != "jobs" || step.Jobs != 5 || step.Calls != 6 || step.RetryAt == nil || !step.RetryAt.Equal(f.at.Add(2*time.Minute)) {
		t.Fatalf("second jobs step: %+v", step)
	}
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.State != "running" || step.Phase != "jobs" || step.Jobs != 0 || step.Calls != 0 || step.RetryAt == nil || !step.RetryAt.Equal(f.at.Add(2*time.Minute)) {
		t.Fatalf("with only the broken run left, before its deadline: %+v", step)
	}
	if s := readStoredRun(t, f, newest, 1); s.jobsRead || !s.failed {
		t.Fatalf("the broken run is marked, not filled: %+v", s)
	}
	// At the exact deadline it is filled once GitHub can answer.
	f.at = *step.RetryAt
	delete(broken, newest)
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.Jobs != 1 {
		t.Fatalf("retried run: %+v", step)
	}
	if s := readStoredRun(t, f, newest, 1); !s.jobsRead || s.failed || s.waitMS == nil || *s.waitMS != 30000 {
		t.Fatalf("retried run facts: %+v", s)
	}
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.State != "done" {
		t.Fatalf("finished: %+v", step)
	}
	calls := len(g.calls)
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if len(g.calls) != calls {
		t.Fatalf("a finished jobs pass read GitHub again")
	}

	// GitHub that cannot be read at all is an error, and no run is marked.
	seedBareRuns(t, f, 3)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_runs SET jobs_read_at=NULL,jobs_failed_at=NULL`)
		return err
	})
	g.respond = func(string) (any, error) { return nil, errRead }
	f.call(t, f.person, "POST", path, map[string]any{}, 502, nil)
	var marked int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_metric_runs WHERE jobs_failed_at IS NOT NULL`).Scan(&marked)
	})
	if marked != 0 {
		t.Fatalf("%d runs marked failed although GitHub was unreachable", marked)
	}

	// Only a person who may manage delivery runs it.
	var reader string
	f.tx(t, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'jobs_reader','jobs_reader') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		for _, permission := range []string{"delivery.read", "nodes.read"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.person.TenantID, role, permission); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Jobs reader') RETURNING id::text`, f.person.TenantID).Scan(&reader); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.person.TenantID, reader, role, f.project)
		return err
	})
	f.call(t, tenant.Principal{ID: reader, TenantID: f.person.TenantID, Kind: tenant.Person}, "POST", path, map[string]any{}, 403, nil)
}

func TestDeliveryMetricsReportsReviewAuditsAndEscapedDefects(t *testing.T) {
	// Risk: an audit or defect without its link or a severity is stored, a
	// correction adds a second row, a rollout incident is not counted as an
	// escaped defect, or the report ends up among the timing marks.
	f, _ := newMetricsFixture(t)
	facts := "/api/projects/" + f.project + "/delivery/metrics/facts"
	fact := func(kind, key, outcome string, extra map[string]any) map[string]any {
		out := map[string]any{"kind": kind, "key": key, "at": f.at.Add(-time.Hour), "outcome": outcome}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	post := func(want int, facts_ ...map[string]any) {
		t.Helper()
		list := make([]any, len(facts_))
		for i, v := range facts_ {
			list[i] = v
		}
		f.call(t, f.person, "POST", facts, map[string]any{"facts": list}, want, nil)
	}
	post(400, fact("review_audit", "audit-1", "medium", nil))                                               // no audited pull request
	post(400, fact("review_audit", "audit-1", "bad", map[string]any{"pull_request": 7}))                    // not a severity
	post(400, fact("escaped_defect", "AEON-9", "high", nil))                                                // not caused by anything
	post(400, fact("escaped_defect", "AEON-9", "critical", map[string]any{"pull_request": 7}))              // not a severity
	post(400, fact("merge_round", "round-1", "model", map[string]any{"pull_request": 7, "release": "126"})) // release belongs to releases and defects
	post(200, fact("review_audit", "audit-1", "medium", map[string]any{"pull_request": 7}),
		fact("escaped_defect", "AEON-9", "low", map[string]any{"release": "126", "started_at": f.at.Add(-3 * time.Hour)}))
	// The same key corrects: re-rated, re-linked, still one row.
	post(200, fact("escaped_defect", "AEON-9", "high", map[string]any{"pull_request": 8}))
	var rows, marks int
	var severity string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT count(*),max(severity) FILTER (WHERE kind='escaped_defect') FROM delivery_metric_reports`).Scan(&rows, &severity); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_metric_marks`).Scan(&marks)
	})
	if rows != 2 || severity != "high" || marks != 0 {
		t.Fatalf("reports=%d severity=%s marks=%d", rows, severity, marks)
	}
	// A rollout incident of a release is the production side of a defect.
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/flow/rollout", release126(f, f.at.Add(-50*time.Minute), nil), 200, nil)

	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	audits := metricByKey(t, out.Metrics, "review_audits")
	wantWindow(t, audits, 7, "partial", 1, f64(1), nil, nil)
	wantCounts(t, audits, 7, map[string]int{"medium": 1, "high": 0})
	defects := metricByKey(t, out.Metrics, "escaped_defects")
	wantWindow(t, defects, 7, "partial", 2, f64(2), nil, nil)
	wantCounts(t, defects, 7, map[string]int{"high": 1, "medium": 1, "low": 0})
}

func TestReadJobFactsKeepsEveryConclusionAndTheWorstName(t *testing.T) {
	// Risk: a job that is not in the requesting project's required checks is
	// dropped, or a failed matrix leg is hidden by an earlier success of the
	// same name.
	created := time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC)
	jobs := []any{
		map[string]any{"name": "go", "conclusion": "success", "created_at": created, "started_at": created.Add(time.Second)},
		map[string]any{"name": "go", "conclusion": "failure", "created_at": created, "started_at": created.Add(2 * time.Second)},
		map[string]any{"name": "lint", "conclusion": "success", "created_at": created, "started_at": created.Add(time.Second)},
	}
	body := map[string]any{"total_count": len(jobs), "jobs": jobs}
	get := func(string, any) error { return nil }
	get = func(_ string, out any) error {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
	facts, err := readJobFacts(get, 1, 1)
	if err != nil || !facts.Complete || facts.Conclusions["go"] != "failure" || facts.Conclusions["lint"] != "success" || len(facts.Conclusions) != 2 {
		t.Fatalf("%+v %v", facts, err)
	}
	jobs[0].(map[string]any)["conclusion"] = strings.Repeat("x", 33)
	if _, err = readJobFacts(get, 1, 1); err == nil {
		t.Fatal("a conclusion past the bound was accepted")
	}
	jobs[0].(map[string]any)["conclusion"] = "failure"
	jobs[0].(map[string]any)["name"] = strings.Repeat("n", jobNameLimit+1)
	if _, err = readJobFacts(get, 1, 1); err == nil {
		t.Fatal("a job name past the bound was accepted")
	}
}

func TestDeliveryCompleteJobMapServesEveryProject(t *testing.T) {
	// Risk: the jobs pass stores only the requesting project's required checks,
	// so another project, or a check added later, can never see a job that was
	// already read.
	f, g := newMetricsFixture(t)
	head := strings.Repeat("d", 40)
	created := f.at.Add(-2 * time.Hour)
	suite := map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": head, "head_branch": "work/shared", "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{}}}
	run := githubRunBody(501, 1, defaultCIWorkflow, "pull_request", "work/shared", head, created, 10*time.Minute, "success", 900)
	jobs := githubJobsBody(created, 30, nil)
	list := jobs["jobs"].([]any)
	list = append(list, map[string]any{"name": "lint", "conclusion": "failure", "created_at": created, "started_at": created.Add(time.Second)})
	jobs["jobs"], jobs["total_count"] = list, len(list)
	g.respond = func(path string) (any, error) {
		switch path {
		case "/actions/runs?check_suite_id=900&head_sha=" + head + "&per_page=2":
			return map[string]any{"total_count": 1, "workflow_runs": []any{run}}, nil
		case "/actions/runs/501/attempts/1/jobs?per_page=100&page=1":
			return jobs, nil
		}
		return nil, errRead
	}
	auditSend(t, f, "check_suite", "shared-jobs", suite, 204)
	var raw []byte
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT required_jobs FROM delivery_metric_runs WHERE run_id=501 AND attempt=1`).Scan(&raw)
	})
	var stored storedJobFacts
	if err := json.Unmarshal(raw, &stored); err != nil || !stored.Complete || stored.Conclusions["lint"] != "failure" || stored.Conclusions["go"] != "success" {
		t.Fatalf("stored map: %s %v", raw, err)
	}
	var other string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'AEON-2',id,'Second delivery project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&other)
	})
	f.call(t, f.person, "PUT", "/api/projects/"+other+"/delivery/metrics/source", map[string]any{"repository": f.m.config.Repository}, 200, nil)
	f.call(t, f.person, "PUT", "/api/projects/"+other+"/delivery-settings", map[string]any{"required_checks": []string{"lint"}}, 200, nil)
	calls := len(g.calls)
	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+other+"/delivery/metrics", nil, 200, &out)
	wantWindow(t, metricByKey(t, out.Metrics, "required_checks_green"), 30, "partial", 1, f64(0), nil, nil)
	if len(g.calls) != calls {
		t.Fatal("the second project re-read jobs that were already stored")
	}
	// A check added later is missing when no job had that name, never unknown.
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery-settings", map[string]any{"required_checks": []string{"not-a-job"}}, 200, nil)
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	added := metricByKey(t, out.Metrics, "required_checks_green")
	wantWindow(t, added, 30, "partial", 1, f64(0), nil, nil)
	if added.Reason != nil && strings.Contains(*added.Reason, "without job facts") {
		t.Fatalf("an absent name on a complete map is missing, not unread: %s", *added.Reason)
	}
}

func TestDeliveryLegacyJobMapIsRefreshedOnce(t *testing.T) {
	// Risk: a row written as a flat list of one project's checks is treated as
	// finished, so a check it does not name stays unknown forever.
	f, g := newMetricsFixture(t)
	seedBareRuns(t, f, 1)
	const id = int64(1001)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_runs SET jobs_read_at=$2,required_jobs='{"go":"success"}'::jsonb WHERE run_id=$1 AND attempt=1`, id, f.at)
		return err
	})
	g.respond = func(p string) (any, error) {
		if p != "/actions/runs/1001/attempts/1/jobs?per_page=100&page=1" {
			return nil, errRead
		}
		body := githubJobsBody(f.at.Add(-time.Hour), 30, nil)
		list := body["jobs"].([]any)
		list = append(list, map[string]any{"name": "special", "conclusion": "failure", "created_at": f.at.Add(-time.Hour), "started_at": f.at.Add(-time.Hour).Add(time.Second)})
		body["jobs"], body["total_count"] = list, len(list)
		return body, nil
	}
	var step BackfillResult
	path := "/api/projects/" + f.project + "/delivery/metrics/backfill"
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.Jobs != 1 {
		t.Fatalf("legacy map was not read again: %+v", step)
	}
	var raw []byte
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT required_jobs FROM delivery_metric_runs WHERE run_id=$1 AND attempt=1`, id).Scan(&raw)
	})
	var stored storedJobFacts
	if err := json.Unmarshal(raw, &stored); err != nil || !stored.Complete || stored.Conclusions["special"] != "failure" || stored.Conclusions["go"] != "success" {
		t.Fatalf("refreshed map: %s %v", raw, err)
	}
	calls := len(g.calls)
	f.call(t, f.person, "POST", path, map[string]any{}, 200, &step)
	if step.State != "done" || len(g.calls) != calls {
		t.Fatalf("a complete map was read again: %+v calls %d→%d", step, calls, len(g.calls))
	}
}

func TestDeliveryPreflightCatchupListsOnlyPreflight(t *testing.T) {
	// Risk: a finished CI backfill reports preflight as fully covered, or the
	// catch-up reads CI and pulls a second time.
	f, g := newMetricsFixture(t)
	since := f.at.AddDate(0, 0, -30)
	cursor, err := json.Marshal(backfillCursor{Since: since, Phase: "done", Workflows: []string{defaultCIWorkflow, defaultNightlyWorkflow}})
	if err != nil {
		t.Fatal(err)
	}
	created := f.at.Add(-3 * time.Hour)
	run := metricRun{ID: 700, Attempt: 1, Workflow: defaultPreflightWorkflow, Name: "preflight", Event: "workflow_dispatch", Branch: "main", Head: strings.Repeat("b", 40),
		Created: created, Started: created.Add(time.Minute), Completed: created.Add(6 * time.Minute), Conclusion: "failure"}
	f.tx(t, func(tx pgx.Tx) error {
		if err := upsertRunsTx(t.Context(), tx, f.person.TenantID, f.m.config.Repository, "webhook", []metricRun{run}, f.at); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_cursor=$2,backfill_since=$3,backfill_done_at=$4 WHERE project_id=$1`, f.project, cursor, since, f.at)
		return err
	})
	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	before := metricByKey(t, out.Metrics, "preflight_red_rate")
	wantWindow(t, before, 30, "partial", 1, f64(100), nil, nil)
	if before.Windows[1].Coverage.Full {
		t.Fatal("preflight coverage followed the CI backfill")
	}
	g.respond = func(p string) (any, error) {
		if strings.Contains(p, "ci-preflight.yml") {
			return map[string]any{"total_count": 0, "workflow_runs": []any{}}, nil
		}
		return nil, fmt.Errorf("unexpected GitHub read %s", p)
	}
	var step BackfillResult
	backfill := "/api/projects/" + f.project + "/delivery/metrics/backfill"
	for i := 0; i < 4 && step.Phase != "jobs" && step.Phase != "done"; i++ {
		f.call(t, f.person, "POST", backfill, map[string]any{}, 200, &step)
	}
	sawPreflight, sawCI := false, false
	for _, call := range g.calls {
		if strings.Contains(call, "ci-preflight.yml") {
			sawPreflight = true
		}
		if strings.Contains(call, "/actions/workflows/ci.yml") || strings.Contains(call, "/pulls?") {
			sawCI = true
		}
	}
	if !sawPreflight || sawCI {
		t.Fatalf("catch-up calls: %v", g.calls)
	}
	for i := 0; step.State != "done" && i < 4; i++ {
		f.call(t, f.person, "POST", backfill, map[string]any{}, 200, &step)
	}
	if step.State != "done" {
		t.Fatalf("catch-up did not finish: %+v", step)
	}
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	wantWindow(t, metricByKey(t, out.Metrics, "preflight_red_rate"), 30, "ok", 1, f64(100), nil, nil)
}

func TestDeliveryBackfillSkipsConfirmedAbsentPreflight(t *testing.T) {
	// Risk: a repository with no ci-preflight.yml returns 502 forever, or the
	// finished backfill reports that missing workflow as fully covered.
	f, g := newMetricsFixture(t)
	runAt := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	g.respond = func(p string) (any, error) {
		switch {
		case strings.Contains(p, "ci-preflight.yml"):
			return nil, crossreview.ErrNotFound
		case strings.Contains(p, "/actions/workflows/ci.yml/runs") && strings.Contains(p, "2026-10-06"):
			return map[string]any{"total_count": 1, "workflow_runs": []any{
				githubRunBody(610, 1, defaultCIWorkflow, "pull_request", "work/a", strings.Repeat("a", 40), runAt, 8*time.Minute, "success", 0),
			}}, nil
		case strings.HasPrefix(p, "/actions/workflows/"):
			return map[string]any{"total_count": 0, "workflow_runs": []any{}}, nil
		case strings.Contains(p, "/jobs?"):
			return githubJobsBody(runAt, 30, nil), nil
		case p == "":
			return map[string]any{"full_name": f.m.config.Repository, "default_branch": "main"}, nil
		case strings.Contains(p, "/pulls?"):
			return []any{}, nil
		default:
			return nil, fmt.Errorf("unexpected GitHub read %s", p)
		}
	}
	var step BackfillResult
	backfill := "/api/projects/" + f.project + "/delivery/metrics/backfill"
	body := map[string]any{"days": 1}
	jobs := 0
	for i := 0; i < 6 && step.State != "done"; i++ {
		f.call(t, f.person, "POST", backfill, body, 200, &step)
		body = map[string]any{}
		jobs += step.Jobs
	}
	if step.State != "done" || jobs < 1 {
		t.Fatalf("backfill without preflight: %+v jobs %d", step, jobs)
	}
	var preflightRuns int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_metric_runs WHERE repository=$1 AND workflow_path LIKE '%ci-preflight.yml'`, f.m.config.Repository).Scan(&preflightRuns)
	})
	if preflightRuns != 0 {
		t.Fatalf("stored %d runs of a workflow GitHub says is absent", preflightRuns)
	}
	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	preflight := metricByKey(t, out.Metrics, "preflight_red_rate")
	if metricWindow(t, preflight, 30).Coverage.Full || preflight.Status == "ok" {
		t.Fatalf("absent preflight looks covered: status %s coverage %+v", preflight.Status, metricWindow(t, preflight, 30).Coverage)
	}
	before := len(g.calls)
	f.call(t, f.person, "POST", backfill, map[string]any{}, 200, &step)
	for _, call := range g.calls[before:] {
		if strings.Contains(call, "ci-preflight.yml") {
			t.Fatalf("a later step asked for the absent workflow again: %v", g.calls[before:])
		}
	}
}

func TestDeliveryLegacyCatchupLeavesAbsentPreflightUncovered(t *testing.T) {
	// Risk: a finished CI backfill either blocks on the missing preflight
	// workflow or, after one 404, starts the catch-up again on every call.
	f, g := newMetricsFixture(t)
	since := f.at.AddDate(0, 0, -30)
	cursor, err := json.Marshal(backfillCursor{Since: since, Phase: "done", Workflows: []string{defaultCIWorkflow, defaultNightlyWorkflow}})
	if err != nil {
		t.Fatal(err)
	}
	created := f.at.Add(-3 * time.Hour)
	run := metricRun{ID: 710, Attempt: 1, Workflow: defaultPreflightWorkflow, Name: "preflight", Event: "workflow_dispatch", Branch: "main", Head: strings.Repeat("c", 40),
		Created: created, Started: created.Add(time.Minute), Completed: created.Add(6 * time.Minute), Conclusion: "failure"}
	f.tx(t, func(tx pgx.Tx) error {
		if err := upsertRunsTx(t.Context(), tx, f.person.TenantID, f.m.config.Repository, "webhook", []metricRun{run}, f.at); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET backfill_cursor=$2,backfill_since=$3,backfill_done_at=$4 WHERE project_id=$1`, f.project, cursor, since, f.at)
		return err
	})
	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	before := metricByKey(t, out.Metrics, "preflight_red_rate")
	wantWindow(t, before, 30, "partial", 1, f64(100), nil, nil)
	if before.Windows[1].Coverage.Full {
		t.Fatal("preflight coverage followed the CI backfill")
	}
	g.respond = func(p string) (any, error) {
		if strings.Contains(p, "ci-preflight.yml") {
			return nil, crossreview.ErrNotFound
		}
		return nil, fmt.Errorf("unexpected GitHub read %s", p)
	}
	var step BackfillResult
	backfill := "/api/projects/" + f.project + "/delivery/metrics/backfill"
	for i := 0; i < 4 && step.State != "done"; i++ {
		f.call(t, f.person, "POST", backfill, map[string]any{}, 200, &step)
	}
	sawPreflight, sawCI := false, false
	for _, call := range g.calls {
		if strings.Contains(call, "ci-preflight.yml") {
			sawPreflight = true
		}
		if strings.Contains(call, "/actions/workflows/ci.yml") || strings.Contains(call, "/pulls?") {
			sawCI = true
		}
	}
	if !sawPreflight || sawCI || step.State != "done" {
		t.Fatalf("absent catch-up: state %s calls %v", step.State, g.calls)
	}
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	after := metricByKey(t, out.Metrics, "preflight_red_rate")
	wantWindow(t, after, 30, "partial", 1, f64(100), nil, nil)
	if metricWindow(t, after, 30).Coverage.Full {
		t.Fatal("absent preflight became fully covered")
	}
	calls := len(g.calls)
	f.call(t, f.person, "POST", backfill, map[string]any{}, 200, &step)
	if step.State != "done" || len(g.calls) != calls {
		t.Fatalf("catch-up restarted: %+v calls %v", step, g.calls[calls:])
	}
}
