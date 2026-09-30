// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestTwelveRunsLearnP75AndExactCohorts(t *testing.T) {
	now := instant("2026-09-29T12:00:00Z")
	w := LearnedWindow{Kind: "weekly", Minutes: 10080, Plan: "Pro"}
	for i := 1; i <= 12; i++ {
		w.ObserveRun(RunSample{ID: fmt.Sprint(i), Profile: "profile", Model: "model", At: now, Percent: float64(i), Hours: 1, Tokens: 1e6})
	}
	got := w.Summarize(now, DefaultSchedule(), "profile")
	if got.HoldPercent != 9 || got.RunCount != 12 || got.RunPercent != 6 || got.PerMillion != 9 {
		t.Fatalf("p75 cohort: %+v", got)
	}
	w.ObserveRun(RunSample{ID: "12", Profile: "profile", Model: "model", At: now, Percent: 12, Hours: 1, Tokens: 1e6})
	if len(w.Runs) != 12 {
		t.Fatal("replay trained twice")
	}
	for i := 0; i < 3; i++ {
		w.ObserveRun(RunSample{ID: fmt.Sprint("foreign", i), Profile: "other", Model: "other", At: now, Percent: 90, Hours: 1})
	}
	if g := w.Summarize(now, DefaultSchedule(), "profile"); g.HoldPercent != 9 {
		t.Fatal("another profile changed hold")
	}
	if g := w.Summarize(now, DefaultSchedule(), ""); g.HoldPercent != 25 {
		t.Fatal("generic advice did not take conservative cohort")
	}
	w.Runs = []RunSample{{ID: "one", Profile: "profile", At: now, Percent: 20, Hours: 1}, {ID: "two", Profile: "profile", At: now, Percent: 20, Hours: 1}}
	if w.Summarize(now, DefaultSchedule(), "profile").HoldPercent != 0 {
		t.Fatal("learned before three runs")
	}
}
func TestLearningDecayAndFiveWorkDays(t *testing.T) {
	now := instant("2026-10-02T22:00:00Z")
	s := DefaultSchedule()
	w := LearnedWindow{Kind: "weekly", Minutes: 10080}
	for day := 0; day < 5; day++ {
		start := instant("2026-09-28T08:00:00Z").AddDate(0, 0, day)
		for h := 0; h < 14; h++ {
			w.ObserveUse(start.Add(time.Duration(h)*time.Hour), start.Add(time.Duration(h+1)*time.Hour), 2, true)
		}
	}
	got := w.Summarize(now, s, "")
	if math.Abs(got.AutoReserve-28) > 2 || got.WorkDays != 5 {
		t.Fatalf("own use: %+v", got)
	}
	w.Minutes = 300
	if got := w.Summarize(now, s, ""); math.Abs(got.AutoReserve-10) > 2 {
		t.Fatalf("short H_ref: %+v", got)
	}
	w.Own = w.Own[:56]
	if w.Summarize(now, s, "").AutoReserve != 0 {
		t.Fatal("learned reserve before five days")
	}
	w.Runs = nil
	for i := 0; i < 3; i++ {
		w.ObserveRun(RunSample{ID: fmt.Sprint(i), At: now, Percent: 8, Hours: 1})
		w.ObserveRun(RunSample{ID: fmt.Sprint("old", i), At: now.Add(-42 * 24 * time.Hour), Percent: 80, Hours: 1})
	}
	if w.Summarize(now, s, "").HoldPercent != 8 {
		t.Fatal("14-day half life not applied")
	}
}
func TestOwnUseExcludesManagedAndRejectsBadIntervals(t *testing.T) {
	now := instant("2026-09-29T12:00:00Z")
	w := LearnedWindow{}
	w.ObserveUse(now.Add(-time.Hour), now, 12, false)
	w.ObserveUse(now.Add(-7*time.Hour), now, 10, true)
	w.ObserveUse(now.Add(-time.Hour), now, -5, true)
	if len(w.Own) != 0 || len(w.Burn) != 1 {
		t.Fatal("ambiguous interval learned as own use")
	}
}
func TestBlindEstimatesRequireObservedCyclesAndExposeEvidence(t *testing.T) {
	now := instant("2026-09-29T12:00:00Z")
	if BlindEstimate(nil, 500, 0, now) != nil {
		t.Fatal("invented blind percentage")
	}
	one := LimitSample{At: now.Add(-7 * 24 * time.Hour), Tokens: 1000}
	if BlindEstimate([]LimitSample{one}, 500, 0, now) != nil {
		t.Fatal("invented duration from a single hit")
	}
	hits := []LimitSample{one, {At: now, Tokens: 1200}}
	got := BlindEstimate(hits, 600, 0, now)
	if got == nil || math.Abs(got.UsedPercent-600.0/11) > 0.01 || got.Evidence.Samples != 2 || got.PlusMinus < 3 || got.WindowMinutes != 10080 {
		t.Fatalf("blind estimate: %+v", got)
	}
	if got.Validate(now) != nil {
		t.Fatal("invalid learned reading")
	}
	// Cost calibration must never mix dollar and token levels.
	hits[0].Cost = 100
	hits[1].Cost = 120
	if got := BlindEstimate(hits, 99999, 50, now); math.Abs(got.UsedPercent-500.0/11) > 0.01 {
		t.Fatal("mixed tokens and dollars")
	}
}
func TestThroughputUsesWeekendExcessButHonoursRest(t *testing.T) {
	now := instant("2026-10-03T11:00:00Z")
	reset := instant("2026-10-05T09:00:00Z")
	s := DefaultSchedule()
	s.Reserve = ReserveOff
	in := PlanInput{Now: now, Reset: reset, WindowStart: now.Add(-time.Hour), WindowLength: 7 * 24 * time.Hour, Remaining: 50, Throughput: 8}
	got, err := Plan(in, s)
	if err != nil || !got.AllowOff || got.AvailableNowPercent <= 0 || math.Abs(got.WouldExpirePercent-42) > 0.1 {
		t.Fatalf("weekend %+v %v", got, err)
	}
	s.OffDays = "rest"
	rest, _ := Plan(in, s)
	if rest.AvailableNowPercent != 0 || rest.AllowOff {
		t.Fatal("rest day overridden")
	}
	s.OffDays = "expire"
	in.Throughput = 60
	fast, _ := Plan(in, s)
	if fast.AllowOff || fast.AvailableNowPercent != 0 {
		t.Fatal("off-day work without expiring excess")
	}
}
func TestWorkHoursRequireEvidenceAndAreOnlySuggestions(t *testing.T) {
	now := instant("2026-10-02T20:00:00Z")
	w := LearnedWindow{Kind: "weekly", Minutes: 10080}
	s := DefaultSchedule()
	for d := 0; d < 5; d++ {
		for h := 9; h < 19; h++ {
			at := instant("2026-09-28T00:00:00Z").Add(time.Duration(d*24+h) * time.Hour)
			w.ObserveUse(at, at.Add(time.Hour), 1, true)
		}
	}
	got := WorkHours([]LearnedWindow{w}, now, s)
	if got == nil || got.Days != 5 || got.Start < 9 || got.End > 19 {
		t.Fatalf("hours %+v", got)
	}
	if s.Week[0].Start != 8 || s.Week[0].End != 22 {
		t.Fatal("suggestion changed schedule")
	}
}

func TestBlindNamedWindowCalibratesFirstHit(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	r := BlindEstimate([]LimitSample{{At: now.Add(-time.Hour), Tokens: 1000, Reset: &reset, Window: "5h"}}, 500, 0, now)
	if r == nil || r.WindowMinutes != 300 || r.UsedPercent != 50 || r.Evidence.Samples != 1 || !r.ResetsAt.Equal(reset) {
		t.Fatalf("first named cycle %+v", r)
	}
}
