// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func historyWorkflowBody(workflow, prefix string) map[string]any {
	content := "name: History fixture\njobs:\n"
	for _, name := range defaultRequiredChecks {
		content += "  " + prefix + name + ":\n    runs-on: ubuntu-latest\n    steps: []\n"
	}
	return map[string]any{"type": "file", "path": workflow, "encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))}
}

func historyWorkflowFixture(u *url.URL) (any, bool) {
	switch u.Path {
	case "/contents/.github/workflows":
		return []any{map[string]any{"type": "file", "path": defaultCIWorkflow, "sha": strings.Repeat("c", 40)}, map[string]any{"type": "file", "path": defaultNightlyWorkflow, "sha": strings.Repeat("d", 40)}}, true
	case "/contents/" + defaultCIWorkflow:
		return historyWorkflowBody(defaultCIWorkflow, ""), true
	case "/contents/" + defaultNightlyWorkflow:
		return historyWorkflowBody(defaultNightlyWorkflow, "nightly-"), true
	}
	return nil, false
}

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
		if body, ok := historyWorkflowFixture(u); ok {
			return body, nil
		}
		switch {
		case path == "":
			return map[string]string{"full_name": repository, "default_branch": "main"}, nil
		case u.Path == "/commits/main":
			return map[string]any{"sha": head(1), "commit": map[string]any{"tree": map[string]string{"sha": head(800)}}}, nil
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

func TestDeliveryChecksHistoryReadsRecentNightsOnOldDefaultHead(t *testing.T) {
	// Risk: a quiet repository's head is older than the commit-date window,
	// even though scheduled suites on that same head ran inside the window.
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	head, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	since := dayStart(now.AddDate(0, 0, -90))
	oldCommit := since.Add(-30 * 24 * time.Hour)
	pinned, reads := 0, 0
	get := func(endpoint string, out any) error {
		reads++
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if body, ok := historyWorkflowFixture(u); ok {
			return writeMetricJSON(out, body)
		}
		switch u.Path {
		case "":
			return writeMetricJSON(out, map[string]string{"full_name": "example/delivery", "default_branch": "main"})
		case "/commits/main":
			pinned++
			return writeMetricJSON(out, map[string]any{"sha": head, "commit": map[string]any{"tree": map[string]string{"sha": tree}, "committer": map[string]any{"date": oldCommit}}})
		case "/commits":
			// The timestamp-filtered commit listing has no recent commits.
			return writeMetricJSON(out, []any{})
		case "/commits/" + head + "/check-suites":
			return writeMetricJSON(out, map[string]any{"total_count": 4, "check_suites": []any{
				historySuiteBody(201, head, "main", now.Add(-48*time.Hour), true),
				historySuiteBody(202, head, "main", now.Add(-24*time.Hour), true),
				historySuiteBody(203, head, "main", since.Add(-time.Hour), true),
				historySuiteBody(204, head, "main", now.Add(time.Hour), true),
			}})
		case "/check-suites/201/check-runs", "/check-suites/202/check-runs":
			id := int64(201)
			at := now.Add(-48 * time.Hour)
			if strings.Contains(u.Path, "/202/") {
				id, at = 202, now.Add(-24*time.Hour)
			}
			return writeMetricJSON(out, historyChecksBody(id, head, at, true))
		case "/git/matching-refs/heads/gh-readonly-queue/main/", "/pulls":
			return writeMetricJSON(out, []any{})
		default:
			return fmt.Errorf("unexpected quiet repository read %s", endpoint)
		}
	}
	cursor := backfillCursor{Since: since, Phase: "runs", Page: 1}
	var runs []metricRun
	for step := 0; cursor.Phase != "done" && step < 10; step++ {
		before := reads
		next, batch, err := checksBackfillStep(get, "example/delivery", defaultCIWorkflow, defaultNightlyWorkflow, cursor, now)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Calls != reads-before || batch.Calls > backfillCallBudget {
			t.Fatalf("unbounded history step: %+v", batch)
		}
		runs = append(runs, batch.Runs...)
		if err := writeMetricJSON(&cursor, next); err != nil {
			t.Fatal(err)
		}
	}
	if cursor.Phase != "done" || len(runs) != 2 {
		t.Fatalf("old head lost recent nightly suites: phase=%s runs=%+v", cursor.Phase, runs)
	}
	if pinned != 1 || cursor.Checks.Anchor != head {
		t.Fatalf("default head was not pinned once: reads=%d cursor=%+v", pinned, cursor.Checks)
	}
	for _, run := range runs {
		if run.ID != -201 && run.ID != -202 || run.Head != head || run.HeadTree != tree {
			t.Fatalf("wrong suite identity or timestamp window: %+v", run)
		}
	}
	metrics := computeMetrics(metricInput{ChecksHistory: true, Runs: runs, CIWorkflow: defaultCIWorkflow, NightlyWorkflow: defaultNightlyWorkflow}, now)
	wantWindow(t, metricByKey(t, metrics, "nightly_green"), 7, "partial", 2, f64(100), nil, nil)
}

func TestDeliveryChecksHistoryActionsReplacesOnlyItsStoredSuite(t *testing.T) {
	// Risk: enabling Actions erases distinct nightly observations on one SHA,
	// or the corresponding suite identity is lost during a database round trip.
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	head := strings.Repeat("a", 40)
	first := now.Add(-48 * time.Hour)
	second := now.Add(-24 * time.Hour)
	checks := []metricRun{
		{ID: -901, Attempt: 1, Workflow: defaultNightlyWorkflow, Name: "Nightly", Event: "push", Branch: "main", Head: head, Created: first, Started: first, Completed: first.Add(time.Minute), Conclusion: "failure"},
		{ID: -902, Attempt: 1, Workflow: defaultNightlyWorkflow, Name: "Nightly", Event: "push", Branch: "main", Head: head, Created: second, Started: second, Completed: second.Add(time.Minute), Conclusion: "success"},
	}
	var body githubRun
	if err := writeMetricJSON(&body, githubRunBody(500, 1, defaultNightlyWorkflow, "schedule", "main", head, second, time.Minute, "success", 902)); err != nil {
		t.Fatal(err)
	}
	exact, ok, err := metricRunFrom(body)
	if err != nil || !ok {
		t.Fatalf("invalid exact fixture: %+v %v", exact, err)
	}
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprintf("verified-suite-%t", known), func(t *testing.T) {
			f, _ := newMetricsFixture(t)
			if !known {
				body.Suite = 0
				exact, ok, err = metricRunFrom(body)
				if err != nil || !ok {
					t.Fatal("invalid Actions fixture without a suite")
				}
			}
			var stored metricInput
			f.tx(t, func(tx pgx.Tx) error {
				if err := upsertRunsTx(t.Context(), tx, f.person.TenantID, f.m.config.Repository, "backfill", append(append([]metricRun{}, checks...), exact), f.at); err != nil {
					return err
				}
				var err error
				stored, err = loadMetricInputTx(t.Context(), tx, metricSourceRow{Repository: f.m.config.Repository, CIWorkflow: defaultCIWorkflow, NightlyWorkflow: defaultNightlyWorkflow}, f.at)
				return err
			})
			preferred := preferredMetricRuns(stored.Runs)
			ids := map[int64]bool{}
			for _, run := range preferred {
				ids[run.ID] = true
			}
			want := 3
			if known {
				want = 2
			}
			if len(preferred) != want || !ids[-901] || !ids[500] || ids[-902] == known {
				t.Fatalf("Actions erased unrelated nights or retained its own suite: known=%t runs=%+v", known, preferred)
			}
			if known {
				stored.Runs = preferred
				wantWindow(t, metricByKey(t, computeMetrics(stored, f.at), "nightly_green"), 7, "partial", 2, f64(50), nil, nil)
			}
		})
	}
}

func TestDeliveryChecksHistorySharedWorkflowIgnoresProjectLabels(t *testing.T) {
	// Risk: a second project's workflow settings, or the first project's
	// webhook configuration, relabel the repository's shared suite facts.
	f, g := newMetricsFixture(t)
	g.source = historyChecks
	fixture := historyChecksFixture(t, f.m.config.Repository, f.at, 1)
	g.respond = func(endpoint string) (any, error) {
		if endpoint == "/check-suites/201" {
			return historySuiteBody(201, fmt.Sprintf("%040x", 1), "main", f.at.Add(-27*time.Hour), true), nil
		}
		return fixture(endpoint)
	}
	var other string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'AEON-2',id,'Second history project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&other)
	})
	customCI, customNightly := ".github/workflows/custom-ci.yml", ".github/workflows/custom-nightly.yml"
	f.call(t, f.person, "PUT", "/api/projects/"+other+"/delivery/metrics/source", map[string]any{"repository": f.m.config.Repository, "ci_workflow": customCI, "nightly_workflow": customNightly}, 200, nil)
	assertShared := func() {
		t.Helper()
		for id, workflow := range map[int64]string{-1: defaultCIWorkflow, -201: defaultNightlyWorkflow} {
			var stored string
			f.tx(t, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT workflow_path FROM delivery_metric_runs WHERE run_id=$1`, id).Scan(&stored)
			})
			if stored != workflow {
				t.Fatalf("project configuration relabeled shared suite %d: %s != %s", id, stored, workflow)
			}
		}
		var out DeliveryMetrics
		f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
		wantWindow(t, metricByKey(t, out.Metrics, "nightly_green"), 7, "partial", 1, f64(100), nil, nil)
		f.call(t, f.person, "GET", "/api/projects/"+other+"/delivery/metrics", nil, 200, &out)
		wantWindow(t, metricByKey(t, out.Metrics, "nightly_green"), 7, "no_data", 0, nil, nil, nil)
	}
	for _, project := range []string{f.project, other} {
		var result BackfillResult
		f.call(t, f.person, "POST", "/api/projects/"+project+"/delivery/metrics/backfill", map[string]any{"restart": true, "days": 90}, 200, &result)
		for i := 0; result.State == "running" && i < 10; i++ {
			f.at = f.at.Add(time.Minute)
			f.call(t, f.person, "POST", "/api/projects/"+project+"/delivery/metrics/backfill", map[string]any{}, 200, &result)
		}
		if result.State != "done" {
			t.Fatalf("history did not finish: %+v", result)
		}
		assertShared()
	}
	// Both configured sources are custom before ingress chooses its scope.
	// Repository contents still determine the shared nightly identity.
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery/metrics/source", map[string]any{"repository": f.m.config.Repository, "ci_workflow": customCI, "nightly_workflow": customNightly}, 200, nil)
	auditSend(t, f, "check_suite", "shared-checks-nightly", map[string]any{"action": "completed", "check_suite": historySuiteBody(201, fmt.Sprintf("%040x", 1), "main", f.at.Add(-27*time.Hour), true)}, 204)
	var stored string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT workflow_path FROM delivery_metric_runs WHERE run_id=-201`).Scan(&stored)
	})
	if stored != defaultNightlyWorkflow {
		t.Fatalf("webhook used a project's label for shared suite: %s", stored)
	}
}

func TestDeliveryChecksWorkflowIdentityWithholdsAmbiguityAndCachesBlobs(t *testing.T) {
	// Risk: workflow inference guesses across duplicate definitions, reads
	// unbounded content, reuses changed definitions, or exhausts a step budget.
	head := strings.Repeat("a", 40)
	otherHead := strings.Repeat("b", 40)
	otherNightly := ".github/workflows/other-nightly.yml"
	for _, tc := range []string{"unique", "duplicate nightly", "too many files", "oversized file", "aliases", "wrong size", "budget"} {
		t.Run(tc, func(t *testing.T) {
			calls := 0
			get := func(endpoint string, out any) error {
				calls++
				u, err := url.Parse(endpoint)
				if err != nil || u.Query().Get("ref") != head {
					t.Fatalf("workflow snapshot is not pinned: %s", endpoint)
				}
				body, ok := historyWorkflowFixture(u)
				if u.Path == "/contents/.github/workflows" {
					list := body.([]any)
					switch tc {
					case "duplicate nightly":
						body = append(list, map[string]any{"path": otherNightly, "type": "file", "sha": strings.Repeat("e", 40)})
					case "too many files":
						for len(list) <= checksWorkflowLimit {
							list = append(list, map[string]any{"path": fmt.Sprintf(".github/workflows/file-%d.yml", len(list)), "type": "file", "sha": strings.Repeat("e", 40)})
						}
						body = list
					}
				} else if u.Path == "/contents/"+defaultCIWorkflow {
					file := body.(map[string]any)
					switch tc {
					case "oversized file":
						file["size"] = checksWorkflowByteLimit + 1
					case "aliases":
						raw := "anchor: &jobs\n  go: {}\njobs: *jobs\n"
						file["content"], file["size"] = base64.StdEncoding.EncodeToString([]byte(raw)), len(raw)
					case "wrong size":
						file["size"] = 1
					}
				} else if u.Path == "/contents/"+otherNightly {
					body, ok = historyWorkflowBody(otherNightly, "nightly-"), true
				}
				if !ok {
					t.Fatalf("unexpected workflow read: %s", endpoint)
				}
				return writeMetricJSON(out, body)
			}
			budget := checksWorkflowLimit + 1
			if tc == "budget" {
				budget = 2
			}
			workflows, pending, err := checksWorkflows(get, head, nil, budget)
			if tc == "wrong size" {
				if err != errRead {
					t.Fatalf("malformed snapshot accepted for wrong reason: %v", err)
				}
				return
			}
			if err != nil || pending != (tc == "budget") || calls > budget {
				t.Fatalf("unbounded workflow read or wrong resumption: calls=%d pending=%t err=%v", calls, pending, err)
			}
			if tc == "unique" || tc == "duplicate nightly" {
				wantNightly := defaultNightlyWorkflow
				if tc == "duplicate nightly" {
					wantNightly = ""
				}
				if workflows.CI != defaultCIWorkflow || workflows.Nightly != wantNightly {
					t.Fatalf("ambiguous workflow identity: %+v", workflows)
				}
			} else if workflows.CI != "" || workflows.Nightly != "" {
				t.Fatalf("unsupported workflow snapshot guessed: %+v", workflows)
			}
			if tc != "unique" {
				return
			}
			// A new commit with the same workflow blobs needs only its directory.
			// A changed blob needs file reads and cannot use the old mapping.
			for _, changed := range []bool{false, true} {
				reads := 0
				cached, resumed, err := checksWorkflows(func(endpoint string, out any) error {
					reads++
					u, _ := url.Parse(endpoint)
					if u.Path != "/contents/.github/workflows" || u.Query().Get("ref") != otherHead {
						t.Fatalf("cached workflow unexpectedly re-read a file: %s", endpoint)
					}
					body, _ := historyWorkflowFixture(u)
					if changed {
						body.([]any)[0].(map[string]any)["sha"] = strings.Repeat("f", 40)
					}
					return writeMetricJSON(out, body)
				}, otherHead, &workflows, 1)
				if err != nil || reads != 1 || resumed != changed {
					t.Fatalf("wrong cache/budget behavior: %+v reads=%d resumed=%t err=%v", cached, reads, resumed, err)
				}
				if !changed && (cached.CI != workflows.CI || cached.Nightly != workflows.Nightly || cached.Head != otherHead) {
					t.Fatalf("unchanged blob mapping lost: %+v", cached)
				}
				if changed && (cached.CI != "" || cached.Nightly != "") {
					t.Fatalf("changed blob reused a stale mapping: %+v", cached)
				}
			}
		})
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
	exact.Suite = -derived.ID
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
