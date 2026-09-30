// SPDX-License-Identifier: AGPL-3.0-only
package reviewgate

import (
	"strings"
	"testing"
)

func TestParseFinalVerdict(t *testing.T) {
	cases := []struct {
		name, text, verdict string
		findings            int
	}{
		{"ok", "No blocking findings.\nVERDICT: ok\n", "ok", 0},
		{"changes", "FINDING: high internal/api.go:42 Missing tenant constraint.\nVERDICT: changes", "changes", 1},
		{"quoted old verdict", "Old report: VERDICT: changes\nVERDICT: ok", "ok", 0},
		{"embedded verdict only", "VERDICT: ok\nThe run is still waiting.", "", 0},
		{"no verdict", "Tests are running.", "", 0},
		{"changes without locations", "VERDICT: changes", "", 0},
		{"absolute path", "FINDING: high /tmp/a.go:1 Broken.\nVERDICT: changes", "", 0},
		{"traversal", "FINDING: high ../a.go:1 Broken.\nVERDICT: changes", "", 0},
		{"bad line", "FINDING: high a.go:0 Broken.\nVERDICT: changes", "", 0},
		{"contradiction", "FINDING: high a.go:1 Broken.\nVERDICT: ok", "", 1},
		{"bound", strings.Repeat("a", MaxOutput) + "\nVERDICT: ok", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Parse(tc.text)
			if r.Verdict != tc.verdict || len(r.Findings) != tc.findings {
				t.Fatalf("unexpected parsed result: %+v", r)
			}
			if tc.verdict == "" && r.Reason == "" {
				t.Fatal("closed result lacks reason")
			}
		})
	}
}
func TestGateRequiresIndependentFinishedVerifiedRun(t *testing.T) {
	family, profile, model := "anthropic", "profile", "review-model"
	b := Binding{AuthorFamily: "openai", ReviewerFamily: &family, ProfileID: &profile}
	result := Parse("VERDICT: ok")
	if ok, _ := Gate("completed", "vendor_reported", &model, b, result); !ok {
		t.Fatal("valid gate closed")
	}
	for _, status := range []string{"queued", "starting", "running", "waiting", "failed", "cancelled", "ownership_lost"} {
		if ok, _ := Gate(status, "vendor_reported", &model, b, result); ok {
			t.Fatal("unfinished or failed gate opened")
		}
	}
	if ok, _ := Gate("completed", "unverified", &model, b, result); ok {
		t.Fatal("unverified gate opened")
	}
	family = "openai"
	if ok, _ := Gate("completed", "vendor_reported", &model, b, result); ok {
		t.Fatal("author reviewed itself")
	}
}

func TestModelMatchesRequiresRequestedPin(t *testing.T) {
	for _, tc := range []struct {
		requested, effective string
		want                 bool
	}{{"review-model", "review-model", true}, {"fable", "claude-fable-5-1", true}, {"opus", "claude-opus-5", true}, {"fable", "claude-haiku-5", false}, {"review-model", "weaker-model", false}, {"", "", false}} {
		if ModelMatches(tc.requested, tc.effective) != tc.want {
			t.Fatal("review model pin mismatch")
		}
	}
}
