// SPDX-License-Identifier: AGPL-3.0-only
package ticketbenefits

import (
	"encoding/json"
	"testing"
)

func TestCompletionRequirements(t *testing.T) {
	valid := map[string]any{"pill_en": "Clear release notes", "pill_de": "Verständliche Release Notes", "benefit_en": "Every ticket says what you gain.", "benefit_de": "Jedes Ticket erklärt seinen Nutzen."}
	check := func(fields map[string]any) []string { raw, _ := json.Marshal(fields); return Issues(raw) }
	if got := check(valid); len(got) != 0 {
		t.Fatal(got)
	}
	for _, key := range []string{"pill_en", "pill_de", "benefit_en", "benefit_de"} {
		old := valid[key]
		for _, bad := range []any{nil, 7, "", " \t\n\u00a0"} {
			valid[key] = bad
			if len(check(valid)) != 1 {
				t.Fatalf("%s %v: %v", key, bad, check(valid))
			}
		}
		valid[key] = old
	}
	for _, pill := range []string{"Word", "One two three four five"} {
		valid["pill_en"] = pill
		if len(check(valid)) != 1 {
			t.Fatal(check(valid))
		}
	}
	valid["pill_en"] = "One\u00a0two"
	if len(check(valid)) != 0 {
		t.Fatal(check(valid))
	}
	valid["hide_from_release_notes"] = true
	delete(valid, "benefit_de")
	if len(check(valid)) != 1 {
		t.Fatal("hidden bypass")
	}
	for _, tc := range []struct {
		k, b, a string
		want    int
	}{{"ticket", "open", "done", 4}, {"ticket", "done", "done", 0}, {"ticket", "done", "open", 0}, {"task", "open", "done", 0}, {"ticket", "", "done", 4}} {
		if got := Transition(tc.k, tc.b, tc.a, json.RawMessage(`{}`)); len(got) != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
}
