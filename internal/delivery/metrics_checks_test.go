// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func historySuiteBody(id int64, head, branch string, at time.Time, nightly bool) map[string]any {
	pulls := []any{}
	if branch != "main" {
		pulls = append(pulls, map[string]any{"number": 7})
	}
	return map[string]any{"id": id, "head_sha": head, "head_branch": branch, "created_at": at, "status": "completed", "app": map[string]string{"slug": "github-actions"}, "pull_requests": pulls}
}

func historyChecksBody(id int64, head string, at time.Time, nightly bool) map[string]any {
	checks := []any{}
	for i, name := range defaultRequiredChecks {
		if nightly {
			name = "nightly-" + name
		}
		checks = append(checks, map[string]any{"id": id*10 + int64(i), "head_sha": head, "name": name, "status": "completed", "conclusion": "success", "started_at": at.Add(time.Duration(i) * time.Minute), "completed_at": at.Add(time.Duration(i+2) * time.Minute), "check_suite": map[string]any{"id": id}, "app": map[string]string{"slug": "github-actions"}})
	}
	return map[string]any{"total_count": len(checks), "check_runs": checks}
}

// A reachable main snapshot, one PR head, one queue head and one nightly suite.
// There are enough heads to force persisted continuation across read budgets.
func historyChecksFixture(t *testing.T, repository string, now time.Time, mainHeads int) func(string) (any, error) {
	t.Helper()
	at := now.Add(-27 * time.Hour)
	head := func(n int64) string { return fmt.Sprintf("%040x", n) }
	return func(path string) (any, error) {
		if strings.HasPrefix(path, "/actions/") {
			t.Fatal("least privilege history called Actions")
		}
		u, err := url.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case path == "":
			return map[string]string{"full_name": repository, "default_branch": "main"}, nil
		case u.Path == "/commits":
			if u.Query().Get("page") != "1" {
				t.Fatal("unexpected main page")
			}
			commits := []any{}
			for i := 1; i <= mainHeads; i++ {
				commits = append(commits, map[string]any{"sha": head(int64(i)), "commit": map[string]any{"tree": map[string]string{"sha": head(800)}}})
			}
			return commits, nil
		case strings.HasSuffix(u.Path, "/check-suites"):
			parts := strings.Split(u.Path, "/")
			n, err := strconv.ParseInt(parts[2], 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			branch := "main"
			if n == 101 {
				branch = "work/history"
			}
			if n == 102 {
				branch = "gh-readonly-queue/main/pr-7-" + head(101)
			}
			suites := []any{historySuiteBody(n, head(n), branch, at, false)}
			if n == 1 {
				suites = append(suites, historySuiteBody(201, head(n), branch, at, true))
			}
			return map[string]any{"total_count": len(suites), "check_suites": suites}, nil
		case strings.HasPrefix(u.Path, "/check-suites/") && strings.HasSuffix(u.Path, "/check-runs"):
			parts := strings.Split(u.Path, "/")
			n, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			h := head(n)
			if n == 201 {
				h = head(1)
			}
			return historyChecksBody(n, h, at, n == 201), nil
		case strings.HasPrefix(u.Path, "/git/matching-refs/"):
			return []any{map[string]any{"ref": "refs/heads/gh-readonly-queue/main/pr-7-" + head(101), "object": map[string]string{"sha": head(102)}}}, nil
		case u.Path == "/pulls":
			return []any{map[string]any{"number": 7, "created_at": at.Add(-time.Hour), "updated_at": at.Add(time.Hour), "merged_at": at.Add(time.Hour), "closed_at": at.Add(time.Hour), "head": map[string]string{"ref": "work/history", "sha": head(101)}, "base": map[string]any{"ref": "main", "repo": map[string]string{"full_name": repository}}}}, nil
		default:
			return nil, fmt.Errorf("unexpected fixture read %s", path)
		}
	}
}

func TestDeliveryChecksHistoryResumesAndReportsHonestMetrics(t *testing.T) {
	// Risk: resumable checks history silently loses heads, calls Actions, invents
	// first attempts/runner waits, reports full coverage, or double-counts facts.
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	respond := historyChecksFixture(t, "example/delivery", now, 60)
	cursor := backfillCursor{Since: dayStart(now.AddDate(0, 0, -90)), Phase: "runs", Page: 1}
	var runs []metricRun
	var pulls []metricPull
	steps := 0
	for ; steps < 10; steps++ {
		calls := 0
		get := func(path string, out any) error {
			calls++
			body, err := respond(path)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(body)
			return json.Unmarshal(raw, out)
		}
		next, batch, err := checksBackfillStep(get, "example/delivery", defaultCIWorkflow, defaultNightlyWorkflow, cursor, now)
		if err != nil {
			t.Fatal(err)
		}
		if calls != batch.Calls || calls > backfillCallBudget {
			t.Fatalf("unbounded reads: %d / %d", calls, batch.Calls)
		}
		runs = append(runs, batch.Runs...)
		pulls = append(pulls, batch.Pulls...)
		raw, _ := json.Marshal(next)
		if err := json.Unmarshal(raw, &cursor); err != nil {
			t.Fatal(err)
		}
		if cursor.Phase == "done" {
			break
		}
	}
	if steps < 1 || cursor.Phase != "done" || !cursor.Truncated || cursor.ReadSource != historyChecks || len(runs) != 63 || len(pulls) != 1 {
		t.Fatalf("history: steps=%d cursor=%+v runs=%d pulls=%d", steps, cursor, len(runs), len(pulls))
	}
	metrics := computeMetrics(metricInput{ChecksHistory: true, Runs: runs, Pulls: pulls, CIWorkflow: defaultCIWorkflow, NightlyWorkflow: defaultNightlyWorkflow, Truncated: true}, now)
	for _, m := range metrics {
		if m.Key == "pr_ci_wall" || m.Key == "queue_run_wall" || m.Key == "nightly_green" {
			if m.Status != "partial" || m.Windows[0].N != 1 || m.Windows[0].Value == nil || m.Windows[0].Coverage.Full || !strings.Contains(m.Source, "checks + contents") {
				t.Fatalf("observation reported as exact: %+v", m)
			}
			if m.Key != "nightly_green" && *m.Windows[0].Value != 6 {
				t.Fatal("wrong observed check span")
			}
		}
		if m.Key == "first_attempt_green" || m.Key == "required_checks_green" || m.Key == "runner_wait" || m.Key == "queue_runs_per_pr" {
			if m.Status != "no_data" || m.Latest != nil || m.Windows[0].N != 0 || m.Windows[0].Value != nil {
				t.Fatalf("invented metric: %+v", m)
			}
		}
		if m.Key == "pr_open_to_merged" && (m.Windows[0].N != 1 || m.Windows[0].Value == nil || *m.Windows[0].Value != 120) {
			t.Fatal("PR timing was not retained")
		}
	}
	derived := runs[0]
	exact := derived
	exact.ID = 900
	exact.Workflow = "ci.yml"
	exact.Name = "CI"
	exact.Started = exact.Started.Add(time.Minute)
	preferred := preferredMetricRuns([]metricRun{derived, exact})
	if len(preferred) != 1 || preferred[0].ID != exact.ID {
		t.Fatal("Actions did not supersede the derived sample")
	}
}

func TestDeliveryChecksSuiteRejectsMixedIdentityAndBoundedLists(t *testing.T) {
	// Risk: another head/app or a truncated/ambiguous listing becomes a green.
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	head := strings.Repeat("a", 40)
	var suite metricCheckSuite
	raw, _ := json.Marshal(historySuiteBody(7, head, "work/checks", now.Add(-time.Hour), false))
	_ = json.Unmarshal(raw, &suite)
	for _, tc := range []string{"wrong head", "wrong suite", "other app", "oversized", "unknown names", "ambiguous", "incomplete"} {
		t.Run(tc, func(t *testing.T) {
			body := historyChecksBody(7, head, now.Add(-time.Hour), false)
			checks := body["check_runs"].([]any)
			switch tc {
			case "wrong head":
				checks[0].(map[string]any)["head_sha"] = strings.Repeat("b", 40)
			case "wrong suite":
				checks[0].(map[string]any)["check_suite"] = map[string]any{"id": 8}
			case "other app":
				checks[0].(map[string]any)["app"] = map[string]string{"slug": "untrusted"}
			case "oversized":
				body["total_count"] = 501
			case "unknown names":
				checks[0].(map[string]any)["name"] = "unknown"
			case "ambiguous":
				extra := historyChecksBody(7, head, now.Add(-time.Hour), true)["check_runs"].([]any)
				body["check_runs"] = append(checks, extra...)
				body["total_count"] = 10
			case "incomplete":
				checks[0].(map[string]any)["status"] = "in_progress"
			}
			runs, truncated, err := suiteCheckRuns(func(_ string, out any) error { raw, _ := json.Marshal(body); return json.Unmarshal(raw, out) }, suite, defaultCIWorkflow, defaultNightlyWorkflow, "main", now)
			if len(runs) != 0 {
				t.Fatal("unsafe check suite became a fact")
			}
			if tc == "oversized" {
				if err != nil || !truncated {
					t.Fatal("bound was not reported")
				}
			} else if tc == "unknown names" || tc == "ambiguous" || tc == "incomplete" {
				if err != nil || truncated {
					t.Fatal("unknown workflow guessed")
				}
			} else if err != errRead {
				t.Fatal("wrong refusal reason")
			}
		})
	}
}

func TestDeliveryChecksHistoryPersistsPacingAndUsesActionsAfterGrant(t *testing.T) {
	// Risk: server continuation hot-loops, reports the wrong source, or reuses a
	// checks cursor after an Actions grant. This exercises storage and the API.
	f, g := newMetricsFixture(t)
	g.source = historyChecks
	g.respond = historyChecksFixture(t, f.m.config.Repository, f.at, 60)
	var first BackfillResult
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{"restart": true, "days": 90}, 200, &first)
	if first.State != "running" || first.RetryAt == nil || !first.RetryAt.Equal(f.at.Add(time.Minute)) || first.Calls > 40 || first.Day != nil {
		t.Fatalf("pacing: %+v", first)
	}
	before := len(g.calls)
	var paused BackfillResult
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{}, 200, &paused)
	if len(g.calls) != before || paused.RetryAt == nil {
		t.Fatal("pacing ignored")
	}
	for i := 0; i < 10 && paused.State != "done"; i++ {
		f.at = f.at.Add(time.Minute)
		f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/backfill", map[string]any{}, 200, &paused)
	}
	var view DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &view)
	if paused.State != "done" || view.Source.HistorySource == nil || *view.Source.HistorySource != historyChecks || view.Source.CoveredSince != nil || view.Source.LastReadError != nil {
		t.Fatalf("checks source: %+v", view.Source)
	}
	g.source = historyActions
	g.respond = func(path string) (any, error) {
		if strings.HasPrefix(path, "/actions/workflows/") {
			return map[string]any{"total_count": 0, "workflow_runs": []any{}}, nil
		}
		if path == "" {
			return map[string]string{"full_name": f.m.config.Repository, "default_branch": "main"}, nil
		}
		if strings.HasPrefix(path, "/pulls?") {
			return []any{}, nil
		}
		return nil, fmt.Errorf("unexpected Actions continuation %s", path)
	}
	var changed BackfillResult
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/check-again", nil, 200, &changed)
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &view)
	if changed.State != "running" || view.Source.HistorySource == nil || *view.Source.HistorySource != historyActions || view.Source.BackfillDoneAt != nil {
		t.Fatal("grant change did not restart Actions history")
	}
}
