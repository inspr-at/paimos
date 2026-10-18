// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"math"
	"testing"
	"time"
)

// Risk: inferred credits, expired data or a natural reset spend a credit;
// learning timing overflows or increases the pace indefinitely.
func TestResetPlanVendorFreshnessTimingAndRaisedPace(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	report := ResetReport{ResetCredits: ResetCredits{Count: 2, ExpiresAt: []time.Time{now.Add(4 * time.Hour), now.Add(24 * time.Hour)}, Source: "vendor"}, ReadAt: now}
	reading := Reading{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 80, ReadAt: now, ResetsAt: now.Add(4 * 24 * time.Hour), Source: "harness"}
	plan := PlanReset(now, report, reading, 10)
	if plan == nil || !plan.PlannedAt.Equal(now.Add(2*time.Hour)) || !plan.RaisedPaceUntil.Equal(reading.ResetsAt) || plan.RaisedPacePoints <= 20 || plan.RaisedPacePoints > 21 {
		t.Fatalf("wrong learning plan %+v", plan)
	}
	reading.UsedPercent = 95
	plan = PlanReset(now, report, reading, 0)
	if plan == nil || !plan.PlannedAt.Equal(report.ExpiresAt[0].Add(-5*time.Minute)) {
		t.Fatalf("reset must wait until late in expiry %+v", plan)
	}
	reading.UsedPercent = 100
	if plan = PlanReset(now, report, reading, 0); plan == nil || !plan.PlannedAt.Equal(now) {
		t.Fatal("exhausted window should reset now")
	}
	reading.UsedPercent = 80
	for _, rate := range []float64{0, 1e-300, -1, math.Inf(1), math.NaN()} {
		if PlanReset(now, report, reading, rate) != nil {
			t.Fatalf("unknown/insufficient burn got plan: %v", rate)
		}
	}
	reading.ResetsAt = now.Add(time.Hour)
	if PlanReset(now, report, reading, 10) != nil {
		t.Fatal("natural reset before expiry must not spend")
	}
	reading.ResetsAt = now.Add(4 * 24 * time.Hour)
	report.ReadAt = now.Add(-2*time.Minute - time.Nanosecond)
	if PlanReset(now, report, reading, 10) != nil {
		t.Fatal("stale reset data got plan")
	}
	report.ReadAt = now
	reading.ReadAt = now.Add(-2*time.Minute - time.Nanosecond)
	if PlanReset(now, report, reading, 10) != nil {
		t.Fatal("stale window got plan")
	}
	reading.ReadAt = now
	reading.Source = "estimate"
	if PlanReset(now, report, reading, 10) != nil {
		t.Fatal("estimated window got plan")
	}
	report.Source = "inferred"
	if report.Validate(now) == nil {
		t.Fatal("inferred credits accepted")
	}
	report.Source = "vendor"
	report.Count = 33
	if report.Validate(now) == nil {
		t.Fatal("unbounded credits accepted")
	}
	report.Count = 2
	report.ExpiresAt[0] = now
	if report.Validate(now) == nil {
		t.Fatal("already expired observation accepted")
	}
	// Time advancement drops exactly the expired credit, without inventing any.
	report.ExpiresAt[0] = now.Add(time.Minute)
	credits := report.Credits(now.Add(2 * time.Minute))
	if credits.Count != 1 || len(credits.ExpiresAt) != 1 || !credits.ExpiresAt[0].Equal(report.ExpiresAt[1]) {
		t.Fatalf("wrong expiring credits %+v", credits)
	}
}
