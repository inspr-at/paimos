// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
)

func TestGoldenSplitLineageAndCaution(t *testing.T) {
	kernel := filepath.Join("testdata", "split", "AGENTS-KERNEL.md")
	repo := filepath.Join("testdata", "split", "AGENTS.md")
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{kernel, repo}})

	locked, ok := findText(got, "🔴 Never force-push the default branch.")
	if !ok || locked.Layer != LayerCompany || locked.Set != "git" || locked.Strength != StrengthLocked {
		t.Fatalf("locked %+v", locked)
	}
	if locked.Sources[0].HeadingPath != "Synthetic kernel / Git" || locked.Sources[0].SHA256 == "" || locked.Sources[0].Path == "" {
		t.Fatalf("lineage %+v", locked.Sources)
	}
	if !strings.Contains(locked.Details, "The remote keeps the old commits.") {
		t.Fatalf("details %q", locked.Details)
	}

	caution, ok := findText(got, "🟡 Ask before rewriting local commits.")
	if !ok || caution.Strength != StrengthNormal || caution.Layer != LayerCompany {
		t.Fatalf("caution %+v", caution)
	}
	if hasUnresolved(got, "strength_unspecified", caution.Sources[0].StartLine) {
		t.Fatal("explicit caution was treated as unspecified strength")
	}

	safety, ok := findText(got, "🟡 Ask before deleting generated files.")
	if !ok || safety.Strength != StrengthNormal || safety.Sources[0].HeadingPath != "Synthetic kernel / Hard safety" {
		t.Fatalf("safety caution %+v", safety)
	}
	if hasUnresolved(got, "strength_unspecified", safety.Sources[0].StartLine) {
		t.Fatal("caution under a locked heading was unspecified")
	}

	review, ok := findText(got, "Prefer small commits.")
	if !ok || review.Layer != LayerCompany || strings.Join(review.Roles, ",") != "reviewer" || strings.Join(review.Harnesses, ",") != "codex" {
		t.Fatalf("review %+v", review)
	}
	project, ok := findText(got, "Mention the ticket in the message.")
	if !ok || project.Layer != LayerProject || project.Sources[0].HeadingPath != "Repo delta / Git" {
		t.Fatalf("project %+v", project)
	}
	if len(got.Contradictions) != 0 {
		t.Fatalf("split fixtures are not conflicts: %+v", got.Contradictions)
	}

	overridden := mustBuild(t, Request{
		Context: ContextTemplate,
		Files:   []string{kernel},
		Layers:  map[string]Layer{kernel: LayerPerson},
	})
	moved, ok := findText(overridden, locked.Text)
	if !ok || moved.Layer != LayerPerson || !strings.HasPrefix(moved.Identity, "person/") {
		t.Fatalf("layer map %+v", moved)
	}
}

func TestGoldenPersonalSectionAndPackDetails(t *testing.T) {
	personal := filepath.Join("testdata", "personal", "CLAUDE.md")
	got := mustBuild(t, Request{Context: ContextPerson, Section: SectionPersonal, Files: []string{personal}})
	if len(got.Rules) != 1 {
		t.Fatalf("rules %d", len(got.Rules))
	}
	rule := got.Rules[0]
	if rule.Layer != LayerPerson || rule.Text != "Prefer short status notes." || rule.Sources[0].HeadingPath != "Synthetic global / Personal section" {
		t.Fatalf("personal %+v", rule)
	}
	if _, ok := findText(got, "Keep shared checks in the kernel."); ok {
		t.Fatal("kernel section was included in the personal import")
	}

	packPath := filepath.Join("testdata", "pack", "AGENTS-DOMAIN-SECRETS.md")
	pack := mustBuild(t, Request{Context: ContextTemplate, Files: []string{packPath}})
	secret, ok := findText(pack, "Keep credentials out of the always-on file.")
	if !ok || secret.Placement != PlacementOnDemand {
		t.Fatalf("pack %+v", secret)
	}
	if !strings.Contains(secret.Details, "PACK-BODY-START") || !strings.Contains(secret.Details, "PACK-BODY-END") {
		t.Fatalf("pack details %q", secret.Details)
	}
	if strings.Contains(alwaysOnDocument(pack.Rules), "PACK-BODY-START") {
		t.Fatal("pack body entered the always-on projection")
	}
	if pack.AlwaysOn.Bytes > AlwaysOnBudget || !pack.AlwaysOn.Insert {
		t.Fatalf("pack budget %+v", pack.AlwaysOn)
	}
	if !pack.Adapter.Ready {
		t.Fatalf("pack was not draftable: %s", pack.Adapter.Reason)
	}
	mapped, err := MapDraft(pack)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapped) != 1 || mapped[0].Text != secret.Text || !strings.Contains(mapped[0].Details, "PACK-BODY-START") || !strings.Contains(mapped[0].Details, "PACK-BODY-END") || strings.Contains(mapped[0].Text, "PACK-BODY") {
		t.Fatalf("pack was not attached as draft details: %+v", mapped)
	}
}

func TestGoldenContradictionsReport(t *testing.T) {
	kernel := filepath.Join("testdata", "contradict", "AGENTS-KERNEL.md")
	repo := filepath.Join("testdata", "contradict", "AGENTS.md")
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{kernel, repo}})
	var found Contradiction
	for _, item := range got.Contradictions {
		if item.Kind == "cross_layer_directive" {
			found = item
		}
	}
	if found.Kind == "" || found.Topic != "force-push the default branch" {
		t.Fatalf("contradiction %+v", got.Contradictions)
	}
	if strings.Join(found.Layers, ",") != "company,project" || len(found.RuleIDs) != 2 || strings.Join(found.Headings, ",") != "Git" {
		t.Fatalf("layers/rules %+v", found)
	}
	if got.Adapter.Ready {
		t.Fatal("a contradiction was draftable")
	}

	dir := t.TempDir()
	if err := WriteReport(dir, got); err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(filepath.Join(dir, "contradictions.md"))
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile(filepath.Join(dir, "contradictions.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(md)
	if !strings.Contains(text, "cross_layer_directive") || !strings.Contains(text, found.Topic) || !strings.Contains(text, "company, project") {
		t.Fatalf("markdown:\n%s", text)
	}
	var report ContradictionReport
	if err := json.Unmarshal(js, &report); err != nil {
		t.Fatal(err)
	}
	if report.PlanID != got.PlanID || len(report.Contradictions) != len(got.Contradictions) || report.Contradictions[0].Kind != "cross_layer_directive" {
		t.Fatalf("json %+v", report)
	}
	if err := WriteReport(dir, got); err == nil {
		t.Fatal("report overwrote existing files")
	}

	same := mustBuild(t, Request{Context: ContextTemplate, Files: []string{
		writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "# Synthetic kernel\n\n## Git\n\n- 🔴 Never force-push the default branch.\n  Why: shared history.\n"),
		writeDoc(t, t.TempDir(), "AGENTS.md", "<!-- aeon-context: template -->\n# Repo delta\n\n## Git\n\n- Never force-push the default branch.\n  Why: the repository agrees.\n"),
	}})
	for _, item := range same.Contradictions {
		if item.Kind == "cross_layer_directive" {
			t.Fatalf("same directive flagged: %+v", item)
		}
	}
}

func TestAlwaysOnBudgetKeepsDefault(t *testing.T) {
	if AlwaysOnBudget != rules.LegacyMaxBytes {
		t.Fatalf("importer budget %d, default %d", AlwaysOnBudget, rules.LegacyMaxBytes)
	}
	over := mustBuild(t, Request{Context: ContextTemplate, Files: []string{
		writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "- "+strings.Repeat("x", AlwaysOnBudget)+"\n  Why: too long for the session file.\n"),
	}})
	if over.AlwaysOn.Insert || over.AlwaysOn.Budget != rules.LegacyMaxBytes || over.Adapter.Ready {
		t.Fatalf("over-cap proposal was acceptable: %+v ready %v", over.AlwaysOn, over.Adapter.Ready)
	}
	if _, err := MapDraft(over); err == nil {
		t.Fatal("over-cap proposal mapped to a draft")
	}
}

func TestBuildRejectsUnknownLayerMapping(t *testing.T) {
	path := writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", "- Keep the check.\n  Why: evidence.\n")
	if _, err := Build(context.Background(), Request{Context: ContextTemplate, Files: []string{path}, Layers: map[string]Layer{path: "workspace"}}); err == nil {
		t.Fatal("unknown layer accepted")
	}
	if _, err := Build(context.Background(), Request{Context: ContextTemplate, Files: []string{path}, Layers: map[string]Layer{filepath.Join(t.TempDir(), "missing.md"): LayerCompany}}); err == nil {
		t.Fatal("unused layer mapping accepted")
	}
}
