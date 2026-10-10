// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"math"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
)

// ProbeFreshness is the oldest successful owner-daemon probe that can still
// accept a new reservation.
const ProbeFreshness = agentplan.DailyFreshness

func elapsedFraction(now, start, end time.Time) float64 {
	span := end.Sub(start)
	if span <= 0 {
		return 0
	}
	f := float64(now.Sub(start)) / float64(span)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// paceFraction is min(1, pace(f)+burst). An unknown pace model allows nothing.
func paceFraction(model string, f, burst float64) float64 {
	var pace float64
	switch model {
	case "steady":
		pace = f
	case "frontload":
		pace = 1 - (1-f)*(1-f)
	case "unrestricted":
		pace = 1
	default:
		return 0
	}
	sum := pace + burst
	if sum < 0 {
		return 0
	}
	if sum > 1 {
		return 1
	}
	return sum
}

func allowedUnits(allowance int64, fraction float64) int64 {
	if allowance <= 0 || fraction <= 0 {
		return 0
	}
	if fraction >= 1 {
		return allowance
	}
	return int64(math.Floor(fraction*float64(allowance) + 1e-9))
}

func addUsage(used, reserved, estimate int64) (int64, bool) {
	if used < 0 || reserved < 0 || estimate < 0 {
		return 0, false
	}
	if used > math.MaxInt64-reserved {
		return 0, false
	}
	sum := used + reserved
	if sum > math.MaxInt64-estimate {
		return 0, false
	}
	return sum + estimate, true
}

// fits reports whether estimate can be reserved on the window at now, and the
// projected used+reserved+estimate.
func fits(w Window, now time.Time, estimate int64) (int64, bool) {
	if estimate < 1 || !now.Before(w.EndsAt) || now.Before(w.StartsAt) {
		return 0, false
	}
	projected, ok := addUsage(w.Used, w.Reserved, estimate)
	if !ok || projected > w.Allowance || w.usageCeiling != nil && projected > *w.usageCeiling {
		return 0, false
	}
	if w.capacityReadAt != nil {
		if !w.capacityAllowed || w.capacityRetired {
			return 0, false
		}
		if (w.capacityKind == "refresh" || now.Sub(*w.capacityReadAt) > 10*time.Minute && w.capacitySource != "estimate") && (w.capacityRefreshRun != nil || w.Reserved > 0 || estimate > 1) {
			return 0, false
		}
		if w.capacityBudget != nil && float64(w.Reserved+estimate) > *w.capacityBudget {
			return 0, false
		}
	}
	fraction := windowPaceFraction(w, now)
	if projected > allowedUnits(w.Allowance, fraction) {
		return 0, false
	}
	return projected, true
}

// Measured windows use Plan’s schedule-derived budget. A readiness observation
// gives no vendor start time, so linear pacing from its reading time would
// count the same policy twice and reject every freshly measured account.
func windowPaceFraction(w Window, now time.Time) float64 {
	if w.capacityBudget != nil {
		return 1
	}
	return paceFraction(w.PaceModel, elapsedFraction(now, w.StartsAt, w.EndsAt), w.BurstRatio)
}
