// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"reflect"
	"testing"
	"time"
)

var buildingNow = time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
}

func ended(t time.Time) *time.Time { return &t }

func build(start, end time.Time, actor, outcome string) buildingStep {
	return buildingStep{Key: "build", Kind: "work", Actor: actor, Outcome: outcome, Started: start, Ended: ended(end)}
}

func signal(key, outcome string, at time.Time) buildingStep {
	return buildingStep{Key: key, Kind: "work", Actor: "ci", Outcome: outcome, Started: at, Ended: ended(at)}
}

// buildingFixture is one window of merged changes: a first try, a four-round
// change with every mark, a coordinator claim that starts its build, a change
// without any recorded round, an open build that did not swallow its fix, and
// one change of the window before.
func buildingFixture() []buildingItem {
	return []buildingItem{
		{ID: "a", Ticket: "ta", Ref: "HRB-1", Title: "First try", Merged: at(17, 12, 0), Kind: "work", Priority: "medium", Steps: []buildingStep{
			build(at(17, 10, 0), at(17, 11, 0), "agent", "ok"),
			signal("review", "ok", at(17, 11, 20)),
			{Key: "queue", Kind: "work", Actor: "agent", Started: at(17, 11, 30), Ended: ended(at(17, 11, 30))},
			signal("queue", "green", at(17, 12, 0)),
		}},
		{ID: "b", Ticket: "tb", Ref: "HRB-2", Title: "Four rounds", Merged: at(16, 13, 0), Kind: "work", Priority: "medium", Steps: []buildingStep{
			build(at(16, 8, 0), at(16, 9, 0), "agent", "ok"),
			// The work-queue round of the same worker overlaps the harness session: one round.
			{Key: "build", Kind: "work", Actor: "agent", Outcome: "ok", Started: at(16, 8, 5), Ended: ended(at(16, 8, 50))},
			signal("ci", "red", at(16, 9, 10)),
			{Key: "build", Kind: "rework", Actor: "agent", Outcome: "ok", Started: at(16, 9, 40), Ended: ended(at(16, 10, 40))},
			signal("ci", "green", at(16, 10, 50)),
			signal("review", "changes", at(16, 11, 0)),
			{Key: "merge_round", Kind: "rework", Actor: "agent", Outcome: "ok", Started: at(16, 11, 15), Ended: ended(at(16, 11, 25))},
			{Key: "build", Kind: "rework", Actor: "agent", Outcome: "ok", Started: at(16, 11, 40), Ended: ended(at(16, 12, 30))},
			// A flaky run proves nothing; the gate passes the last round.
			signal("ci", "flaky", at(16, 12, 35)),
			signal("review", "ok", at(16, 12, 40)),
			{Key: "queue", Kind: "wait", Actor: "queue", Started: at(16, 12, 50), Ended: ended(at(16, 12, 55))},
			signal("queue", "green", at(16, 13, 0)),
		}},
		{ID: "c", Ticket: "tc", Ref: "HRB-3", Title: "Claim starts its build", Merged: at(15, 11, 0), Kind: "work", Priority: "high", Bug: true, Steps: []buildingStep{
			{Key: "merge_round", Kind: "work", Actor: "agent", Started: at(15, 9, 0), Ended: ended(at(15, 9, 0))},
			build(at(15, 9, 2), at(15, 10, 0), "agent", "ok"),
		}},
		{ID: "e", Ticket: "te", Ref: "HRB-5", Title: "Checks only", Merged: at(14, 10, 0), Kind: "work", Priority: "low", Steps: []buildingStep{
			signal("ci", "green", at(14, 9, 0)),
		}},
		{ID: "f", Ticket: "tf", Ref: "HRB-6", Title: "Open build", Merged: at(13, 11, 30), Kind: "work", Priority: "low", Steps: []buildingStep{
			{Key: "build", Kind: "work", Actor: "person", Started: at(13, 9, 0)},
			build(at(13, 10, 0), at(13, 11, 0), "person", "ok"),
		}},
		{ID: "d", Ticket: "td", Ref: "HRB-4", Title: "Window before", Merged: at(8, 12, 0), Kind: "work", Priority: "high", Steps: []buildingStep{
			build(at(8, 10, 0), at(8, 10, 30), "agent", "ok"),
			signal("review", "changes", at(8, 10, 40)),
			build(at(8, 11, 0), at(8, 11, 30), "agent", "ok"),
		}},
	}
}

func buildingMetricOf(t *testing.T, out deliveryBuilding, key string) buildingMetric {
	t.Helper()
	for _, m := range out.Metrics {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("metric %s missing: %+v", key, out.Metrics)
	return buildingMetric{}
}

func wantValue(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func worstKeys(m buildingMetric) []string {
	out := []string{}
	for _, w := range m.Worst {
		out = append(out, w.Key)
	}
	return out
}

func TestDeliveryBuildingRoundsAndWindows(t *testing.T) {
	// Risk: overlapping builder steps or a coordinator claim count as extra
	// rounds, an open session swallows its fix, a change without rounds is
	// counted as a first try, the window before leaks into this one, or the
	// worst items come out in the wrong order.
	since := at(8, 10, 0)
	out := aggregateBuilding("p", buildingFixture(), &since, buildingTargetDefaults(), timingFilter{Days: 7}, buildingNow)
	if out.Coverage.Items != 4 || out.Coverage.WithoutRounds != 1 || out.Coverage.Partial {
		t.Fatalf("coverage %+v", out.Coverage)
	}
	b := deriveBuilding(buildingFixture()[1])
	marks := []string{}
	for _, r := range b.Rounds {
		marks = append(marks, r.Outcome)
	}
	if !reflect.DeepEqual(marks, []string{roundFailed, roundChanges, roundMergeForward, roundPassed}) || !reflect.DeepEqual(b.Waits, []float64{30, 15, 15, 10}) || b.Lead != 300 {
		t.Fatalf("four rounds: %v waits %v lead %v", marks, b.Waits, b.Lead)
	}

	first := buildingMetricOf(t, out, "first_try")
	wantValue(t, "first try", first.Current.Value, 50)
	if first.Current.Count != 2 || first.Current.N != 4 || first.Previous.N != 1 || first.Target == nil || first.Target.Value != 60 || first.Better != "higher" {
		t.Fatalf("first try %+v", first)
	}
	// 50 % against 0 % before: better; 60/50 = 1.2× the target: close.
	wantValue(t, "first try change", first.Change.Value, 50)
	if first.Change.Trend != "better" || first.Verdict.Level != "close" {
		t.Fatalf("first try judgement %+v %+v", first.Change, first.Verdict)
	}
	if !reflect.DeepEqual(worstKeys(first), []string{"HRB-2", "HRB-6"}) {
		t.Fatalf("first-try worst lists a first try: %v", worstKeys(first))
	}

	rounds := buildingMetricOf(t, out, "rounds")
	wantValue(t, "rounds p50", rounds.Current.Value, 1.5)
	wantValue(t, "rounds mean", rounds.Current.Mean, 2)
	if rounds.Current.Count != 8 || rounds.Change.Trend != "better" || rounds.Verdict.Level != "on" {
		t.Fatalf("rounds %+v", rounds)
	}
	if !reflect.DeepEqual(worstKeys(rounds), []string{"HRB-2", "HRB-6", "HRB-1", "HRB-3"}) {
		t.Fatalf("rounds worst order %v", worstKeys(rounds))
	}
	if w := rounds.Worst[0]; w.Value != 4 || w.NodeID != "tb" || !reflect.DeepEqual(w.Rounds, marks) {
		t.Fatalf("worst item %+v", w)
	}
	if w := rounds.Worst[1]; !reflect.DeepEqual(w.Rounds, []string{roundFailed, roundPassed}) {
		t.Fatalf("open build was not a round of its own: %+v", w)
	}

	idle := buildingMetricOf(t, out, "idle")
	// Waits 10 · 30 15 15 10 · 60 · 30: the open build has none.
	if idle.Current.N != 7 || idle.Current.Items != 4 {
		t.Fatalf("idle counts %+v", idle.Current)
	}
	wantValue(t, "idle p50", idle.Current.Value, 15)
	wantValue(t, "idle p90", idle.Current.P90, 42)
	// The window before: 20 min after the gate asked for changes, 30 min after the fix.
	if idle.Previous.N != 2 || idle.Verdict.Level != "on" || !reflect.DeepEqual(worstKeys(idle), []string{"HRB-3", "HRB-2", "HRB-6", "HRB-1"}) {
		t.Fatalf("idle %+v worst %v", idle, worstKeys(idle))
	}

	lead := buildingMetricOf(t, out, "lead")
	wantValue(t, "lead p50", lead.Current.Value, 135)
	wantValue(t, "lead p90", lead.Current.P90, 255)
	if lead.Target != nil || lead.Verdict.Level != "none" || !reflect.DeepEqual(worstKeys(lead), []string{"HRB-2", "HRB-6", "HRB-1", "HRB-3"}) {
		t.Fatalf("lead %+v worst %v", lead, worstKeys(lead))
	}

	// Seven daily buckets, each with its own items: one change merged on the 16th.
	if len(rounds.Buckets) != 7 || rounds.Buckets[4].N != 1 || *rounds.Buckets[4].Value != 4 || rounds.Buckets[4].State != "observed" {
		t.Fatalf("rounds buckets %+v", rounds.Buckets)
	}
}

func TestDeliveryBuildingFiltersAndEmptyWindow(t *testing.T) {
	// Risk: a filter misses the window before or the worst list, an empty
	// window reports zero instead of nothing, or days before the first
	// recorded round look measured.
	since := at(8, 10, 0)
	items := buildingFixture()
	high := aggregateBuilding("p", items, &since, buildingTargetDefaults(), timingFilter{Days: 7, Priority: "high"}, buildingNow)
	if m := buildingMetricOf(t, high, "first_try"); m.Current.N != 1 || m.Previous.N != 1 || *m.Current.Value != 100 || !reflect.DeepEqual(worstKeys(m), []string{}) {
		t.Fatalf("priority filter %+v", m)
	}
	bug := aggregateBuilding("p", items, &since, buildingTargetDefaults(), timingFilter{Days: 7, Type: "bug"}, buildingNow)
	if m := buildingMetricOf(t, bug, "rounds"); m.Current.N != 1 || !reflect.DeepEqual(worstKeys(m), []string{"HRB-3"}) {
		t.Fatalf("type filter %+v", m)
	}
	// The coordinator's claim does not make an agent the builder of a person's work.
	person := aggregateBuilding("p", items, &since, buildingTargetDefaults(), timingFilter{Days: 7, WorkedBy: "person"}, buildingNow)
	if m := buildingMetricOf(t, person, "rounds"); m.Current.N != 1 || m.Previous.N != 0 || !reflect.DeepEqual(worstKeys(m), []string{"HRB-6"}) {
		t.Fatalf("worker filter %+v", m)
	}
	if facts := deriveBuilding(items[2]); facts.Worker != "agent" || len(facts.Rounds) != 1 || facts.Rounds[0].MergeForward {
		t.Fatalf("claim and build %+v", facts)
	}

	late := at(14, 10, 0)
	empty := aggregateBuilding("p", nil, &late, buildingTargetDefaults(), timingFilter{Days: 7}, buildingNow)
	if !empty.Coverage.Partial || empty.Coverage.Items != 0 || len(empty.Metrics) != 4 {
		t.Fatalf("empty coverage %+v", empty)
	}
	for _, m := range empty.Metrics {
		if m.Current.Value != nil || m.Current.N != 0 || m.Change.Trend != "none" || m.Change.Value != nil || m.Verdict.Level != "none" || len(m.Worst) != 0 {
			t.Fatalf("empty %s reports figures: %+v", m.Key, m)
		}
		states := []string{}
		for _, b := range m.Buckets {
			states = append(states, b.State)
		}
		if !reflect.DeepEqual(states, []string{"no_data", "no_data", "partial", "observed", "observed", "observed", "observed"}) {
			t.Fatalf("%s bucket states %v", m.Key, states)
		}
	}
	never := aggregateBuilding("p", nil, nil, buildingTargetDefaults(), timingFilter{Days: 30}, buildingNow)
	if !never.Coverage.Partial || never.Since != nil || len(never.Metrics[0].Buckets) != 30 || never.Metrics[0].Buckets[29].State != "no_data" {
		t.Fatalf("never recorded %+v", never.Metrics[0].Buckets)
	}
}

func TestDeliveryBuildingJudgements(t *testing.T) {
	// Risk: the change or the verdict disagrees with the rest of Delivery:
	// shares move in points, the others by 10 %, a lower count is better.
	if c := buildingChangeOf(f64(30), f64(32), true, "higher"); c.Trend != "same" || *c.Value != -2 {
		t.Fatalf("share within 3 points: %+v", c)
	}
	if c := buildingChangeOf(f64(22), f64(31), false, "lower"); c.Trend != "better" || *c.Value != -9 {
		t.Fatalf("idle fell: %+v", c)
	}
	if c := buildingChangeOf(f64(3.2), f64(2.9), false, "lower"); c.Trend != "worse" {
		t.Fatalf("rounds rose: %+v", c)
	}
	if c := buildingChangeOf(f64(3), nil, false, "lower"); c.Trend != "none" || c.Value != nil {
		t.Fatalf("no window before: %+v", c)
	}
	if v := buildingVerdictOf(f64(3.2), &buildingTarget{Value: 1.6, Direction: "max"}); v.Level != "far" || *v.Ratio != 2 {
		t.Fatalf("rounds verdict %+v", v)
	}
	if v := buildingVerdictOf(f64(22), &buildingTarget{Value: 15, Direction: "max"}); v.Level != "close" {
		t.Fatalf("idle verdict %+v", v)
	}
	if v := buildingVerdictOf(f64(28), &buildingTarget{Value: 60, Direction: "min"}); v.Level != "far" {
		t.Fatalf("first-try verdict %+v", v)
	}
}
