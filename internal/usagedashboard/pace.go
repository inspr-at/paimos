// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"math"
	"strconv"
	"time"
)

// paceCap is the cumulative allowance the registered pace model permits at now.
// The formula matches internal/agentaccounts: steady is f, frontload is
// 1-(1-f)^2, unrestricted is 1, then min(1, pace+burst). An unknown model or
// burst returns ok=false so the dashboard does not invent a cap.
func paceCap(model string, start, end, now time.Time, allowance int64, burst string) (int64, bool) {
	if allowance <= 0 {
		return 0, false
	}
	ratio, err := strconv.ParseFloat(burst, 64)
	if err != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
		return 0, false
	}
	fraction := paceFraction(model, elapsedFraction(now, start, end), ratio)
	if model != "steady" && model != "frontload" && model != "unrestricted" {
		return 0, false
	}
	return allowedUnits(allowance, fraction), true
}

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

// applyMeasuredAvailability publishes declared allowance, explicit
// reservations, and schedule capacity. Measured usage and the availability
// derived from it stay null while the window is provisional, including mixed
// settled evidence. A failed subtraction stays null; it does not become zero.
func applyMeasuredAvailability(w *AllowanceWindow, used int64, now time.Time) {
	if cap, ok := paceCap(w.PaceModel, w.StartsAt, w.EndsAt, now, w.Allowance, w.BurstRatio); ok {
		w.PaceCap = &cap
	}
	if w.Provisional {
		return
	}
	measured := used
	w.Used = &measured
	if remaining, ok := subInt(w.Allowance, used); ok {
		if remaining, ok = subInt(remaining, w.Reserved); ok {
			w.HardRemaining = &remaining
		}
	}
	if w.PaceCap == nil {
		return
	}
	if head, ok := subInt(*w.PaceCap, used); ok {
		if head, ok = subInt(head, w.Reserved); ok {
			w.Headroom = &head
		}
	}
}

func subInt(a, b int64) (int64, bool) {
	if b > 0 && a < math.MinInt64+b {
		return 0, false
	}
	if b < 0 && a > math.MaxInt64+b {
		return 0, false
	}
	return a - b, true
}
