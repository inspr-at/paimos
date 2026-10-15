// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

func approvedAdmission() AdmissionInput {
	b := reviewgate.RunBinding{Ticket: "ticket", Repository: "owner/repo", PR: 7, Target: "main", Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
	b.Scope = b.Base + ".." + b.Head
	r := &AdmissionApproval{Binding: b, Revision: 1, RunID: "authenticated-run", Kind: "review", AuthorFamily: "openai", ReviewerFamily: "anthropic", Harness: "claude", Model: "claude-opus-4-6", Verdict: "ok", VisualImpact: "no", Reason: "No presentation changes"}
	return AdmissionInput{Binding: b, Revision: 1, Open: true, CIGreen: true, ScopeVerified: true, Review: r, Diff: AdmissionDiff{MergeBase: b.Base, Paths: []string{"docs/feature.md"}, Complete: true}, Policy: AdmissionPolicy{BlockOnNightlyRed: true, NightlyState: "green"}}
}
func TestAdmissionRules(t *testing.T) {
	// Risk: any single missing prerequisite, stale binding, same-family review,
	// explicit hold or red nightly could admit a head. Each row changes one fact.
	cases := []struct {
		name, reason string
		change       func(*AdmissionInput)
	}{
		{"approved", "admissible", func(*AdmissionInput) {}},
		{"invalid_scope", "binding_invalid", func(i *AdmissionInput) { i.Binding.Scope = "HEAD" }},
		{"stacked_target", "target_not_main", func(i *AdmissionInput) { i.Binding.Target = "work/stack" }},
		{"closed", "pull_not_open", func(i *AdmissionInput) { i.Open = false }},
		{"scope_unproven", "scope_unverified", func(i *AdmissionInput) { i.ScopeVerified = false }},
		{"ci_red", "ci_not_green", func(i *AdmissionInput) { i.CIGreen = false }},
		{"missing_review", "review_missing_or_stale", func(i *AdmissionInput) { i.Review = nil }},
		{"new_head", "review_missing_or_stale", func(i *AdmissionInput) {
			i.Binding.Head = strings.Repeat("c", 40)
			i.Binding.Scope = i.Binding.Base + ".." + i.Binding.Head
		}},
		{"new_scope", "review_missing_or_stale", func(i *AdmissionInput) {
			i.Binding.Base = strings.Repeat("d", 40)
			i.Binding.Scope = i.Binding.Base + ".." + i.Binding.Head
		}},
		{"retarget_back", "review_missing_or_stale", func(i *AdmissionInput) { i.Revision++ }},
		{"wrong_repo", "review_missing_or_stale", func(i *AdmissionInput) { i.Review.Binding.Repository = "other/repo" }},
		{"wrong_pr", "review_missing_or_stale", func(i *AdmissionInput) { i.Review.Binding.PR++ }},
		{"wrong_ticket", "review_missing_or_stale", func(i *AdmissionInput) { i.Review.Binding.Ticket = "other" }},
		{"review_target", "review_missing_or_stale", func(i *AdmissionInput) { i.Review.Binding.Target = "release" }},
		{"no_run", "review_missing_or_stale", func(i *AdmissionInput) { i.Review.RunID = "" }},
		{"same_family_even_policy_off", "review_not_cross_family", func(i *AdmissionInput) { i.Review.ReviewerFamily = "openai" }},
		{"unknown_family", "review_not_cross_family", func(i *AdmissionInput) { i.Review.AuthorFamily = "" }},
		{"missing_route", "review_not_cross_family", func(i *AdmissionInput) { i.Review.Model = "" }},
		{"review_changes", "review_changes_requested", func(i *AdmissionInput) { i.Review.Verdict = "changes" }},
		{"visual_yes", "visual_missing_or_stale", func(i *AdmissionInput) { i.Review.VisualImpact = "yes" }},
		{"visual_uncertain", "visual_missing_or_stale", func(i *AdmissionInput) { i.Review.VisualImpact = "uncertain" }},
		{"backend_not_exempt", "visual_missing_or_stale", func(i *AdmissionInput) { i.Diff.Paths = []string{"internal/nodes/nodes.go"} }},
		{"nightly_red", "nightly_red", func(i *AdmissionInput) { i.Policy.NightlyState = "red" }},
		{"nightly_unknown", "nightly_unknown", func(i *AdmissionInput) { i.Policy.NightlyState = "unknown" }},
		{"nightly_fix", "admissible", func(i *AdmissionInput) { i.Policy.NightlyState = "red"; i.Fix = true }},
		{"policy_disabled", "admissible", func(i *AdmissionInput) { i.Policy.NightlyState = "unknown"; i.Policy.BlockOnNightlyRed = false }},
	}
	for _, kind := range []string{"release_cut", "wave", "manual"} {
		cases = append(cases, struct {
			name, reason string
			change       func(*AdmissionInput)
		}{kind, "hold_" + kind, func(i *AdmissionInput) {
			i.Fix = true
			i.CIGreen = false
			i.Policy.NightlyState = "red"
			i.Holds = []AdmissionHold{{Kind: kind, Reason: "Visible hold reason"}}
		}})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			in := approvedAdmission()
			tt.change(&in)
			out := EvaluateAdmission(in)
			if out.Reason != tt.reason || out.Admissible != (tt.reason == "admissible") {
				t.Fatalf("got %+v want %s", out, tt.reason)
			}
			if strings.HasPrefix(tt.reason, "hold_") && out.HoldReason != "Visible hold reason" {
				t.Fatal("hold reason hidden")
			}
		})
	}
	in := approvedAdmission()
	in.Review.VisualImpact = "yes"
	v := *in.Review
	v.Kind = "visual"
	in.Visual = &v
	if got := EvaluateAdmission(in); !got.Admissible || !got.VisualRequired {
		t.Fatal(got)
	}
	in.Visual.Verdict = "changes"
	if got := EvaluateAdmission(in); got.Reason != "visual_changes_requested" {
		t.Fatal(got)
	}
	in.Visual.Verdict = "ok"
	in.Visual.Revision++
	if got := EvaluateAdmission(in); got.Reason != "visual_missing_or_stale" {
		t.Fatal(got)
	}
}
func TestVisualAdmissionClassifier(t *testing.T) {
	// Risk: backend-only, unknown, truncated, malformed and rename diffs can
	// masquerade as presentation-independent changes, independently of S2.
	for _, tt := range []struct {
		file   string
		exempt bool
	}{
		{"docs/features/queue.md", true}, {"internal/delivery/admission_test.go", true}, {"web/tests/queue.spec.ts", true},
		{".github/workflows/ci.yml", true}, {"scripts/test-tiers/impact-plan.mjs", true},
		{"scripts/playwright-safe.mjs", true}, {"internal/db/pool.go", true}, {"internal/db/acquire_metrics.go", true},
		{"internal/nodes/nodes.go", false}, {"internal/authz/authz.go", false}, {"internal/db/migrations/1356_delivery_admission.sql", false},
		{"api/areas/nodes.yaml", false}, {"web/src/App.vue", false}, {"web/public/icon.svg", false}, {"web/tests/../../src/App.vue", false},
		{"../docs/readme.md", false}, {"/docs/readme.md", false}, {"docs/a\x00.md", false},
	} {
		t.Run(tt.file, func(t *testing.T) {
			required, _ := VisualReviewRequired("no", "Reviewed", AdmissionDiff{Complete: true, Paths: []string{tt.file}})
			if required == tt.exempt {
				t.Fatalf("%s required=%v", tt.file, required)
			}
		})
	}
	for _, diff := range []AdmissionDiff{{}, {Complete: true}, {Paths: []string{"docs/a.md"}}, {Complete: true, Paths: []string{"docs/a.md", "web/src/App.vue"}}} {
		if required, _ := VisualReviewRequired("no", "Reviewed", diff); !required {
			t.Fatal("unproven diff exempted")
		}
	}
	for _, impact := range []string{"yes", "uncertain", "", "invalid"} {
		if required, _ := VisualReviewRequired(impact, "Reviewed", AdmissionDiff{Complete: true, Paths: []string{"docs/a.md"}}); !required {
			t.Fatal("reviewer impact ignored")
		}
	}
}
func TestAdmissionDiffReadsMergeBaseAndRenameSides(t *testing.T) {
	// Risk: target tip is mistaken for merge-base, or rename/truncation hides UI.
	p := Pull{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
	mergeBase := strings.Repeat("c", 40)
	raw := `{"merge_base_commit":{"sha":"` + mergeBase + `"},"files":[{"filename":"docs/a.md","previous_filename":"web/src/App.vue","status":"renamed"}]}`
	get := func(endpoint string, out any) error {
		if endpoint != "/compare/"+p.Base+"..."+p.Head+"?per_page=1&page=1" {
			t.Fatal(endpoint)
		}
		return json.Unmarshal([]byte(raw), out)
	}
	if err := readAdmissionDiff(get, &p); err != nil {
		t.Fatal(err)
	}
	if p.AdmissionDiff.MergeBase != mergeBase || len(p.AdmissionDiff.Paths) != 2 {
		t.Fatal(p.AdmissionDiff)
	}
	if required, _ := VisualReviewRequired("no", "Reviewed", *p.AdmissionDiff); !required {
		t.Fatal("rename exempted")
	}
	files := make([]map[string]string, 300)
	for i := range files {
		files[i] = map[string]string{"filename": "docs/a.md", "status": "modified"}
	}
	encoded, _ := json.Marshal(map[string]any{"merge_base_commit": map[string]string{"sha": mergeBase}, "files": files})
	raw = string(encoded)
	if err := readAdmissionDiff(get, &p); err != nil || p.AdmissionDiff.Complete {
		t.Fatal("boundary not conservative", err)
	}
	unavailable := errors.New("read failed")
	if err := readAdmissionDiff(func(string, any) error { return unavailable }, &p); !errors.Is(err, unavailable) {
		t.Fatal("partial read lost", err)
	}
}
func TestAdmissionCIExcludesOwnContext(t *testing.T) {
	// Risk: a required admission status causes a dependency cycle or bootstraps CI.
	o := Observation{Settings: defaults()}
	for _, name := range *o.Settings.RequiredChecks {
		o.Checks = append(o.Checks, Check{Name: name, Status: "completed", Conclusion: "success"})
	}
	required := append([]string{}, *o.Settings.RequiredChecks...)
	required = append(required, AdmissionContext)
	o.Settings.RequiredChecks = &required
	if !admissionCIGreen(o) {
		t.Fatal("admission depended on itself")
	}
	o.Settings.RequiredChecks = &[]string{AdmissionContext}
	if admissionCIGreen(o) {
		t.Fatal("self-only prerequisites admitted")
	}
	o.Settings.RequiredChecks = nil
	if admissionCIGreen(o) {
		t.Fatal("missing prerequisites admitted")
	}
}
