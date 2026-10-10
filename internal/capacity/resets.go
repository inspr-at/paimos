// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"errors"
	"math"
	"slices"
	"time"
)

const MaxResetCredits = 32

// ResetCredits is an explicit vendor observation, never an inferred allowance.
// Each spendable credit has an expiry; equal expiries represent separate credits.
type ResetCredits struct {
	Count     int         `json:"count"`
	ExpiresAt []time.Time `json:"expires_at"`
	Source    string      `json:"source"`
}
type ResetReport struct {
	ResetCredits
	ReadAt          time.Time `json:"read_at"`
	BindingRevision int64     `json:"binding_revision"`
	UndoSupported   bool      `json:"undo_supported"`
}

func (r ResetReport) Validate(now time.Time) error {
	if r.Source != "vendor" || r.Count < 0 || r.Count > MaxResetCredits || len(r.ExpiresAt) != r.Count || r.ReadAt.IsZero() || r.ReadAt.After(now) || r.BindingRevision < 0 {
		return errors.New("invalid vendor reset report")
	}
	for _, at := range r.ExpiresAt {
		if !at.After(r.ReadAt) || at.Sub(r.ReadAt) > 367*24*time.Hour {
			return errors.New("invalid reset expiry")
		}
	}
	return nil
}
func (r ResetReport) Credits(now time.Time) ResetCredits {
	out := ResetCredits{Source: "vendor", ExpiresAt: []time.Time{}}
	for _, at := range r.ExpiresAt {
		if at.After(now) {
			out.ExpiresAt = append(out.ExpiresAt, at)
		}
	}
	slices.SortFunc(out.ExpiresAt, func(a, b time.Time) int { return a.Compare(b) })
	out.Count = len(out.ExpiresAt)
	return out
}

type ResetPlan struct {
	PlannedAt        time.Time `json:"planned_at"`
	RaisedPacePoints float64   `json:"raised_pace_points"`
	RaisedPaceUntil  time.Time `json:"raised_pace_until"`
}

// PlanReset uses the current window's learned burn. Wait until 90% is used,
// but leave five minutes for the next observation and the bounded vendor call.
// Unknown/stale observations and natural resets before expiry give no plan.
func PlanReset(now time.Time, report ResetReport, reading Reading, burnPerHour float64) *ResetPlan {
	credits := report.Credits(now)
	if report.Validate(now) != nil || credits.Count == 0 || now.Sub(report.ReadAt) > 2*time.Minute || reading.Validate(now) != nil || reading.ReadAt.After(now) || now.Sub(reading.ReadAt) > 2*time.Minute || reading.Source == "estimate" || !reading.ResetsAt.After(now) || math.IsNaN(burnPerHour) || math.IsInf(burnPerHour, 0) || burnPerHour < 0 {
		return nil
	}
	expiry := credits.ExpiresAt[0]
	if !expiry.Before(reading.ResetsAt) {
		return nil
	}
	latest := expiry.Add(-5 * time.Minute)
	planned := latest
	if reading.UsedPercent >= 100 {
		planned = now
	} else if burnPerHour > 0 {
		available := math.Max(0, latest.Sub(now).Hours())
		if reading.UsedPercent+burnPerHour*available < 90 {
			return nil
		}
		hours := (100 - reading.UsedPercent) / burnPerHour
		if hours < available {
			planned = now.Add(time.Duration(hours * float64(time.Hour)))
		}
	} else if reading.UsedPercent < 90 {
		return nil
	}
	if planned.Before(now) {
		planned = now
	}
	// Only the reclaimed, observed use is spread over the remaining window.
	days := math.Max(reading.ResetsAt.Sub(planned).Hours()/24, 1)
	return &ResetPlan{PlannedAt: planned, RaisedPacePoints: math.Min(50, reading.UsedPercent/days), RaisedPaceUntil: reading.ResetsAt}
}
