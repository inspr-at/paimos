// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"strings"
	"testing"
	"time"
)

var metricNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func metricFixtureRun(id int64, attempt int, workflow, event, branch, head string, created time.Time, wait, wall float64, conclusion string) metricRun {
	started := created.Add(time.Duration(wait * float64(time.Minute)))
	return metricRun{ID: id, Attempt: attempt, Workflow: workflow, Name: "CI", Event: event, Branch: branch, Head: head, Created: created, Started: started, Completed: started.Add(time.Duration(wall * float64(time.Minute))), Conclusion: conclusion}
}

func metricByKey(t *testing.T, metrics []Metric, key string) Metric {
	t.Helper()
	for _, m := range metrics {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("metric %s missing", key)
	return Metric{}
}

func wantWindow(t *testing.T, m Metric, days int, status string, n int, value, p50, p90 *float64) {
	t.Helper()
	for _, w := range m.Windows {
		if w.Days != days {
			continue
		}
		same := func(a, b *float64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
		if w.Status != status || w.N != n || !same(w.Value, value) || !same(w.P50, p50) || !same(w.P90, p90) {
			t.Fatalf("%s %dd = {%s n=%d value=%v p50=%v p90=%v}, want {%s n=%d value=%v p50=%v p90=%v}", m.Key, days, w.Status, w.N, deref(w.Value), deref(w.P50), deref(w.P90), status, n, deref(value), deref(p50), deref(p90))
		}
		return
	}
	t.Fatalf("%s has no %d-day window", m.Key, days)
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func f64(v float64) *float64 { return &v }

func TestDeliveryMetricsFromFixedFacts(t *testing.T) {
	// Risk: a wrong percentile, window edge, attempt filter or flake rule
	// shows Markus a number that is not what the Arion contract defines.
	ci, nightly := ".github/workflows/ci.yml", ".github/workflows/nightly-full.yml"
	h := func(c string) string { return strings.Repeat(c, 40) }
	d := func(days float64) time.Time { return metricNow.Add(-time.Duration(days * 24 * float64(time.Hour))) }
	covered := d(40)
	reported := d(5)
	in := metricInput{CIWorkflow: ci, NightlyWorkflow: nightly, Covered: &covered,
		Runs: []metricRun{
			// work/a: first attempt failed, the re-run of the same run went green: a flake.
			metricFixtureRun(1, 1, ci, "pull_request", "work/a", h("1"), d(2), 2, 10, "failure"),
			metricFixtureRun(1, 2, ci, "pull_request", "work/a", h("1"), d(2).Add(30*time.Minute), 2, 12, "success"),
			metricFixtureRun(2, 1, ci, "pull_request", "work/b", h("2"), d(1), 2, 20, "success"),
			// A real failure 20 days ago, never green.
			metricFixtureRun(3, 1, ci, "pull_request", "work/d", h("3"), d(20), 2, 30, "failure"),
			// Cancelled (superseded) runs count in no wall and no green rate.
			metricFixtureRun(4, 1, ci, "pull_request", "work/c", h("4"), d(1), 0, 1, "cancelled"),
			// Another workflow never counts as CI.
			metricFixtureRun(5, 1, ".github/workflows/docs.yml", "pull_request", "work/a", h("1"), d(1), 0, 99, "success"),
			metricFixtureRun(10, 1, ci, "merge_group", "gh-readonly-queue/main/pr-7-"+h("a"), h("a"), d(1).Add(-50*time.Minute), 0, 15, "failure"),
			// The queue runs of PR 7 come before its merge, as measure-v2 counts them.
			metricFixtureRun(11, 1, ci, "merge_group", "gh-readonly-queue/main/pr-7-"+h("b"), h("b"), d(1).Add(-30*time.Minute), 0, 14, "success"),
			metricFixtureRun(12, 1, ci, "merge_group", "gh-readonly-queue/main/pr-8-"+h("c"), h("c"), d(25), 0, 16, "success"),
			metricFixtureRun(20, 1, nightly, "schedule", "main", h("d"), d(1), 0, 50, "success"),
			metricFixtureRun(21, 1, nightly, "schedule", "main", h("e"), d(2), 0, 50, "failure"),
			metricFixtureRun(21, 2, nightly, "schedule", "main", h("e"), d(2).Add(2*time.Hour), 0, 50, "success"),
			metricFixtureRun(22, 1, nightly, "schedule", "main", h("f"), d(3), 0, 50, "failure"),
		},
		Pulls: []metricPull{
			{Number: 7, Opened: d(1).Add(-time.Hour), Merged: ptrTime(d(1))},
			{Number: 8, Opened: d(10), Merged: ptrTime(d(10).Add(30 * time.Minute))},
			{Number: 9, Opened: d(3)},
		},
		Marks: []metricMark{
			{Kind: "review_status", Head: h("2"), At: d(1).Add(-10 * time.Minute), Outcome: "pending"},
			{Kind: "review_status", Head: h("2"), At: d(1), Outcome: "success"},
			{Kind: "review_status", Head: h("3"), At: d(20), Outcome: "pending"},
			{Kind: "review_status", Head: h("3"), At: d(20).Add(5 * time.Minute), Outcome: "failure"},
			{Kind: "review", Key: "gate-1", Head: h("5"), Started: ptrTime(metricNow.Add(-3 * time.Hour)), At: metricNow.Add(-2 * time.Hour), Outcome: "changes"},
			{Kind: "merge_round", Key: "r1", At: reported, Outcome: "scripted"},
			{Kind: "merge_round", Key: "r2", At: d(4), Outcome: "scripted"},
			{Kind: "merge_round", Key: "r3", At: d(3), Outcome: "model"},
		},
	}
	metrics := computeMetrics(in, metricNow)
	numbers := map[int]bool{}
	for _, m := range metrics {
		numbers[m.Number] = true
		if len(m.Daily) != metricDays || m.Daily[0].Date != "2026-09-09" || m.Daily[metricDays-1].Date != "2026-10-08" {
			t.Fatalf("%s daily series %d points from %s", m.Key, len(m.Daily), m.Daily[0].Date)
		}
	}
	// The ten Arion numbers, then the eight v5 readings (AEON-1016): time to
	// first green per commit, confirmed flakes, inferred ejections, unclassified
	// queue runs, runner wait, preflight, review audits and escaped defects.
	for n := 1; n <= 18; n++ {
		if !numbers[n] {
			t.Fatalf("numbers covered: %v", numbers)
		}
	}
	if len(numbers) != 18 {
		t.Fatalf("numbers covered: %v", numbers)
	}

	wall := metricByKey(t, metrics, "pr_ci_wall")
	wantWindow(t, wall, 7, "ok", 2, f64(15), f64(15), f64(19))
	wantWindow(t, wall, 30, "ok", 3, f64(20), f64(20), f64(28))
	if wall.Status != "ok" || wall.Reason != nil || wall.Latest == nil || wall.Latest.Value != 20 || wall.Target == nil || wall.Target.Value != 7 {
		t.Fatalf("PR CI wall: %+v", wall)
	}
	wantWindow(t, metricByKey(t, metrics, "queue_run_wall"), 30, "ok", 3, f64(15), f64(15), f64(15.8))
	green := metricByKey(t, metrics, "first_attempt_green")
	wantWindow(t, green, 7, "ok", 2, f64(50), nil, nil)
	wantWindow(t, green, 30, "ok", 3, f64(33.3), nil, nil)
	wantWindow(t, metricByKey(t, metrics, "flaked_failures"), 30, "ok", 3, f64(33.3), nil, nil)
	wantWindow(t, metricByKey(t, metrics, "time_to_first_green"), 7, "ok", 2, f64(33), f64(33), f64(41.8))
	wantWindow(t, metricByKey(t, metrics, "pr_open_to_merged"), 30, "ok", 2, f64(45), f64(45), f64(57))
	wantWindow(t, metricByKey(t, metrics, "queue_runs_per_pr"), 30, "ok", 2, f64(1.5), f64(1.5), f64(1.9))

	review := metricByKey(t, metrics, "review_time")
	wantWindow(t, review, 30, "partial", 3, f64(10), f64(10), f64(50))
	if review.Status != "partial" || review.Reason == nil || !strings.Contains(*review.Reason, "aeon/review") {
		t.Fatalf("review time must say why it is partial: %+v", review)
	}
	wantWindow(t, metricByKey(t, metrics, "review_changes_share"), 30, "partial", 3, f64(66.7), nil, nil)

	rounds := metricByKey(t, metrics, "merge_rounds_model_share")
	wantWindow(t, rounds, 7, "partial", 3, f64(33.3), nil, nil)
	wantWindow(t, rounds, 30, "partial", 3, f64(33.3), nil, nil)
	if rounds.Status != "partial" || rounds.Reason == nil || !strings.Contains(*rounds.Reason, "2026-10-03") {
		t.Fatalf("merge rounds must name the start of reports: %+v", rounds)
	}

	release := metricByKey(t, metrics, "release_queue_to_live")
	if release.Status != "no_data" || release.Latest != nil || release.Reason == nil || !strings.Contains(*release.Reason, "No release") {
		t.Fatalf("release without reports: %+v", release)
	}
	wantWindow(t, release, 30, "no_data", 0, nil, nil, nil)

	night := metricByKey(t, metrics, "nightly_green")
	wantWindow(t, night, 7, "ok", 3, f64(66.7), nil, nil)
	want := map[string]*float64{"2026-10-07": f64(100), "2026-10-06": f64(100), "2026-10-05": f64(0), "2026-10-04": nil}
	for _, p := range night.Daily {
		w, ok := want[p.Date]
		if !ok {
			continue
		}
		if w == nil && p.Value != nil || w != nil && (p.Value == nil || *p.Value != *w) {
			t.Fatalf("night %s = %v, want %v", p.Date, deref(p.Value), deref(w))
		}
	}
}

func TestDeliveryMetricsQueueRunsCountOnlyMergedPulls(t *testing.T) {
	// Risk: a pull request that entered the merge queue and then failed or was
	// abandoned still counts, so 1.0 no longer means every counted PR merged
	// on its first queue run.
	ci := ".github/workflows/ci.yml"
	h := func(c string) string { return strings.Repeat(c, 40) }
	d := func(days float64) time.Time { return metricNow.Add(-time.Duration(days * 24 * float64(time.Hour))) }
	covered := d(40)
	metrics := computeMetrics(metricInput{
		CIWorkflow: ci, NightlyWorkflow: ".github/workflows/nightly-full.yml", Covered: &covered,
		Runs: []metricRun{
			metricFixtureRun(1, 1, ci, "merge_group", "gh-readonly-queue/main/pr-7-"+h("a"), h("a"), d(1), 0, 10, "success"),
			metricFixtureRun(2, 1, ci, "merge_group", "gh-readonly-queue/main/pr-8-"+h("b"), h("b"), d(1), 0, 10, "failure"),
			metricFixtureRun(3, 1, ci, "merge_group", "gh-readonly-queue/main/pr-8-"+h("c"), h("c"), d(1).Add(time.Hour), 0, 10, "failure"),
		},
		Pulls: []metricPull{
			{Number: 7, Opened: d(2), Merged: ptrTime(d(1))},
			{Number: 8, Opened: d(2), Closed: ptrTime(d(1))},
		},
	}, metricNow)
	wantWindow(t, metricByKey(t, metrics, "queue_runs_per_pr"), 30, "ok", 1, f64(1), f64(1), f64(1))
}

func TestDeliveryMetricsNeverReportMissingDataAsZero(t *testing.T) {
	// Risk: an empty or half-backfilled history renders as 0 minutes or 0 %.
	empty := computeMetrics(metricInput{CIWorkflow: ".github/workflows/ci.yml", NightlyWorkflow: ".github/workflows/nightly-full.yml"}, metricNow)
	for _, m := range empty {
		if m.Status != "no_data" || m.Reason == nil || m.Latest != nil {
			t.Fatalf("%s without facts: status %s reason %v latest %v", m.Key, m.Status, m.Reason, m.Latest)
		}
		for _, w := range m.Windows {
			if w.Status != "no_data" || w.Value != nil || w.P50 != nil || w.N != 0 {
				t.Fatalf("%s window %+v", m.Key, w)
			}
		}
		for _, p := range m.Daily {
			if p.Status != "no_data" || p.Value != nil || p.P50 != nil || p.N != 0 {
				t.Fatalf("%s day %+v", m.Key, p)
			}
		}
	}
	// Webhook facts only since three days ago: the 30-day window is partial
	// and says since when; the 7-day window too, because it starts earlier.
	covered := metricNow.Add(-3 * 24 * time.Hour)
	run := metricFixtureRun(1, 1, ".github/workflows/ci.yml", "pull_request", "work/a", strings.Repeat("1", 40), metricNow.Add(-time.Hour), 1, 9, "success")
	half := computeMetrics(metricInput{CIWorkflow: ".github/workflows/ci.yml", NightlyWorkflow: ".github/workflows/nightly-full.yml", Covered: &covered, Runs: []metricRun{run}}, metricNow)
	wall := metricByKey(t, half, "pr_ci_wall")
	wantWindow(t, wall, 7, "partial", 1, f64(9), f64(9), f64(9))
	if wall.Status != "partial" || wall.Reason == nil || !strings.Contains(*wall.Reason, "2026-10-05 12:00") {
		t.Fatalf("partial coverage reason: %+v", wall)
	}
	if today, before := wall.Daily[metricDays-1], wall.Daily[metricDays-2]; today.Status != "ok" || today.N != 1 || before.Status != "no_data" || before.Value != nil {
		t.Fatalf("daily status: today %+v, yesterday %+v", today, before)
	}
	// Truncated reads are partial even with full coverage.
	old := metricNow.Add(-60 * 24 * time.Hour)
	truncated := computeMetrics(metricInput{CIWorkflow: ".github/workflows/ci.yml", NightlyWorkflow: ".github/workflows/nightly-full.yml", Covered: &old, Truncated: true, Runs: []metricRun{run}}, metricNow)
	if m := metricByKey(t, truncated, "pr_ci_wall"); m.Status != "partial" || m.Reason == nil || !strings.Contains(*m.Reason, "missing") {
		t.Fatalf("truncated: %+v", m)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
