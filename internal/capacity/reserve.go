// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"math"
	"time"
)

// Keep for you (AEON-292 T2): the runway reserve. While the person's own work
// hours lie ahead of a window's reset, agents leave them R of that window. The
// reserve shrinks as the reset comes close, because after the reset the window
// is new anyway, so keeping it would only waste it:
//
//	R_eff = R × min(1, H_you / H_ref)
//
// H_you is the person's work-band hours (the week only, whatever the night
// model) between now and the reset; H_ref = min(window length, one work-day
// band). The reserve is a floor on top of the paced share, never a second share.
const (
	// AutoReserve is Auto until Aeon has learned the person's own use (T6).
	AutoReserve = 30.0
	MinReserve  = 10.0
	MaxReserve  = 80.0
)

// Reserve modes. An empty mode inherits from the next scope up (account, pool,
// person); on the person's own schedule it means Auto.
const (
	ReserveInherit = ""
	ReserveAuto    = "auto"
	ReserveFixed   = "fixed"
	ReserveOff     = "off"
)

func validReserve(mode string, percent float64) bool {
	switch mode {
	case ReserveInherit, ReserveAuto, ReserveOff:
		return percent == 0
	case ReserveFixed:
		return !math.IsNaN(percent) && percent >= MinReserve && percent <= MaxReserve && math.Mod(percent, 5) == 0
	}
	return false
}

// ReserveLevel is R for this schedule. auto is the learned Auto level; zero
// means not learned yet.
func (s Schedule) ReserveLevel(auto float64) float64 {
	switch s.Reserve {
	case ReserveOff:
		return 0
	case ReserveFixed:
		return s.ReservePercent
	}
	if auto > 0 {
		return auto
	}
	return AutoReserve
}

// bandHours is the longest work-day band, the day part of H_ref.
func (s Schedule) bandHours() float64 {
	band := 0.0
	for _, d := range s.Week {
		if d.On {
			band = math.Max(band, d.End-d.Start)
		}
	}
	return band
}

// Runway returns R_eff for a window of length resetting at reset, and the end
// of the person's last work band before that reset: the reserve is gone after
// it. It walks back from the reset on the same local half-hour grid as weight
// and stops once H_ref work hours are found, so long windows stay cheap. A
// zero length means unknown and uses the work-day band alone.
func (s Schedule) Runway(now, reset time.Time, length time.Duration, level float64) (float64, *time.Time) {
	if level <= 0 || !reset.After(now) {
		return 0, nil
	}
	ref := s.bandHours()
	if length > 0 {
		ref = math.Min(ref, length.Hours())
	}
	if ref <= 0 {
		return 0, nil
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return 0, nil
	}
	hours := 0.0
	var until *time.Time
	for t := reset; t.After(now) && hours < ref; {
		local := t.In(loc)
		back := time.Duration(local.Minute()%30)*time.Minute + time.Duration(local.Second())*time.Second + time.Duration(local.Nanosecond())
		if back == 0 {
			back = 30 * time.Minute
		}
		prev := t.Add(-back)
		if prev.Before(now) {
			prev = now
		}
		if s.WorkingAt(prev.Add(t.Sub(prev) / 2)) {
			if until == nil {
				end := t
				until = &end
			}
			hours += t.Sub(prev).Hours()
		}
		t = prev
	}
	if until == nil {
		return 0, nil
	}
	return level * math.Min(1, hours/ref), until
}
