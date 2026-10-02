// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"math"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{AllowedKinds: []string{}, AllowedTags: []string{}, AllowedAreas: []string{}, MaxTicketEstimateHours: 8, ParallelLimit: 2, Budget: Budget{8}, Window: Window{Timezone: "Europe/Vienna", Days: []int{1, 2, 3, 4, 5, 6, 7}, Start: "22:00", End: "06:00"}}
}
func TestPolicyBounds(t *testing.T) {
	if err := validatePolicy(testPolicy()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Policy){
		"zero_budget":       func(p *Policy) { p.Budget.AgentHours = 0 },
		"nan_budget":        func(p *Policy) { p.Budget.AgentHours = math.NaN() },
		"infinite_estimate": func(p *Policy) { p.MaxTicketEstimateHours = math.Inf(1) },
		"parallel":          func(p *Policy) { p.ParallelLimit = 21 },
		"host_timezone":     func(p *Policy) { p.Window.Timezone = "Local" },
		"unknown_timezone":  func(p *Policy) { p.Window.Timezone = "no/such_zone" },
		"day":               func(p *Policy) { p.Window.Days = []int{0} },
		"duplicate_day":     func(p *Policy) { p.Window.Days = []int{1, 1} },
		"endpoint":          func(p *Policy) { p.Window.Start = "24:00" },
		"zero_window":       func(p *Policy) { p.Window.End = p.Window.Start },
		"duplicate_filter":  func(p *Policy) { p.AllowedTags = []string{"fix", "fix"} },
		"oversized_filter":  func(p *Policy) { p.AllowedKinds = make([]string, 33) },
		"missing_array":     func(p *Policy) { p.AllowedAreas = nil },
		"nul":               func(p *Policy) { p.AllowedTags = []string{"fix\x00"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := testPolicy()
			change(&p)
			if validatePolicy(p) == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	for _, s := range []Scope{{Kind: "backlog"}, {Kind: "queued_tickets", ReleaseNodeID: ptr("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")}, {Kind: "release"}, {Kind: "release", ReleaseNodeID: ptr("invalid")}} {
		if validateScope(s) == nil {
			t.Fatalf("accepted scope %+v", s)
		}
	}
	if validateScope(Scope{Kind: "release", ReleaseNodeID: ptr("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")}) != nil {
		t.Fatal("extensible release scope rejected")
	}
}
func ptr[T any](v T) *T { return &v }
func TestWindowInstances(t *testing.T) {
	for _, tc := range []struct {
		name            string
		w               Window
		now, start, end string
	}{
		{"overnight", Window{"UTC", []int{4}, "22:00", "02:00"}, "2026-10-02T00:30:00Z", "2026-10-01T22:00:00Z", "2026-10-02T02:00:00Z"},
		{"end_exclusive", Window{"UTC", []int{4}, "22:00", "02:00"}, "2026-10-02T02:00:00Z", "2026-10-08T22:00:00Z", "2026-10-09T02:00:00Z"},
		{"spring_forward", Window{"Europe/Vienna", []int{7}, "01:00", "04:00"}, "2026-03-28T23:30:00Z", "2026-03-29T00:00:00Z", "2026-03-29T02:00:00Z"},
		{"fall_back", Window{"Europe/Vienna", []int{7}, "01:00", "04:00"}, "2026-10-24T22:30:00Z", "2026-10-24T23:00:00Z", "2026-10-25T03:00:00Z"},
		{"nonexistent_endpoint", Window{"Europe/Vienna", []int{7}, "02:30", "04:00"}, "2026-03-28T23:30:00Z", "2026-04-05T00:30:00Z", "2026-04-05T02:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.now)
			v := nextWindow(tc.w, now)
			if v == nil || v.StartsAt.Format(time.RFC3339) != tc.start || v.EndsAt.Format(time.RFC3339) != tc.end {
				t.Fatalf("window %+v", v)
			}
		})
	}
}
func TestPreviewPolicyAndPreparationDecisions(t *testing.T) {
	l := Lane{Policy: testPolicy()}
	l.Policy.AllowedKinds = []string{"ticket"}
	l.Policy.AllowedTags = []string{"fix"}
	l.Policy.AllowedAreas = []string{"backend"}
	fields := map[string]any{"area": "backend", "tags": []any{"fix"}, "acceptance_criteria": "tests pass", "estimate_hours": float64(2)}
	var item PreviewItem
	previewEligibility(l, &item, "ticket", "open", "", fields, true, false, false)
	if !item.Eligible || item.Preparation != "none" {
		t.Fatalf("ready %+v", item)
	}
	delete(fields, "estimate_hours")
	previewEligibility(l, &item, "ticket", "open", "", fields, true, false, false)
	if item.Preparation != "automatic_estimate" || item.Eligible {
		t.Fatalf("automatic estimate %+v", item)
	}
	delete(fields, "acceptance_criteria")
	previewEligibility(l, &item, "ticket", "open", "", fields, true, false, false)
	if item.Preparation != "criteria_draft_requires_person" {
		t.Fatalf("criteria %+v", item)
	}
	fields["acceptance_criteria"] = "keep this"
	fields["estimate_hours"] = float64(9)
	previewEligibility(l, &item, "ticket", "open", "", fields, false, true, true)
	for _, why := range []string{"not_person_queued", "estimate_above_lane_limit", "unresolved_blocker", "existing_work"} {
		if !includes(item.Exclusions, why) {
			t.Fatalf("missing %s: %+v", why, item)
		}
	}
}
