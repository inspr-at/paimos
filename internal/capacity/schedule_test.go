// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("got %v want %v", got, want)
	}
}
func TestPacingScheduleBranches(t *testing.T) {
	for _, tt := range []struct {
		name, start, end, off string
		nights                float64
		hours, today          float64
	}{
		{"weekday", "2026-09-28T08:00:00Z", "2026-09-29T08:00:00Z", "rest", 0, 14, 70},
		{"partial", "2026-09-28T21:30:00Z", "2026-09-29T09:00:00Z", "rest", 0, 1.5, 70.0 / 3},
		{"night", "2026-09-28T08:00:00Z", "2026-09-29T08:00:00Z", "rest", .5, 19, 70 * 17.0 / 19},
		{"weekend rest", "2026-10-03T08:00:00Z", "2026-10-04T22:00:00Z", "rest", 0, 0, 0},
		{"weekend expiry", "2026-10-03T08:00:00Z", "2026-10-04T22:00:00Z", "expire", 0, 28, 35},
		{"carry into work", "2026-10-03T08:00:00Z", "2026-10-05T22:00:00Z", "expire", 0, 14, 0},
		{"work normally", "2026-10-03T08:00:00Z", "2026-10-05T22:00:00Z", "normal", 0, 42, 70.0 / 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := DefaultSchedule()
			s.OffDays = tt.off
			if tt.nights > 0 {
				s.Nights = true
				s.Night.K = tt.nights
			}
			p, e := Pace(70, instant(tt.start), instant(tt.end), s)
			if e != nil {
				t.Fatal(e)
			}
			near(t, p.UsableHours, tt.hours)
			near(t, p.SuggestedTodayPercent, tt.today)
		})
	}
}
func TestScheduleModesOwnershipAndReducedRates(t *testing.T) {
	s := DefaultSchedule()
	if s.Shifts.K[2] != .5 || s.Blocks[0] != .5 {
		t.Fatal("reduced shifts and blocks must default to 50 percent")
	}
	s.Nights = true
	s.Night.K = .3
	near(t, s.rate(instant("2026-10-03T02:00:00Z"), false), .3) // Friday's night.
	near(t, s.rate(instant("2026-10-04T02:00:00Z"), false), 0)
	s.Model = "shifts"
	s.Shifts = Shifts{6, 14, 22, []float64{.2, .7, .4}}
	near(t, s.rate(instant("2026-09-28T09:00:00Z"), false), .2) // Replaces 1x work hours.
	near(t, s.rate(instant("2026-09-28T16:00:00Z"), false), .7)
	near(t, s.rate(instant("2026-10-03T05:00:00Z"), false), .4)
	near(t, s.rate(instant("2026-10-03T07:00:00Z"), false), 0)
	s.Shifts = Shifts{22, 6, 14, []float64{1, .2, .9}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	near(t, s.rate(instant("2026-10-03T02:00:00Z"), false), 1)
	s.Model = "blocks"
	s.Blocks[9] = .3
	s.Blocks[10] = 0
	near(t, s.rate(instant("2026-09-28T09:30:00Z"), false), .3)
	near(t, s.rate(instant("2026-09-28T10:30:00Z"), false), 0)
	s.Nights = false
	near(t, s.rate(instant("2026-09-28T10:30:00Z"), false), 1) // Toggle preserves model but disables it.
	s.Week[0] = Day{true, 9.5, 12.5}
	h, _ := s.weight(instant("2026-09-28T00:00:00Z"), instant("2026-09-29T00:00:00Z"), false)
	near(t, h, 3)
}
func TestSchedulePlanUsageAndOverrides(t *testing.T) {
	s := DefaultSchedule()
	s.OffDays = "rest"
	in := PlanInput{Now: instant("2026-09-28T15:00:00Z"), WindowStart: instant("2026-09-28T08:00:00Z"), Reset: instant("2026-09-29T22:00:00Z"), Remaining: 60, UsedToday: 20}
	p, e := Plan(in, s)
	if e != nil {
		t.Fatal(e)
	}
	near(t, p.BudgetPercent, 40)
	near(t, p.SuggestedTodayPercent, 20)
	near(t, p.UsableHours, 21)
	if p.PeriodStart.Hour() != 4 || p.Ahead || p.Finish == nil {
		t.Fatal(p)
	}
	in.UsedToday = 50
	in.Remaining = 30
	p, _ = Plan(in, s)
	if !p.Ahead {
		t.Fatal("ahead missing")
	}
	near(t, p.SuggestedTodayPercent, 0)
	in.Override = "sprint"
	p, _ = Plan(in, s)
	near(t, p.SuggestedTodayPercent, 30)
	in.Override = "hold"
	p, _ = Plan(in, s)
	near(t, p.SuggestedTodayPercent, 0)
	in.Override = ""
	in.Reset = instant("2026-09-28T20:00:00Z")
	p, _ = Plan(in, s)
	near(t, p.BudgetPercent, 80)
	s.Nights = true
	s.Model = "shifts"
	a, b := s.Period(instant("2026-09-29T05:00:00Z"))
	if a.Day() != 28 || a.Hour() != 6 || b.Day() != 29 {
		t.Fatal(a, b)
	}
	s.Model = "blocks"
	a, _ = s.Period(in.Now)
	if a.Hour() != 0 {
		t.Fatal(a)
	}
}
func TestScheduleDSTAndValidation(t *testing.T) {
	s := DefaultSchedule()
	s.Timezone = "Europe/Vienna"
	s.OffDays = "normal"
	s.Nights = true
	s.Model = "blocks"
	for i := range s.Blocks {
		s.Blocks[i] = 1
	}
	for _, tt := range []struct {
		a, b  string
		hours float64
	}{{"2026-03-28T23:00:00Z", "2026-03-29T22:00:00Z", 23}, {"2026-10-24T22:00:00Z", "2026-10-25T23:00:00Z", 25}} {
		p, e := Pace(50, instant(tt.a), instant(tt.b), s)
		if e != nil {
			t.Fatal(e)
		}
		near(t, p.UsableHours, tt.hours)
		near(t, p.SuggestedTodayPercent, 50)
	}
	for _, change := range []func(*Schedule){func(s *Schedule) { s.Timezone = "bad/zone" }, func(s *Schedule) { s.Week = Preset(0) }, func(s *Schedule) { s.Week[0].Start = 8.1 }, func(s *Schedule) { s.Week[0].End = 7 }, func(s *Schedule) { s.Night.K = math.NaN() }, func(s *Schedule) { s.Shifts.Late = 23 }, func(s *Schedule) { s.Blocks[0] = .95 }, func(s *Schedule) { s.Blocks = s.Blocks[:23] }, func(s *Schedule) { s.OffDays = "whatever" }} {
		bad := DefaultSchedule()
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("bad schedule accepted")
		}
	}
	raw, _ := json.Marshal(DefaultSchedule())
	var round Schedule
	if e := json.Unmarshal(raw, &round); e != nil || round.Validate() != nil {
		t.Fatal(e)
	}
	for _, v := range []float64{-1, 101, math.NaN(), math.Inf(1)} {
		if _, e := Pace(v, time.Now(), time.Now().Add(time.Hour), s); e == nil {
			t.Fatal("bad remaining accepted")
		}
	}
}

func TestPlanZeroRemainingAndInvalidTotal(t *testing.T) {
	s := DefaultSchedule("Europe/Vienna")
	if s.Timezone != "Europe/Vienna" {
		t.Fatal(s.Timezone)
	}
	in := PlanInput{Now: instant("2026-09-28T12:00:00Z"), Reset: instant("2026-09-30T12:00:00Z"), WindowStart: instant("2026-09-28T08:00:00Z"), UsedToday: 100}
	for _, override := range []string{"", "sprint", "hold"} {
		in.Override = override
		p, err := Plan(in, s)
		if err != nil || p.SuggestedTodayPercent != 0 {
			t.Fatal(p, err)
		}
	}
	in.Remaining = 1
	if _, err := Plan(in, s); err == nil {
		t.Fatal("remaining + used_today > 100 accepted")
	}
	s.Override = "unknown"
	if s.Validate() == nil {
		t.Fatal("unknown override accepted")
	}
}

func TestCurrentBandAndSprintReset(t *testing.T) {
	s := DefaultSchedule()
	in := PlanInput{Now: instant("2026-09-28T23:00:00Z"), WindowStart: instant("2026-09-28T08:00:00Z"), Reset: instant("2026-09-30T22:00:00Z"), Remaining: 60}
	p, err := Plan(in, s)
	if err != nil || p.AvailableNowPercent != 0 {
		t.Fatal("off band allowed", p, err)
	}
	s.Override = "sprint"
	until := in.Now.Add(time.Hour)
	s.OverrideUntil = &until
	p, err = Plan(in, s)
	if err != nil || p.AvailableNowPercent != 60 {
		t.Fatal("Sprint not literal", p, err)
	}
	in.Now = until
	p, err = Plan(in, s)
	if err != nil || p.AvailableNowPercent != 0 {
		t.Fatal("Sprint survived reset", p, err)
	}
	s.Override = "hold"
	in.Now = instant("2026-09-29T12:00:00Z")
	p, err = Plan(in, s)
	if err != nil || p.AvailableNowPercent != 0 {
		t.Fatal("Hold allowed", p, err)
	}
}
