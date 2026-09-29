// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKernelAndRepoLayers(t *testing.T) {
	dir := t.TempDir()
	kernel := writeDoc(t, dir, "kernel/AGENTS-KERNEL.md", `
# Synthetic kernel

## Git

- 🔴 Never force-push the default branch.
  Why: it rewrites shared history.
  The remote keeps the old commits.

  A second detail line.

- Prefer small commits.
  roles: reviewer
  harness: codex, claude
  expires: 2026-10-01
  source: AEON-250
`)
	repo := writeDoc(t, dir, "repo/AGENTS.md", `
<!-- aeon-context: template -->
# Repo delta

## Git

- Mention the ticket in the message.
`)
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{kernel, repo}})
	if got.Mode != "preview" || got.Context != ContextTemplate {
		t.Fatalf("mode %s context %s", got.Mode, got.Context)
	}
	locked, ok := findText(got, "🔴 Never force-push the default branch.")
	if !ok {
		t.Fatal("missing locked rule")
	}
	if locked.Layer != LayerCompany || locked.Set != "git" || locked.Strength != StrengthLocked {
		t.Fatalf("locked rule = %+v", locked)
	}
	if locked.Why != "it rewrites shared history." {
		t.Fatalf("why %q", locked.Why)
	}
	if locked.Details != "The remote keeps the old commits.\n\nA second detail line." {
		t.Fatalf("details %q", locked.Details)
	}
	normal, ok := findText(got, "Prefer small commits.")
	if !ok || normal.Strength != StrengthNormal || normal.Layer != LayerCompany {
		t.Fatalf("normal rule = %+v", normal)
	}
	if strings.Join(normal.Roles, ",") != "reviewer" || strings.Join(normal.Harnesses, ",") != "claude,codex" {
		t.Fatalf("flags roles=%v harnesses=%v", normal.Roles, normal.Harnesses)
	}
	if normal.Expires != "2026-10-01" || normal.Source != "AEON-250" || !normal.Enabled {
		t.Fatalf("explicit fields = %+v", normal)
	}
	project, ok := findText(got, "Mention the ticket in the message.")
	if !ok || project.Layer != LayerProject || project.Set != "git" {
		t.Fatalf("project rule = %+v", project)
	}
	if locked.AlwaysOnEligible && project.AlwaysOnEligible && got.AlwaysOn.Insert && got.AlwaysOn.Bytes <= AlwaysOnBudget {
		return
	}
	t.Fatalf("always-on %+v eligible locked=%v project=%v", got.AlwaysOn, locked.AlwaysOnEligible, project.AlwaysOnEligible)
}

func TestLineageAndReplay(t *testing.T) {
	body := "# Synthetic kernel\n\n## Git\n\n<!-- aeon-rule: git.no-force -->\n- 🔴 Never force-push the default branch.\n  Why: shared history.\n"
	dir := t.TempDir()
	path := writeDoc(t, dir, "a/AGENTS-KERNEL.md", body)
	first := mustBuild(t, Request{Context: ContextTemplate, Files: []string{path}})
	second := mustBuild(t, Request{Context: ContextTemplate, Files: []string{path}})
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if !bytes.Equal(left, right) {
		t.Fatal("replay changed the proposal")
	}
	rule, ok := findText(first, "🔴 Never force-push the default branch.")
	if !ok {
		t.Fatal("missing rule")
	}
	if rule.ExplicitID != "git.no-force" || rule.Identity != "company/git/-/git.no-force" {
		t.Fatalf("identity %s explicit %s", rule.Identity, rule.ExplicitID)
	}
	if rule.Sources[0].StartLine != 5 || rule.Sources[0].EndLine != 7 {
		t.Fatalf("lines %d-%d", rule.Sources[0].StartLine, rule.Sources[0].EndLine)
	}
	if rule.Sources[0].HeadingPath != "Synthetic kernel / Git" {
		t.Fatalf("heading path %q", rule.Sources[0].HeadingPath)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	chunk := strings.Join(lines[4:7], "\n") + "\n"
	sum := sha256.Sum256([]byte(chunk))
	if rule.Sources[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("line hash %s", rule.Sources[0].SHA256)
	}
	fileSum := sha256.Sum256([]byte(body))
	if rule.Sources[0].FileSHA256 != hex.EncodeToString(fileSum[:]) || rule.Sources[0].Path != path {
		t.Fatalf("file lineage %+v", rule.Sources[0])
	}

	other := writeDoc(t, t.TempDir(), "b/AGENTS-KERNEL.md", body)
	moved := mustBuild(t, Request{Context: ContextTemplate, Files: []string{other}})
	movedRule, _ := findText(moved, rule.Text)
	if movedRule.ID != rule.ID || moved.PlanID != first.PlanID {
		t.Fatalf("ids moved rule %s/%s plan %s/%s", movedRule.ID, rule.ID, moved.PlanID, first.PlanID)
	}
}

func TestDuplicateAndContradictionPreserved(t *testing.T) {
	body := "# Synthetic kernel\n\n## Git\n\n<!-- aeon-rule: git.no-force -->\n- 🔴 Never force-push the default branch.\n  Why: shared history.\n"
	other := "# Synthetic kernel\n\n## Git\n\n<!-- aeon-rule: git.no-force -->\n- 🔴 Never force-push the default branch.\n  Why: a different reason.\n"
	dir := t.TempDir()
	a := writeDoc(t, dir, "a/AGENTS-KERNEL.md", body)
	b := writeDoc(t, dir, "b/AGENTS-KERNEL.md", body)
	dup := mustBuild(t, Request{Context: ContextTemplate, Files: []string{a, b}})
	if len(dup.Rules) != 1 || len(dup.Duplicates) != 1 || len(dup.Rules[0].Sources) != 2 {
		t.Fatalf("duplicate rules=%d dups=%d sources=%d", len(dup.Rules), len(dup.Duplicates), len(dup.Rules[0].Sources))
	}
	if len(dup.Contradictions) != 0 {
		t.Fatalf("identical replay is not a contradiction: %+v", dup.Contradictions)
	}
	single := mustBuild(t, Request{Context: ContextTemplate, Files: []string{a}})
	if dup.Rules[0].ID != single.Rules[0].ID {
		t.Fatal("duplicate replay changed the rule id")
	}

	c := writeDoc(t, dir, "c/AGENTS-KERNEL.md", other)
	conflict := mustBuild(t, Request{Context: ContextTemplate, Files: []string{a, c}})
	if len(conflict.Rules) != 2 || len(conflict.Contradictions) != 1 {
		t.Fatalf("rules=%d contradictions=%d", len(conflict.Rules), len(conflict.Contradictions))
	}
	item := conflict.Contradictions[0]
	if item.Kind != "same_identity_difference" || item.Identity != "company/git/-/git.no-force" {
		t.Fatalf("contradiction %+v", item)
	}
	if strings.Join(item.Fields, ",") != "why" {
		t.Fatalf("fields %v", item.Fields)
	}
	whys := map[string]bool{}
	for _, rule := range conflict.Rules {
		if rule.Identity != item.Identity {
			t.Fatalf("rule identity %s", rule.Identity)
		}
		whys[rule.Why] = true
		found := false
		for _, id := range item.RuleIDs {
			if id == rule.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("rule %s missing from contradiction", rule.ID)
		}
	}
	if !whys["shared history."] || !whys["a different reason."] {
		t.Fatalf("whys discarded: %v", whys)
	}
}

func TestHeuristicIsNotPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "AGENTS-KERNEL.md", `
## Git

- Prefer small commits.

## Review

- Prefer small commits.
`)
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{path}})
	if len(got.Rules) != 2 || len(got.Heuristics) != 1 || len(got.Contradictions) != 0 {
		t.Fatalf("rules=%d heuristics=%d contradictions=%d", len(got.Rules), len(got.Heuristics), len(got.Contradictions))
	}
	hint := got.Heuristics[0]
	if hint.Authoritative || hint.Kind != "normalized_text" {
		t.Fatalf("heuristic %+v", hint)
	}
	if !strings.Contains(hint.Note, "not authoritative precedence") {
		t.Fatalf("note %q", hint.Note)
	}
	if hint.RuleIDs[0] == hint.RuleIDs[1] {
		t.Fatal("heuristic collapsed two identities")
	}
}

func TestLockedSectionUncertaintyAndNestedRule(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "AGENTS-KERNEL.md", `
## Hard safety

- Prefer small commits.
- 🔴 Never print secrets.
- State the assumption.
  - 🔴 Never force-push the default branch.
  - Mention the ticket in the message.
`)
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{path}})
	plain, ok := findText(got, "Prefer small commits.")
	if !ok || plain.Strength != StrengthNormal {
		t.Fatalf("plain %+v", plain)
	}
	if !hasUnresolved(got, "strength_unspecified", plain.Sources[0].StartLine) {
		t.Fatal("missing strength uncertainty")
	}
	locked, ok := findText(got, "🔴 Never print secrets.")
	if !ok || locked.Strength != StrengthLocked {
		t.Fatalf("locked %+v", locked)
	}
	if hasUnresolved(got, "strength_unspecified", locked.Sources[0].StartLine) {
		t.Fatal("explicit lock was marked uncertain")
	}
	nested, ok := findText(got, "🔴 Never force-push the default branch.")
	if !ok || nested.Strength != StrengthLocked {
		t.Fatal("nested locked rule was discarded")
	}
	blob, _ := json.Marshal(got)
	if !bytes.Contains(blob, []byte("Mention the ticket in the message.")) {
		t.Fatal("nested plain instruction was discarded")
	}
}

func TestModelReviewHintsRetainAllSourceRules(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "AGENTS-KERNEL.md", `
## Review

- Use Claude as the reviewer before merge.
- Never call claude or codex from a worker.
- Prefer small commits.
  Reviewed by Claude as the reviewer.
  Keep the ticket id.
`)
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{path}})
	if _, ok := findText(got, "Use Claude as the reviewer before merge."); !ok {
		t.Fatal("reviewer route was discarded")
	}
	kept, ok := findText(got, "Never call claude or codex from a worker.")
	if !ok || kept.Strength != StrengthNormal {
		t.Fatal("tool prohibition was dropped")
	}
	plain, ok := findText(got, "Prefer small commits.")
	if !ok {
		t.Fatal("missing plain rule")
	}
	if !strings.Contains(plain.Details, "Claude") {
		t.Fatal("model hint discarded source detail")
	}
	if !strings.Contains(plain.Details, "Keep the ticket id.") {
		t.Fatalf("detail discarded: %q", plain.Details)
	}
	if !hasUnresolved(got, "stale_model_or_review_name", 0) {
		t.Fatal("missing stale-name report")
	}
	if got.Adapter.Ready {
		t.Fatal("unresolved routing must block apply")
	}
}

func TestRolePackAndSecretsFilename(t *testing.T) {
	dir := t.TempDir()
	role := writeDoc(t, dir, "AGENTS-AGENT-BUILDER.md", `
## Limits

- Stay inside the ticket.
`)
	pack := writeDoc(t, dir, "AGENTS-DOMAIN-SECRETS.md", "- Keep the pack on demand.\n  "+strings.Repeat("d", 20000)+"\n")
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{role, pack}})
	builder, ok := findText(got, "Stay inside the ticket.")
	if !ok || builder.Layer != LayerAgent || strings.Join(builder.Roles, ",") != "builder" {
		t.Fatalf("role rule %+v", builder)
	}
	secret, ok := findText(got, "Keep the pack on demand.")
	if !ok || secret.Placement != PlacementOnDemand || len(secret.Details) != 20000 {
		t.Fatalf("pack placement %s details %d", secret.Placement, len(secret.Details))
	}
	doc := alwaysOnDocument(got.Rules)
	if strings.Contains(doc, strings.Repeat("d", 80)) || strings.Contains(doc, "on-demand:") || !strings.Contains(doc, "Stay inside the ticket.") || !strings.Contains(doc, "Keep the pack on demand.") || !strings.Contains(doc, "[import-") {
		t.Fatalf("session projection %q", doc)
	}
	if !got.AlwaysOn.Insert || got.AlwaysOn.Bytes != len(doc) || got.AlwaysOn.Bytes > AlwaysOnBudget {
		t.Fatalf("always-on %+v doc %d", got.AlwaysOn, len(doc))
	}
	if secret.Details != strings.Repeat("d", 20000) {
		t.Fatal("pack details were truncated or altered")
	}
}

func TestAlwaysOnBudgetRefusesWithoutTruncation(t *testing.T) {
	id := "import-" + strings.Repeat("a", 64)
	overhead := len(sessionHeader) + len(sessionRuleLine(id, ""))
	dir := t.TempDir()
	fit := writeDoc(t, dir, "fit/AGENTS-KERNEL.md", "- "+strings.Repeat("x", AlwaysOnBudget-overhead)+"\n")
	over := writeDoc(t, dir, "over/AGENTS-KERNEL.md", "- "+strings.Repeat("x", AlwaysOnBudget-overhead+1)+"\n")
	wide := writeDoc(t, dir, "wide/AGENTS-KERNEL.md", "- "+strings.Repeat("ä", 6000)+"\n")

	fitted := mustBuild(t, Request{Context: ContextTemplate, Files: []string{fit}})
	if !fitted.AlwaysOn.Insert || fitted.AlwaysOn.Bytes != AlwaysOnBudget {
		t.Fatalf("fit %+v", fitted.AlwaysOn)
	}
	if len(fitted.Rules[0].Text) != AlwaysOnBudget-overhead {
		t.Fatalf("fit text %d", len(fitted.Rules[0].Text))
	}

	overflow := mustBuild(t, Request{Context: ContextTemplate, Files: []string{over}})
	if overflow.AlwaysOn.Insert || overflow.AlwaysOn.Bytes <= AlwaysOnBudget {
		t.Fatalf("overflow %+v", overflow.AlwaysOn)
	}
	if !strings.Contains(overflow.AlwaysOn.Explanation, "not truncated") {
		t.Fatalf("explanation %q", overflow.AlwaysOn.Explanation)
	}
	if len(overflow.Rules[0].Text) != AlwaysOnBudget-overhead+1 || overflow.Rules[0].AlwaysOnEligible {
		t.Fatalf("text was trimmed: %d eligible %v", len(overflow.Rules[0].Text), overflow.Rules[0].AlwaysOnEligible)
	}

	unicode := mustBuild(t, Request{Context: ContextTemplate, Files: []string{wide}})
	text := unicode.Rules[0].Text
	if utf8.RuneCountInString(text) != 6000 || len(text) != 12000 {
		t.Fatalf("unicode text runes=%d bytes=%d", utf8.RuneCountInString(text), len(text))
	}
	if unicode.AlwaysOn.Insert || unicode.AlwaysOn.Bytes <= len(text) {
		t.Fatalf("unicode budget %+v", unicode.AlwaysOn)
	}
}

func TestProhibitedSymlinkBoundsAndMixedContext(t *testing.T) {
	dir := t.TempDir()
	secret := "SECRET-BODY-SHOULD-NOT-SURFACE"
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Build(context.Background(), Request{Context: ContextTemplate, Files: []string{envPath}})
	if !errors.Is(err, ErrProhibitedPath) || strings.Contains(err.Error(), secret) {
		t.Fatalf("env: %v", err)
	}

	hidden := filepath.Join(dir, ".inspr")
	if err := os.Mkdir(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	hiddenFile := writeDoc(t, hidden, "AGENTS-KERNEL.md", "- Prefer small commits.\n")
	_, err = Build(context.Background(), Request{Context: ContextTemplate, Files: []string{hiddenFile}})
	if !errors.Is(err, ErrProhibitedPath) {
		t.Fatalf("inspr: %v", err)
	}

	target := writeDoc(t, dir, "target/AGENTS-KERNEL.md", "- "+secret+"\n")
	link := filepath.Join(dir, "AGENTS-KERNEL.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err = Build(context.Background(), Request{Context: ContextTemplate, Files: []string{link}})
	if !errors.Is(err, ErrSymlink) || strings.Contains(err.Error(), secret) {
		t.Fatalf("symlink: %v", err)
	}

	huge := filepath.Join(dir, "huge/AGENTS-KERNEL.md")
	if err := os.MkdirAll(filepath.Dir(huge), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = Build(context.Background(), Request{Context: ContextTemplate, Files: []string{huge}})
	if !errors.Is(err, ErrByteBound) {
		t.Fatalf("bound: %v", err)
	}

	binary := writeDoc(t, dir, "bin/AGENTS-KERNEL.md", "ok")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, 0xff)
	if err := os.WriteFile(binary, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Build(context.Background(), Request{Context: ContextTemplate, Files: []string{binary}})
	if !errors.Is(err, ErrNotText) {
		t.Fatalf("binary: %v", err)
	}

	directory := filepath.Join(dir, "directory", "AGENTS.md")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), Request{Context: ContextTemplate, Files: []string{directory}}); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("directory: %v", err)
	}

	kernel := writeDoc(t, dir, "kernel/AGENTS-KERNEL.md", "- Prefer small commits.\n")
	profile := writeDoc(t, dir, "person/AGENTS-PROFILE.md", "- Prefer short replies.\n")
	_, err = Build(context.Background(), Request{Context: ContextTemplate, Files: []string{kernel, profile}})
	if !errors.Is(err, ErrMixedContext) {
		t.Fatalf("person mix: %v", err)
	}
	person := mustBuild(t, Request{Context: ContextPerson, Files: []string{profile}})
	if len(person.Rules) != 1 || person.Rules[0].Layer != LayerPerson {
		t.Fatalf("profile %+v", person.Rules)
	}

	private := writeDoc(t, dir, "doctrine-private/AGENTS-KERNEL.md", "- Keep private kernel rules here.\n")
	_, err = Build(context.Background(), Request{Context: ContextTemplate, Files: []string{kernel, private}})
	if !errors.Is(err, ErrMixedContext) {
		t.Fatalf("private mix: %v", err)
	}
	priv := mustBuild(t, Request{Context: ContextPrivate, Files: []string{private}})
	if priv.Rules[0].Layer != LayerCompany || priv.Files[0].Trust != ContextPrivate {
		t.Fatalf("private file %+v", priv.Files[0])
	}

	mixed := writeDoc(t, dir, "repo/AGENTS.md", `
## Git

- Keep commits small.

## Personal

- Prefer short replies.
`)
	_, err = Build(context.Background(), Request{Context: ContextProject, Files: []string{mixed}})
	if !errors.Is(err, ErrMixedContext) {
		t.Fatalf("section mix: %v", err)
	}
	onlyPerson := mustBuild(t, Request{Context: ContextPerson, Section: SectionPersonal, Files: []string{mixed}})
	if len(onlyPerson.Rules) != 1 || onlyPerson.Rules[0].Text != "Prefer short replies." || onlyPerson.Rules[0].Layer != LayerPerson {
		t.Fatalf("personal section %+v", onlyPerson.Rules)
	}
	onlyRepo := mustBuild(t, Request{Context: ContextProject, Section: SectionKernel, Files: []string{mixed}})
	if len(onlyRepo.Rules) != 1 || onlyRepo.Rules[0].Text != "Keep commits small." || onlyRepo.Rules[0].Layer != LayerProject {
		t.Fatalf("kernel section %+v", onlyRepo.Rules)
	}
}

func TestPrivatePathBoundaryAndCanonicalFiles(t *testing.T) {
	const innocuous = "- Keep the synthetic boundary check.\n"
	annotated := "<!-- aeon-context: template -->\n" + innocuous
	sectioned := annotated + "\n## Kernel\n\n- Keep the synthetic kernel line.\n\n## Other\n\n- Keep the synthetic other line.\n"
	dir := t.TempDir()

	for _, rel := range []string{
		"doctrine-private",
		"Doctrine-Private",
		"DOCTRINE-PRIVATE",
		"inspr-doctrine-private",
		"Inspr-Doctrine-Private",
		"INSPR-DOCTRINE-PRIVATE",
		"vendor/inspr-doctrine-private/docs",
	} {
		t.Run("dir/"+rel, func(t *testing.T) {
			path := writeDoc(t, dir, filepath.Join(rel, "AGENTS-DOMAIN-DEV.md"), innocuous)
			assertTemplateRefused(t, path, SectionAll)
			assertTemplateRefused(t, path, SectionKernel)
			assertTemplateRefused(t, path, SectionPersonal)
			got := mustBuild(t, Request{Context: ContextPrivate, Files: []string{path}})
			file := got.Files[0]
			if file.Kind != "domain" || file.Layer != LayerCompany || file.Trust != ContextPrivate || file.Placement != PlacementOnDemand {
				t.Fatalf("private domain file %+v", file)
			}
			if len(got.Rules) != 1 || got.Rules[0].Text != "Keep the synthetic boundary check." || got.Rules[0].Layer != LayerCompany || got.Rules[0].Placement != PlacementOnDemand {
				t.Fatalf("private domain rule %+v", got.Rules)
			}
		})
	}

	for _, root := range []string{"doctrine-private", "inspr-doctrine-private"} {
		t.Run("annotation/"+root, func(t *testing.T) {
			domain := writeDoc(t, dir, filepath.Join(root, "annotated", "AGENTS-DOMAIN-OPS.md"), sectioned)
			for _, section := range []string{SectionAll, SectionKernel, SectionPersonal} {
				assertTemplateRefused(t, domain, section)
			}
			repo := writeDoc(t, dir, filepath.Join(root, "annotated", "AGENTS.md"), annotated)
			for _, section := range []string{SectionAll, SectionKernel, SectionPersonal} {
				assertTemplateRefused(t, repo, section)
			}
			local := mustBuild(t, Request{Context: ContextPrivate, Files: []string{domain}})
			if local.Files[0].Trust != ContextPrivate || local.Files[0].Layer != LayerCompany || len(local.Rules) != 3 {
				t.Fatalf("annotated private domain %+v rules %d", local.Files[0], len(local.Rules))
			}
			for _, rule := range local.Rules {
				if rule.Layer != LayerCompany || rule.Placement != PlacementOnDemand {
					t.Fatalf("annotated private rule %+v", rule)
				}
			}
		})
	}

	for _, rel := range []string{
		"private",
		"Private",
		"doctrine-private-notes",
		"not-doctrine-private",
		"pre-doctrine-private",
		"inspr-doctrine",
		"inspr-doctrine-private-backup",
		"not-inspr-doctrine-private",
		"my-inspr-doctrine-private",
		"docs/doctrine-private.md",
	} {
		t.Run("unrelated/"+rel, func(t *testing.T) {
			path := writeDoc(t, dir, filepath.Join(rel, "AGENTS-DOMAIN-DEV.md"), innocuous)
			got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{path}})
			file := got.Files[0]
			if file.Kind != "domain" || file.Layer != LayerCompany || file.Trust != ContextTemplate || file.Placement != PlacementOnDemand {
				t.Fatalf("unrelated directory became private: %+v", file)
			}
			if len(got.Rules) != 1 || got.Rules[0].Identity == "" || !strings.HasPrefix(got.Rules[0].Identity, "company/preamble/-/t-") {
				t.Fatalf("unrelated identity %+v", got.Rules)
			}
		})
	}

	core := writeDoc(t, dir, "canonical/AGENTS-CORE-PRIVATE.md", innocuous)
	coreAnnotated := writeDoc(t, dir, "canonical/annotated/AGENTS-CORE-PRIVATE.md", sectioned)
	for _, path := range []string{core, coreAnnotated} {
		assertTemplateRefused(t, path, SectionAll)
		assertTemplateRefused(t, path, SectionKernel)
		assertTemplateRefused(t, path, SectionPersonal)
	}
	corePlan := mustBuild(t, Request{Context: ContextPrivate, Files: []string{core}})
	if corePlan.Files[0].Kind != "core" || corePlan.Files[0].Layer != LayerCompany || corePlan.Files[0].Trust != ContextPrivate || corePlan.Files[0].Placement != PlacementOnDemand {
		t.Fatalf("private core %+v", corePlan.Files[0])
	}
	publicCore := mustBuild(t, Request{Context: ContextTemplate, Files: []string{writeDoc(t, dir, "canonical/AGENTS-CORE.md", innocuous)}})
	if publicCore.Files[0].Kind != "core" || publicCore.Files[0].Trust != ContextTemplate || publicCore.Files[0].Placement != PlacementOnDemand {
		t.Fatalf("public core %+v", publicCore.Files[0])
	}
	if publicCore.Rules[0].ID != corePlan.Rules[0].ID || publicCore.Rules[0].Identity != corePlan.Rules[0].Identity || publicCore.PlanID == corePlan.PlanID {
		t.Fatalf("core identity public %s/%s private %s/%s", publicCore.Rules[0].Identity, publicCore.PlanID, corePlan.Rules[0].Identity, corePlan.PlanID)
	}

	version := writeDoc(t, dir, "canonical/AGENTS-VERSIONING.md", innocuous)
	versionPlan := mustBuild(t, Request{Context: ContextTemplate, Files: []string{version}})
	if versionPlan.Files[0].Kind != "versioning" || versionPlan.Files[0].Layer != LayerCompany || versionPlan.Files[0].Trust != ContextTemplate || versionPlan.Files[0].Placement != PlacementOnDemand {
		t.Fatalf("versioning %+v", versionPlan.Files[0])
	}
	if versionPlan.Rules[0].Layer != LayerCompany || versionPlan.Rules[0].Placement != PlacementOnDemand || !strings.HasPrefix(versionPlan.Rules[0].Identity, "company/preamble/-/t-") {
		t.Fatalf("versioning rule %+v", versionPlan.Rules[0])
	}
	versionPrivate := writeDoc(t, dir, "inspr-doctrine-private/docs/AGENTS-VERSIONING.md", innocuous)
	assertTemplateRefused(t, versionPrivate, SectionKernel)
	versionLocal := mustBuild(t, Request{Context: ContextPrivate, Files: []string{versionPrivate}})
	if versionLocal.Files[0].Kind != "versioning" || versionLocal.Files[0].Layer != LayerCompany || versionLocal.Files[0].Trust != ContextPrivate || versionLocal.Files[0].Placement != PlacementOnDemand {
		t.Fatalf("private versioning %+v", versionLocal.Files[0])
	}

	marked := writeDoc(t, dir, "public/AGENTS-DOMAIN-DEV.md", "- A synthetic doctrine-private mention stays guarded.\n")
	assertTemplateRefused(t, marked, SectionAll)
	personal := writeDoc(t, dir, "public/AGENTS-DOMAIN-OPS.md", "- Reach synthetic@example.invalid for this fixture.\n")
	assertTemplateRefused(t, personal, SectionKernel)

	for _, name := range []string{"README.md", "agents-versioning.md", "AGENTS-FOO.md"} {
		path := writeDoc(t, dir, filepath.Join("unknown", name), innocuous)
		_, err := Build(context.Background(), Request{Context: ContextTemplate, Files: []string{path}})
		if !errors.Is(err, ErrUnrecognizedFile) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestSupportedDoctrineIdentitiesStayStable(t *testing.T) {
	const body = "- Keep commits small.\n"
	dir := t.TempDir()
	type want struct {
		name, kind, placement, role string
		layer                       Layer
		trust, plan                 TrustContext
	}
	cases := []want{
		{"AGENTS-KERNEL.md", "kernel", PlacementAlwaysOn, "", LayerCompany, ContextTemplate, ContextTemplate},
		{"AGENTS-KERNEL-PRIVATE.md", "kernel", PlacementAlwaysOn, "", LayerCompany, ContextPrivate, ContextPrivate},
		{"AGENTS-CORE.md", "core", PlacementOnDemand, "", LayerCompany, ContextTemplate, ContextTemplate},
		{"AGENTS-DOMAIN-DEV.md", "domain", PlacementOnDemand, "", LayerCompany, ContextTemplate, ContextTemplate},
		{"AGENTS-AGENT-SYSOP.md", "role", PlacementAlwaysOn, "sysop", LayerAgent, ContextTemplate, ContextTemplate},
		{"AGENTS-PROFILE.md", "profile", PlacementOnDemand, "", LayerPerson, ContextPerson, ContextPerson},
		{"AGENTS.md", "repo", PlacementAlwaysOn, "", LayerProject, ContextProject, ContextProject},
		{"CLAUDE.md", "claude", PlacementAlwaysOn, "", LayerProject, ContextProject, ContextProject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeDoc(t, dir, filepath.Join("stable", tc.name), body)
			got := mustBuild(t, Request{Context: tc.plan, Files: []string{path}})
			file := got.Files[0]
			if file.Kind != tc.kind || file.Layer != tc.layer || file.Trust != tc.trust || file.Placement != tc.placement || file.Role != tc.role {
				t.Fatalf("file %+v", file)
			}
			again := mustBuild(t, Request{Context: tc.plan, Files: []string{path}})
			if again.PlanID != got.PlanID || again.Rules[0].ID != got.Rules[0].ID || again.Rules[0].Identity != got.Rules[0].Identity {
				t.Fatal("replay changed a supported identity")
			}
		})
	}

	explicit := "# Synthetic kernel\n\n## Git\n\n<!-- aeon-rule: git.no-force -->\n- 🔴 Never force-push the default branch.\n  Why: shared history.\n"
	path := writeDoc(t, dir, "stable/explicit/AGENTS-KERNEL.md", explicit)
	got := mustBuild(t, Request{Context: ContextTemplate, Files: []string{path}})
	if got.Rules[0].Identity != "company/git/-/git.no-force" || got.Rules[0].ExplicitID != "git.no-force" {
		t.Fatalf("explicit identity %s", got.Rules[0].Identity)
	}
}

func assertTemplateRefused(t *testing.T, path, section string) {
	t.Helper()
	var out bytes.Buffer
	err := Run(context.Background(), Options{Request: Request{Context: ContextTemplate, Section: section, Files: []string{path}}}, &out, nil)
	if !errors.Is(err, ErrMixedContext) || out.Len() != 0 {
		t.Fatalf("template section %s: %v out=%q", section, err, out.String())
	}
	if strings.Contains(err.Error(), "synthetic") || strings.Contains(out.String(), "synthetic") {
		t.Fatal("public-template refusal leaked fixture text")
	}
}

func writeDoc(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "#") && !strings.HasPrefix(body, "-") && !strings.HasPrefix(body, "\n") && !strings.HasPrefix(body, "{") {
		body = strings.TrimPrefix(body, "\n")
	}
	if strings.HasPrefix(body, "\n") {
		body = strings.TrimPrefix(body, "\n")
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustBuild(t *testing.T, in Request) Proposal {
	t.Helper()
	got, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func findText(p Proposal, text string) (Rule, bool) {
	for _, rule := range p.Rules {
		if rule.Text == text {
			return rule, true
		}
	}
	return Rule{}, false
}

func hasUnresolved(p Proposal, kind string, line int) bool {
	for _, item := range p.Unresolved {
		if item.Kind == kind && (line == 0 || item.Line == line) {
			return true
		}
	}
	return false
}

func dirEntries(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}
