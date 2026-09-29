// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import "time"

// WorkingAt describes the person's work bands, independent of agent night work.
func (s Schedule) WorkingAt(now time.Time) bool {
	loc, _ := time.LoadLocation(s.Timezone)
	t := now.In(loc)
	d := s.Week[weekday(t)]
	h := float64(t.Hour()) + float64(t.Minute())/60
	return d.On && h >= d.Start && h < d.End
}

// NextStart uses the same half-hour grid and DST rules as Plan. A nil result
// means that no scheduled time is available in the next complete week.
func (s Schedule) NextStart(now time.Time, allowOff bool) *time.Time {
	loc, _ := time.LoadLocation(s.Timezone)
	for t := now; t.Before(now.AddDate(0, 0, 8)); {
		if s.rate(t.In(loc), allowOff) > 0 {
			return &t
		}
		local := t.In(loc)
		t = t.Add(time.Duration(30-local.Minute()%30)*time.Minute - time.Duration(local.Second())*time.Second - time.Duration(local.Nanosecond()))
	}
	return nil
}

// ActiveOverride is the override in force at now: Sprint ends at its reset,
// Away at its date, and Hold at its date when it has one.
func (s Schedule) ActiveOverride(now time.Time) string {
	if s.Override == "" || s.OverrideUntil != nil && !now.Before(*s.OverrideUntil) {
		return ""
	}
	return s.Override
}
