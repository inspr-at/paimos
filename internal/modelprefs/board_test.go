// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

import (
	"reflect"
	"testing"
)

func rankedFixture() BoardState {
	person := "11111111-1111-4111-8111-111111111111"
	workspace := EmptyBoardProfile("workspace", nil)
	workspace.ID = "workspace"
	own := EmptyBoardProfile("person", &person)
	own.ID = "person"
	return BoardState{Workspace: workspace, Person: &own, SmallHours: 2, FixRounds: 3, Kinds: []Kind{{Slug: "backend", Labels: []string{"api"}}, {Slug: "other"}}, Lines: []BoardLine{{"openai:sol", "openai", true, true, nil}, {"openai:astra", "openai", true, true, nil}, {"anthropic:opus", "anthropic", true, true, nil}, {"anthropic:sonnet", "anthropic", true, false, nil}, {"anthropic:fable", "anthropic", true, true, nil}, {"xai:grok", "xai", false, true, nil}, {"openai:terra", "openai", true, true, nil}}}
}

// Risk: preference inheritance or pins could silently bypass a hard restriction
// or let a catalog discovery start work without anyone placing it.
func TestRankedBoardSixStepResolution(t *testing.T) {
	s := rankedFixture()
	s.Orders = []BoardOrder{{ProfileID: "person", Column: "backend", Situation: "first", Rank: []string{"openai:sol", "anthropic:sonnet", "anthropic:opus"}, Not: []string{"anthropic:sonnet"}, Thinking: str("max")}}
	s.Rules = []Rule{{Scope: "workspace", Column: "backend", Line: "anthropic:opus", Lock: "top"}, {Scope: "project", Column: "backend", Line: "openai:astra", Lock: "top"}, {Scope: "workspace", Column: "backend", Line: "anthropic:fable", Lock: "bottom"}, {Scope: "workspace", Column: "backend", Line: "openai:sol", Lock: "not"}}
	got := ResolveBoard(s, BoardQuery{Area: "api", EstimateHours: 1}, func(line string, level int) (bool, string) {
		if level != 4 {
			t.Errorf("small work effort level = %d; want 4", level)
		}
		if line != "anthropic:fable" {
			return false, "vendor stop"
		}
		return true, ""
	})
	want := []string{"openai:astra", "anthropic:opus", "anthropic:fable"}
	if !reflect.DeepEqual(got.Rank, want) || got.PreferenceOf.Person != s.Person.PersonID || got.Column != "backend" || got.Situation != "first" || got.Selected != "anthropic:fable" || got.CardIndex != 3 || got.Lock == nil || got.Lock.Value != "bottom" || len(got.Held) != 2 {
		t.Fatalf("six steps lost a decision: %+v", got)
	}
	if got.Selected == "openai:terra" {
		t.Fatal("a new line ran without placement")
	}
}
func TestRankedBoardSparseSituationsAndReviewRules(t *testing.T) {
	s := rankedFixture()
	s.Orders = []BoardOrder{{ProfileID: "person", Column: "other", Situation: "first", Rank: []string{"openai:sol", "anthropic:opus"}, Not: []string{}, Thinking: str("deep")}}
	fix := ResolveBoard(s, BoardQuery{Area: "backend", FixRound: 1}, nil)
	if fix.Column != "backend" || fix.Situation != "fix" || fix.Source != "follows" || fix.Thinking != "lean" {
		t.Fatalf("sparse fix inheritance: %+v", fix)
	}
	stuck := ResolveBoard(s, BoardQuery{Area: "backend", FixRound: 4, PreviousFamily: "openai"}, nil)
	if stuck.Rank[0] != "anthropic:opus" || stuck.Situation != "stuck" || stuck.Thinking != "deep" {
		t.Fatalf("stuck did not switch family and deepen: %+v", stuck)
	}
	review := ResolveBoard(s, BoardQuery{Review: true, AuthorFamily: "anthropic"}, func(line string, level int) (bool, string) {
		if level != 4 {
			t.Fatalf("review ran at %d", level)
		}
		return true, ""
	})
	if review.Column != "review:anthropic" || review.Selected != "openai:sol" || review.Locks["anthropic:opus"] == nil || review.Locks["anthropic:opus"].Kind != "cross_family" {
		t.Fatalf("cross-family review: %+v", review)
	}
	for _, line := range review.Rank {
		if line == "anthropic:opus" || line == "anthropic:sonnet" || line == "anthropic:fable" {
			t.Fatal("same-family reviewer escaped hard rule")
		}
	}
}
func TestRankedBoardCapabilityIsNotALockAndNearestEffortTiesUp(t *testing.T) {
	s := rankedFixture()
	s.Rules = []Rule{{Scope: "workspace", Column: "design", Line: "xai:grok", Lock: "bottom", Why: "Keep until tools exist"}}
	got := ResolveBoard(s, BoardQuery{Column: "design"}, nil)
	if len(got.Cant) != 1 || got.Cant[0].Line != "xai:grok" || got.Locks["xai:grok"].Kind != "rule" {
		t.Fatalf("capability overwrote the retained rule: %+v", got)
	}
	for _, line := range got.Rank {
		if line == "xai:grok" {
			t.Fatal("tool-free line can design")
		}
	}
	if NearestEffort(3, []int{2, 4}) != 4 || NearestEffort(5, []int{2, 3, 4}) != 4 {
		t.Fatal("nearest registered effort must tie up")
	}
	s.Rules = nil
	s.Orders = []BoardOrder{{ProfileID: "person", Column: "design", Situation: "first", Rank: []string{"xai:grok"}, Not: []string{}}}
	got = ResolveBoard(s, BoardQuery{Column: "design"}, nil)
	if len(got.Cant) != 1 || got.Locks["xai:grok"] != nil {
		t.Fatal("a capability alone acquired a lock")
	}
}
func TestRankedBoardThinkingOnlyRowPreservesInheritedRank(t *testing.T) {
	s := rankedFixture()
	s.Orders = []BoardOrder{{ProfileID: "person", Column: "backend", Situation: "first", Thinking: str("max"), Not: []string{}}}
	got := ResolveBoard(s, BoardQuery{Column: "backend"}, nil)
	if got.Source != "template" || got.Thinking != "max" || !reflect.DeepEqual(got.Rank, TemplateRank("balanced", "backend")) {
		t.Fatalf("thinking-only row erased the template: %+v", got)
	}
}

// Risk: a sparse situation can lose First build's order or reduce explicit
// column thinking, and reviews/concepts can accidentally acquire fix settings.
func TestRankedBoardExpertSituationThinking(t *testing.T) {
	s := rankedFixture()
	s.Orders = []BoardOrder{{ProfileID: "person", Column: "backend", Situation: "first", Rank: []string{"openai:sol", "anthropic:opus"}, Not: []string{}, Thinking: str("deep")}}
	fix := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "fix"}, nil)
	if !fix.FollowsFirst || !reflect.DeepEqual(fix.Rank, s.Orders[0].Rank) || fix.Thinking != "standard" || fix.ThinkingColumn {
		t.Fatalf("First build fallback: %+v", fix)
	}
	s.Orders = append(s.Orders, BoardOrder{ProfileID: "person", Column: "backend", Situation: "fix", Thinking: str("max")})
	fix = ResolveBoard(s, BoardQuery{Column: "backend", Situation: "fix"}, nil)
	if !fix.FollowsFirst || fix.Thinking != "max" || !fix.ThinkingColumn || !reflect.DeepEqual(fix.Rank, s.Orders[0].Rank) {
		t.Fatalf("thinking-only override: %+v", fix)
	}
	s.Orders[1].Thinking = nil
	s.Orders[1].Rank = []string{"anthropic:opus", "openai:sol"}
	fix = ResolveBoard(s, BoardQuery{Column: "backend", Situation: "fix"}, nil)
	if fix.FollowsFirst || fix.Thinking != "standard" || fix.ThinkingColumn || fix.Rank[0] != "anthropic:opus" {
		t.Fatalf("rank must not override automatic thinking: %+v", fix)
	}
	s.Orders = append(s.Orders, BoardOrder{ProfileID: "workspace", Column: "backend", Situation: "fix", Thinking: str("max")})
	fix = ResolveBoard(s, BoardQuery{Column: "backend", Situation: "fix"}, nil)
	if fix.Thinking != "max" || !fix.ThinkingColumn || fix.ThinkingSource != "default" {
		t.Fatalf("workspace situation lost to inherited First build: %+v", fix)
	}
	stuck := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "stuck", PreviousFamily: "openai"}, nil)
	if !stuck.FollowsFirst || stuck.Thinking != "deep" || stuck.Rank[0] != "anthropic:opus" {
		t.Fatalf("stuck fallback: %+v", stuck)
	}
	for _, column := range []string{"review:openai", "concept"} {
		d := ResolveBoard(s, BoardQuery{Column: column, Situation: "fix"}, nil)
		if d.Situation != "first" || d.FollowsFirst || column == "review:openai" && d.EffortLevel != 4 {
			t.Fatalf("fixed column situation: %+v", d)
		}
	}
	s.Person = nil
	s.Orders = append(s.Orders, BoardOrder{ProfileID: "workspace", Column: "backend", Situation: "first", Rank: []string{"anthropic:opus", "openai:sol"}})
	first := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "first"}, nil)
	if first.Source != "own" || first.Rank[0] != "anthropic:opus" {
		t.Fatalf("workspace template concealed its column order: %+v", first)
	}
}
