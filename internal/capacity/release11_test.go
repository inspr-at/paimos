// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"errors"
	"math"
	"time"
)

// release11Plan is Plan exactly as release 11 shipped it (ab02e358), frozen as
// the reference for "reserve: off reproduces release 11" (AEON-375). Only the
// name changed; it shares the schedule helpers, which the reserve left alone.
func release11Plan(in PlanInput, s Schedule) (Pacing, error) {
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
