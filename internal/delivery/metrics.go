// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"time"
)

// Delivery metrics (AEON-993) follow the Project Arion measurement contract:
// first-attempt wall and time to green are separate numbers, every number
// names its source and window, and missing data is never reported as zero.

const (
	metricDays      = 30
	metricShortDays = 7
	metricSlack     = 7 * 24 * time.Hour
)

type metricRun struct {
	ID         int64
	Attempt    int
	Workflow   string
	Name       string
	Event      string
	Branch     string
	Head       string
	PR         *int64
	Created    time.Time
	Started    time.Time
	Completed  time.Time
	Conclusion string
	Source     string
}

type metricPull struct {
	Number int64
	Branch string
	Opened time.Time
	Merged *time.Time
	Closed *time.Time
	Source string
}

type metricMark struct {
	Kind    string
	Key     string
	PR      *int64
	Head    string
	Started *time.Time
	At      time.Time
	Outcome string
	Source  string
}

// metricInput is everything one project's metrics are computed from. Covered
// is the earliest time from which run and pull facts are complete: the
// finished backfill's start, else the first fact the webhook recorded.
// CoveredUntil, when set, is the last instant those facts are known to be
// complete. Nil means coverage runs through now.
type metricInput struct {
	Runs            []metricRun
	Pulls           []metricPull
	Marks           []metricMark
	CIWorkflow      string
	NightlyWorkflow string
	Covered         *time.Time
	CoveredUntil    *time.Time
	Truncated       bool
}

type MetricSample struct {
	Value float64   `json:"value"`
	At    time.Time `json:"at"`
}

type MetricWindow struct {
	Days   int      `json:"days"`
	Status string   `json:"status"`
	N      int      `json:"n"`
	Value  *float64 `json:"value"`
	P50    *float64 `json:"p50"`
	P90    *float64 `json:"p90"`
}

type MetricPoint struct {
	Date   string   `json:"date"`
	Status string   `json:"status"`
	N      int      `json:"n"`
	Value  *float64 `json:"value"`
	P50    *float64 `json:"p50"`
	P90    *float64 `json:"p90"`
}

type MetricTarget struct {
	Value     float64 `json:"value"`
	Direction string  `json:"direction"`
	Note      string  `json:"note"`
	Source    string  `json:"source"`
}

type Metric struct {
	Number     int            `json:"number"`
	Key        string         `json:"key"`
	Label      string         `json:"label"`
	Unit       string         `json:"unit"`
	Source     string         `json:"source"`
	Definition string         `json:"definition"`
	Status     string         `json:"status"`
	Reason     *string        `json:"reason"`
	Latest     *MetricSample  `json:"latest"`
	Windows    []MetricWindow `json:"windows"`
	Daily      []MetricPoint  `json:"daily"`
	Target     *MetricTarget  `json:"target"`
}

// metricSpec describes how one sample list becomes a window value. Duration
// and count metrics report p50 as their value, except mean counts; rate
// metrics report the share of true samples in percent.
type metricSpec struct {
	number                        int
	key, label, unit, source, def string
	aggregate                     string // "p50", "mean" or "share"
	target                        *MetricTarget
	coverage, coverageEnd         *time.Time
	partialReason, noDataReason   string
	alwaysPartial                 bool
}

type metricPoint struct {
	at    time.Time
	value float64
}

func arion(value float64, direction, note string) *MetricTarget {
	return &MetricTarget{Value: value, Direction: direction, Note: note, Source: "Project Arion"}
}

// percentile interpolates linearly between ranks, as measure.py does.
func percentile(values []float64, p float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	xs := append([]float64(nil), values...)
	sort.Float64s(xs)
	k := float64(len(xs)-1) * p
	f := int(k)
	c := min(f+1, len(xs)-1)
	v := round1(xs[f] + (xs[c]-xs[f])*(k-float64(f)))
	return &v
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func minutes(from, to time.Time) float64 { return to.Sub(from).Minutes() }

func dayStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (s metricSpec) summarize(values []float64) (value, p50, p90 *float64) {
	if len(values) == 0 {
		return nil, nil, nil
	}
	switch s.aggregate {
	case "share":
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		v := round1(100 * sum / float64(len(values)))
		return &v, nil, nil
	case "mean":
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		v := math.Round(100*sum/float64(len(values))) / 100
		return &v, percentile(values, .5), percentile(values, .9)
	}
	p50, p90 = percentile(values, .5), percentile(values, .9)
	return p50, p50, p90
}

// incomplete reports a window that starts before coverage, ends after coverage,
// or belongs to an incomplete source.
func (s metricSpec) incomplete(start, end time.Time, truncated bool) bool {
	if s.alwaysPartial || truncated || s.coverage == nil || s.coverage.After(start) {
		return true
	}
	return s.coverageEnd != nil && s.coverageEnd.Before(end)
}

// build turns timestamped samples into windows and a daily series. A window
// that starts before the covered time, ends after CoveredUntil, or belongs to
// an incomplete source is partial; an empty window is "no_data", never zero.
func (s metricSpec) build(now time.Time, samples []metricPoint, truncated bool) Metric {
	out := Metric{Number: s.number, Key: s.key, Label: s.label, Unit: s.unit, Source: s.source, Definition: s.def, Target: s.target, Windows: []MetricWindow{}, Daily: []MetricPoint{}}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })
	for _, sample := range samples {
		if !sample.at.After(now) && !sample.at.Before(now.Add(-metricDays*24*time.Hour)) {
			value := sample.value
			if s.aggregate == "share" {
				value *= 100
			}
			out.Latest = &MetricSample{Value: round1(value), At: sample.at}
		}
	}
	reasons := []string{}
	windowStart := now.Add(-metricDays * 24 * time.Hour)
	for _, days := range []int{metricShortDays, metricDays} {
		start := now.Add(-time.Duration(days) * 24 * time.Hour)
		values := []float64{}
		for _, sample := range samples {
			if !sample.at.Before(start) && !sample.at.After(now) {
				values = append(values, sample.value)
			}
		}
		w := MetricWindow{Days: days, Status: "ok", N: len(values)}
		w.Value, w.P50, w.P90 = s.summarize(values)
		if len(values) == 0 {
			w.Status = "no_data"
		} else if s.incomplete(start, now, truncated) {
			w.Status = "partial"
		}
		out.Windows = append(out.Windows, w)
	}
	first := dayStart(now).AddDate(0, 0, -(metricDays - 1))
	for i := range metricDays {
		day := first.AddDate(0, 0, i)
		values := []float64{}
		for _, sample := range samples {
			if !sample.at.Before(day) && sample.at.Before(day.AddDate(0, 0, 1)) && !sample.at.After(now) {
				values = append(values, sample.value)
			}
		}
		p := MetricPoint{Date: day.Format("2006-01-02"), Status: "ok", N: len(values)}
		p.Value, p.P50, p.P90 = s.summarize(values)
		if len(values) == 0 {
			p.Status = "no_data"
		} else if s.incomplete(day, day.AddDate(0, 0, 1), truncated) {
			p.Status = "partial"
		}
		out.Daily = append(out.Daily, p)
	}
	long := out.Windows[len(out.Windows)-1]
	switch {
	case long.N == 0:
		out.Status = "no_data"
		reasons = append(reasons, s.noDataReason)
	case s.incomplete(windowStart, now, truncated):
		out.Status = "partial"
		if truncated {
			reasons = append(reasons, "Some facts are missing: a day had more runs than GitHub lists (1 000), or the window holds more facts than one read returns.")
		}
		if s.alwaysPartial {
			reasons = append(reasons, s.partialReason)
		} else if s.coverage == nil || s.coverage.After(windowStart) {
			reasons = append(reasons, coverageReason(s.coverage))
		}
		if s.coverageEnd != nil && s.coverageEnd.Before(now) {
			reasons = append(reasons, coverageEndReason(*s.coverageEnd))
		}
	default:
		out.Status = "ok"
	}
	if len(reasons) > 0 {
		reason := reasons[0]
		for _, r := range reasons[1:] {
			reason += " " + r
		}
		out.Reason = &reason
	}
	return out
}

func coverageReason(covered *time.Time) string {
	if covered == nil {
		return "History is not complete: no backfill has finished yet."
	}
	return fmt.Sprintf("Facts are complete only since %s UTC; earlier days are missing until the backfill finishes.", covered.UTC().Format("2006-01-02 15:04"))
}

func coverageEndReason(end time.Time) string {
	return fmt.Sprintf("GitHub facts are complete only through %s UTC; later time is unobserved.", end.UTC().Format("2006-01-02 15:04"))
}

func sameWorkflow(run metricRun, workflow string) bool {
	return run.Workflow == workflow || path.Base(run.Workflow) == path.Base(workflow)
}

func failed(conclusion string) bool {
	return conclusion == "failure" || conclusion == "timed_out" || conclusion == "startup_failure"
}

// computeMetrics derives the ten delivery numbers. It is pure: tests pass
// fixed facts and an injected now.
func computeMetrics(in metricInput, now time.Time) []Metric {
	now = now.UTC()
	ci := path.Base(in.CIWorkflow)
	nightly := path.Base(in.NightlyWorkflow)
	runSource := func(event string) string {
		return fmt.Sprintf("GitHub Actions workflow %s, event %s (GitHub App check suites and backfill)", ci, event)
	}
	noRuns := "No completed " + ci + " runs in the last 30 days."
	out := []Metric{}

	// Attempt-1 runs per event, and every attempt for flake and green lookups.
	firstAttempts := map[string][]metricRun{}
	byRun := map[int64][]metricRun{}
	bySHA := map[string][]metricRun{}
	for _, run := range in.Runs {
		if !sameWorkflow(run, in.CIWorkflow) {
			continue
		}
		byRun[run.ID] = append(byRun[run.ID], run)
		bySHA[run.Event+"/"+run.Head] = append(bySHA[run.Event+"/"+run.Head], run)
		if run.Attempt == 1 && run.Conclusion != "skipped" {
			firstAttempts[run.Event] = append(firstAttempts[run.Event], run)
		}
	}

	// 1 and 2: first-attempt wall per run.
	wall := func(event string) []metricPoint {
		points := []metricPoint{}
		for _, run := range firstAttempts[event] {
			if run.Conclusion == "cancelled" || run.Started.IsZero() || run.Completed.Before(run.Started) {
				continue
			}
			points = append(points, metricPoint{run.Completed, minutes(run.Started, run.Completed)})
		}
		return points
	}
	out = append(out, metricSpec{number: 1, key: "pr_ci_wall", label: "PR CI duration (wall)", unit: "minutes", source: runSource("pull_request"),
		def:       "First attempt only: run started → run completed, per pull_request run. Runner wait before the start, re-runs and cancelled (superseded) runs are not included; time to green is number 4.",
		aggregate: "p50", target: arion(5, "max", "8 after Phase 2, 5 after Phase 3"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: noRuns}.build(now, wall("pull_request"), in.Truncated))
	out = append(out, metricSpec{number: 2, key: "queue_run_wall", label: "Merge-queue run duration (wall)", unit: "minutes", source: runSource("merge_group"),
		def:       "First attempt only: run started → run completed, per merge_group run; cancelled runs are not included.",
		aggregate: "p50", target: arion(7, "max", "3 on a reuse hit"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No completed " + ci + " merge-queue runs in the last 30 days."}.build(now, wall("merge_group"), in.Truncated))

	// 3: first-attempt green rate, and failures that only flaked.
	green, flaked := []metricPoint{}, []metricPoint{}
	for _, run := range firstAttempts["pull_request"] {
		if run.Conclusion == "cancelled" || run.Conclusion == "neutral" || run.Conclusion == "action_required" || run.Conclusion == "stale" {
			continue
		}
		ok := 0.0
		if run.Conclusion == "success" {
			ok = 1
		}
		green = append(green, metricPoint{run.Completed, ok})
		flake := 0.0
		if failed(run.Conclusion) {
			for _, later := range append(append([]metricRun{}, byRun[run.ID]...), bySHA[run.Event+"/"+run.Head]...) {
				if later.Conclusion == "success" && later.Completed.After(run.Completed) {
					flake = 1
					break
				}
			}
		}
		flaked = append(flaked, metricPoint{run.Completed, flake})
	}
	out = append(out, metricSpec{number: 3, key: "first_attempt_green", label: "First-attempt green rate", unit: "percent", source: runSource("pull_request"),
		def:       "Share of first attempts of pull_request runs that concluded success. Cancelled runs (superseded by a newer push) are left out.",
		aggregate: "share", coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: noRuns}.build(now, green, in.Truncated))
	out = append(out, metricSpec{number: 3, key: "flaked_failures", label: "Failures that only flaked", unit: "percent", source: runSource("pull_request"),
		def:       "Share of first attempts of pull_request runs that failed although a re-run of the same run, or another run on the same head commit, later succeeded without a code change. This is the false-failure rate of the Arion contract, without infra classification.",
		aggregate: "share", target: arion(5, "max", "False failures ≤ 10 % after 2 weeks, ≤ 5 % after 4 (WP1.1)"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: noRuns}.build(now, flaked, in.Truncated))

	// 4: time to first green per pull request branch.
	branches := map[string][]metricRun{}
	for _, run := range in.Runs {
		if sameWorkflow(run, in.CIWorkflow) && run.Event == "pull_request" && run.Branch != "" {
			branches[run.Branch] = append(branches[run.Branch], run)
		}
	}
	toGreen := []metricPoint{}
	for _, runs := range branches {
		sort.Slice(runs, func(i, j int) bool { return runs[i].Created.Before(runs[j].Created) })
		var firstGreen *metricRun
		for i := range runs {
			if runs[i].Conclusion == "success" && (firstGreen == nil || runs[i].Completed.Before(firstGreen.Completed)) {
				firstGreen = &runs[i]
			}
		}
		if firstGreen != nil {
			toGreen = append(toGreen, metricPoint{firstGreen.Completed, minutes(runs[0].Created, firstGreen.Completed)})
		}
	}
	out = append(out, metricSpec{number: 4, key: "time_to_first_green", label: "Time to first green", unit: "minutes", source: runSource("pull_request"),
		def:       "Per pull request branch: first run created → first successful run completed, including runner wait, failed runs and re-runs. Branches that never went green are not counted.",
		aggregate: "p50", target: arion(12, "max", ""), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No pull request branch reached a green " + ci + " run in the last 30 days."}.build(now, toGreen, in.Truncated))

	// 5: pull request opened → merged.
	merged := []metricPoint{}
	for _, pull := range in.Pulls {
		if pull.Merged != nil && !pull.Merged.Before(pull.Opened) {
			merged = append(merged, metricPoint{*pull.Merged, minutes(pull.Opened, *pull.Merged)})
		}
	}
	out = append(out, metricSpec{number: 5, key: "pr_open_to_merged", label: "PR opened → merged", unit: "minutes", source: "GitHub pull requests (GitHub App pull_request events and backfill)",
		def:       "Per merged pull request: created → merged, counted on the merge day.",
		aggregate: "p50", target: arion(40, "max", ""), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No pull request was merged in the last 30 days."}.build(now, merged, in.Truncated))

	// 6: merge-queue runs per pull request.
	queued := map[int64][]metricRun{}
	for _, run := range in.Runs {
		if !sameWorkflow(run, in.CIWorkflow) || run.Event != "merge_group" {
			continue
		}
		if match := queueBranch.FindStringSubmatch(run.Branch); match != nil {
			if n, err := strconv.ParseInt(match[1], 10, 64); err == nil {
				queued[n] = append(queued[n], run)
			}
		}
	}
	perPR := []metricPoint{}
	for _, runs := range queued {
		last := runs[0].Completed
		for _, run := range runs {
			if run.Completed.After(last) {
				last = run.Completed
			}
		}
		perPR = append(perPR, metricPoint{last, float64(len(runs))})
	}
	out = append(out, metricSpec{number: 6, key: "queue_runs_per_pr", label: "Queue runs per PR", unit: "runs", source: runSource("merge_group"),
		def:       "Per pull request named in the merge-queue branch (gh-readonly-queue/…/pr-N-…): every queue run attempt, counted on the day of its last run. The value is the mean; a group run counts for the PR its branch names.",
		aggregate: "mean", target: arion(1.1, "max", "0.1 extra queue runs per PR"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No merge-queue runs in the last 30 days."}.build(now, perPR, in.Truncated))

	// 7: review time and the share of "changes" verdicts.
	reviewTime, changes := []metricPoint{}, []metricPoint{}
	var reviewCovered *time.Time
	reported := map[string]bool{}
	for _, mark := range in.Marks {
		if mark.Kind != "review" || mark.Started == nil {
			continue
		}
		if mark.Head != "" {
			reported[mark.Head] = true
		}
		reviewTime = append(reviewTime, metricPoint{mark.At, minutes(*mark.Started, mark.At)})
		changes = append(changes, metricPoint{mark.At, boolValue(mark.Outcome == "changes")})
		reviewCovered = earlier(reviewCovered, mark.At)
	}
	statuses := map[string]map[string]time.Time{}
	for _, mark := range in.Marks {
		if mark.Kind == "review_status" && !reported[mark.Head] {
			if statuses[mark.Head] == nil {
				statuses[mark.Head] = map[string]time.Time{}
			}
			statuses[mark.Head][mark.Outcome] = mark.At
			reviewCovered = earlier(reviewCovered, mark.At)
		}
	}
	for _, states := range statuses {
		pending, ok := states["pending"]
		var verdict *time.Time
		outcome := ""
		for _, state := range []string{"success", "failure"} {
			if at, found := states[state]; found && !at.Before(pending) && (verdict == nil || at.Before(*verdict)) {
				v := at
				verdict, outcome = &v, state
			}
		}
		if verdict == nil {
			continue
		}
		if ok {
			reviewTime = append(reviewTime, metricPoint{*verdict, minutes(pending, *verdict)})
		}
		changes = append(changes, metricPoint{*verdict, boolValue(outcome == "failure")})
	}
	reviewPartial := "Counts only reviews that post the aeon/review commit status or are reported through POST …/delivery/metrics/facts; gates that run elsewhere and are not reported are missing."
	reviewNone := "No review verdict was seen in the last 30 days: no aeon/review status and no reported review."
	out = append(out, metricSpec{number: 7, key: "review_time", label: "Review time", unit: "minutes", source: "aeon/review commit status (GitHub App status events) and reported reviews",
		def:       "Per reviewed head: review requested (aeon/review pending, or the reported start) → first verdict.",
		aggregate: "p50", target: arion(8, "max", "First review round (WP1.8)"), coverage: reviewCovered, alwaysPartial: true, partialReason: reviewPartial, noDataReason: reviewNone}.build(now, reviewTime, false))
	out = append(out, metricSpec{number: 7, key: "review_changes_share", label: "Share of “changes” verdicts", unit: "percent", source: "aeon/review commit status (GitHub App status events) and reported reviews",
		def:       "Share of first verdicts per head that asked for changes (aeon/review failure, or a reported “changes”).",
		aggregate: "share", target: arion(25, "max", "WP1.8"), coverage: reviewCovered, alwaysPartial: true, partialReason: reviewPartial, noDataReason: reviewNone}.build(now, changes, false))

	// 8, 9: reported facts only; coverage starts with the first report.
	rounds, releases := []metricPoint{}, []metricPoint{}
	var roundsCovered, releasesCovered *time.Time
	for _, mark := range in.Marks {
		switch mark.Kind {
		case "merge_round":
			rounds = append(rounds, metricPoint{mark.At, boolValue(mark.Outcome == "model")})
			roundsCovered = earlier(roundsCovered, mark.At)
		case "release":
			if mark.Started != nil && mark.Outcome == "live" {
				releases = append(releases, metricPoint{mark.At, minutes(*mark.Started, mark.At)})
			}
			releasesCovered = earlier(releasesCovered, mark.At)
		}
	}
	out = append(out, metricSpec{number: 8, key: "merge_rounds_model_share", label: "Merge rounds solved by a model", unit: "percent", source: "Merge rounds reported through POST …/delivery/metrics/facts (kind merge_round)",
		def:       "Share of merge rounds (a PR conflicting with a moved main) that needed a model instead of the scripted merge. PAIMOS cannot see merge rounds itself; it counts what the merge tooling reports.",
		aggregate: "share", target: arion(20, "max", "WP1.7"), coverage: roundsCovered, noDataReason: "No merge round has been reported yet."}.build(now, rounds, false))
	out = append(out, metricSpec{number: 9, key: "release_queue_to_live", label: "Release: merge queue → live", unit: "minutes", source: "Releases reported through POST …/delivery/metrics/facts (kind release)",
		def:       "Per release: the release PR enters the merge queue → the version is live on the production host, as reported by the release tooling. PAIMOS cannot observe the host itself.",
		aggregate: "p50", target: arion(24, "max", "~12 with blue/green and a canary"), coverage: releasesCovered, noDataReason: "No release has been reported yet."}.build(now, releases, false))

	// 10: nightly full run, one verdict per night.
	nights := map[string]metricRun{}
	finals := map[int64]metricRun{}
	for _, run := range in.Runs {
		if !sameWorkflow(run, in.NightlyWorkflow) {
			continue
		}
		if old, ok := finals[run.ID]; !ok || run.Attempt > old.Attempt {
			finals[run.ID] = run
		}
	}
	for _, run := range finals {
		night := dayStart(run.Created).Format("2006-01-02")
		old, ok := nights[night]
		scheduled := run.Event == "schedule"
		if !ok || scheduled && old.Event != "schedule" || scheduled == (old.Event == "schedule") && run.Created.Before(old.Created) {
			nights[night] = run
		}
	}
	nightPoints := []metricPoint{}
	for _, run := range nights {
		nightPoints = append(nightPoints, metricPoint{run.Created, boolValue(run.Conclusion == "success")})
	}
	night := metricSpec{number: 10, key: "nightly_green", label: "Nightly full run green", unit: "percent", source: fmt.Sprintf("GitHub Actions workflow %s (GitHub App check suites and backfill)", nightly),
		def:       "One verdict per night (UTC day the run was created): the scheduled run's last attempt, else the night's first run; green only on success. The value is the share of green nights; nights without a run are not counted.",
		aggregate: "share", target: arion(100, "min", "7 green nights in a row (WP1.2)"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No completed " + nightly + " run in the last 30 days."}.build(now, nightPoints, in.Truncated)
	out = append(out, night)
	return out
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func earlier(current *time.Time, at time.Time) *time.Time {
	if current == nil || at.Before(*current) {
		v := at
		return &v
	}
	return current
}

func later(current *time.Time, at time.Time) *time.Time {
	if current == nil || at.After(*current) {
		v := at
		return &v
	}
	return current
}
