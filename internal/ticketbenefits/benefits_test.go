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
	}{{"work", "open", "done", 4}, {"work", "done", "done", 0}, {"work", "", "done", 4}, {"ticket", "open", "done", 4}, {"ticket", "done", "done", 0}, {"ticket", "done", "open", 0}, {"task", "open", "done", 0}, {"ticket", "", "done", 4}} {
		if got := Transition(tc.k, tc.b, tc.a, json.RawMessage(`{}`)); len(got) != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
}

func TestCompletionTransitions(t *testing.T) {
	completed := []string{"done", "accepted", "delivered"}
	other := []string{"", "open", "new", "backlog", "in_progress", "qa", "cancelled", "canceled", "archived", "custom-complete"}
	for _, after := range completed {
		if !Completed(after) {
			t.Fatalf("%s must count as completed", after)
		}
		for _, before := range other {
			if got := Transition("ticket", before, after, json.RawMessage(`{}`)); len(got) != 4 {
				t.Fatalf("%s -> %s bypassed requirements: %v", before, after, got)
			}
		}
		for _, before := range completed {
			if got := Transition("ticket", before, after, json.RawMessage(`{}`)); len(got) != 0 {
				t.Fatalf("%s -> %s blocked historical edit: %v", before, after, got)
			}
		}
		if got := Transition("task", "open", after, json.RawMessage(`{}`)); len(got) != 0 {
			t.Fatalf("non-ticket gate: %v", got)
		}
	}
	for _, after := range other {
		if Completed(after) || len(Transition("ticket", "open", after, json.RawMessage(`{}`))) != 0 {
			t.Fatalf("%s must not imply successful completion", after)
		}
	}
}

func TestUnifiedWorkLeavesKeepCompletionRequirements(t *testing.T) {
	for _, state := range []string{"done", "accepted", "delivered"} {
		if len(Transition("work", "open", state, json.RawMessage(`{}`))) != 4 {
			t.Fatal("work leaf gate missing", state)
		}
		if len(Transition("work", "done", state, json.RawMessage(`{}`))) != 0 {
			t.Fatal("historical completed work blocked", state)
		}
	}
}
