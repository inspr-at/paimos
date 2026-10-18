// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"math"
	"slices"
	"testing"
)

func TestQueueReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, kind, state string
		fields            map[string]any
		blocker           bool
		missing           []string
	}{
		{"work leaf", "work", "open", map[string]any{"estimate_hours": 2.0, "acceptance_criteria": "pass"}, false, []string{}},
		{"work parent", "parent", "open", map[string]any{"estimate_hours": 2.0, "acceptance_criteria": "pass"}, false, []string{"status"}},
		{"ready", "ticket", "Open", map[string]any{"estimate_hours": 2.0, "acceptance_criteria": "- [ ] tests pass"}, false, []string{}},
		{"new", "task", "new", map[string]any{"estimate_hours": 0.5, "acceptance_criteria": []any{"test"}}, false, []string{}},
		{"empty", "ticket", "backlog", map[string]any{}, false, []string{"estimate", "criteria"}},
		{"unknown blocker", "ticket", "blocked", map[string]any{"estimate_hours": 2.0, "acceptance_criteria": "pass"}, false, []string{"blocker"}},
		{"named blocker", "ticket", "blocked", map[string]any{"estimate_hours": 2.0, "acceptance_criteria": "pass"}, true, []string{}},
		{"active", "ticket", "in-progress", map[string]any{"estimate_hours": 2.0, "acceptance_criteria": "pass"}, false, []string{"status"}},
		{"epic", "epic", "open", map[string]any{"estimate_hours": 2.0, "acceptance_criteria": "pass"}, false, []string{"status"}},
		{"malformed", "ticket", "open", map[string]any{"estimate_hours": "2", "acceptance_criteria": "- [ ]"}, false, []string{"estimate", "criteria"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Check(tc.kind, tc.state, "Work", "", tc.fields, tc.blocker)
			if !slices.Equal(r.Missing, tc.missing) || r.Ready != (len(tc.missing) == 0) {
				t.Fatalf("readiness %+v; missing %v", r, tc.missing)
			}
		})
	}
	for _, h := range []float64{0, -1, 201, math.NaN(), math.Inf(1)} {
		if Hours(map[string]any{"estimate_hours": h}) != 0 {
			t.Fatal("accepted invalid estimate")
		}
	}
	for _, text := range []string{"Fix permissions", "Tenant RLS", "Security review", "Authentication flow"} {
		if !Security(text, "", nil) {
			t.Fatalf("missed security route %s", text)
		}
	}
	if Security("Author a page", "", nil) {
		t.Fatal("unrelated title marked security")
	}
}
