// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// arionFixture is the value-free record set Project Arion v5's measure-v2.py
// measured (testdata/arion_w2.json.gz, built by arion_w2_convert.py): one row per
// pull-request or merge-queue run with its first attempt, its latest attempt,
// the conclusions of its five required checks and the worst runner wait.
type arionFixture struct {
	Required []string `json:"required"`
	Runs     [][]any  `json:"runs"`
	Pulls    [][]any  `json:"pulls"`
}

func parseTime(t *testing.T, v any) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, v.(string))
	if err != nil {
		t.Fatal(err)
	}
	return at.UTC()
}

// arionInput turns the records into the facts the Delivery page stores, as they
// stood at end: an attempt that completed later does not exist yet.
func arionInput(t *testing.T, end time.Time) metricInput {
	t.Helper()
	file, err := os.Open("testdata/arion_w2.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var fx arionFixture
	if err = json.NewDecoder(zr).Decode(&fx); err != nil {
		t.Fatal(err)
	}
	covered := end.AddDate(0, 0, -30)
	in := metricInput{CIWorkflow: defaultCIWorkflow, NightlyWorkflow: defaultNightlyWorkflow, Required: fx.Required, Covered: &covered}
	for _, r := range fx.Runs {
		id, event, branch, head := int64(r[0].(float64)), r[1].(string), r[2].(string), r[3].(string)
		created := parseTime(t, r[4])
		// Attempt 1, with the job facts the first attempt carries.
		run := metricRun{ID: id, Attempt: 1, Workflow: defaultCIWorkflow, Name: "CI", Event: event, Branch: branch, Head: head, Created: created,
			Started: parseTime(t, r[6]), Completed: parseTime(t, r[7]), Conclusion: r[5].(string), JobsRead: true, Required: map[string]string{}}
		for i, name := range fx.Required {
			run.Required[name] = r[11].([]any)[i].(string)
		}
		if wait, ok := r[12].(float64); ok {
			ms := int(wait*60000 + 0.5)
			run.WorstWaitMS = &ms
		}
		if run.Completed.Before(end) {
			in.Runs = append(in.Runs, run)
		}
		if attempt := int(r[8].(float64)); attempt > 1 && r[10] != nil {
			later := metricRun{ID: id, Attempt: attempt, Workflow: defaultCIWorkflow, Name: "CI", Event: event, Branch: branch, Head: head, Created: created,
				Started: created, Completed: parseTime(t, r[10]), Conclusion: r[9].(string)}
			if later.Completed.Before(end) {
				in.Runs = append(in.Runs, later)
			}
		}
	}
	for _, p := range fx.Pulls {
		pull := metricPull{Number: int64(p[0].(float64)), Opened: parseTime(t, p[1])}
		if merged := parseTime(t, p[2]); merged.Before(end) {
			pull.Merged = &merged
		}
		in.Pulls = append(in.Pulls, pull)
	}
	return in
}

func TestDeliveryNumbersEqualMeasureV2(t *testing.T) {
	// Risk: the Delivery page and the measurement the Arion plan is steered by
	// count differently, so the page says "on target" while measure-v2.py says
	// otherwise. The window is W2 of the plan (2026-10-05 00:00 .. 10-09 04:00Z);
	// every expectation is the number measure-v2-summary.json gives for it.
	end := time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC)
	metrics := computeMetricsFor(arionInput(t, end), end, []int{5})
	window := func(key string) MetricWindow {
		t.Helper()
		m := metricByKey(t, metrics, key)
		if len(m.Windows) != 1 || m.Windows[0].Days != 5 {
			t.Fatalf("%s windows: %+v", key, m.Windows)
		}
		return m.Windows[0]
	}
	check := func(key string, status string, n int, value, p50, p90 *float64, counts map[string]int) {
		t.Helper()
		w := window(key)
		same := func(a, b *float64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
		if w.Status != status || w.N != n || !same(w.Value, value) || !same(w.P50, p50) || !same(w.P90, p90) {
			t.Fatalf("%s = {%s n=%d value=%v p50=%v p90=%v}, want {%s n=%d value=%v p50=%v p90=%v}", key, w.Status, w.N, deref(w.Value), deref(w.P50), deref(w.P90), status, n, deref(value), deref(p50), deref(p90))
		}
		for name, want := range counts {
			if w.Counts[name] != want {
				t.Fatalf("%s count %s = %d, want %d (all: %v)", key, name, w.Counts[name], want, w.Counts)
			}
		}
	}
	check("first_attempt_green", "ok", 427, f64(62.3), nil, nil, nil)
	check("required_checks_green", "ok", 427, f64(65.8), nil, nil, nil)
	check("pr_ci_wall", "ok", 427, f64(16.1), f64(16.1), f64(28.4), nil)
	check("queue_run_wall", "ok", 260, f64(15.4), f64(15.4), f64(26.2), nil)
	check("runner_wait", "ok", 427, f64(6.2), f64(1.2), f64(6.2), nil)
	check("time_to_first_green_commit", "ok", 267, f64(15.9), f64(15.9), f64(27), map[string]int{"never_green": 192, "superseded": 169})
	check("time_to_first_green", "ok", 130, f64(51.9), f64(51.9), f64(637.1), map[string]int{"never_green": 19, "first_run_green": 47})
	check("pr_open_to_merged", "ok", 152, f64(120), f64(120), f64(1127.8), nil)
	check("queue_runs_per_pr", "ok", 151, f64(1.45), f64(1), f64(2), nil)
	check("queue_ejections", "ok", 151, f64(59.6), nil, nil, map[string]int{"events": 90, "base": 151})
	check("queue_unclassified", "ok", 151, f64(15.9), nil, nil, map[string]int{"events": 24, "base": 151})
	check("flaked_failures", "ok", 427, f64(0.2), nil, nil, map[string]int{"confirmed": 1, "suspect": 0})
	// The number behind "62.3 %": 266 of 427 first attempts were green.
	if g := window("time_to_first_green"); g.Counts["never_green"]+g.N != 149 {
		t.Fatalf("branches in the window: %d, want 149", g.Counts["never_green"]+g.N)
	}
}

// ---------- Fixed facts for each v5 reading (AEON-1016) ----------

const arionCI = ".github/workflows/ci.yml"

func arionDays(days float64) time.Time {
	return metricNow.Add(-time.Duration(days * 24 * float64(time.Hour)))
}

func arionSha(c string) string { return strings.Repeat(c, 40) }

func arionBase(runs []metricRun) metricInput {
	covered := arionDays(40)
	return metricInput{CIWorkflow: arionCI, NightlyWorkflow: defaultNightlyWorkflow, Covered: &covered, Runs: runs}
}

// jobs marks a first attempt as read, with the conclusions of the five required checks.
func jobs(run metricRun, conclusions map[string]string, waitMS int) metricRun {
	run.JobsRead = true
	run.Required = map[string]string{"go": "success", "web": "success", "release-check": "success", "e2e": "success", "migration-compat": "success"}
	for name, conclusion := range conclusions {
		run.Required[name] = conclusion
	}
	if waitMS >= 0 {
		run.WorstWaitMS = &waitMS
	}
	return run
}

func wantCounts(t *testing.T, m Metric, days int, counts map[string]int) {
	t.Helper()
	got := metricWindow(t, m, days).Counts
	for name, want := range counts {
		if got[name] != want {
			t.Fatalf("%s %dd counts[%s] = %d, want %d (all %v)", m.Key, days, name, got[name], want, got)
		}
	}
}

func TestDeliveryTimeToFirstGreenPerCommitAndBranchKeepsNeverGreen(t *testing.T) {
	// Risk: a branch or commit that never went green drops out of the number
	// (so the median looks better than it is), or a branch that began before the
	// window is counted in it because it went green inside.
	mk := metricFixtureRun
	runs := []metricRun{
		// work/a: the first commit failed and was superseded; the second went green 20 minutes in.
		mk(1, 1, arionCI, "pull_request", "work/a", arionSha("1"), arionDays(2), 1, 9, "failure"),
		mk(2, 1, arionCI, "pull_request", "work/a", arionSha("2"), arionDays(2).Add(time.Hour), 2, 18, "success"),
		// work/b: one failed commit, nothing after it.
		mk(3, 1, arionCI, "pull_request", "work/b", arionSha("3"), arionDays(1), 0, 5, "failure"),
		// work/c: green on its very first run.
		mk(4, 1, arionCI, "pull_request", "work/c", arionSha("4"), arionDays(1).Add(2*time.Hour), 1, 14, "success"),
		// work/d: red first attempt, green re-run of the same commit 42 minutes after creation.
		mk(5, 1, arionCI, "pull_request", "work/d", arionSha("5"), arionDays(3), 0, 10, "failure"),
		mk(5, 2, arionCI, "pull_request", "work/d", arionSha("5"), arionDays(3), 30, 12, "success"),
		// work/e began nine days ago and went green yesterday: not in the 7-day window.
		mk(6, 1, arionCI, "pull_request", "work/e", arionSha("6"), arionDays(9), 0, 10, "failure"),
		mk(7, 1, arionCI, "pull_request", "work/e", arionSha("7"), arionDays(1).Add(3*time.Hour), 0, 10, "success"),
	}
	metrics := computeMetrics(arionBase(runs), metricNow)
	commit := metricByKey(t, metrics, "time_to_first_green_commit")
	wantWindow(t, commit, 7, "ok", 4, f64(17.5), f64(17.5), f64(35.4))
	wantCounts(t, commit, 7, map[string]int{"never_green": 2, "superseded": 1})
	wantCounts(t, commit, 30, map[string]int{"never_green": 3, "superseded": 2})
	branch := metricByKey(t, metrics, "time_to_first_green")
	wantWindow(t, branch, 7, "ok", 3, f64(42), f64(42), f64(72.4))
	wantCounts(t, branch, 7, map[string]int{"never_green": 1, "first_run_green": 1})
	// 30 days: work/e counts, from its first run to its green one (8 days and 3 h 10 min).
	wantWindow(t, branch, 30, "ok", 4, f64(61), f64(61), f64(8221))
	wantCounts(t, branch, 30, map[string]int{"never_green": 1, "first_run_green": 1})
}

func TestDeliveryRequiredChecksGreenSitsBesideWorkflowGreen(t *testing.T) {
	// Risk: a run that is red only outside the required checks counts as red
	// for the merge rule (or a missing required job counts as green), and runs
	// whose jobs are not read yet are silently dropped instead of marking the
	// window partial.
	mk := metricFixtureRun
	r := func(id int64, conclusion string, jobsRead bool, conclusions map[string]string) metricRun {
		run := mk(id, 1, arionCI, "pull_request", "work/"+strings.Repeat("x", int(id)), arionSha("a"), arionDays(1).Add(time.Duration(id)*time.Minute), 0, 10, conclusion)
		if jobsRead {
			run = jobs(run, conclusions, 1000)
		}
		return run
	}
	runs := []metricRun{
		r(1, "failure", true, nil),                                 // red only outside the required checks: required green
		r(2, "success", true, nil),                                 // green
		r(3, "failure", true, map[string]string{"go": "failure"}),  // a required check failed
		r(4, "failure", true, map[string]string{"e2e": "missing"}), // a required job never ran
		r(5, "success", true, map[string]string{"web": "skipped"}), // skipped passes, as GitHub counts it
		r(6, "success", false, nil),                                // jobs not read yet
	}
	metrics := computeMetrics(arionBase(runs), metricNow)
	wantWindow(t, metricByKey(t, metrics, "first_attempt_green"), 7, "ok", 6, f64(50), nil, nil)
	required := metricByKey(t, metrics, "required_checks_green")
	wantWindow(t, required, 7, "partial", 5, f64(60), nil, nil)
	if required.Reason == nil || !strings.Contains(*required.Reason, "1 run without job facts yet") {
		t.Fatalf("a run without job facts must say so: %+v", required.Reason)
	}
	// A run read for other check names is unknown too: never guessed.
	other := runs[1]
	other.Required = map[string]string{"lint": "success"}
	unknown := metricByKey(t, computeMetrics(arionBase([]metricRun{other}), metricNow), "required_checks_green")
	wantWindow(t, unknown, 7, "no_data", 0, nil, nil, nil)
	if unknown.Reason == nil || !strings.Contains(*unknown.Reason, "without job facts yet") || strings.Contains(*unknown.Reason, "No completed") {
		t.Fatalf("runs exist but their jobs are unknown; the reason must say that, not that no runs ran: %+v", unknown.Reason)
	}
	// The project's own required checks decide, not a fixed list.
	in := arionBase(runs[:2])
	in.Required = []string{"go", "lint"}
	wantWindow(t, metricByKey(t, computeMetrics(in, metricNow), "required_checks_green"), 7, "no_data", 0, nil, nil, nil)
}

func TestDeliveryConfirmedFlakesAndSuspectsStayApart(t *testing.T) {
	// Risk: a fix commit is reported as a flake, or a real flake behind a third
	// attempt is missed; and a suspect (red, then green on the very same tree)
	// is reported as confirmed.
	mk := metricFixtureRun
	tree := func(run metricRun, t string) metricRun { run.HeadTree = strings.Repeat(t, 40); return run }
	hour := time.Hour
	runs := []metricRun{
		// work/f: red, then the next commit green on the same tree: a suspect.
		tree(mk(1, 1, arionCI, "pull_request", "work/f", arionSha("1"), arionDays(2), 0, 10, "failure"), "a"),
		tree(mk(2, 1, arionCI, "pull_request", "work/f", arionSha("2"), arionDays(2).Add(hour), 0, 10, "success"), "a"),
		// work/g: the next commit changed the tree: a fix, not a suspect.
		tree(mk(3, 1, arionCI, "pull_request", "work/g", arionSha("3"), arionDays(2), 0, 10, "failure"), "b"),
		tree(mk(4, 1, arionCI, "pull_request", "work/g", arionSha("4"), arionDays(2).Add(hour), 0, 10, "success"), "c"),
		// work/h: no tree known on either commit: never a suspect.
		mk(5, 1, arionCI, "pull_request", "work/h", arionSha("5"), arionDays(2), 0, 10, "failure"),
		mk(6, 1, arionCI, "pull_request", "work/h", arionSha("6"), arionDays(2).Add(hour), 0, 10, "success"),
		// work/i: red, green, red again on the same run: confirmed, though the latest attempt is red.
		mk(7, 1, arionCI, "pull_request", "work/i", arionSha("7"), arionDays(1), 0, 10, "failure"),
		mk(7, 2, arionCI, "pull_request", "work/i", arionSha("7"), arionDays(1), 20, 10, "success"),
		mk(7, 3, arionCI, "pull_request", "work/i", arionSha("7"), arionDays(1), 40, 10, "failure"),
		// work/j: red twice: not flaky.
		mk(8, 1, arionCI, "pull_request", "work/j", arionSha("8"), arionDays(1), 0, 10, "failure"),
		mk(8, 2, arionCI, "pull_request", "work/j", arionSha("8"), arionDays(1), 20, 10, "failure"),
		// work/k: confirmed on its own commit, and the next commit is green on the same tree:
		// green on the commit itself rules out a suspect.
		tree(mk(9, 1, arionCI, "pull_request", "work/k", arionSha("9"), arionDays(1), 0, 10, "failure"), "d"),
		tree(mk(9, 2, arionCI, "pull_request", "work/k", arionSha("9"), arionDays(1), 20, 10, "success"), "d"),
		tree(mk(10, 1, arionCI, "pull_request", "work/k", arionSha("0"), arionDays(1).Add(hour), 0, 10, "success"), "d"),
	}
	flaky := metricByKey(t, computeMetrics(arionBase(runs), metricNow), "flaked_failures")
	wantWindow(t, flaky, 7, "ok", 10, f64(20), nil, nil)
	wantCounts(t, flaky, 7, map[string]int{"confirmed": 2, "suspect": 1})
}

func TestDeliveryQueueEjectionsAndUnclassifiedRunsPerHundredMergedPRs(t *testing.T) {
	// Risk: a red queue run is not inferred as an ejection, a cancelled or
	// reordered run is labelled as one, an unmerged PR inflates the base, or a
	// run created after the merge counts as a queue try.
	mk := metricFixtureRun
	merged := arionDays(1)
	at := func(m time.Duration) time.Time { return merged.Add(m) }
	q := func(id int64, pr int, created time.Time, conclusion string, red bool) metricRun {
		run := mk(id, 1, arionCI, "merge_group", "gh-readonly-queue/main/pr-"+strconvI(pr)+"-"+arionSha("a"), arionSha("a"), created, 0, 10, conclusion)
		if conclusion == "cancelled" {
			return run
		}
		if red {
			return jobs(run, map[string]string{"go": "failure"}, 0)
		}
		return jobs(run, nil, 0)
	}
	runs := []metricRun{
		q(1, 7, at(-3*time.Hour), "failure", true),    // PR 7: ejected,
		q(2, 7, at(-2*time.Hour), "cancelled", false), // then replaced (cancelled),
		q(3, 7, at(-time.Hour), "success", false),     // then merged.
		q(4, 8, at(-90*time.Minute), "failure", true), // PR 8 never merged: an ejection, not in the base.
		q(5, 9, at(-time.Hour), "success", false),     // PR 9 merged first time.
		q(6, 9, at(2*time.Hour), "success", false),    // a run after PR 9's merge is not a try.
	}
	in := arionBase(runs)
	in.Pulls = []metricPull{
		{Number: 7, Opened: at(-5 * time.Hour), Merged: ptrTime(merged)},
		{Number: 8, Opened: at(-5 * time.Hour), Closed: ptrTime(merged)},
		{Number: 9, Opened: at(-5 * time.Hour), Merged: ptrTime(merged.Add(time.Minute))},
	}
	metrics := computeMetrics(in, metricNow)
	ejections := metricByKey(t, metrics, "queue_ejections")
	wantWindow(t, ejections, 7, "ok", 2, f64(100), nil, nil)
	wantCounts(t, ejections, 7, map[string]int{"events": 2, "base": 2})
	unclassified := metricByKey(t, metrics, "queue_unclassified")
	wantWindow(t, unclassified, 7, "ok", 2, f64(50), nil, nil)
	wantCounts(t, unclassified, 7, map[string]int{"events": 1, "base": 2})
	wantWindow(t, metricByKey(t, metrics, "queue_runs_per_pr"), 7, "ok", 2, f64(2), f64(2), f64(2.8))

	// A queue run whose jobs are not read cannot be classified: the window is
	// partial and says why; the run is not guessed into either group.
	unknown := q(10, 11, at(-4*time.Hour), "failure", true)
	unknown.JobsRead, unknown.Required = false, nil
	in.Runs = append(in.Runs, unknown, q(11, 11, at(-3*time.Hour), "success", false))
	in.Pulls = append(in.Pulls, metricPull{Number: 11, Opened: at(-6 * time.Hour), Merged: ptrTime(merged.Add(2 * time.Minute))})
	metrics = computeMetrics(in, metricNow)
	if m := metricByKey(t, metrics, "queue_unclassified"); m.Status != "partial" || m.Reason == nil || !strings.Contains(*m.Reason, "without job facts yet") {
		t.Fatalf("unclassified with an unknown run: %+v", m)
	}
	wantWindow(t, metricByKey(t, metrics, "queue_ejections"), 7, "partial", 3, f64(66.7), nil, nil)
}

func strconvI(n int) string { return strconv.Itoa(n) }

func TestDeliveryRunnerWaitIsTheP90OfTheWorstJobPerRun(t *testing.T) {
	// Risk: runner wait is judged by its median (the plan judges p90), a skipped
	// job's zero wait pulls it down, or runs without job facts vanish.
	mk := metricFixtureRun
	r := func(id int64, waitMS int) metricRun {
		return jobs(mk(id, 1, arionCI, "pull_request", "work/w"+strconvI(int(id)), arionSha("a"), arionDays(1).Add(time.Duration(id)*time.Minute), 0, 10, "success"), nil, waitMS)
	}
	runs := []metricRun{r(1, 60000), r(2, 120000), r(3, 600000), r(4, -1)} // run 4: every job was skipped
	wait := metricByKey(t, computeMetrics(arionBase(runs), metricNow), "runner_wait")
	wantWindow(t, wait, 7, "ok", 3, f64(8.4), f64(2), f64(8.4))
	unread := mk(5, 1, arionCI, "pull_request", "work/w5", arionSha("a"), arionDays(1), 0, 10, "success")
	wait = metricByKey(t, computeMetrics(arionBase(append(runs, unread)), metricNow), "runner_wait")
	wantWindow(t, wait, 7, "partial", 3, f64(8.4), f64(2), f64(8.4))
}

func TestDeliveryPreflightRedRateIsItsOwnLine(t *testing.T) {
	// Risk: preflight runs count as CI greens, a cancelled dispatch counts as
	// red, or a later green attempt hides the red first one.
	mk := metricFixtureRun
	pre := ".github/workflows/ci-preflight.yml"
	runs := []metricRun{
		mk(1, 1, pre, "workflow_dispatch", "main", arionSha("1"), arionDays(1), 0, 8, "success"),
		mk(2, 1, pre, "workflow_dispatch", "main", arionSha("2"), arionDays(1), 0, 8, "failure"),
		mk(2, 2, pre, "workflow_dispatch", "main", arionSha("2"), arionDays(1), 0, 8, "success"),
		mk(3, 1, pre, "workflow_dispatch", "main", arionSha("3"), arionDays(1), 0, 1, "cancelled"),
		mk(4, 1, arionCI, "pull_request", "work/a", arionSha("4"), arionDays(1), 0, 10, "success"),
	}
	metrics := computeMetrics(arionBase(runs), metricNow)
	wantWindow(t, metricByKey(t, metrics, "preflight_red_rate"), 7, "ok", 2, f64(50), nil, nil)
	wantWindow(t, metricByKey(t, metrics, "first_attempt_green"), 7, "ok", 1, f64(100), nil, nil)
	if m := metricByKey(t, computeMetrics(arionBase(nil), metricNow), "preflight_red_rate"); m.Status != "no_data" || m.Reason == nil || m.Windows[0].Value != nil {
		t.Fatalf("no preflight run is no data, never 0 %%: %+v", m)
	}
}

func TestDeliveryReviewAuditsAndEscapedDefectsCountBySeverity(t *testing.T) {
	// Risk: a defect without a rollout or a ticket link is invented from main-branch
	// red, severities are mixed up, a rollout incident is not an escaped defect,
	// or an empty report list reads as "0 defects" before anyone reported.
	first := arionDays(5)
	in := arionBase(nil)
	in.Reports = []metricReport{
		{Kind: "review_audit", Key: "a1", Severity: "none", At: arionDays(4)},
		{Kind: "review_audit", Key: "a2", Severity: "medium", At: arionDays(3)},
		{Kind: "review_audit", Key: "a3", Severity: "high", At: arionDays(2)},
		{Kind: "escaped_defect", Key: "AEON-1", Severity: "high", PR: ptrInt64(5), At: first},
		{Kind: "escaped_defect", Key: "AEON-2", Severity: "low", Release: strPtr("126"), At: arionDays(1)},
	}
	in.Incidents = []metricIncident{{Key: "pool", Severity: "high", At: arionDays(2)}, {Key: "slow", Severity: "medium", At: arionDays(1)}}
	metrics := computeMetrics(in, metricNow)
	audits := metricByKey(t, metrics, "review_audits")
	wantWindow(t, audits, 7, "partial", 3, f64(3), nil, nil)
	wantCounts(t, audits, 7, map[string]int{"high": 1, "medium": 1, "low": 0, "clean": 1})
	defects := metricByKey(t, metrics, "escaped_defects")
	wantWindow(t, defects, 7, "partial", 4, f64(4), nil, nil)
	wantCounts(t, defects, 7, map[string]int{"high": 2, "medium": 1, "low": 1})
	// Coverage starts with the first report: the days before it are no data, not zero.
	if w := metricWindow(t, defects, 7); w.Coverage.CoveredDays != 6 || w.Coverage.Full {
		t.Fatalf("defect coverage: %+v", w.Coverage)
	}
	empty := computeMetrics(arionBase(nil), metricNow)
	for _, key := range []string{"review_audits", "escaped_defects"} {
		if m := metricByKey(t, empty, key); m.Status != "no_data" || metricWindow(t, m, 7).Value != nil || m.Reason == nil {
			t.Fatalf("%s without reports: %+v", key, m)
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestDeliveryTargetsAreArionV5(t *testing.T) {
	// Risk: a tile is judged against a target the plan no longer holds (the
	// "3 on a reuse hit" queue target, the 24-minute release) or against none.
	metrics := computeMetrics(arionBase(nil), metricNow)
	want := map[string]struct {
		value     float64
		direction string
	}{
		"pr_ci_wall": {7, "max"}, "queue_run_wall": {7, "max"}, "first_attempt_green": {80, "min"}, "required_checks_green": {80, "min"},
		"time_to_first_green": {20, "max"}, "pr_open_to_merged": {60, "max"}, "queue_runs_per_pr": {1.1, "max"}, "review_time": {8, "max"},
		"review_changes_share": {25, "max"}, "merge_rounds_model_share": {20, "max"}, "release_queue_to_live": {54, "max"}, "nightly_green": {100, "min"},
		"flaked_failures": {2, "max"}, "queue_ejections": {10, "max"}, "runner_wait": {1, "max"},
	}
	for key, w := range want {
		m := metricByKey(t, metrics, key)
		if m.Target == nil || m.Target.Value != w.value || m.Target.Direction != w.direction || m.Target.Source != "Arion" {
			t.Fatalf("%s target: %+v, want %v %s", key, m.Target, w.value, w.direction)
		}
	}
	// No target is invented where the plan sets none (it sets them after WP1.6, or says "falling").
	for _, key := range []string{"time_to_first_green_commit", "queue_unclassified", "preflight_red_rate", "review_audits", "escaped_defects"} {
		if m := metricByKey(t, metrics, key); m.Target != nil {
			t.Fatalf("%s has a target: %+v", key, m.Target)
		}
	}
	queue := metricByKey(t, metrics, "queue_run_wall").Target
	if strings.Contains(strings.ToLower(queue.Note), "reuse") {
		t.Fatalf("the reuse-hit target is gone from v5: %q", queue.Note)
	}
	release := metricByKey(t, metrics, "release_queue_to_live").Target
	if !strings.Contains(release.Note, "61") || !strings.Contains(release.Note, "54") || !strings.Contains(release.Note, "+ W") {
		t.Fatalf("release target note: %q", release.Note)
	}
}
