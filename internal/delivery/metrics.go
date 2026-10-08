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
	metricLongDays  = 365
	metricSlack     = 7 * 24 * time.Hour
	// metricReleaseLimit bounds the release facts one metric carries.
	metricReleaseLimit = 50
)

// metricWindowDays are the windows of the page's window switch (AEON-1001).
// Every window is whole UTC days ending today, so its value, coverage and
// chart buckets describe the same days; previous is the window just before.
var metricWindowDays = []int{metricShortDays, metricDays, 90, 180, metricLongDays}

// metricSeriesDays are the windows that get a bucketed series; 7 and 30
// days read the daily points.
var metricSeriesDays = []int{90, 180, metricLongDays}

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
	// Release facts (AEON-1001), reported with kind release only.
	Release *string
	Tag     *string
	Healthy *time.Time
}

// metricInput is everything one project's metrics are computed from. Covered
// is the earliest time from which run and pull facts are complete: the
// finished backfill's start, else the first fact the webhook recorded.
// CoveredUntil, when set, is the last instant those facts are known to be
// complete. Nil means coverage runs through now. ReadFrom, when set, is the
// earliest time the bounded read still returns every fact; older facts were
// cut by the read bound.
type metricInput struct {
	Runs            []metricRun
	Pulls           []metricPull
	Marks           []metricMark
	CIWorkflow      string
	NightlyWorkflow string
	Covered         *time.Time
	CoveredUntil    *time.Time
	ReadFrom        *time.Time
	Truncated       bool
}

type MetricSample struct {
	Value float64   `json:"value"`
	At    time.Time `json:"at"`
}

// MetricCoverage says how much of a window the source observed. A day is
// covered when any part of it lies inside the time the source's facts are
// complete; a bucket is covered when any of its days is.
type MetricCoverage struct {
	WindowDays     int     `json:"window_days"`
	Bucket         string  `json:"bucket"`
	BucketsTotal   int     `json:"buckets_total"`
	BucketsCovered int     `json:"buckets_covered"`
	CoveredDays    int     `json:"covered_days"`
	From           *string `json:"from"`
	Full           bool    `json:"full"`
}

// MetricPrevious is the same-length window just before a window.
type MetricPrevious struct {
	Status   string         `json:"status"`
	N        int            `json:"n"`
	Value    *float64       `json:"value"`
	P50      *float64       `json:"p50"`
	P90      *float64       `json:"p90"`
	Coverage MetricCoverage `json:"coverage"`
}

type MetricWindow struct {
	Days     int             `json:"days"`
	Status   string          `json:"status"`
	N        int             `json:"n"`
	Value    *float64        `json:"value"`
	P50      *float64        `json:"p50"`
	P90      *float64        `json:"p90"`
	Coverage MetricCoverage  `json:"coverage"`
	Previous *MetricPrevious `json:"previous"`
}

type MetricPoint struct {
	Date   string   `json:"date"`
	Status string   `json:"status"`
	N      int      `json:"n"`
	Value  *float64 `json:"value"`
	P50    *float64 `json:"p50"`
	P90    *float64 `json:"p90"`
}

// MetricBucket is one chart point of a long window: a week (90, 180 days)
// or a calendar month (365 days), clipped to the window. From and To are
// inclusive UTC dates.
type MetricBucket struct {
	Days   int      `json:"days"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Bucket string   `json:"bucket"`
	Status string   `json:"status"`
	N      int      `json:"n"`
	Value  *float64 `json:"value"`
	P50    *float64 `json:"p50"`
	P90    *float64 `json:"p90"`
}

// ReleaseFact is one release as the release tooling reported it.
type ReleaseFact struct {
	Key       string     `json:"key"`
	Release   *string    `json:"release"`
	Tag       *string    `json:"tag"`
	Outcome   string     `json:"outcome"`
	QueuedAt  time.Time  `json:"queued_at"`
	LiveAt    *time.Time `json:"live_at"`
	HealthyAt *time.Time `json:"healthy_at"`
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
	Series     []MetricBucket `json:"series"`
	Target     *MetricTarget  `json:"target"`
	Releases   *[]ReleaseFact `json:"releases,omitempty"`
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
	readFrom                      *time.Time
	partialReason, noDataReason   string
	alwaysPartial                 bool
}

type metricPoint struct {
	at    time.Time
	value float64
}

// metricSpan is a run of whole UTC days, first and last inclusive.
type metricSpan struct{ first, last time.Time }

func (s metricSpan) end() time.Time { return s.last.AddDate(0, 0, 1) }

func (s metricSpan) days() int { return int(s.end().Sub(s.first) / (24 * time.Hour)) }

// windowSpan is the window of days ending today; back shifts it to the
// window just before.
func windowSpan(now time.Time, days int, back bool) metricSpan {
	last := dayStart(now)
	if back {
		last = last.AddDate(0, 0, -days)
	}
	return metricSpan{last.AddDate(0, 0, -(days - 1)), last}
}

func bucketKind(days int) string {
	switch {
	case days <= metricDays:
		return "day"
	case days <= 180:
		return "week"
	}
	return "month"
}

// buckets cuts a span into chart buckets, oldest first: days; weeks of seven
// days ending on the span's last day; or calendar months. The oldest week and
// both edge months are clipped to the span.
func (s metricSpan) buckets(kind string) []metricSpan {
	out := []metricSpan{}
	switch kind {
	case "day":
		for d := s.first; !d.After(s.last); d = d.AddDate(0, 0, 1) {
			out = append(out, metricSpan{d, d})
		}
	case "week":
		for last := s.last; !last.Before(s.first); last = last.AddDate(0, 0, -7) {
			first := last.AddDate(0, 0, -6)
			if first.Before(s.first) {
				first = s.first
			}
			out = append([]metricSpan{{first, last}}, out...)
		}
	default:
		for m := time.Date(s.first.Year(), s.first.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(s.last); m = m.AddDate(0, 1, 0) {
			b := metricSpan{m, m.AddDate(0, 1, -1)}
			if b.first.Before(s.first) {
				b.first = s.first
			}
			if b.last.After(s.last) {
				b.last = s.last
			}
			out = append(out, b)
		}
	}
	return out
}

func arion(value float64, direction, note string) *MetricTarget {
	return &MetricTarget{Value: value, Direction: direction, Note: note, Source: "Arion"}
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

// covered is the start of complete facts: the source's coverage, moved later
// when the bounded read cut older facts.
func (s metricSpec) covered() *time.Time {
	if s.coverage == nil || s.readFrom == nil || !s.readFrom.After(*s.coverage) {
		return s.coverage
	}
	return s.readFrom
}

// incomplete reports a window that starts before coverage, ends after coverage,
// or belongs to an incomplete source.
func (s metricSpec) incomplete(start, end time.Time, truncated bool) bool {
	covered := s.covered()
	if s.alwaysPartial || truncated || covered == nil || covered.After(start) {
		return true
	}
	return s.coverageEnd != nil && s.coverageEnd.Before(end)
}

// coverageOf counts the covered days and buckets of a span. Days after now
// do not exist yet and are never part of a span.
func (s metricSpec) coverageOf(span metricSpan, windowDays int, now time.Time) MetricCoverage {
	kind := bucketKind(windowDays)
	buckets := span.buckets(kind)
	out := MetricCoverage{WindowDays: windowDays, Bucket: kind, BucketsTotal: len(buckets)}
	covered := s.covered()
	if covered == nil {
		return out
	}
	until := now
	if s.coverageEnd != nil && s.coverageEnd.Before(until) {
		until = *s.coverageEnd
	}
	first, last := dayStart(*covered), dayStart(until)
	if first.Before(span.first) {
		first = span.first
	}
	if last.After(span.last) {
		last = span.last
	}
	if last.Before(first) {
		return out
	}
	from := first.Format("2006-01-02")
	out.From = &from
	out.CoveredDays = metricSpan{first, last}.days()
	for _, b := range buckets {
		if !b.last.Before(first) && !b.first.After(last) {
			out.BucketsCovered++
		}
	}
	out.Full = out.CoveredDays == span.days()
	return out
}

// between returns the values of sorted samples at or after from and before
// to, never after now.
func between(samples []metricPoint, from, to, now time.Time) []float64 {
	if limit := now.Add(time.Nanosecond); limit.Before(to) {
		to = limit
	}
	i := sort.Search(len(samples), func(i int) bool { return !samples[i].at.Before(from) })
	values := []float64{}
	for ; i < len(samples) && samples[i].at.Before(to); i++ {
		values = append(values, samples[i].value)
	}
	return values
}

// measure aggregates one span: no samples are "no_data", never zero; a span
// outside complete coverage or of an incomplete source is "partial".
func (s metricSpec) measure(samples []metricPoint, span metricSpan, now time.Time, truncated bool) (status string, n int, value, p50, p90 *float64) {
	values := between(samples, span.first, span.end(), now)
	value, p50, p90 = s.summarize(values)
	end := span.end()
	if now.Before(end) {
		end = now
	}
	switch {
	case len(values) == 0:
		status = "no_data"
	case s.incomplete(span.first, end, truncated):
		status = "partial"
	default:
		status = "ok"
	}
	return status, len(values), value, p50, p90
}

// build turns timestamped samples into windows, a daily series and bucketed
// long series. A span that starts before the covered time, ends after
// CoveredUntil, or belongs to an incomplete source is partial; an empty span
// is "no_data", never zero. Nothing is extrapolated.
func (s metricSpec) build(now time.Time, samples []metricPoint, truncated bool) Metric {
	out := Metric{Number: s.number, Key: s.key, Label: s.label, Unit: s.unit, Source: s.source, Definition: s.def, Target: s.target, Windows: []MetricWindow{}, Daily: []MetricPoint{}, Series: []MetricBucket{}}
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
	for _, days := range metricWindowDays {
		span := windowSpan(now, days, false)
		w := MetricWindow{Days: days, Coverage: s.coverageOf(span, days, now)}
		w.Status, w.N, w.Value, w.P50, w.P90 = s.measure(samples, span, now, truncated)
		before := windowSpan(now, days, true)
		p := MetricPrevious{Coverage: s.coverageOf(before, days, now)}
		p.Status, p.N, p.Value, p.P50, p.P90 = s.measure(samples, before, now, truncated)
		w.Previous = &p
		out.Windows = append(out.Windows, w)
	}
	for _, day := range windowSpan(now, metricDays, false).buckets("day") {
		p := MetricPoint{Date: day.first.Format("2006-01-02")}
		p.Status, p.N, p.Value, p.P50, p.P90 = s.measure(samples, day, now, truncated)
		out.Daily = append(out.Daily, p)
	}
	for _, days := range metricSeriesDays {
		kind := bucketKind(days)
		for _, b := range windowSpan(now, days, false).buckets(kind) {
			p := MetricBucket{Days: days, From: b.first.Format("2006-01-02"), To: b.last.Format("2006-01-02"), Bucket: kind}
			p.Status, p.N, p.Value, p.P50, p.P90 = s.measure(samples, b, now, truncated)
			out.Series = append(out.Series, p)
		}
	}
	// The metric's own status follows the 30-day window.
	windowStart := windowSpan(now, metricDays, false).first
	var long MetricWindow
	for _, w := range out.Windows {
		if w.Days == metricDays {
			long = w
		}
	}
	covered := s.covered()
	switch {
	case long.N == 0:
		out.Status = "no_data"
		reasons = append(reasons, s.noDataReason)
	case s.incomplete(windowStart, now, truncated):
		out.Status = "partial"
		if truncated {
			reasons = append(reasons, "Some facts are missing: a day had more runs than GitHub lists (1 000).")
		}
		if s.alwaysPartial {
			reasons = append(reasons, s.partialReason)
		} else if covered == nil || covered.After(windowStart) {
			if covered != nil && s.readFrom != nil && covered.Equal(*s.readFrom) && !s.coverage.Equal(*s.readFrom) {
				reasons = append(reasons, readFromReason(*covered))
			} else {
				reasons = append(reasons, coverageReason(covered))
			}
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

func readFromReason(from time.Time) string {
	return fmt.Sprintf("Facts are complete only since %s UTC: the window holds more facts than one read returns.", from.UTC().Format("2006-01-02 15:04"))
}

func coverageEndReason(end time.Time) string {
	return fmt.Sprintf("GitHub facts are complete only through %s UTC; later time is unobserved.", end.UTC().Format("2006-01-02 15:04"))
}

// releaseFacts lists the reported releases of the longest window, newest
// first and bounded.
func releaseFacts(marks []metricMark, now time.Time) []ReleaseFact {
	from := windowSpan(now, metricLongDays, false).first
	out := []ReleaseFact{}
	for _, mark := range marks {
		if mark.Kind != "release" || mark.Started == nil || mark.At.Before(from) || mark.At.After(now) {
			continue
		}
		f := ReleaseFact{Key: mark.Key, Release: mark.Release, Tag: mark.Tag, Outcome: mark.Outcome, QueuedAt: mark.Started.UTC()}
		if mark.Outcome == "live" {
			at := mark.At.UTC()
			f.LiveAt = &at
			if mark.Healthy != nil {
				healthy := mark.Healthy.UTC()
				f.HealthyAt = &healthy
			}
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].QueuedAt, out[j].QueuedAt
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > metricReleaseLimit {
		out = out[:metricReleaseLimit]
	}
	return out
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
		aggregate: "p50", target: arion(5, "max", "8 after Phase 2, 5 after Phase 3"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: noRuns, readFrom: in.ReadFrom}.build(now, wall("pull_request"), in.Truncated))
	out = append(out, metricSpec{number: 2, key: "queue_run_wall", label: "Merge-queue run duration (wall)", unit: "minutes", source: runSource("merge_group"),
		def:       "First attempt only: run started → run completed, per merge_group run; cancelled runs are not included.",
		aggregate: "p50", target: arion(7, "max", "3 on a reuse hit"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No completed " + ci + " merge-queue runs in the last 30 days.", readFrom: in.ReadFrom}.build(now, wall("merge_group"), in.Truncated))

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
		aggregate: "share", target: arion(80, "min", "D4″"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: noRuns, readFrom: in.ReadFrom}.build(now, green, in.Truncated))
	out = append(out, metricSpec{number: 3, key: "flaked_failures", label: "Failures that only flaked", unit: "percent", source: runSource("pull_request"),
		def:       "Share of first attempts of pull_request runs that failed although a re-run of the same run, or another run on the same head commit, later succeeded without a code change. This is the false-failure rate of the Arion contract, without infra classification.",
		aggregate: "share", target: arion(5, "max", "False failures ≤ 10 % after 2 weeks, ≤ 5 % after 4 (WP1.1)"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: noRuns, readFrom: in.ReadFrom}.build(now, flaked, in.Truncated))

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
		aggregate: "p50", target: arion(12, "max", ""), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No pull request branch reached a green " + ci + " run in the last 30 days.", readFrom: in.ReadFrom}.build(now, toGreen, in.Truncated))

	// 5: pull request opened → merged.
	merged := []metricPoint{}
	for _, pull := range in.Pulls {
		if pull.Merged != nil && !pull.Merged.Before(pull.Opened) {
			merged = append(merged, metricPoint{*pull.Merged, minutes(pull.Opened, *pull.Merged)})
		}
	}
	out = append(out, metricSpec{number: 5, key: "pr_open_to_merged", label: "PR opened → merged", unit: "minutes", source: "GitHub pull requests (GitHub App pull_request events and backfill)",
		def:       "Per merged pull request: created → merged, counted on the merge day.",
		aggregate: "p50", target: arion(40, "max", ""), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No pull request was merged in the last 30 days.", readFrom: in.ReadFrom}.build(now, merged, in.Truncated))

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
		aggregate: "mean", target: arion(1.1, "max", "0.1 extra queue runs per PR"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No merge-queue runs in the last 30 days.", readFrom: in.ReadFrom}.build(now, perPR, in.Truncated))

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
		aggregate: "p50", target: arion(8, "max", "First review round (WP1.8)"), coverage: reviewCovered, alwaysPartial: true, partialReason: reviewPartial, noDataReason: reviewNone, readFrom: in.ReadFrom}.build(now, reviewTime, false))
	out = append(out, metricSpec{number: 7, key: "review_changes_share", label: "Share of “changes” verdicts", unit: "percent", source: "aeon/review commit status (GitHub App status events) and reported reviews",
		def:       "Share of first verdicts per head that asked for changes (aeon/review failure, or a reported “changes”).",
		aggregate: "share", target: arion(25, "max", "WP1.8"), coverage: reviewCovered, alwaysPartial: true, partialReason: reviewPartial, noDataReason: reviewNone, readFrom: in.ReadFrom}.build(now, changes, false))

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
		aggregate: "share", target: arion(20, "max", "WP1.7"), coverage: roundsCovered, noDataReason: "No merge round has been reported yet.", readFrom: in.ReadFrom}.build(now, rounds, false))
	release := metricSpec{number: 9, key: "release_queue_to_live", label: "Release: merge queue → live", unit: "minutes", source: "Releases reported through POST …/delivery/metrics/facts (kind release)",
		def:       "Per release: the release PR enters the merge queue → the version is live on the production host, as reported by the release tooling. PAIMOS cannot observe the host itself.",
		aggregate: "p50", target: arion(24, "max", "~12 with blue/green and a canary"), coverage: releasesCovered, noDataReason: "No release has been reported yet.", readFrom: in.ReadFrom}.build(now, releases, false)
	facts := releaseFacts(in.Marks, now)
	release.Releases = &facts
	out = append(out, release)

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
		aggregate: "share", target: arion(100, "min", "7 green nights in a row (WP1.2)"), coverage: in.Covered, coverageEnd: in.CoveredUntil, noDataReason: "No completed " + nightly + " run in the last 30 days.", readFrom: in.ReadFrom}.build(now, nightPoints, in.Truncated)
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
