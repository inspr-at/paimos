// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func metricWindow(t *testing.T, m Metric, days int) MetricWindow {
	t.Helper()
	for _, w := range m.Windows {
		if w.Days == days {
			return w
		}
	}
	t.Fatalf("%s has no %d-day window", m.Key, days)
	return MetricWindow{}
}

func wantCoverage(t *testing.T, what string, c MetricCoverage, bucket string, total, covered, days int, from string, full bool) {
	t.Helper()
	gotFrom := ""
	if c.From != nil {
		gotFrom = *c.From
	}
	if c.Bucket != bucket || c.BucketsTotal != total || c.BucketsCovered != covered || c.CoveredDays != days || gotFrom != from || c.Full != full {
		t.Fatalf("%s coverage = %+v (from %q), want %s %d/%d buckets, %d days from %q, full %v", what, c, gotFrom, bucket, covered, total, days, from, full)
	}
}

func TestDeliveryMetricsWindowsCoverPreviousAndBuckets(t *testing.T) {
	// Risk: a long window or its comparison counts days the source never
	// observed, extrapolates a bucket, or shifts its buckets, so the tile
	// says "covers 4 of 13 weeks" about different days than the chart.
	ci := ".github/workflows/ci.yml"
	h := strings.Repeat("1", 40)
	covered := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)
	run := func(id int64, completed time.Time, wall float64) metricRun {
		return metricFixtureRun(id, 1, ci, "pull_request", "work/a", h, completed.Add(-time.Duration(wall)*time.Minute), 0, wall, "success")
	}
	in := metricInput{CIWorkflow: ci, NightlyWorkflow: ".github/workflows/nightly-full.yml", Covered: &covered, Runs: []metricRun{
		// One run before coverage: kept as partial, never stretched over the gap.
		run(1, time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), 30),
		// The 7 days before the 7-day window (25 Sep – 1 Oct).
		run(2, time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC), 20),
		run(3, time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC), 24),
		// The 7-day window (2 – 8 Oct).
		run(4, time.Date(2026, 10, 2, 0, 30, 0, 0, time.UTC), 10),
		run(5, time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC), 12),
		// After now: never counted.
		run(6, time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC), 99),
	}}
	wall := metricByKey(t, computeMetrics(in, metricNow), "pr_ci_wall")
	if len(wall.Windows) != 5 {
		t.Fatalf("windows: %+v", wall.Windows)
	}
	for i, days := range []int{7, 30, 90, 180, 365} {
		if wall.Windows[i].Days != days || wall.Windows[i].Coverage.WindowDays != days || wall.Windows[i].Previous == nil {
			t.Fatalf("window %d: %+v", i, wall.Windows[i])
		}
	}

	w7 := metricWindow(t, wall, 7)
	wantWindow(t, wall, 7, "ok", 2, f64(11), f64(11), f64(11.8))
	wantCoverage(t, "7d", w7.Coverage, "day", 7, 7, 7, "2026-10-02", true)
	if p := w7.Previous; p.Status != "ok" || p.N != 2 || p.Value == nil || *p.Value != 22 {
		t.Fatalf("7d previous: %+v", p)
	}
	wantCoverage(t, "7d previous", w7.Previous.Coverage, "day", 7, 7, 7, "2026-09-25", true)

	// 30 days: 9 Sep – 8 Oct starts before coverage (11 Sep).
	w30 := metricWindow(t, wall, 30)
	wantWindow(t, wall, 30, "partial", 4, f64(16), f64(16), f64(22.8))
	wantCoverage(t, "30d", w30.Coverage, "day", 30, 28, 28, "2026-09-11", false)
	// The 30 days before (10 Aug – 8 Sep) hold one fact from before coverage.
	if p := w30.Previous; p.Status != "partial" || p.N != 1 || p.Value == nil || *p.Value != 30 {
		t.Fatalf("30d previous: %+v", p)
	}
	wantCoverage(t, "30d previous", w30.Previous.Coverage, "day", 30, 0, 0, "", false)

	// 90 days: 11 Jul – 8 Oct in 13 weeks ending today; 11 Sep – 8 Oct is 28
	// days in 4 weeks, as the approved design example says.
	w90 := metricWindow(t, wall, 90)
	wantWindow(t, wall, 90, "partial", 5, f64(20), f64(20), f64(27.6))
	wantCoverage(t, "90d", w90.Coverage, "week", 13, 4, 28, "2026-09-11", false)
	if p := w90.Previous; p.Status != "no_data" || p.N != 0 || p.Value != nil {
		t.Fatalf("90d previous: %+v", p)
	}
	wantCoverage(t, "90d previous", w90.Previous.Coverage, "week", 13, 0, 0, "", false)
	wantCoverage(t, "180d", metricWindow(t, wall, 180).Coverage, "week", 26, 4, 28, "2026-09-11", false)
	// 365 days: 9 Oct 2025 – 8 Oct 2026 in 13 calendar months, both edges clipped.
	wantCoverage(t, "365d", metricWindow(t, wall, 365).Coverage, "month", 13, 2, 28, "2026-09-11", false)

	series := map[int][]MetricBucket{}
	for _, b := range wall.Series {
		series[b.Days] = append(series[b.Days], b)
	}
	if len(series[90]) != 13 || len(series[180]) != 26 || len(series[365]) != 13 || len(wall.Series) != 52 {
		t.Fatalf("series sizes: 90=%d 180=%d 365=%d", len(series[90]), len(series[180]), len(series[365]))
	}
	edge := func(b MetricBucket, from, to, bucket string) {
		t.Helper()
		if b.From != from || b.To != to || b.Bucket != bucket {
			t.Fatalf("bucket %+v, want %s – %s (%s)", b, from, to, bucket)
		}
	}
	edge(series[90][0], "2026-07-11", "2026-07-16", "week")
	edge(series[90][12], "2026-10-02", "2026-10-08", "week")
	edge(series[180][0], "2026-04-12", "2026-04-16", "week")
	edge(series[365][0], "2025-10-09", "2025-10-31", "month")
	edge(series[365][12], "2026-10-01", "2026-10-08", "month")
	for _, b := range wall.Series {
		if b.N == 0 && (b.Status != "no_data" || b.Value != nil || b.P50 != nil || b.P90 != nil) {
			t.Fatalf("empty bucket must be no_data with null values: %+v", b)
		}
	}
	month := func(from string) MetricBucket {
		for _, b := range series[365] {
			if b.From == from {
				return b
			}
		}
		t.Fatalf("no month from %s", from)
		return MetricBucket{}
	}
	// August holds only the fact from before coverage: partial, not ok.
	if b := month("2026-08-01"); b.Status != "partial" || b.N != 1 || *b.Value != 30 {
		t.Fatalf("August: %+v", b)
	}
	if b := month("2026-09-01"); b.Status != "partial" || b.N != 2 || *b.Value != 22 {
		t.Fatalf("September starts before coverage: %+v", b)
	}
	if b := month("2026-10-01"); b.Status != "ok" || b.N != 2 || *b.Value != 11 {
		t.Fatalf("October: %+v", b)
	}
	if b := month("2026-07-01"); b.Status != "no_data" {
		t.Fatalf("July without facts: %+v", b)
	}
	if wall.Status != "partial" {
		t.Fatalf("the metric status follows the 30-day window: %s", wall.Status)
	}
}

func TestDeliveryMetricsReadCutKeepsShortWindowsWhole(t *testing.T) {
	// Risk: the bounded read of two years of facts either drops old facts
	// silently or marks every window partial, though only old days are cut.
	ci := ".github/workflows/ci.yml"
	covered := metricNow.AddDate(-2, 0, 0)
	readFrom := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	run := metricFixtureRun(1, 1, ci, "pull_request", "work/a", strings.Repeat("1", 40), metricNow.Add(-2*time.Hour), 0, 9, "success")
	in := metricInput{CIWorkflow: ci, NightlyWorkflow: ".github/workflows/nightly-full.yml", Covered: &covered, ReadFrom: &readFrom, Runs: []metricRun{run}}
	wall := metricByKey(t, computeMetrics(in, metricNow), "pr_ci_wall")
	wantWindow(t, wall, 7, "ok", 1, f64(9), f64(9), f64(9))
	wantWindow(t, wall, 30, "ok", 1, f64(9), f64(9), f64(9))
	wantWindow(t, wall, 90, "partial", 1, f64(9), f64(9), f64(9))
	wantCoverage(t, "90d", metricWindow(t, wall, 90).Coverage, "week", 13, 6, 38, "2026-09-01", false)
	if wall.Status != "ok" || wall.Reason != nil {
		t.Fatalf("a cut before the 30-day window keeps the metric ok: %+v", wall)
	}
	later := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	in.ReadFrom = &later
	wall = metricByKey(t, computeMetrics(in, metricNow), "pr_ci_wall")
	wantWindow(t, wall, 30, "partial", 1, f64(9), f64(9), f64(9))
	if wall.Status != "partial" || wall.Reason == nil || !strings.Contains(*wall.Reason, "more facts than one read returns") || !strings.Contains(*wall.Reason, "2026-10-01 00:00") {
		t.Fatalf("a cut inside the 30-day window says why: %+v", wall.Reason)
	}
}

func TestDeliveryMetricsReleaseFactsAndGreenTarget(t *testing.T) {
	// Risk: the page names the wrong release, invents a live time for a
	// failed release, or shows no target for green on the first try.
	started := func(t time.Time) *time.Time { return &t }
	name, tag := "126", "v261008161926.0.0"
	healthy := metricNow.Add(-2 * time.Hour)
	in := metricInput{CIWorkflow: ".github/workflows/ci.yml", NightlyWorkflow: ".github/workflows/nightly-full.yml", Marks: []metricMark{
		{Kind: "release", Key: "125", Started: started(metricNow.Add(-50 * time.Hour)), At: metricNow.Add(-49 * time.Hour), Outcome: "failed"},
		{Kind: "release", Key: "261008161926.0.0", Release: &name, Tag: &tag, Started: started(metricNow.Add(-4 * time.Hour)), At: metricNow.Add(-3 * time.Hour), Healthy: &healthy, Outcome: "live"},
		// Older than the longest window: not listed.
		{Kind: "release", Key: "old", Started: started(metricNow.AddDate(-1, 0, -2)), At: metricNow.AddDate(-1, 0, -1), Outcome: "live"},
		{Kind: "merge_round", Key: "r1", At: metricNow.Add(-time.Hour), Outcome: "model"},
	}}
	metrics := computeMetrics(in, metricNow)
	release := metricByKey(t, metrics, "release_queue_to_live")
	if release.Releases == nil || len(*release.Releases) != 2 {
		t.Fatalf("releases: %+v", release.Releases)
	}
	newest, failed := (*release.Releases)[0], (*release.Releases)[1]
	if newest.Key != "261008161926.0.0" || newest.Release == nil || *newest.Release != "126" || newest.Tag == nil || *newest.Tag != tag ||
		!newest.QueuedAt.Equal(metricNow.Add(-4*time.Hour)) || newest.LiveAt == nil || !newest.LiveAt.Equal(metricNow.Add(-3*time.Hour)) || newest.HealthyAt == nil || !newest.HealthyAt.Equal(healthy) {
		t.Fatalf("newest release: %+v", newest)
	}
	if failed.Key != "125" || failed.Outcome != "failed" || failed.LiveAt != nil || failed.HealthyAt != nil || failed.Release != nil {
		t.Fatalf("failed release: %+v", failed)
	}
	for _, m := range metrics {
		if m.Key != "release_queue_to_live" && m.Releases != nil {
			t.Fatalf("%s carries releases", m.Key)
		}
	}
	green := metricByKey(t, metrics, "first_attempt_green")
	if green.Target == nil || *green.Target != (MetricTarget{Value: 80, Direction: "min", Note: "D4″", Source: "Arion"}) {
		t.Fatalf("green target: %+v", green.Target)
	}
	// On the wire, every window names coverage and previous, and releases
	// appear only on the release metric.
	raw, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	var wire []map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, m := range wire {
		if _, ok := m["series"]; !ok {
			t.Fatalf("%v without series", m["key"])
		}
		if _, ok := m["releases"]; ok != (m["key"] == "release_queue_to_live") {
			t.Fatalf("%v releases present %v", m["key"], ok)
		}
		for _, w := range m["windows"].([]any) {
			window := w.(map[string]any)
			if _, ok := window["coverage"]; !ok {
				t.Fatalf("%v window without coverage", m["key"])
			}
			if _, ok := window["previous"]; !ok {
				t.Fatalf("%v window without previous", m["key"])
			}
		}
	}
}

func TestDeliveryMetricsReleaseFactsRoundTrip(t *testing.T) {
	// Risk: the release name, tag or healthy time is dropped on the way to
	// the page, a later correction is ignored, or release fields slip onto
	// facts of another kind.
	f, _ := newMetricsFixture(t)
	path := "/api/projects/" + f.project + "/delivery/metrics/facts"
	queued, live := f.at.Add(-2*time.Hour), f.at.Add(-50*time.Minute)
	fact := map[string]any{"kind": "release", "key": "261008161926.0.0", "release": "126", "tag": "v261008161926.0.0", "started_at": queued, "at": live, "outcome": "live"}
	f.call(t, f.person, "POST", path, map[string]any{"facts": []any{fact}}, 200, nil)
	// The release tooling reports healthy later under the same key.
	fact["healthy_at"] = live.Add(15 * time.Minute)
	f.call(t, f.person, "POST", path, map[string]any{"facts": []any{fact}}, 200, nil)
	for _, bad := range []map[string]any{
		{"kind": "merge_round", "key": "r1", "pull_request": 7, "at": live, "outcome": "model", "tag": "v1"},
		{"kind": "release", "key": "r2", "started_at": queued, "at": live, "outcome": "failed", "healthy_at": live.Add(time.Minute)},
		{"kind": "release", "key": "r3", "started_at": queued, "at": live, "outcome": "live", "healthy_at": live.Add(-time.Minute)},
		{"kind": "release", "key": "r4", "started_at": queued, "at": live, "outcome": "live", "release": "126 beta"},
	} {
		f.call(t, f.person, "POST", path, map[string]any{"facts": []any{bad}}, 400, nil)
	}
	var out DeliveryMetrics
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/metrics", nil, 200, &out)
	release := metricByKey(t, out.Metrics, "release_queue_to_live")
	if release.Releases == nil || len(*release.Releases) != 1 {
		t.Fatalf("releases: %+v", release.Releases)
	}
	got := (*release.Releases)[0]
	if got.Release == nil || *got.Release != "126" || got.Tag == nil || *got.Tag != "v261008161926.0.0" || !got.QueuedAt.Equal(queued) ||
		got.LiveAt == nil || !got.LiveAt.Equal(live) || got.HealthyAt == nil || !got.HealthyAt.Equal(live.Add(15*time.Minute)) {
		t.Fatalf("release fact: %+v", got)
	}
	// Reports start with this release, so the window is partial from 6 Oct on.
	wantWindow(t, release, 7, "partial", 1, f64(70), f64(70), f64(70))
	if w := metricWindow(t, release, 7); w.Previous == nil || w.Previous.Status != "no_data" || w.Coverage.From == nil || *w.Coverage.From != "2026-10-06" {
		t.Fatalf("7-day window: %+v previous %+v", w, w.Previous)
	}
}
