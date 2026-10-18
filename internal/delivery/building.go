// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"math"
	"sort"
	"time"
)

// Delivery › Numbers › Building it (AEON-1493): how often a change merges after
// its first round, how many rounds it takes, how long it sits idle between
// rounds and how long it takes from the first build to the merge. Everything
// comes from the Flow projection of the event log (AEON-1004, AEON-1472):
// builder steps (harness sessions and work-queue rounds), review gate verdicts,
// check runs, the merge queue and the merge that ends a change.
//
// An item is a Flow change linked to a ticket, counted in the window where it
// merged. A round is one builder pass: the first build, a fix round or a
// merge-forward round. Builder steps that overlap in time are one round (a
// work-queue round and the harness session of the same worker), and a
// coordinator's instant claim joins the build it starts. A history that does
// not start with the initial build is left out of first-try and lead: a lone
// fix or merge-forward is not a first try and has no lead time. Nothing is
// invented: an item without a recorded round is not counted, a round whose end
// is unknown has no idle sample, and a window without items has no value.

// BuildingTargets are the delivery settings' targets for Building it. Lead time
// has none yet: it is watched.
type BuildingTargets struct {
	FirstTryRate   float64 `json:"first_try_rate"`
	RoundsPerItem  float64 `json:"rounds_per_item"`
	IdleP50Minutes float64 `json:"idle_p50_minutes"`
}

// buildingTargetDefaults apply when no scope has saved Building it targets.
func buildingTargetDefaults() BuildingTargets {
	return BuildingTargets{FirstTryRate: 60, RoundsPerItem: 1.6, IdleP50Minutes: 15}
}

// buildingTargetsInput is a write. A missing field stays nil so a partial
// object is refused rather than stored as zero.
type buildingTargetsInput struct {
	FirstTryRate   *float64 `json:"first_try_rate"`
	RoundsPerItem  *float64 `json:"rounds_per_item"`
	IdleP50Minutes *float64 `json:"idle_p50_minutes"`
}

// buildingTargetsFrom accepts one complete triple inside the delivery bounds.
// Nil means the caller omitted targets and inherits; it is not valid here.
func buildingTargetsFrom(in *buildingTargetsInput) (BuildingTargets, bool) {
	if in == nil || in.FirstTryRate == nil || in.RoundsPerItem == nil || in.IdleP50Minutes == nil {
		return BuildingTargets{}, false
	}
	out := BuildingTargets{FirstTryRate: *in.FirstTryRate, RoundsPerItem: *in.RoundsPerItem, IdleP50Minutes: *in.IdleP50Minutes}
	if !targetIn(out.FirstTryRate, 0, 100) || !targetIn(out.RoundsPerItem, 0, 100) || !targetIn(out.IdleP50Minutes, 0, 10080) {
		return BuildingTargets{}, false
	}
	return out, true
}

func targetIn(v, min, max float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= min && v <= max
}

type buildingStep struct {
	Key, Kind, Actor, Outcome string
	Started                   time.Time
	Ended                     *time.Time
}

type buildingItem struct {
	ID, Ticket, Ref, Title string
	Merged                 time.Time
	Kind, Priority         string
	Bug                    bool
	Steps                  []buildingStep
}

const (
	roundPassed       = "passed"
	roundChanges      = "changes"
	roundFailed       = "failed"
	roundMergeForward = "merge_forward"
	roundNone         = "none"
)

type buildingRound struct {
	Start        time.Time
	End          *time.Time
	MergeForward bool
	// Initial is a first build (build/work), including one a coordinator's
	// instant claim joined. A fix or a merge-forward round is not.
	Initial  bool
	Instant  bool
	Finished bool
	Outcome  string
	// Worker is who built the round; a coordinator's instant claim is not.
	Worker string
}

type buildingFacts struct {
	Rounds []buildingRound
	// Waits are the idle minutes after each round with a known end.
	Waits []float64
	Lead  float64
	// Worker is the kind of principal that built the first round (agent or person).
	Worker string
}

func builderStep(s buildingStep) bool {
	return (s.Key == "build" || s.Key == "merge_round") && (s.Kind == "work" || s.Kind == "rework") && (s.Actor == "agent" || s.Actor == "person")
}

// initialBuild is the first build of a change, not a fix and not a merge-forward.
func initialBuild(s buildingStep) bool {
	return s.Key == "build" && s.Kind == "work"
}

// historyComplete is a recorded history that starts at the initial build.
// A lone fix or merge-forward round is not one, so first-try and lead withhold it.
func historyComplete(rounds []buildingRound) bool {
	return len(rounds) > 0 && rounds[0].Initial
}

// A signal judges a round: a gate verdict, or a check or merge-queue run that
// ended green or red. A flaky run proves nothing about the change.
func signalStep(s buildingStep) bool {
	if s.Ended == nil {
		return false
	}
	switch s.Key {
	case "review":
		return s.Kind == "work" && (s.Outcome == "ok" || s.Outcome == "changes")
	case "ci", "queue":
		return s.Outcome == "green" || s.Outcome == "red"
	}
	return false
}

// latestSignal is the last signal that ended after from and no later than to.
func latestSignal(signals []buildingStep, from, to time.Time) *buildingStep {
	var out *buildingStep
	for i := range signals {
		end := *signals[i].Ended
		if end.After(from) && !end.After(to) && (out == nil || !end.Before(*out.Ended)) {
			out = &signals[i]
		}
	}
	return out
}

func deriveBuilding(it buildingItem) buildingFacts {
	steps := append([]buildingStep(nil), it.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Started.Before(steps[j].Started) })
	var builders, signals, gates []buildingStep
	var queues []time.Time
	for _, s := range steps {
		if s.Started.After(it.Merged) {
			continue
		}
		if builderStep(s) {
			builders = append(builders, s)
		} else if signalStep(s) && !s.Ended.After(it.Merged) {
			signals = append(signals, s)
			if s.Key != "queue" {
				gates = append(gates, s)
			}
		}
		if s.Key == "queue" {
			queues = append(queues, s.Started)
		}
	}
	out := buildingFacts{}
	for _, s := range builders {
		instant := s.Ended != nil && !s.Ended.After(s.Started)
		if n := len(out.Rounds); n > 0 {
			last := &out.Rounds[n-1]
			// An open step never swallows later rounds: its end is unknown.
			if last.End != nil && (s.Started.Before(*last.End) || last.Instant && latestSignal(signals, *last.End, s.Started) == nil) {
				switch {
				case s.Ended == nil:
					last.End = nil
				case s.Ended.After(*last.End):
					end := *s.Ended
					last.End = &end
				}
				last.MergeForward = last.MergeForward && s.Key == "merge_round"
				last.Initial = last.Initial || initialBuild(s)
				last.Instant = last.Instant && instant
				last.Finished = last.Finished || s.Outcome == "ok"
				if last.Worker == "" && !instant {
					last.Worker = s.Actor
				}
				continue
			}
		}
		r := buildingRound{Start: s.Started, MergeForward: s.Key == "merge_round", Initial: initialBuild(s), Instant: instant, Finished: s.Outcome == "ok"}
		if !instant {
			r.Worker = s.Actor
		}
		if s.Ended != nil {
			end := *s.Ended
			r.End = &end
		}
		out.Rounds = append(out.Rounds, r)
	}
	if len(out.Rounds) == 0 {
		return out
	}
	out.Worker = out.Rounds[0].Worker
	if out.Worker == "" {
		out.Worker = builders[0].Actor
	}
	out.Lead = it.Merged.Sub(out.Rounds[0].Start).Minutes()
	last := len(out.Rounds) - 1
	for i := range out.Rounds {
		r := &out.Rounds[i]
		boundary := it.Merged
		if i < last {
			boundary = out.Rounds[i+1].Start
		}
		verdict, judged := roundFromSignals(signals, r.Start, boundary)
		switch {
		case r.MergeForward:
			r.Outcome = roundMergeForward
		case i == last:
			// The merge proves the last round passed its required checks.
			r.Outcome = roundPassed
		case verdict == roundChanges:
			r.Outcome = roundChanges
		case verdict == roundPassed:
			r.Outcome = roundPassed
		case judged || !r.Finished:
			r.Outcome = roundFailed
		default:
			r.Outcome = roundNone
		}
		if r.End == nil {
			continue
		}
		// A round ends with its gate verdict or checks when those came after
		// the builder finished; merge-queue runs already follow the next step.
		end := *r.End
		if judged := latestSignal(gates, r.Start, boundary); judged != nil && judged.Ended.After(end) {
			end = *judged.Ended
		}
		next := boundary
		if i == last {
			// The next step after the last round is the merge enqueue, else the merge.
			for _, q := range queues {
				if !q.Before(end) {
					next = q
					break
				}
			}
		}
		if !next.Before(end) {
			out.Waits = append(out.Waits, next.Sub(end).Minutes())
		}
	}
	return out
}

// roundFromSignals keeps review and checks apart. Within one of them the later
// verdict stands; a success in the other does not clear an unresolved negative.
// Checks are CI and the merge queue. The last round is not judged here: the
// merge proves it passed.
func roundFromSignals(signals []buildingStep, from, to time.Time) (string, bool) {
	var review, check *buildingStep
	for i := range signals {
		end := *signals[i].Ended
		if !end.After(from) || end.After(to) {
			continue
		}
		switch signals[i].Key {
		case "review":
			if review == nil || !end.Before(*review.Ended) {
				review = &signals[i]
			}
		case "ci", "queue":
			if check == nil || !end.Before(*check.Ended) {
				check = &signals[i]
			}
		}
	}
	if review == nil && check == nil {
		return "", false
	}
	if review != nil && review.Outcome == "changes" {
		return roundChanges, true
	}
	if check != nil && check.Outcome == "red" {
		return roundFailed, true
	}
	if review != nil && review.Outcome == "ok" || check != nil && check.Outcome == "green" {
		return roundPassed, true
	}
	return roundFailed, true
}

// ---------- The response ----------

type buildingTarget struct {
	Value     float64 `json:"value"`
	Direction string  `json:"direction"`
	Source    string  `json:"source"`
}

type buildingMeasure struct {
	N     int      `json:"n"`
	Items int      `json:"items"`
	Count int      `json:"count"`
	Value *float64 `json:"value"`
	P50   *float64 `json:"p50"`
	P90   *float64 `json:"p90"`
	Mean  *float64 `json:"mean"`
}

type buildingBucket struct {
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	State string    `json:"state"`
	buildingMeasure
}

type buildingChange struct {
	Value *float64 `json:"value"`
	Trend string   `json:"trend"`
}

type buildingVerdict struct {
	Level string   `json:"level"`
	Ratio *float64 `json:"ratio"`
}

type buildingWorst struct {
	NodeID     string   `json:"node_id"`
	Key        string   `json:"key"`
	Title      string   `json:"title"`
	Value      float64  `json:"value"`
	Rounds     []string `json:"rounds"`
	MoreRounds int      `json:"more_rounds"`
}

type buildingMetric struct {
	Key      string           `json:"key"`
	Unit     string           `json:"unit"`
	Better   string           `json:"better"`
	Target   *buildingTarget  `json:"target"`
	Current  buildingMeasure  `json:"current"`
	Previous buildingMeasure  `json:"previous"`
	Change   buildingChange   `json:"change"`
	Verdict  buildingVerdict  `json:"verdict"`
	Buckets  []buildingBucket `json:"buckets"`
	Worst    []buildingWorst  `json:"worst"`
}

type buildingCoverage struct {
	Items         int  `json:"items"`
	WithoutRounds int  `json:"without_rounds"`
	Partial       bool `json:"partial"`
}

type deliveryBuilding struct {
	ProjectID   string           `json:"project_id"`
	GeneratedAt time.Time        `json:"generated_at"`
	Window      int              `json:"window"`
	Since       *time.Time       `json:"since"`
	Coverage    buildingCoverage `json:"coverage"`
	Metrics     []buildingMetric `json:"metrics"`
}

const (
	buildingWorstLimit  = 5
	buildingStripRounds = 20
)

// buildingKeys are the four numbers in the order the page shows them.
var buildingKeys = []string{"first_try", "rounds", "idle", "lead"}

type buildingSample struct {
	item  buildingItem
	facts buildingFacts
}

func (f timingFilter) admits(it buildingItem, worker string) bool {
	if f.Type != "" && f.Type != it.Kind && !(it.Kind == "work" && (f.Type == "bug" && it.Bug || f.Type == "other" && !it.Bug)) {
		return false
	}
	if f.Priority != "" && f.Priority != it.Priority {
		return false
	}
	return f.WorkedBy == "" || f.WorkedBy == worker
}

func buildingValue(v float64) *float64 { return &v }

func measureOf(key string, samples []buildingSample) buildingMeasure {
	m := buildingMeasure{}
	switch key {
	case "first_try":
		// A fix or merge-forward with no recorded initial build is withheld, not a miss.
		for _, s := range samples {
			if !historyComplete(s.facts.Rounds) {
				continue
			}
			m.N++
			m.Items++
			if len(s.facts.Rounds) == 1 {
				m.Count++
			}
		}
		if m.N > 0 {
			m.Value = buildingValue(round1(100 * float64(m.Count) / float64(m.N)))
		}
	case "rounds":
		values := make([]float64, 0, len(samples))
		for _, s := range samples {
			values = append(values, float64(len(s.facts.Rounds)))
			m.Count += len(s.facts.Rounds)
		}
		m.N, m.Items = len(values), len(values)
		if m.N > 0 {
			m.P50, m.P90 = percentile(values, .5), percentile(values, .9)
			m.Value, m.Mean = m.P50, buildingValue(round1(float64(m.Count)/float64(m.N)))
		}
	case "idle":
		values := []float64{}
		for _, s := range samples {
			if len(s.facts.Waits) > 0 {
				m.Items++
				values = append(values, s.facts.Waits...)
			}
		}
		m.N = len(values)
		if m.N > 0 {
			m.P50, m.P90 = percentile(values, .5), percentile(values, .9)
			m.Value = m.P50
		}
	case "lead":
		values := make([]float64, 0, len(samples))
		for _, s := range samples {
			if historyComplete(s.facts.Rounds) {
				values = append(values, s.facts.Lead)
			}
		}
		m.N, m.Items = len(values), len(values)
		if m.N > 0 {
			m.P50, m.P90 = percentile(values, .5), percentile(values, .9)
			m.Value = m.P50
		}
	}
	return m
}

// On target up to 1×, close up to 1.5×, far off beyond (as every Delivery verdict).
func buildingVerdictOf(value *float64, target *buildingTarget) buildingVerdict {
	if value == nil || target == nil {
		return buildingVerdict{Level: "none"}
	}
	var ratio float64
	switch {
	case target.Direction == "min":
		ratio = target.Value / math.Max(*value, 0.01)
	case target.Value > 0:
		ratio = *value / target.Value
	case *value > 0:
		return buildingVerdict{Level: "far"}
	}
	level := "far"
	if ratio <= 1 {
		level = "on"
	} else if ratio <= 1.5 {
		level = "close"
	}
	r := math.Round(ratio*100) / 100
	return buildingVerdict{Level: level, Ratio: &r}
}

// The change against the window just before: steady within 3 points for the
// share and within 10 % for the others.
func buildingChangeOf(current, previous *float64, percent bool, better string) buildingChange {
	if current == nil || previous == nil {
		return buildingChange{Trend: "none"}
	}
	change := round1(*current - *previous)
	steady := math.Abs(change) < 3
	if !percent {
		steady = *previous == 0 && change == 0 || *previous != 0 && math.Abs(*current-*previous)/math.Abs(*previous) < 0.1
	}
	trend := "worse"
	switch {
	case steady:
		trend = "same"
	case better == "higher" && change > 0 || better == "lower" && change < 0:
		trend = "better"
	}
	return buildingChange{Value: &change, Trend: trend}
}

func worstOf(key string, samples []buildingSample) []buildingWorst {
	pool := []buildingSample{}
	value := func(s buildingSample) float64 { return float64(len(s.facts.Rounds)) }
	switch key {
	case "first_try":
		for _, s := range samples {
			if historyComplete(s.facts.Rounds) && len(s.facts.Rounds) > 1 {
				pool = append(pool, s)
			}
		}
	case "rounds":
		pool = append(pool, samples...)
	case "idle":
		for _, s := range samples {
			if len(s.facts.Waits) > 0 {
				pool = append(pool, s)
			}
		}
		value = func(s buildingSample) float64 {
			longest := 0.0
			for _, w := range s.facts.Waits {
				longest = math.Max(longest, w)
			}
			return longest
		}
	case "lead":
		for _, s := range samples {
			if historyComplete(s.facts.Rounds) {
				pool = append(pool, s)
			}
		}
		value = func(s buildingSample) float64 { return s.facts.Lead }
	}
	sort.SliceStable(pool, func(i, j int) bool {
		a, b := value(pool[i]), value(pool[j])
		if a != b {
			return a > b
		}
		if pool[i].facts.Lead != pool[j].facts.Lead {
			return pool[i].facts.Lead > pool[j].facts.Lead
		}
		return pool[i].item.Ref < pool[j].item.Ref
	})
	out := []buildingWorst{}
	for _, s := range pool[:min(len(pool), buildingWorstLimit)] {
		w := buildingWorst{NodeID: s.item.Ticket, Key: s.item.Ref, Title: s.item.Title, Value: round1(value(s)), Rounds: []string{}}
		for i, r := range s.facts.Rounds {
			if i == buildingStripRounds {
				w.MoreRounds = len(s.facts.Rounds) - i
				break
			}
			w.Rounds = append(w.Rounds, r.Outcome)
		}
		out = append(out, w)
	}
	return out
}

func aggregateBuilding(project string, items []buildingItem, since *time.Time, targets BuildingTargets, filter timingFilter, now time.Time) deliveryBuilding {
	out := deliveryBuilding{ProjectID: project, GeneratedAt: now, Window: filter.Days, Since: since, Metrics: []buildingMetric{}}
	span := windowSpan(now, filter.Days, false)
	from, previous := span.first, windowSpan(now, filter.Days, true).first
	var current, prior []buildingSample
	for _, it := range items {
		if it.Merged.Before(previous) || it.Merged.After(now) {
			continue
		}
		facts := deriveBuilding(it)
		if !filter.admits(it, facts.Worker) {
			continue
		}
		inside := !it.Merged.Before(from)
		if len(facts.Rounds) == 0 {
			if inside {
				out.Coverage.WithoutRounds++
			}
			continue
		}
		if inside {
			current = append(current, buildingSample{it, facts})
		} else {
			prior = append(prior, buildingSample{it, facts})
		}
	}
	out.Coverage.Items = len(current)
	out.Coverage.Partial = since == nil || from.Before(*since)
	parts := span.buckets(bucketKind(filter.Days))
	for _, key := range buildingKeys {
		m := buildingMetric{Key: key, Unit: "minutes", Better: "lower", Buckets: []buildingBucket{}}
		switch key {
		case "first_try":
			m.Unit, m.Better = "percent", "higher"
			m.Target = &buildingTarget{Value: targets.FirstTryRate, Direction: "min", Source: "delivery_settings"}
		case "rounds":
			m.Unit = "rounds"
			m.Target = &buildingTarget{Value: targets.RoundsPerItem, Direction: "max", Source: "delivery_settings"}
		case "idle":
			m.Target = &buildingTarget{Value: targets.IdleP50Minutes, Direction: "max", Source: "delivery_settings"}
		}
		m.Current, m.Previous = measureOf(key, current), measureOf(key, prior)
		m.Change = buildingChangeOf(m.Current.Value, m.Previous.Value, key == "first_try", m.Better)
		m.Verdict = buildingVerdictOf(m.Current.Value, m.Target)
		for _, part := range parts {
			start, end := part.first, part.end()
			if end.After(now) {
				end = now
			}
			inBucket := []buildingSample{}
			for _, s := range current {
				if !s.item.Merged.Before(start) && s.item.Merged.Before(end) || end.Equal(now) && s.item.Merged.Equal(now) {
					inBucket = append(inBucket, s)
				}
			}
			b := buildingBucket{From: start, To: end, State: "observed", buildingMeasure: measureOf(key, inBucket)}
			if since == nil || !end.After(*since) {
				b.State = "no_data"
			} else if start.Before(*since) {
				b.State = "partial"
			}
			m.Buckets = append(m.Buckets, b)
		}
		m.Worst = worstOf(key, current)
		out.Metrics = append(out.Metrics, m)
	}
	return out
}
