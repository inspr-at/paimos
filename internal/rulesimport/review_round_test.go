// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/client"
)

func TestDirectiveOppositionRespectsTighteningAudienceAndHeadings(t *testing.T) {
	tight := mustBuild(t, Request{Context: ContextTemplate, Files: []string{
		writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "# Synthetic kernel\n\n## Git\n\n- May use signed commits.\n  Why: optional locally.\n"),
		writeDoc(t, t.TempDir(), "AGENTS.md", "<!-- aeon-context: template -->\n# Repo\n\n## Git\n\n- Must use signed commits.\n  Why: the project tightens the rule.\n"),
	}})
	for _, item := range tight.Contradictions {
		if item.Kind == "cross_layer_directive" {
			t.Fatalf("tightening flagged: %+v", item)
		}
	}

	permit := mustBuild(t, Request{Context: ContextTemplate, Files: []string{
		writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "# Synthetic kernel\n\n## Git\n\n- May use signed commits.\n  Why: optional locally.\n"),
		writeDoc(t, t.TempDir(), "AGENTS.md", "<!-- aeon-context: template -->\n# Repo\n\n## Git\n\n- Never use signed commits.\n  Why: the project forbids them.\n"),
	}})
	for _, item := range permit.Contradictions {
		if item.Kind == "cross_layer_directive" {
			t.Fatalf("permission versus prohibition flagged: %+v", item)
		}
	}

	opposed := mustBuild(t, Request{Context: ContextTemplate, Files: []string{
		writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "# Synthetic kernel\n\n## Hard safety\n\n### Git\n\n- **Never** force-push.\n  Why: shared history.\n"),
		writeDoc(t, t.TempDir(), "AGENTS.md", "<!-- aeon-context: template -->\n# Repo\n\n## Git\n\n- Always force-push.\n  Why: a conflicting instruction.\n"),
	}})
	var found Contradiction
	for _, item := range opposed.Contradictions {
		if item.Kind == "cross_layer_directive" {
			found = item
		}
	}
	if found.Topic != "force-push" || strings.Join(found.Headings, ",") != "Git,Hard safety / Git" || strings.Join(found.Layers, ",") != "company,project" {
		t.Fatalf("heading/emphasis contradiction %+v", found)
	}

	disjoint := mustBuild(t, Request{Context: ContextTemplate, Files: []string{
		writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "# Synthetic kernel\n\n## Git\n\n- Must use signed commits.\n  Why: builders sign.\n  roles: builder\n  harness: codex\n"),
		writeDoc(t, t.TempDir(), "AGENTS.md", "<!-- aeon-context: template -->\n# Repo\n\n## Git\n\n- Never use signed commits.\n  Why: reviewers do not.\n  roles: reviewer\n  harness: cursor\n"),
	}})
	for _, item := range disjoint.Contradictions {
		if item.Kind == "cross_layer_directive" {
			t.Fatalf("disjoint audience flagged: %+v", item)
		}
	}

	overlap := mustBuild(t, Request{Context: ContextTemplate, Files: []string{
		writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "# Synthetic kernel\n\n## Git\n\n- Must use signed commits.\n  Why: the floor.\n"),
		writeDoc(t, t.TempDir(), "AGENTS.md", "<!-- aeon-context: template -->\n# Repo\n\n## Git\n\n- Never use signed commits.\n  Why: one role disagrees.\n  roles: builder\n"),
	}})
	if !hasCrossLayer(overlap, "use signed commits") {
		t.Fatalf("overlapping audience missed: %+v", overlap.Contradictions)
	}
}

func TestSessionBudgetCountsRenderedIdentities(t *testing.T) {
	var body strings.Builder
	body.WriteString("# Synthetic kernel\n\n## Rules\n\n")
	for i := 0; i < 30; i++ {
		body.WriteString("- " + padRule(i) + "\n  Why: evidence.\n")
	}
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", body.String())}})
	if len(got.Rules) != 30 {
		t.Fatalf("rules %d", len(got.Rules))
	}
	doc := alwaysOnDocument(got.Rules)
	if got.AlwaysOn.Bytes != 12832 || got.AlwaysOn.Bytes != len(doc) || got.AlwaysOn.Insert || !strings.HasPrefix(doc, sessionHeader) {
		t.Fatalf("budget %+v doc %d", got.AlwaysOn, len(doc))
	}
	line := sessionRuleLine(importedIdentity(got.Rules[0].Identity), got.Rules[0].Text)
	if !strings.Contains(doc, line) || strings.Contains(doc, "PACK-BODY") {
		t.Fatalf("renderer line missing:\n%s", doc)
	}
}

func TestDivergedBaselineAndLineageReplacement(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "AGENTS.md", "## Git\n- Never force-push.\n  Why: shared history.\n- Prefer small commits.\n  Why: easier review.\n")
	first := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	mapped, err := MapDraft(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapped) != 2 {
		t.Fatalf("mapped %d", len(mapped))
	}
	current := &DraftSet{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Imported", Revision: 4,
		Scope: DraftScope{Layer: LayerProject}, Rules: append([]DraftRule(nil), mapped...),
	}
	var puts int
	api := serveDraft(t, current, &puts)

	edited := append([]DraftRule(nil), mapped...)
	edited[0].Why = "Edited in Aeon."
	current.Rules = edited
	_, err = ApplyDraft(context.Background(), api, first, Target{SetID: current.ID, Revision: current.Revision})
	if !errors.Is(err, ErrDraftConflict) || puts != 0 {
		t.Fatalf("diverged edit err=%v puts=%d", err, puts)
	}
	if current.Rules[0].Why != "Edited in Aeon." {
		t.Fatalf("edit replaced: %+v", current.Rules[0])
	}

	current.Rules = append([]DraftRule(nil), mapped...)
	if err := os.WriteFile(path, []byte("## Git\n- Never force-push the default branch.\n  Why: shared history.\n- Prefer small commits.\n  Why: easier review.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	var priorID, nextID string
	for _, rule := range first.Rules {
		if rule.Text == "Never force-push." {
			priorID = rule.Identity
		}
	}
	for _, rule := range second.Rules {
		if rule.Text == "Never force-push the default branch." {
			nextID = rule.Identity
		}
	}
	if priorID == "" || nextID == "" || priorID == nextID {
		t.Fatalf("unannotated identity did not follow the text change: %s %s", priorID, nextID)
	}
	result, err := ApplyDraft(context.Background(), api, second, Target{SetID: current.ID, Revision: current.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Updated != 2 || result.Unchanged != 0 || len(current.Rules) != 2 || puts != 1 {
		t.Fatalf("lineage replacement %+v rules=%d puts=%d", result, len(current.Rules), puts)
	}
	texts := map[string]bool{}
	for _, rule := range current.Rules {
		texts[rule.Text] = true
	}
	if !texts["Never force-push the default branch."] || !texts["Prefer small commits."] || texts["Never force-push."] {
		t.Fatalf("obsolete rule kept: %+v", current.Rules)
	}
	current.Rules[0].Why = "Edited in Aeon after the text change."
	_, err = ApplyDraft(context.Background(), api, second, Target{SetID: current.ID, Revision: current.Revision})
	if !errors.Is(err, ErrDraftConflict) || puts != 1 {
		t.Fatalf("lineage match overwrote an Aeon edit: %v puts=%d", err, puts)
	}
	if current.Rules[0].Why != "Edited in Aeon after the text change." {
		t.Fatal("lineage match replaced the edited rule")
	}
}

func TestAmbiguousLineageReplacementIsReported(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "AGENTS.md", "## Git\n- Red blue green.\n  Why: one.\n- Red blue yellow.\n  Why: two.\n")
	first := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	mapped, err := MapDraft(first)
	if err != nil {
		t.Fatal(err)
	}
	current := &DraftSet{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Imported", Revision: 2,
		Scope: DraftScope{Layer: LayerProject}, Rules: mapped,
	}
	var puts int
	api := serveDraft(t, current, &puts)
	if err := os.WriteFile(path, []byte("## Git\n\n- Red blue orange.\n  Why: one.\n- Red blue purple.\n  Why: two.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	_, err = ApplyDraft(context.Background(), api, second, Target{SetID: current.ID, Revision: current.Revision})
	if !errors.Is(err, ErrDraftConflict) || !strings.Contains(err.Error(), "unresolved replacement") || puts != 0 {
		t.Fatalf("ambiguous replacement err=%v puts=%d", err, puts)
	}
	if len(current.Rules) != 2 || current.Rules[0].Text != mapped[0].Text {
		t.Fatal("ambiguous apply wrote a draft")
	}
}

func TestBaselineCoversIdentityAndSourceRevision(t *testing.T) {
	path := writeDoc(t, t.TempDir(), "AGENTS.md", "## Git\n- Never force-push.\n  Why: shared history.\n")
	proposal := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	mapped, err := MapDraft(proposal)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*DraftRule)
		kept func(DraftRule) bool
	}{
		{
			name: "identity",
			edit: func(rule *DraftRule) { rule.Identity = "aeon-renamed-rule" },
			kept: func(rule DraftRule) bool { return rule.Identity == "aeon-renamed-rule" },
		},
		{
			name: "revision",
			edit: func(rule *DraftRule) { rule.Source.Revision = "aeon-corrected-revision" },
			kept: func(rule DraftRule) bool { return rule.Source.Revision == "aeon-corrected-revision" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := append([]DraftRule(nil), mapped...)
			tc.edit(&rules[0])
			if !divergedFromBaseline(rules[0]) {
				t.Fatal("edit still matches the imported baseline")
			}
			current := &DraftSet{
				ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Imported", Revision: 4,
				Scope: DraftScope{Layer: LayerProject}, Rules: rules,
			}
			var puts int
			_, err := ApplyDraft(context.Background(), serveDraft(t, current, &puts), proposal, Target{SetID: current.ID, Revision: current.Revision})
			if !errors.Is(err, ErrDraftConflict) || puts != 0 || !tc.kept(current.Rules[0]) || len(current.Rules) != 1 {
				t.Fatalf("Aeon edit overwritten: err=%v puts=%d rules=%+v", err, puts, current.Rules)
			}
		})
	}
}

func TestRetitleDirectlyUnderDocumentTitleUpdatesRule(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "AGENTS.md", "# Repo rules\n\n- Never force-push.\n  Why: shared history.\n")
	first := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	if len(first.Rules) != 1 || first.Rules[0].Sources[0].HeadingPath != "Repo rules / " {
		t.Fatalf("heading under title %+v", first.Rules)
	}
	mapped, err := MapDraft(first)
	if err != nil {
		t.Fatal(err)
	}
	current := &DraftSet{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Imported", Revision: 4,
		Scope: DraftScope{Layer: LayerProject}, Rules: mapped,
	}
	var puts int
	api := serveDraft(t, current, &puts)
	if err := os.WriteFile(path, []byte("# Updated repo rules\n\n- Never force-push the default branch.\n  Why: shared history.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	if len(second.Rules) != 1 || second.Rules[0].Sources[0].HeadingPath != "Updated repo rules / " {
		t.Fatalf("retitled heading %+v", second.Rules)
	}
	result, err := ApplyDraft(context.Background(), api, second, Target{SetID: current.ID, Revision: current.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Updated != 1 || result.Unchanged != 0 || puts != 1 || len(current.Rules) != 1 || current.Rules[0].Text != "Never force-push the default branch." {
		t.Fatalf("retitle under the title appended the changed rule: %+v rules=%+v", result, current.Rules)
	}
}

func TestHeadingsAlignIncludesEmptySection(t *testing.T) {
	if !headingsAlign("Old title / ", "New title / ") || !headingsAlign("Old title / Git", "New title / Git") {
		t.Fatal("retitle lost the section below the title")
	}
	if headingsAlign("Old title / ", "New title / Git") || headingsAlign("Git", "Review") || headingsAlign("Git / Safety", "Review") {
		t.Fatal("unrelated heading paths aligned")
	}
}

func TestRetitleMatchesHeadingBelowDocumentTitle(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "AGENTS.md", "# Repo rules\n\n## Git\n- Never force-push.\n  Why: shared history.\n")
	first := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	mapped, err := MapDraft(first)
	if err != nil {
		t.Fatal(err)
	}
	current := &DraftSet{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "Imported", Revision: 4,
		Scope: DraftScope{Layer: LayerProject}, Rules: mapped,
	}
	var puts int
	api := serveDraft(t, current, &puts)
	if err := os.WriteFile(path, []byte("# Updated repo rules\n\n## Git\n- Never force-push the default branch.\n  Why: shared history.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := mustBuild(t, Request{Context: ContextProject, Files: []string{path}})
	result, err := ApplyDraft(context.Background(), api, second, Target{SetID: current.ID, Revision: current.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Updated != 1 || result.Unchanged != 0 || puts != 1 || len(current.Rules) != 1 || current.Rules[0].Text != "Never force-push the default branch." {
		t.Fatalf("retitle appended the changed rule: %+v rules=%+v", result, current.Rules)
	}
}

func TestDoubleBacktickCodeSpanIsNotEmphasis(t *testing.T) {
	if got := renderMarkdownInline("``foo*bar``"); got != "foo*bar" {
		t.Fatalf("double backtick rendered %q", got)
	}
	assertNoDirectiveConflict(t, "Never delete ``foo*bar``.", "Must delete ``foobar``.")
	same := directivePair(t, "Never delete ``foo*bar``.", "Must delete ``foo*bar``.")
	if !hasCrossLayer(same, "delete foo*bar") {
		t.Fatalf("identical double-backtick spans missed: %+v", same.Contradictions)
	}
}

func TestEmphasisStrippingKeepsIdentifiers(t *testing.T) {
	assertNoDirectiveConflict(t, "Never delete foo_bar.", "Must delete foobar.")
	assertNoDirectiveConflict(t, "Never delete `foo_bar`.", "Must delete `foobar`.")

	same := directivePair(t, "Never delete `foo_bar`.", "Must delete `foo_bar`.")
	if !hasCrossLayer(same, "delete foo_bar") {
		t.Fatalf("identical code identifiers missed: %+v", same.Contradictions)
	}
	emphasized := directivePair(t, "_Never_ delete foo_bar.", "Always delete foo_bar.")
	if !hasCrossLayer(emphasized, "delete foo_bar") {
		t.Fatalf("emphasis around a directive missed: %+v", emphasized.Contradictions)
	}
}

func directivePair(t *testing.T, left, right string) Proposal {
	t.Helper()
	return mustBuild(t, Request{Context: ContextTemplate, Files: []string{
		writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "# Synthetic kernel\n\n## Files\n- "+left+"\n  Why: keep that file.\n"),
		writeDoc(t, t.TempDir(), "AGENTS.md", "<!-- aeon-context: template -->\n# Repo\n\n## Cleanup\n- "+right+"\n  Why: obsolete file.\n"),
	}})
}

func assertNoDirectiveConflict(t *testing.T, left, right string) {
	t.Helper()
	got := directivePair(t, left, right)
	for _, item := range got.Contradictions {
		if item.Kind == "cross_layer_directive" {
			t.Fatalf("distinct identifiers flagged: %+v", item)
		}
	}
}

func hasCrossLayer(p Proposal, topic string) bool {
	for _, item := range p.Contradictions {
		if item.Kind == "cross_layer_directive" && item.Topic == topic {
			return true
		}
	}
	return false
}

func padRule(i int) string {
	suffix := "00"
	if i < 10 {
		suffix = "0" + string(rune('0'+i))
	} else {
		suffix = string(rune('0'+i/10)) + string(rune('0'+i%10))
	}
	return suffix + strings.Repeat("x", 348)
}

func serveDraft(t *testing.T, current *DraftSet, puts *int) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if err := json.NewEncoder(w).Encode(current); err != nil {
				t.Error(err)
			}
		case http.MethodPut:
			var body DraftBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			*puts++
			current.Rules = body.Rules
			current.Revision++
			if err := json.NewEncoder(w).Encode(current); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "synthetic-test-token")
}
