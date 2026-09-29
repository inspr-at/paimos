// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func vienna(t *testing.T, local string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02 15:04", local, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The six worked cases of the design (capacity-design-pass-2 §2.2): R = 30,
// work bands Mon–Fri 08–22, nights on 22–08. 2026-09-29 is a Tuesday.
func TestRunwayReserveDesignCases(t *testing.T) {
	s := DefaultSchedule("Europe/Vienna")
	s.Nights = true
	for _, tt := range []struct {
		name               string
		now, start, reset  string
		length             time.Duration
		remaining, used    float64
		override           string
		reserve, agentsMay float64 // R_eff; what agents may use now (-1: not asserted)
		unchangedByReserve bool    // the floor does not bind yet
		untilLocal         string
	}{
		{"Tue 14:00 Claude 5-hour, 50% used", "2026-09-29 14:00", "2026-09-29 13:00", "2026-09-29 18:00", 5 * time.Hour, 50, 50, "", 24, 26, false, "2026-09-29 18:00"},
		{"Tue 23:30 Claude 5-hour resets 03:10", "2026-09-29 23:30", "2026-09-29 22:10", "2026-09-30 03:10", 5 * time.Hour, 60, 40, "", 0, 60, false, ""},
		{"Tue 10:00 weekly resets Fri 09:14", "2026-09-29 10:00", "2026-09-29 08:00", "2026-10-02 09:14", 7 * 24 * time.Hour, 58, 0, "", 30, -1, true, "2026-10-02 09:14"},
		{"Thu 22:00 same weekly", "2026-10-01 22:00", "2026-10-01 08:00", "2026-10-02 09:14", 7 * 24 * time.Hour, 20, 0, "", 2.6, -1, false, "2026-10-02 09:14"},
		{"Sat 11:00 weekly resets Mon 09:00", "2026-10-03 11:00", "2026-10-03 08:00", "2026-10-05 09:00", 7 * 24 * time.Hour, 50, 0, "", 2.1, -1, false, "2026-10-05 09:00"},
		{"Sprint takes everything", "2026-09-29 14:00", "2026-09-29 13:00", "2026-09-29 18:00", 5 * time.Hour, 50, 50, "sprint", 0, 50, false, ""},
		{"Away takes everything", "2026-09-29 10:00", "2026-09-29 08:00", "2026-10-02 09:14", 7 * 24 * time.Hour, 58, 0, "away", 0, 58, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := PlanInput{Now: vienna(t, tt.now), WindowStart: vienna(t, tt.start), Reset: vienna(t, tt.reset), Remaining: tt.remaining, UsedToday: tt.used, Override: tt.override, WindowLength: tt.length}
			p, err := Plan(in, s)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(p.ReserveEffectivePercent-tt.reserve) > .5 {
				t.Fatalf("R_eff %v want %v", p.ReserveEffectivePercent, tt.reserve)
			}
			if p.ReservePercent != 30 {
				t.Fatalf("R %v", p.ReservePercent)
			}
			if tt.agentsMay >= 0 && math.Abs(p.AvailableNowPercent-tt.agentsMay) > .5 {
				t.Fatalf("agents may use %v want %v", p.AvailableNowPercent, tt.agentsMay)
			}
			off := s
			off.Reserve = ReserveOff
			q, err := Plan(in, off)
			if err != nil {
				t.Fatal(err)
			}
			if tt.unchangedByReserve && (p.AvailableNowPercent != q.AvailableNowPercent || p.AvailableNowPercent == 0) {
				t.Fatalf("the floor should not bind yet: %v vs %v", p.AvailableNowPercent, q.AvailableNowPercent)
			}
			if p.AvailableNowPercent > q.AvailableNowPercent || p.SuggestedTodayPercent != q.SuggestedTodayPercent || p.BudgetPercent != q.BudgetPercent {
				t.Fatal("the reserve must only lower what agents may take now", p, q)
			}
			if tt.untilLocal == "" {
				if p.ReserveUntil != nil {
					t.Fatal("reserve until without a reserve", p.ReserveUntil)
				}
			} else if p.ReserveUntil == nil || !p.ReserveUntil.Equal(vienna(t, tt.untilLocal)) {
				t.Fatalf("reserve until %v want %v", p.ReserveUntil, tt.untilLocal)
			}
		})
	}
}

func TestRunwayReserveEndsWithTheLastWorkBand(t *testing.T) {
	s := DefaultSchedule("Europe/Vienna")
	// A 5-hour window resetting at 01:00 keeps a shrinking reserve until 22:00.
	now, reset := vienna(t, "2026-09-29 20:30"), vienna(t, "2026-09-30 01:00")
	eff, until := s.Runway(now, reset, 5*time.Hour, 30)
	if math.Abs(eff-9) > 1e-9 || until == nil || !until.Equal(vienna(t, "2026-09-29 22:00")) {
		t.Fatal(eff, until)
	}
	// Hold leaves agents nothing and still reports the reserve.
	s.Override = "hold"
	p, err := Plan(PlanInput{Now: now, WindowStart: vienna(t, "2026-09-29 20:00"), Reset: reset, Remaining: 80, WindowLength: 5 * time.Hour}, s)
	if err != nil || p.AvailableNowPercent != 0 || math.Abs(p.ReserveEffectivePercent-9) > 1e-9 {
		t.Fatal(p, err)
	}
	// The floor never goes negative, and it never hands out more than left.
	s.Override = ""
	p, err = Plan(PlanInput{Now: now, WindowStart: vienna(t, "2026-09-29 20:00"), Reset: reset, Remaining: 5, UsedToday: 20, WindowLength: 5 * time.Hour}, s)
	if err != nil || p.AvailableNowPercent != 0 {
		t.Fatal("below the reserve must leave agents nothing", p, err)
	}
	// Fixed and learned Auto levels; unknown length uses the work-day band.
	s.Reserve, s.ReservePercent = ReserveFixed, 50
	if s.ReserveLevel(22) != 50 {
		t.Fatal("fixed level")
	}
	s.Reserve, s.ReservePercent = ReserveAuto, 0
	if s.ReserveLevel(22) != 22 || s.ReserveLevel(0) != AutoReserve {
		t.Fatal("auto level")
	}
	if eff, _ := s.Runway(vienna(t, "2026-09-29 15:00"), vienna(t, "2026-09-29 22:00"), 0, 28); math.Abs(eff-14) > 1e-9 {
		t.Fatal("unknown length", eff)
	}
	// Days off and nights are not the person's hours.
	if eff, until := s.Runway(vienna(t, "2026-10-03 09:00"), vienna(t, "2026-10-04 20:00"), 7*24*time.Hour, 30); eff != 0 || until != nil {
		t.Fatal("weekend reserve", eff, until)
	}
}

func TestRunwayReserveDST(t *testing.T) {
	s := DefaultSchedule("Europe/Vienna")
	s.Week = Preset(7)
	for i := range s.Week {
		s.Week[i] = Day{true, 0, 24}
	}
	// The fall-back day has 25 real hours; 5 of them before a 05:00 reset.
	eff, until := s.Runway(vienna(t, "2026-10-25 00:00"), vienna(t, "2026-10-25 04:00"), 5*time.Hour, 30)
	if math.Abs(eff-30) > 1e-9 || until == nil {
		t.Fatal(eff, until)
	}
	eff, _ = s.Runway(vienna(t, "2026-10-25 00:00"), vienna(t, "2026-10-25 03:00"), 5*time.Hour, 30)
	if math.Abs(eff-24) > 1e-9 { // 00:00–03:00 local spans 4 real hours.
		t.Fatal(eff)
	}
}

func TestReserveValidationAndJSON(t *testing.T) {
	ok := []func(*Schedule){
		func(s *Schedule) {},
		func(s *Schedule) { s.Reserve = ReserveAuto },
		func(s *Schedule) { s.Reserve = ReserveOff },
		func(s *Schedule) { s.Reserve, s.ReservePercent = ReserveFixed, 10 },
		func(s *Schedule) { s.Reserve, s.ReservePercent = ReserveFixed, 80 },
		func(s *Schedule) { u := time.Now(); s.Override, s.OverrideUntil = "away", &u },
		func(s *Schedule) { u := time.Now(); s.Override, s.OverrideUntil = "hold", &u },
	}
	for i, change := range ok {
		s := DefaultSchedule()
		change(&s)
		if err := s.Validate(); err != nil {
			t.Fatal(i, err)
		}
	}
	bad := []func(*Schedule){
		func(s *Schedule) { s.Reserve = "most" },
		func(s *Schedule) { s.Reserve, s.ReservePercent = ReserveFixed, 5 },
		func(s *Schedule) { s.Reserve, s.ReservePercent = ReserveFixed, 85 },
		func(s *Schedule) { s.Reserve, s.ReservePercent = ReserveFixed, 32 },
		func(s *Schedule) { s.Reserve, s.ReservePercent = ReserveFixed, math.NaN() },
		func(s *Schedule) { s.Reserve, s.ReservePercent = ReserveAuto, 30 },
		func(s *Schedule) { s.ReservePercent = 30 },
		func(s *Schedule) { s.Override = "away" },
	}
	for i, change := range bad {
		s := DefaultSchedule()
		change(&s)
		if s.Validate() == nil {
			t.Fatal("accepted", i)
		}
	}
	s := DefaultSchedule()
	s.Reserve, s.ReservePercent = ReserveFixed, 25
	raw, _ := json.Marshal(s)
	var round Schedule
	if err := json.Unmarshal(raw, &round); err != nil || round.Reserve != ReserveFixed || round.ReservePercent != 25 {
		t.Fatal(string(raw), err)
	}
	// Release 11 schedules have no reserve: the person's own one means Auto.
	raw, _ = json.Marshal(DefaultSchedule())
	if err := json.Unmarshal(raw, &round); err != nil || round.Reserve != "" || round.ReserveLevel(0) != AutoReserve {
		t.Fatal(string(raw), err)
	}
}

// reserve: off reproduces release 11's Plan on the existing test corpus and a
// dense grid of schedules, instants, windows and overrides.
func TestReserveOffReproducesRelease11(t *testing.T) {
	type pair struct {
		in PlanInput
		s  Schedule
	}
	var corpus []pair
	// The existing test corpus (schedule_test.go).
	branches := func(off string, nights float64) Schedule {
		s := DefaultSchedule()
		s.OffDays = off
		if nights > 0 {
			s.Nights, s.Night.K = true, nights
		}
		return s
	}
	for _, c := range []struct {
		start, end, off string
		nights          float64
	}{
		{"2026-09-28T08:00:00Z", "2026-09-29T08:00:00Z", "rest", 0},
		{"2026-09-28T21:30:00Z", "2026-09-29T09:00:00Z", "rest", 0},
		{"2026-09-28T08:00:00Z", "2026-09-29T08:00:00Z", "rest", .5},
		{"2026-10-03T08:00:00Z", "2026-10-04T22:00:00Z", "rest", 0},
		{"2026-10-03T08:00:00Z", "2026-10-04T22:00:00Z", "expire", 0},
		{"2026-10-03T08:00:00Z", "2026-10-05T22:00:00Z", "expire", 0},
		{"2026-10-03T08:00:00Z", "2026-10-05T22:00:00Z", "normal", 0},
	} {
		now := instant(c.start)
		corpus = append(corpus, pair{PlanInput{Now: now, Reset: instant(c.end), WindowStart: now, Remaining: 70}, branches(c.off, c.nights)})
	}
	rest := DefaultSchedule()
	rest.OffDays = "rest"
	base := PlanInput{Now: instant("2026-09-28T15:00:00Z"), WindowStart: instant("2026-09-28T08:00:00Z"), Reset: instant("2026-09-29T22:00:00Z"), Remaining: 60, UsedToday: 20}
	for _, o := range []string{"", "sprint", "hold"} {
		in := base
		in.Override = o
		corpus = append(corpus, pair{in, rest})
		in.UsedToday, in.Remaining = 50, 30
		corpus = append(corpus, pair{in, rest})
		in.Reset = instant("2026-09-28T20:00:00Z")
		corpus = append(corpus, pair{in, rest})
	}
	sprint := DefaultSchedule()
	sprint.Override = "sprint"
	until := instant("2026-09-29T00:00:00Z")
	sprint.OverrideUntil = &until
	band := PlanInput{Now: instant("2026-09-28T23:00:00Z"), WindowStart: instant("2026-09-28T08:00:00Z"), Reset: instant("2026-09-30T22:00:00Z"), Remaining: 60}
	corpus = append(corpus, pair{band, DefaultSchedule()}, pair{band, sprint})
	band.Now = until
	corpus = append(corpus, pair{band, sprint})
	// A dense grid: every schedule model and off-day rule, two zones, instants
	// across a week and a DST change, 5-hour to monthly windows.
	var schedules []Schedule
	for _, zone := range []string{"UTC", "Europe/Vienna", "America/New_York"} {
		for _, off := range []string{"rest", "expire", "normal"} {
			for _, model := range []string{"", "daynight", "shifts", "blocks"} {
				s := DefaultSchedule(zone)
				s.OffDays = off
				if model != "" {
					s.Nights, s.Model = true, model
				}
				schedules = append(schedules, s)
			}
		}
		s := DefaultSchedule(zone)
		s.Week = Preset(6)
		s.Week[2] = Day{true, 9.5, 17}
		schedules = append(schedules, s)
	}
	type window struct {
		length, left time.Duration
	}
	windows := []window{{5 * time.Hour, 17 * time.Minute}, {5 * time.Hour, 4*time.Hour + 59*time.Minute}, {7 * 24 * time.Hour, 3 * time.Hour}, {7 * 24 * time.Hour, 29 * time.Hour}, {7 * 24 * time.Hour, 6*24*time.Hour + 23*time.Hour}, {30 * 24 * time.Hour, 12 * 24 * time.Hour}}
	levels := [][2]float64{{100, 0}, {58, 12}, {5, 40}, {0, 100}}
	starts := []time.Time{instant("2026-09-26T05:00:00Z"), instant("2026-10-24T05:00:00Z")}
	for si, s := range schedules {
		for _, start := range starts {
			for step := 0; step < 24; step++ {
				now := start.Add(time.Duration(step) * (7*time.Hour + 13*time.Minute))
				for wi, w := range windows {
					reset := now.Add(w.left)
					level := levels[(si+step+wi)%len(levels)]
					in := PlanInput{Now: now, Reset: reset, WindowStart: reset.Add(-w.length), Remaining: level[0], UsedToday: level[1]}
					if (step+wi)%3 == 0 {
						in.WindowStart = now
					}
					if (si+step)%7 == 0 {
						in.Override = []string{"sprint", "hold"}[step%2]
					}
					corpus = append(corpus, pair{in, s})
				}
			}
		}
	}
	paced := 0
	for i, c := range corpus {
		want, wantErr := release11Plan(c.in, c.s)
		if wantErr == nil && want.AvailableNowPercent > 0 {
			paced++
		}
		off := c.s
		off.Reserve = ReserveOff
		in := c.in
		in.WindowLength = c.in.Reset.Sub(c.in.WindowStart)
		got, gotErr := Plan(in, off)
		if (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d differs from release 11:\n got %+v %v\nwant %+v %v", i, got, gotErr, want, wantErr)
		}
	}
	if len(corpus) < 2000 || paced < len(corpus)/4 {
		t.Fatal("corpus too small", len(corpus), paced)
	}
}
