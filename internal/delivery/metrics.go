// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"fmt"
	"math"
	"sort"
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
	// HeadTree is the tree of the head commit; a retrigger commit shares it
	// with its parent (AEON-1016). Empty when GitHub did not say.
	HeadTree string
	// Job facts (AEON-1016) of attempt 1 of a pull-request or merge-queue run.
	// JobsRead says they were read. JobsComplete says Required is every job
	// conclusion of that attempt, not only the checks one project asked for;
	// a name absent from a complete map did not run. A legacy row is a flat
	// map of whatever was asked then, and is not complete. WorstWaitMS is the
	// longest wait for a runner of any job that ran, nil when none did.
	JobsRead     bool
	JobsComplete bool
	Required     map[string]string
	WorstWaitMS  *int
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

// metricReport is a reported fact that is not a timing mark (AEON-1016): a
// review audit of a merged pull request, or a defect that escaped to production.
// Severity is none (a clean audit), low, medium or high.
type metricReport struct {
	Kind     string
	Key      string
	PR       *int64
	Release  *string
	Severity string
	Started  *time.Time
	At       time.Time
}

// metricIncident is a rollout incident of a release (delivery_flow_incidents):
// the production side of an escaped defect. down is high, degraded medium.
type metricIncident struct {
	Key      string
	Severity string
	At       time.Time
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
	Reports         []metricReport
	Incidents       []metricIncident
	CIWorkflow      string
	NightlyWorkflow string
	// PreflightWorkflow is the exact-commit preflight workflow (AEON-1017);
	// empty reads the default.
	PreflightWorkflow string
	// Required are the project's required checks, as the delivery settings
	// name them; empty reads the defaults.
	Required []string
	// IncidentCovered is when rollout records began for this project.
	IncidentCovered *time.Time
	Covered         *time.Time
	// PreflightCovered is the earliest time preflight runs are complete. It
	// follows a backfill that listed the preflight workflow, not CI coverage.
	// Nil, or the first webhook preflight fact, until that backfill has run.
	PreflightCovered *time.Time
	CoveredUntil     *time.Time
	ReadFrom         *time.Time
	Truncated        bool
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
	// Counts are the exact numbers behind the value that a rounded share
	// cannot give back (never green, suspects, severities). Omitted when a
	// metric has none.
	Counts map[string]int `json:"counts,omitempty"`
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
// metrics report the share of true samples in percent. The p90 aggregate
// (runner wait) reports p90 as its value, count adds the samples up, and
// per100 reports events per 100 of a base (den), as the Arion queue readings do.
type metricSpec struct {
	number                        int
	key, label, unit, source, def string
	aggregate                     string // "p50", "p90", "mean", "share", "count" or "per100"
	target                        *MetricTarget
	coverage, coverageEnd         *time.Time
	readFrom                      *time.Time
	partialReason, noDataReason   string
	alwaysPartial                 bool
	// tagNames name the bits of metricPoint.tags; each becomes a count.
	tagNames []string
	// withhold, when set, means the share cannot be measured yet. Samples and
	// their counts stay; the value is nil and the status is no_data. A zero
	// would claim a measurement that was not made.
	withhold string
	// gaps are the instants of samples that could not be taken because a fact
	// is missing (jobs not read yet). A span holding one is partial.
	gaps      []time.Time
	gapReason string
}

// metricPoint is one sample. none marks a sample that carries no value (a
// commit that never went green): it is counted by its tags but never enters
// the percentiles. den is the base a per100 sample adds to.
type metricPoint struct {
	at    time.Time
	value float64
	den   float64
	tags  uint8
	none  bool
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

// summarize turns the samples of one span into the window's numbers. Samples
// without a value (none) only count; they never enter a percentile.
func (s metricSpec) summarize(points []metricPoint) (n int, value, p50, p90 *float64) {
	values := make([]float64, 0, len(points))
	for _, p := range points {
		if !p.none {
			values = append(values, p.value)
		}
	}
	if s.aggregate == "per100" {
		num, den := 0.0, 0.0
		for _, p := range points {
			num += p.value
			den += p.den
		}
		if den == 0 {
			return 0, nil, nil, nil
		}
		v := round1(100 * num / den)
		return int(den), &v, nil, nil
	}
	if len(values) == 0 {
		return 0, nil, nil, nil
	}
	switch s.aggregate {
	case "share":
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		v := round1(100 * sum / float64(len(values)))
		return len(values), &v, nil, nil
	case "mean":
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		v := math.Round(100*sum/float64(len(values))) / 100
		return len(values), &v, percentile(values, .5), percentile(values, .9)
	case "count":
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		return len(values), &sum, nil, nil
	case "p90":
		p50, p90 = percentile(values, .5), percentile(values, .9)
		return len(values), p90, p50, p90
	}
	p50, p90 = percentile(values, .5), percentile(values, .9)
	return len(values), p50, p50, p90
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
	if s.alwaysPartial || truncated || covered == nil || covered.After(start) || s.gapIn(start, end) {
		return true
	}
	return s.coverageEnd != nil && s.coverageEnd.Before(end)
}

// gapIn reports a missing fact inside [start, end).
func (s metricSpec) gapIn(start, end time.Time) bool {
	i := sort.Search(len(s.gaps), func(i int) bool { return !s.gaps[i].Before(start) })
	return i < len(s.gaps) && s.gaps[i].Before(end)
}

// gapText says how many runs lack their facts.
func (s metricSpec) gapText(missing int) string {
	noun := "runs"
	if missing == 1 {
		noun = "run"
	}
	return fmt.Sprintf("%d %s %s", missing, noun, s.gapReason)
}

// gapsIn counts the missing facts inside [start, end).
func (s metricSpec) gapsIn(start, end time.Time) int {
	i := sort.Search(len(s.gaps), func(i int) bool { return !s.gaps[i].Before(start) })
	j := sort.Search(len(s.gaps), func(i int) bool { return !s.gaps[i].Before(end) })
	return j - i
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

// between returns the sorted samples at or after from and before to, never
// after now.
func between(samples []metricPoint, from, to, now time.Time) []metricPoint {
	if limit := now.Add(time.Nanosecond); limit.Before(to) {
		to = limit
	}
	i := sort.Search(len(samples), func(i int) bool { return !samples[i].at.Before(from) })
	j := i
	for j < len(samples) && samples[j].at.Before(to) {
		j++
	}
	return samples[i:j]
}

// measured is one span's answer.
type measured struct {
	status string
	n      int
	value  *float64
	p50    *float64
	p90    *float64
	counts map[string]int
}

// countsOf adds up the samples carrying each tag, and the events and base of a
// per100 reading. Nil when the metric names none.
func (s metricSpec) countsOf(points []metricPoint) map[string]int {
	if s.aggregate == "per100" {
		num, den := 0.0, 0.0
		for _, p := range points {
			num += p.value
			den += p.den
		}
		return map[string]int{"events": int(num), "base": int(den)}
	}
	if len(s.tagNames) == 0 {
		return nil
	}
	out := map[string]int{}
	for i, name := range s.tagNames {
		out[name] = 0
		for _, p := range points {
			if p.tags&(1<<uint(i)) != 0 {
				out[name]++
			}
		}
	}
	return out
}

// measure aggregates one span: no samples are "no_data", never zero; a span
// outside complete coverage or of an incomplete source is "partial".
func (s metricSpec) measure(samples []metricPoint, span metricSpan, now time.Time, truncated bool) measured {
	points := between(samples, span.first, span.end(), now)
	m := measured{}
	m.n, m.value, m.p50, m.p90 = s.summarize(points)
	end := span.end()
	if now.Before(end) {
		end = now
	}
	switch {
	case len(points) == 0:
		m.status = "no_data"
	case s.withhold != "":
		// The samples are real. The share they would make is not, until the
		// missing evidence exists. Counts still name what was observed.
		m.status = "no_data"
		m.value, m.p50, m.p90 = nil, nil, nil
	case s.incomplete(span.first, end, truncated):
		m.status = "partial"
	default:
		m.status = "ok"
	}
	m.counts = s.countsOf(points)
	return m
}

// build turns timestamped samples into windows, a daily series and bucketed
// long series. A span that starts before the covered time, ends after
// CoveredUntil, or belongs to an incomplete source is partial; an empty span
// is "no_data", never zero. Nothing is extrapolated.
func (s metricSpec) build(now time.Time, samples []metricPoint, truncated bool) Metric {
	return s.buildFor(now, samples, truncated, metricWindowDays)
}

// buildFor is build for chosen windows.
func (s metricSpec) buildFor(now time.Time, samples []metricPoint, truncated bool, windows []int) Metric {
	out := Metric{Number: s.number, Key: s.key, Label: s.label, Unit: s.unit, Source: s.source, Definition: s.def, Target: s.target, Windows: []MetricWindow{}, Daily: []MetricPoint{}, Series: []MetricBucket{}}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })
	for _, sample := range samples {
		if sample.none || s.aggregate == "per100" || s.aggregate == "count" || s.withhold != "" {
			continue
		}
		if !sample.at.After(now) && !sample.at.Before(now.Add(-metricDays*24*time.Hour)) {
			value := sample.value
			if s.aggregate == "share" {
				value *= 100
			}
			out.Latest = &MetricSample{Value: round1(value), At: sample.at}
		}
	}
	reasons := []string{}
	for _, days := range windows {
		span := windowSpan(now, days, false)
		w := MetricWindow{Days: days, Coverage: s.coverageOf(span, days, now)}
		m := s.measure(samples, span, now, truncated)
		w.Status, w.N, w.Value, w.P50, w.P90, w.Counts = m.status, m.n, m.value, m.p50, m.p90, m.counts
		before := windowSpan(now, days, true)
		p := MetricPrevious{Coverage: s.coverageOf(before, days, now)}
		m = s.measure(samples, before, now, truncated)
		p.Status, p.N, p.Value, p.P50, p.P90 = m.status, m.n, m.value, m.p50, m.p90
		w.Previous = &p
		out.Windows = append(out.Windows, w)
	}
	for _, day := range windowSpan(now, metricDays, false).buckets("day") {
		p := MetricPoint{Date: day.first.Format("2006-01-02")}
		m := s.measure(samples, day, now, truncated)
		p.Status, p.N, p.Value, p.P50, p.P90 = m.status, m.n, m.value, m.p50, m.p90
		out.Daily = append(out.Daily, p)
	}
	for _, days := range metricSeriesDays {
		kind := bucketKind(days)
		for _, b := range windowSpan(now, days, false).buckets(kind) {
			p := MetricBucket{Days: days, From: b.first.Format("2006-01-02"), To: b.last.Format("2006-01-02"), Bucket: kind}
			m := s.measure(samples, b, now, truncated)
			p.Status, p.N, p.Value, p.P50, p.P90 = m.status, m.n, m.value, m.p50, m.p90
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
	case s.withhold != "" && long.N > 0:
		out.Status = "no_data"
		reasons = append(reasons, s.withhold)
	case long.N == 0:
		out.Status = "no_data"
		if missing := s.gapsIn(windowStart, now); missing > 0 {
			// Runs exist, but their facts are not read yet: say that, not "no runs".
			reasons = append(reasons, s.gapText(missing))
		} else {
			reasons = append(reasons, s.noDataReason)
		}
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
		if missing := s.gapsIn(windowStart, now); missing > 0 {
			reasons = append(reasons, s.gapText(missing))
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
