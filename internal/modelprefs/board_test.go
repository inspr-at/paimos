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
	t.Run("explicit stuck column thinking stays below deep", func(t *testing.T) {
		s := rankedFixture()
		s.Orders = []BoardOrder{
			{ProfileID: "person", Column: "backend", Situation: "first", Rank: []string{"openai:sol", "anthropic:opus"}, Not: []string{}, Thinking: str("standard")},
			{ProfileID: "person", Column: "backend", Situation: "stuck", Thinking: str("standard")},
		}
		stuck := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "stuck", PreviousFamily: "openai"}, nil)
		if stuck.Thinking != "standard" || !stuck.ThinkingColumn || stuck.EffortLevel != ThinkingLevel("standard") {
			t.Fatalf("explicit stuck thinking was raised: %+v", stuck)
		}
		s.Orders = s.Orders[:1]
		missing := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "stuck", PreviousFamily: "openai"}, nil)
		if missing.Thinking != "deep" || missing.ThinkingColumn || missing.EffortLevel != 4 {
			t.Fatalf("missing stuck row lost the deep floor: %+v", missing)
		}
	})
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
	t.Run("missing column situation follows Other First build", func(t *testing.T) {
		s := rankedFixture()
		s.Orders = []BoardOrder{
			{ProfileID: "person", Column: "other", Situation: "first", Rank: []string{"openai:sol", "anthropic:opus"}, Not: []string{"anthropic:sonnet"}},
			{ProfileID: "person", Column: "other", Situation: "fix", Rank: []string{"anthropic:opus", "openai:sol"}, Not: []string{"openai:sol"}},
		}
		first := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "first"}, nil)
		fix := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "fix"}, nil)
		if !fix.FollowsFirst || fix.Source != "follows" || !reflect.DeepEqual(fix.Rank, first.Rank) || !reflect.DeepEqual(fix.Not, first.Not) {
			t.Fatalf("Other fix leaked into missing backend situation: first=%+v fix=%+v", first, fix)
		}
	})
	t.Run("person without situation follows workspace First build", func(t *testing.T) {
		s := rankedFixture()
		s.Orders = []BoardOrder{
			{ProfileID: "workspace", Column: "backend", Situation: "first", Rank: []string{"openai:sol", "anthropic:opus"}},
			{ProfileID: "workspace", Column: "backend", Situation: "fix", Rank: []string{"anthropic:opus", "openai:sol"}},
		}
		first := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "first"}, nil)
		fix := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "fix"}, nil)
		if !fix.FollowsFirst || !reflect.DeepEqual(fix.Rank, first.Rank) {
			t.Fatalf("workspace fix replaced person's First build: first=%+v fix=%+v", first, fix)
		}
		s.Person = nil
		workspaceFix := ResolveBoard(s, BoardQuery{Column: "backend", Situation: "fix"}, nil)
		if workspaceFix.FollowsFirst || workspaceFix.Rank[0] != "anthropic:opus" {
			t.Fatalf("workspace's explicit situation was ignored: %+v", workspaceFix)
		}
	})
}

// Risk: an omitted kind keeps its own template after the page shows only Default,
// and a first-pick rewrite drops a line the resolved list hid.
func TestRankedBoardOmittedKindsFollowDefault(t *testing.T) {
	s := rankedFixture()
	concept := ResolveBoard(s, BoardQuery{Column: "concept"}, nil)
	other := ResolveBoard(s, BoardQuery{Column: "other"}, nil)
	design := ResolveBoard(s, BoardQuery{Column: "design"}, nil)
	if concept.Source != "template" || !reflect.DeepEqual(concept.Rank, other.Rank) || reflect.DeepEqual(concept.Rank, TemplateRank("balanced", "concept")) || concept.Rank[0] != "openai:sol" {
		t.Fatalf("empty concept kept its own template: %+v", concept.Rank)
	}
	if !reflect.DeepEqual(design.Rank, other.Rank) {
		t.Fatalf("empty design kept its own template: %+v", design.Rank)
	}
	s.Orders = []BoardOrder{{ProfileID: "person", Column: "other", Situation: "first", Rank: []string{"anthropic:sonnet", "openai:sol"}, Not: []string{"xai:grok"}}}
	concept = ResolveBoard(s, BoardQuery{Column: "concept"}, nil)
	if concept.Source != "follows" || !reflect.DeepEqual(concept.Rank, []string{"anthropic:sonnet", "openai:sol"}) || !reflect.DeepEqual(concept.Not, []string{"xai:grok"}) {
		t.Fatalf("concept did not inherit Default: %+v", concept)
	}
	s.Orders = append(s.Orders, BoardOrder{ProfileID: "person", Column: "concept", Situation: "first", Rank: []string{"anthropic:opus", "openai:sol"}, Not: []string{}})
	concept = ResolveBoard(s, BoardQuery{Column: "concept"}, nil)
	if concept.Source != "own" || concept.Rank[0] != "anthropic:opus" {
		t.Fatalf("explicit concept lost: %+v", concept)
	}
	s.Orders = s.Orders[:1]
	concept = ResolveBoard(s, BoardQuery{Column: "concept"}, nil)
	if concept.Source != "follows" || concept.Rank[0] != "anthropic:sonnet" {
		t.Fatalf("reset concept left Default: %+v", concept)
	}
	review := ResolveBoard(s, BoardQuery{Column: "review:openai"}, nil)
	if review.Column != "review:openai" || review.Source != "template" || len(review.Rank) == 0 || review.Rank[0] != "xai:grok" {
		t.Fatalf("review inherited Default: %+v", review)
	}
	s.Orders = []BoardOrder{{ProfileID: "person", Column: "backend", Situation: "first", Rank: []string{"anthropic:opus", "xai:grok", "openai:sol"}, Not: []string{"openai:luna"}}}
	rank, exclusions := PreservedOrder(s, *s.Person, "backend", "first")
	if !reflect.DeepEqual(rank, s.Orders[0].Rank) || !reflect.DeepEqual(exclusions, s.Orders[0].Not) {
		t.Fatalf("preserved order dropped a stored line: %v %v", rank, exclusions)
	}
	resolved := ResolveBoard(s, BoardQuery{Column: "backend"}, nil)
	for _, line := range resolved.Rank {
		if line == "xai:grok" {
			t.Fatal("resolved list kept the tool-free line")
		}
	}
	held := false
	for _, line := range resolved.Cant {
		if line.Line == "xai:grok" {
			held = true
		}
	}
	if !held {
		t.Fatalf("tool-free line was not held: %+v", resolved.Cant)
	}
	s.Orders = []BoardOrder{{ProfileID: "workspace", Column: "other", Situation: "first", Rank: []string{"anthropic:sonnet", "xai:grok"}, Not: []string{"openai:luna"}}}
	rank, exclusions = PreservedOrder(s, *s.Person, "concept", "first")
	if !reflect.DeepEqual(rank, s.Orders[0].Rank) || !reflect.DeepEqual(exclusions, s.Orders[0].Not) {
		t.Fatalf("a person with no order lost the workspace tail: %v %v", rank, exclusions)
	}
	s.Orders = nil
	rank, exclusions = PreservedOrder(s, *s.Person, "concept", "first")
	if !reflect.DeepEqual(rank, TemplateRank("balanced", "other")) || len(exclusions) != 0 {
		t.Fatalf("empty concept preserved its own template: %v %v", rank, exclusions)
	}
	rank, _ = PreservedOrder(s, *s.Person, "review:openai", "first")
	if !reflect.DeepEqual(rank, TemplateRank("balanced", "review:openai")) {
		t.Fatalf("review preserved Default: %v", rank)
	}
}

// Risk: situation fallback or a person's row bypasses the workspace lock or
// never lines, or a closed order acquires an unlisted model after exhaustion.
func TestModelsV2ExceptionDefaultContinuation(t *testing.T) {
	// Risk: stored exceptions have no End field and silently stop at Wait,
	// including after a situation returns to its row.
	for _, tc := range []struct {
		name, end, situation string
		want                 []string
	}{
		{"stored exception", "", "first", []string{"anthropic:opus", "openai:astra"}},
		{"stored situation", "", "fix", []string{"anthropic:sonnet", "anthropic:opus", "openai:astra"}},
		{"explicit wait", "wait", "first", []string{"anthropic:opus"}},
		{"situation then wait", "wait", "fix", []string{"anthropic:sonnet", "anthropic:opus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := rankedFixture()
			s.Person = nil
			s.Orders = []BoardOrder{
				{ProfileID: "workspace", Column: "other", Situation: "first", Rank: []string{"openai:astra"}},
				{ProfileID: "workspace", Column: "backend", Situation: "first", Rank: []string{"anthropic:opus"}, End: tc.end},
				{ProfileID: "workspace", Column: "backend", Situation: "fix", Rank: []string{"anthropic:sonnet"}},
			}
			got := ResolveModelsBoard(s, BoardQuery{Column: "backend", Situation: tc.situation})
			if !reflect.DeepEqual(got.Rank, tc.want) {
				t.Fatalf("exception continuation: %+v; want %v", got, tc.want)
			}
			def := ResolveModelsBoard(s, BoardQuery{Column: "other", Situation: "first"})
			if !reflect.DeepEqual(def.Rank, []string{"openai:astra"}) || len(def.Steps) != 1 {
				t.Fatalf("Default acquired an implicit continuation: %+v", def)
			}
		})
	}
}

func TestModelsV2RowOwnershipAndSituations(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     BoardQuery
		rank      []string
		situation string
		level     int
	}{
		{"row", BoardQuery{Column: "backend"}, []string{"anthropic:opus", "openai:sol", "openai:astra"}, "first", 3},
		{"small", BoardQuery{Column: "backend", EstimateHours: 2}, []string{"anthropic:sonnet", "openai:astra"}, ModelsSmallSituation, 3},
		{"fix", BoardQuery{Column: "backend", FixRound: 1}, []string{"openai:astra"}, "fix", 3},
		{"stuck", BoardQuery{Column: "backend", FixRound: 4}, []string{"anthropic:fable"}, "stuck", 3},
		{"missing kind", BoardQuery{Area: "missing"}, []string{"openai:astra"}, "first", 3},
		{"review", BoardQuery{Column: "backend", Review: true, AuthorFamily: "openai"}, []string{"anthropic:fable"}, "review", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := rankedFixture()
			s.Orders = []BoardOrder{
				{ProfileID: "workspace", Column: "other", Situation: "first", Rank: []string{"openai:astra"}},
				{ProfileID: "person", Column: "backend", Situation: "first", Rank: []string{"anthropic:opus", "openai:sol"}, Not: []string{"xai:grok"}},
				{ProfileID: "person", Column: "backend", Situation: ModelsSmallSituation, Rank: []string{"anthropic:sonnet"}, End: "default"},
				{ProfileID: "person", Column: "backend", Situation: "fix", Rank: []string{"xai:grok", "openai:astra"}, End: "wait"},
				{ProfileID: "person", Column: "backend", Situation: "stuck", Rank: []string{"anthropic:fable"}, End: "wait"},
				{ProfileID: "person", Column: "backend", Situation: "review", Rank: []string{"anthropic:fable"}, End: "wait"},
			}
			got := ResolveModelsBoard(s, tc.query)
			if !reflect.DeepEqual(got.Rank, tc.rank) || got.Situation != tc.situation || got.EffortLevel != tc.level {
				t.Fatalf("order: %+v; want %v/%s/%d", got, tc.rank, tc.situation, tc.level)
			}
			if tc.query.Column == "backend" && got.PreferenceOf.Source != "person" {
				t.Fatalf("owner lost: %+v", got)
			}
			if tc.query.Column == "backend" {
				s.Orders = append(s.Orders, BoardOrder{ProfileID: "workspace", Column: "backend", Situation: "first", Rank: []string{"openai:astra"}, Effort: str("high"), EffortLevel: func() *int { v := 3; return &v }()})
				s.Rules = []Rule{{Scope: "workspace", Column: "backend", Line: "anthropic:opus", Lock: "top"}}
				locked := ResolveModelsBoard(s, tc.query)
				if locked.PreferenceOf.Source == "person" || (!tc.query.Review && !reflect.DeepEqual(locked.Rank, []string{"anthropic:opus", "openai:astra"})) {
					t.Fatalf("personal order bypassed lock: %+v", locked)
				}
			}
		})
	}
	s := rankedFixture()
	auto := ResolveModelsBoard(s, BoardQuery{Column: "backend", Review: true})
	if !auto.Automatic || len(auto.Steps) != 0 {
		t.Fatalf("review fell into build order: %+v", auto)
	}

	s.Orders = []BoardOrder{
		{ProfileID: "workspace", Column: "other", Situation: "first", Rank: []string{"openai:sol"}, Efforts: map[string]string{"openai:sol": "high"}},
		{ProfileID: "workspace", Column: "other", Situation: ModelsSmallSituation, Rank: []string{"anthropic:opus"}, End: "default", Efforts: map[string]string{"anthropic:opus": "xhigh"}},
	}
	s.Person = nil
	small := ResolveModelsBoard(s, BoardQuery{Column: "other", EstimateHours: 1})
	if !reflect.DeepEqual(small.Rank, []string{"anthropic:opus", "openai:sol"}) || len(small.Steps) != 2 || small.Steps[0].Effort != "xhigh" || small.Steps[1].Effort != "high" {
		t.Fatalf("Default small did not return to the row: %+v", small)
	}
	s = rankedFixture()
	s.Orders = []BoardOrder{
		{ProfileID: "workspace", Column: "backend", Situation: "first", Rank: []string{"openai:sol"}},
		{ProfileID: "person", Column: "backend", Situation: "fix", Rank: []string{"anthropic:opus"}, End: "wait"},
	}
	own := ResolveModelsBoard(s, BoardQuery{Column: "backend", FixRound: 1})
	if own.PreferenceOf.Source != "person" || !reflect.DeepEqual(own.Rank, []string{"anthropic:opus"}) {
		t.Fatalf("personal situation disappeared: %+v", own)
	}
	s.Person = nil
	s.Orders = []BoardOrder{
		{ProfileID: "workspace", Column: "other", Situation: "first", Rank: []string{"openai:astra"}, Efforts: map[string]string{"openai:astra": "xhigh"}},
		{ProfileID: "workspace", Column: "backend", Situation: "first", Rank: []string{"openai:sol", "missing:route"}, End: "default", Efforts: map[string]string{"openai:sol": "high"}},
		{ProfileID: "workspace", Column: "backend", Situation: "fix", Rank: []string{"anthropic:opus"}, End: "row"},
		{ProfileID: "workspace", Column: "backend", Situation: "review", Rank: []string{"anthropic:opus"}, End: "auto"},
	}
	resumed := ResolveModelsBoard(s, BoardQuery{Column: "backend", FixRound: 1})
	if !reflect.DeepEqual(resumed.Rank, []string{"anthropic:opus", "openai:sol", "missing:route", "openai:astra"}) || len(resumed.Held) != 0 || resumed.Steps[1].Effort != "high" || resumed.Steps[3].Effort != "xhigh" || resumed.Steps[3].Stage != "default" {
		t.Fatalf("situation lost its row, effort or Default continuation: %+v", resumed)
	}
	reviewer := ResolveModelsBoard(s, BoardQuery{Column: "backend", Review: true, AuthorFamily: "openai"})
	if !reviewer.Automatic || !reflect.DeepEqual(reviewer.Rank, []string{"anthropic:opus"}) || len(reviewer.Steps) != 1 {
		t.Fatalf("saved review lost automatic continuation: %+v", reviewer)
	}
}

// Risk: a kind with no saved row of its own ignores the Default's saved Review
// and Stuck orders while the Default is still its template, so a migrated
// review ladder or build-hard ladder silently becomes Automatic or the row.
func TestModelsV2TemplateKindInheritsDefaultSituations(t *testing.T) {
	s := rankedFixture()
	s.Person = nil
	s.Orders = []BoardOrder{
		{ProfileID: "workspace", Column: "other", Situation: "review", Rank: []string{"anthropic:fable"}, End: "wait"},
		{ProfileID: "workspace", Column: "other", Situation: "stuck", Rank: []string{"anthropic:opus"}, End: "wait"},
	}
	if row := ResolveBoard(s, BoardQuery{Column: "backend"}, nil); row.Source != "template" {
		t.Fatalf("fixture must show the row from the Default's template: %+v", row)
	}
	review := ResolveModelsBoard(s, BoardQuery{Column: "backend", Review: true, AuthorFamily: "openai"})
	if review.Automatic || !reflect.DeepEqual(review.Rank, []string{"anthropic:fable"}) || len(review.Steps) != 1 || review.Situation != "review" {
		t.Fatalf("template kind ignored the Default's Review order: %+v", review)
	}
	stuck := ResolveModelsBoard(s, BoardQuery{Column: "backend", Situation: "stuck"})
	if !reflect.DeepEqual(stuck.Rank, []string{"anthropic:opus"}) || len(stuck.Steps) != 1 || stuck.FollowsFirst {
		t.Fatalf("template kind ignored the Default's Stuck order: %+v", stuck)
	}
	// A kind with its own saved row keeps Automatic review and its own row.
	s.Orders = append(s.Orders, BoardOrder{ProfileID: "workspace", Column: "backend", Situation: "first", Rank: []string{"openai:sol"}})
	if own := ResolveModelsBoard(s, BoardQuery{Column: "backend", Review: true, AuthorFamily: "openai"}); !own.Automatic || len(own.Steps) != 0 {
		t.Fatalf("a kind's own row inherited the Default's Review order: %+v", own)
	}
}
