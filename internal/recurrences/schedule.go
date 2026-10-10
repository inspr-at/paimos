// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Scheduling must also work in minimal server images.
)

// Trigger is deliberately a bounded RFC 5545 subset. Unsupported rule parts
// fail validation rather than silently changing the user's schedule.
type Trigger struct {
	Kind          string `json:"kind"`
	RRULE         string `json:"rrule,omitempty"`
	TimeOfDay     string `json:"time_of_day,omitempty"`
	Timezone      string `json:"timezone,omitempty"`
	StartDate     string `json:"start_date,omitempty"`
	Event         string `json:"event,omitempty"`
	EventStart    string `json:"event_start,omitempty"`
	EventTimezone string `json:"event_timezone,omitempty"`
}
type schedule struct {
	location     *time.Location
	start        time.Time // Calendar dates are advanced in UTC, never by 24h in a zone.
	hour, minute int
	frequency    string
	interval     int
	days         map[time.Weekday]bool
	monthDays    map[int]bool
}

var weekdays = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

func parseSchedule(t Trigger) (*schedule, error) {
	if t.Kind != "time" || t.Event != "" || t.EventStart != "" || t.EventTimezone != "" || len(t.RRULE) > 256 || len(t.Timezone) > 128 || t.Timezone == "Local" || t.Timezone == "" {
		return nil, fmt.Errorf("time trigger requires an RRULE, HH:MM and IANA timezone")
	}
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone")
	}
	clock, err := time.Parse("15:04", t.TimeOfDay)
	if err != nil || clock.Format("15:04") != t.TimeOfDay {
		return nil, fmt.Errorf("time_of_day must be HH:MM")
	}
	start, err := time.Parse(time.DateOnly, t.StartDate)
	if err != nil || start.Year() < 1900 || start.Year() > 9998 {
		return nil, fmt.Errorf("start_date must be a valid date between 1900 and 9998")
	}
	s := &schedule{location: loc, start: start, hour: clock.Hour(), minute: clock.Minute(), interval: 1, days: map[time.Weekday]bool{}, monthDays: map[int]bool{}}
	parts := map[string]string{}
	for _, part := range strings.Split(t.RRULE, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || v == "" || parts[k] != "" {
			return nil, fmt.Errorf("invalid or duplicate RRULE part")
		}
		parts[k] = v
	}
	for k := range parts {
		if k != "FREQ" && k != "BYDAY" && k != "BYMONTHDAY" && k != "INTERVAL" {
			return nil, fmt.Errorf("unsupported RRULE part %s", k)
		}
	}
	s.frequency = parts["FREQ"]
	switch s.frequency {
	case "DAILY":
		if len(parts) != 1 {
			return nil, fmt.Errorf("DAILY supports only FREQ")
		}
	case "WEEKLY":
		if parts["INTERVAL"] != "" {
			return nil, fmt.Errorf("INTERVAL is supported only for MONTHLY")
		}
		if parts["BYMONTHDAY"] != "" {
			return nil, fmt.Errorf("WEEKLY does not support BYMONTHDAY")
		}
		if parts["BYDAY"] == "" {
			s.days[start.Weekday()] = true
		} else {
			for _, d := range strings.Split(parts["BYDAY"], ",") {
				day, ok := weekdays[d]
				if !ok || s.days[day] {
					return nil, fmt.Errorf("invalid or duplicate BYDAY")
				}
				s.days[day] = true
			}
		}
	case "MONTHLY":
		if raw := parts["INTERVAL"]; raw != "" {
			switch raw {
			case "1", "2", "3", "6":
				s.interval, _ = strconv.Atoi(raw)
			default:
				return nil, fmt.Errorf("MONTHLY INTERVAL must be 1, 2, 3 or 6")
			}
		}
		if parts["BYDAY"] != "" {
			return nil, fmt.Errorf("MONTHLY does not support BYDAY")
		}
		if parts["BYMONTHDAY"] == "" {
			s.monthDays[start.Day()] = true
		} else {
			for _, raw := range strings.Split(parts["BYMONTHDAY"], ",") {
				d, err := strconv.Atoi(raw)
				if err != nil || d == 0 || d < -31 || d > 31 || s.monthDays[d] {
					return nil, fmt.Errorf("invalid or duplicate BYMONTHDAY")
				}
				s.monthDays[d] = true
			}
		}
	default:
		return nil, fmt.Errorf("FREQ must be DAILY, WEEKLY or MONTHLY")
	}
	return s, nil
}
func (s *schedule) matches(d time.Time) bool {
	if d.Before(s.start) {
		return false
	}
	switch s.frequency {
	case "WEEKLY":
		return s.days[d.Weekday()]
	case "MONTHLY":
		months := (d.Year()-s.start.Year())*12 + int(d.Month()-s.start.Month())
		if months%s.interval != 0 {
			return false
		}
		last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		return s.monthDays[d.Day()] || s.monthDays[d.Day()-last-1]
	default:
		return true
	}
}

// instant rejects gaps and resolves folds to their earliest instant (RFC 5545
// 3.3.10 / 3.3.5). time.Date's choice in a fold is intentionally unspecified.
func (s *schedule) instant(d time.Time) (time.Time, bool) {
	wall := time.Date(d.Year(), d.Month(), d.Day(), s.hour, s.minute, 0, 0, time.UTC)
	offsets := map[int]bool{}
	for h := -48; h <= 48; h += 6 {
		_, offset := wall.Add(time.Duration(h) * time.Hour).In(s.location).Zone()
		offsets[offset] = true
	}
	var first time.Time
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(s.location)
		if local.Year() == d.Year() && local.Month() == d.Month() && local.Day() == d.Day() && local.Hour() == s.hour && local.Minute() == s.minute && (first.IsZero() || candidate.Before(first)) {
			first = candidate
		}
	}
	return first, !first.IsZero()
}
func (s *schedule) next(after time.Time) (time.Time, error) {
	local := after.In(s.location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if day.Before(s.start) {
		day = s.start
	}
	// A six-month interval on the 31st can skip a short month and a DST gap.
	// Four years bounds that search without dropping a valid sparse schedule.
	for i := 0; i < 4*366; i++ {
		if s.matches(day) {
			if at, ok := s.instant(day); ok && at.After(after) {
				return at, nil
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, fmt.Errorf("no occurrence within four years")
}
func (s *schedule) latest(now time.Time) (time.Time, error) {
	local := now.In(s.location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4*366 && !day.Before(s.start); i++ {
		if s.matches(day) {
			if at, ok := s.instant(day); ok && !at.After(now) {
				return at, nil
			}
		}
		day = day.AddDate(0, 0, -1)
	}
	return time.Time{}, fmt.Errorf("no past occurrence within four years")
}

// Preview uses exactly the scheduler's calendar calculation, strictly after
// the anchor, regardless of whether the recurrence is paused.
func Preview(t Trigger, after time.Time, count int) ([]time.Time, error) {
	out := []time.Time{}
	if count < 1 || count > 100 {
		return nil, fmt.Errorf("count must be 1..100")
	}
	if t.Kind == "event" {
		return out, nil
	}
	s, err := parseSchedule(t)
	if err != nil {
		return nil, err
	}
	for i := 0; i < count; i++ {
		after, err = s.next(after)
		if err != nil {
			return nil, err
		}
		out = append(out, after)
	}
	return out, nil
}
