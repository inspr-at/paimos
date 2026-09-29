// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"errors"
	"math"
	"time"
)

type Day struct {
	On    bool    `json:"on"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}
type Night struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	K     float64 `json:"k"`
}
type Shifts struct {
	Early float64   `json:"early"`
	Late  float64   `json:"late"`
	Night float64   `json:"night"`
	K     []float64 `json:"k"`
}
type Schedule struct {
	OverrideUntil *time.Time `json:"override_until,omitempty"`
	Override      string     `json:"override,omitempty"`
	Timezone      string     `json:"timezone"`
	Week          []Day      `json:"week"`
	OffDays       string     `json:"off_days"`
	Nights        bool       `json:"nights"`
	Model         string     `json:"model"`
	Night         Night      `json:"night"`
	Shifts        Shifts     `json:"shifts"`
	Blocks        []float64  `json:"blocks"`
}

func Preset(days int) []Day {
	w := make([]Day, 7)
	for i := range w {
		w[i] = Day{i < days, 8, 22}
	}
	return w
}
func DefaultSchedule(timezones ...string) Schedule {
	s := Schedule{Timezone: "UTC", Week: Preset(5), OffDays: "expire", Model: "daynight", Night: Night{22, 8, .6}, Shifts: Shifts{6, 14, 22, []float64{1, 1, .5}}, Blocks: make([]float64, 24)}
	if len(timezones) > 0 && timezones[0] != "" {
		s.Timezone = timezones[0]
	}
	for h := range s.Blocks {
		s.Blocks[h] = .5
		if h >= 8 && h < 22 {
			s.Blocks[h] = 1
		}
	}
	return s
}
func grid(v float64, end bool) bool {
	limit := 23.5
	if end {
		limit = 24
	}
	return !math.IsNaN(v) && v >= 0 && v <= limit && math.Mod(v, .5) == 0
}
func rateOK(v float64) bool { return v == 0 || v == 1 || v >= .1 && v <= .9 }
func (s Schedule) Validate() error {
	bad := errors.New("invalid capacity schedule")
	if s.Timezone == "" || (s.Override != "" && s.Override != "sprint" && s.Override != "hold") {
		return bad
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return bad
	}
	if len(s.Week) != 7 || len(s.Blocks) != 24 || len(s.Shifts.K) != 3 {
		return bad
	}
	on := false
	for _, d := range s.Week {
		on = on || d.On
		if !grid(d.Start, false) || !grid(d.End, true) || d.End <= d.Start {
			return bad
		}
	}
	if !on {
		return bad
	}
	if s.OffDays != "rest" && s.OffDays != "expire" && s.OffDays != "normal" {
		return bad
	}
	if s.Model != "daynight" && s.Model != "shifts" && s.Model != "blocks" {
		return bad
	}
	if !grid(s.Night.Start, false) || !grid(s.Night.End, false) || s.Night.Start == s.Night.End || math.IsNaN(s.Night.K) || s.Night.K < .1 || s.Night.K > 1 {
		return bad
	}
	if !grid(s.Shifts.Early, false) || !grid(s.Shifts.Late, false) || !grid(s.Shifts.Night, false) {
		return bad
	}
	l, n := s.shiftBounds()
	if l >= n || n >= s.Shifts.Early+24 {
		return bad
	}
	for _, v := range s.Shifts.K {
		if !rateOK(v) {
			return bad
		}
	}
	for _, v := range s.Blocks {
		if !rateOK(v) {
			return bad
		}
	}
	return nil
}
func (s Schedule) shiftBounds() (float64, float64) {
	l, n := s.Shifts.Late, s.Shifts.Night
	if l <= s.Shifts.Early {
		l += 24
	}
	if n <= s.Shifts.Early {
		n += 24
	}
	return l, n
}
func weekday(t time.Time) int { return int(t.Weekday()+6) % 7 }
func (s Schedule) active(t time.Time, allowOff bool) bool {
	return s.Week[weekday(t)].On || allowOff || s.OffDays == "normal"
}
func (s Schedule) rate(t time.Time, allowOff bool) float64 {
	h := float64(t.Hour()) + float64(t.Minute())/60 + float64(t.Second())/3600
	d := s.Week[weekday(t)]
	yesterday := t.AddDate(0, 0, -1)
	work := 0.0
	if s.active(t, allowOff) && h >= d.Start && h < d.End {
		work = 1
	}
	if !s.Nights {
		return work
	}
	switch s.Model {
	case "daynight":
		n := s.Night
		owns := s.active(t, allowOff)
		inside := h >= n.Start && h < n.End
		if n.Start >= n.End {
			inside = h >= n.Start || h < n.End
			if h < n.End {
				owns = s.active(yesterday, allowOff)
			}
		}
		if inside && owns {
			return math.Max(work, n.K)
		}
		return work
	case "shifts":
		owner := t
		if h < s.Shifts.Early {
			h += 24
			owner = yesterday
		}
		if !s.active(owner, allowOff) {
			return 0
		}
		l, n := s.shiftBounds()
		if h < l {
			return s.Shifts.K[0]
		}
		if h < n {
			return s.Shifts.K[1]
		}
		return s.Shifts.K[2]
	case "blocks":
		if s.active(t, allowOff) {
			return s.Blocks[t.Hour()]
		}
	}
	return 0
}

// Weight integrates exactly at local half-hour boundaries, counting repeated
// DST hours twice and missing hours zero times. All schedule edges use this grid.
func (s Schedule) weight(a, b time.Time, allowOff bool) (hours float64, finish *time.Time) {
	loc, _ := time.LoadLocation(s.Timezone)
	for t := a; t.Before(b); {
		local := t.In(loc)
		next := t.Add(time.Duration(30-local.Minute()%30)*time.Minute - time.Duration(local.Second())*time.Second - time.Duration(local.Nanosecond()))
		if next.After(b) {
			next = b
		}
		rate := s.rate(t.Add(next.Sub(t)/2).In(loc), allowOff)
		hours += next.Sub(t).Hours() * rate
		if rate > 0 {
			end := next
			finish = &end
		}
		t = next
	}
	return
}
func (s Schedule) Period(now time.Time) (time.Time, time.Time) {
	loc, _ := time.LoadLocation(s.Timezone)
	d := now.In(loc)
	boundary := 4.0
	for _, w := range s.Week {
		if w.On {
			boundary = math.Min(boundary, w.Start)
		}
	}
	if s.Nights {
		switch s.Model {
		case "daynight":
			if s.Night.Start >= s.Night.End {
				boundary = math.Min(boundary, s.Night.End)
			}
		case "shifts":
			boundary = s.Shifts.Early
		case "blocks":
			boundary = 0
		}
	}
	start := time.Date(d.Year(), d.Month(), d.Day(), int(boundary), int(math.Mod(boundary, 1)*60), 0, 0, loc)
	if start.After(now) {
		start = start.AddDate(0, 0, -1)
	}
	return start, start.AddDate(0, 0, 1)
}

type Pacing struct {
	AvailableNowPercent   float64    `json:"available_now_percent"`
	UsableHours           float64    `json:"usable_hours"`
	PercentPerHour        float64    `json:"percent_per_hour"`
	SuggestedTodayPercent float64    `json:"suggested_today_percent"`
	BudgetPercent         float64    `json:"budget_percent"`
	UsedTodayPercent      float64    `json:"used_today_percent"`
	TonightPercent        float64    `json:"tonight_percent"`
	PeriodStart           time.Time  `json:"period_start"`
	PeriodEnd             time.Time  `json:"period_end"`
	Finish                *time.Time `json:"finish,omitempty"`
	AllowOff              bool       `json:"allow_off"`
	Unused                bool       `json:"unused"`
	Ahead                 bool       `json:"ahead"`
}
type PlanInput struct {
	Now, Reset, WindowStart time.Time
	Remaining, UsedToday    float64
	Override                string // Empty, sprint, or hold. Overrides affect pacing, never vendor authority.
}

// Plan implements the approved schedule formula. UsedToday must be supplied
// from observed history, including interactive use, not token estimates.
func Plan(in PlanInput, s Schedule) (Pacing, error) {
	if err := s.Validate(); err != nil {
		return Pacing{}, err
	}
	if in.Override == "" {
		in.Override = s.Override
		if in.Override == "sprint" && s.OverrideUntil != nil && !in.Now.Before(*s.OverrideUntil) {
			in.Override = ""
		}
	}
	if math.IsNaN(in.Remaining) || math.IsNaN(in.UsedToday) || in.Remaining < 0 || in.UsedToday < 0 || in.Remaining+in.UsedToday > 100 || in.Reset.Sub(in.Now) > 367*24*time.Hour || in.Override != "" && in.Override != "hold" && in.Override != "sprint" {
		return Pacing{}, errors.New("invalid pacing input")
	}
	if !in.Reset.After(in.Now) {
		return Pacing{}, nil
	}
	a, b := s.Period(in.Now)
	start := a
	if in.WindowStart.After(start) {
		start = in.WindowStart
	}
	if start.After(in.Now) {
		return Pacing{}, errors.New("future capacity window")
	}
	p := Pacing{PeriodStart: a, PeriodEnd: b, UsedTodayPercent: in.UsedToday}
	total, _ := s.weight(start, in.Reset, false)
	if total == 0 && s.OffDays == "expire" {
		total, _ = s.weight(start, in.Reset, true)
		p.AllowOff = total > 0
	}
	p.UsableHours, p.Finish = s.weight(in.Now, in.Reset, p.AllowOff)
	if p.UsableHours > 0 {
		p.PercentPerHour = in.Remaining / p.UsableHours
	}
	leftAtStart := in.Remaining + in.UsedToday
	end := b
	if in.Reset.Before(end) {
		end = in.Reset
	}
	today, _ := s.weight(start, end, p.AllowOff)
	if total > 0 {
		p.BudgetPercent = leftAtStart * today / total
	}
	dayOff := !s.active(a, false)
	if !in.Reset.After(b) && !(dayOff && s.OffDays == "rest") {
		p.BudgetPercent = leftAtStart
	}
	if in.Override == "sprint" {
		p.BudgetPercent = leftAtStart
	}
	if in.Override == "hold" {
		p.BudgetPercent = in.UsedToday
	}
	p.SuggestedTodayPercent = math.Max(0, p.BudgetPercent-in.UsedToday)
	loc, _ := time.LoadLocation(s.Timezone)
	if in.Override == "sprint" || s.rate(in.Now.In(loc), p.AllowOff) > 0 {
		p.AvailableNowPercent = p.SuggestedTodayPercent
	}
	p.Ahead = in.UsedToday > p.BudgetPercent+.5
	p.Unused = in.Remaining > 0 && p.SuggestedTodayPercent == 0 && total == 0
	if s.Nights && (s.Model == "daynight" || s.Model == "shifts") && total > 0 {
		h := s.Night.Start
		if s.Model == "shifts" {
			h = s.Shifts.Night
		}
		nightStart := time.Date(a.Year(), a.Month(), a.Day(), int(h), int(math.Mod(h, 1)*60), 0, 0, a.Location())
		if nightStart.Before(a) {
			nightStart = nightStart.AddDate(0, 0, 1)
		}
		if nightStart.Before(start) {
			nightStart = start
		}
		w, _ := s.weight(nightStart, end, p.AllowOff)
		p.TonightPercent = leftAtStart * w / total
	}
	return p, nil
}

// Pace starts a projection now when a period-boundary usage baseline is absent.
func Pace(remaining float64, now, reset time.Time, s Schedule) (Pacing, error) {
	return Plan(PlanInput{Now: now, Reset: reset, WindowStart: now, Remaining: remaining}, s)
}

func (s *Schedule) UnmarshalJSON(raw []byte) error {
	type wire Schedule
	var v wire
	if err := requiredJSON(raw, &v, "timezone", "week", "off_days", "nights", "model", "night", "shifts", "blocks"); err != nil {
		return err
	}
	*s = Schedule(v)
	return nil
}
func (d *Day) UnmarshalJSON(raw []byte) error {
	type wire Day
	var v wire
	if err := requiredJSON(raw, &v, "on", "start", "end"); err != nil {
		return err
	}
	*d = Day(v)
	return nil
}
func (n *Night) UnmarshalJSON(raw []byte) error {
	type wire Night
	var v wire
	if err := requiredJSON(raw, &v, "start", "end", "k"); err != nil {
		return err
	}
	*n = Night(v)
	return nil
}
func (s *Shifts) UnmarshalJSON(raw []byte) error {
	type wire Shifts
	var v wire
	if err := requiredJSON(raw, &v, "early", "late", "night", "k"); err != nil {
		return err
	}
	*s = Shifts(v)
	return nil
}
