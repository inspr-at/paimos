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

func TestOwnerPolicyDropsStaleReviewNames(t *testing.T) {
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
	if _, ok := findText(got, "Use Claude as the reviewer before merge."); ok {
		t.Fatal("stale reviewer route was imported")
	}
	kept, ok := findText(got, "Never call claude or codex from a worker.")
	if !ok || kept.Strength != StrengthNormal {
		t.Fatal("tool prohibition was dropped")
	}
	plain, ok := findText(got, "Prefer small commits.")
	if !ok {
		t.Fatal("missing plain rule")
	}
	if strings.Contains(plain.Details, "Claude") {
		t.Fatalf("stale detail kept in rule: %q", plain.Details)
	}
	if !strings.Contains(plain.Details, "Keep the ticket id.") {
		t.Fatalf("detail discarded: %q", plain.Details)
	}
	if !hasUnresolved(got, "stale_model_or_review_name", 0) {
		t.Fatal("missing stale-name report")
	}
	if !strings.Contains(got.OwnerPolicy, "2026-09-28") {
		t.Fatal("owner policy missing from proposal")
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
	want := len("on-demand: AGENTS-DOMAIN-SECRETS.md (1 rules)\n") + len("## limits\n- Stay inside the ticket.\n")
	if !got.AlwaysOn.Insert || got.AlwaysOn.Bytes != want {
		t.Fatalf("always-on %+v want %d", got.AlwaysOn, want)
	}
	if secret.Details != strings.Repeat("d", 20000) {
		t.Fatal("pack details were truncated or altered")
	}
}

func TestAlwaysOnBudgetRefusesWithoutTruncation(t *testing.T) {
	overhead := len("## preamble\n- \n")
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

	if _, err := Build(context.Background(), Request{Context: ContextTemplate, Files: []string{dir}}); !errors.Is(err, ErrNotRegular) {
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

func TestCLIPreviewApplyAndAdapterGap(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "AGENTS-KERNEL.md", "- Prefer small commits.\n")
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"--context", "template", "--file", path}, &out); err != nil {
		t.Fatal(err)
	}
	var preview Proposal
	if err := json.Unmarshal(out.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" || preview.Adapter.Ready {
		t.Fatalf("preview adapter %+v", preview.Adapter)
	}
	if len(preview.Rules) != 1 {
		t.Fatalf("rules %d", len(preview.Rules))
	}

	before := dirEntries(t, dir)
	out.Reset()
	err := Run(context.Background(), []string{"apply", "--context", "template", "--tenant", "synthetic", "--file", path}, &out)
	if !errors.Is(err, ErrDraftUnavailable) {
		t.Fatalf("apply: %v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" {
		t.Fatalf("apply printed mode %s", preview.Mode)
	}
	if strings.Join(dirEntries(t, dir), ",") != strings.Join(before, ",") {
		t.Fatal("apply wrote a file")
	}

	if err := Run(context.Background(), []string{"preview", "--publish", "--context", "template", "--file", path}, &out); !errors.Is(err, ErrPublishRefused) {
		t.Fatalf("publish: %v", err)
	}
	if err := Run(context.Background(), []string{"preview", "--context", "template", "--file", path, "--version", "260909113550.0.1"}, &out); !errors.Is(err, ErrPublishRefused) || !strings.Contains(err.Error(), "inspr-calendar-v2") {
		t.Fatalf("bad version: %v", err)
	}
	if err := Run(context.Background(), []string{"preview", "--context", "template", "--file", path, "--version", "260909113550.0.0"}, &out); !errors.Is(err, ErrPublishRefused) || !strings.Contains(err.Error(), "does not publish") {
		t.Fatalf("valid version: %v", err)
	}

	sentinel := "DRAFT-SENTINEL-NOT-A-CONTRACT"
	draft := writeDoc(t, dir, "ar1-api.json", `{"note":"`+sentinel+`"}`+"\n")
	out.Reset()
	if err := Run(context.Background(), []string{"preview", "--context", "template", "--file", path, "--ar1-draft", draft}, &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte(sentinel)) {
		t.Fatal("draft body was copied into the proposal")
	}
	if err := json.Unmarshal(out.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Adapter.Ready || preview.Adapter.DraftSHA == "" || preview.Adapter.DraftBytes == 0 {
		t.Fatalf("adapter %+v", preview.Adapter)
	}
	sum := sha256.Sum256([]byte(`{"note":"` + sentinel + `"}` + "\n"))
	if preview.Adapter.DraftSHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("draft sha %s", preview.Adapter.DraftSHA)
	}

	writer := &fakeWriter{}
	sample := preview
	if err := ImportDraft(context.Background(), DraftRequest{Tenant: "synthetic", Context: ContextTemplate, Authorized: false, Proposal: sample}, writer); !errors.Is(err, ErrDraftUnavailable) || writer.called != 0 {
		t.Fatalf("unauthorized %v called %d", err, writer.called)
	}
	if err := ImportDraft(context.Background(), DraftRequest{Tenant: "synthetic", Context: ContextTemplate, Authorized: true, Publish: true, Proposal: sample}, writer); !errors.Is(err, ErrPublishRefused) || writer.called != 0 {
		t.Fatalf("publish port %v", err)
	}
	if err := ImportDraft(context.Background(), DraftRequest{Tenant: "synthetic", Context: ContextTemplate, Version: "260909113550.0.0", Authorized: true, Proposal: sample}, writer); !errors.Is(err, ErrPublishRefused) || writer.called != 0 {
		t.Fatalf("version port %v", err)
	}
	if err := ImportDraft(context.Background(), DraftRequest{Tenant: "synthetic", Context: ContextTemplate, Authorized: true, Proposal: sample}, writer); err != nil {
		t.Fatal(err)
	}
	if writer.called != 1 || writer.req.Proposal.Mode != "draft" || writer.req.Publish || writer.req.Tenant != "synthetic" || writer.req.Context != ContextTemplate {
		t.Fatalf("draft %+v", writer.req)
	}
}

type fakeWriter struct {
	called int
	req    DraftRequest
}

func (f *fakeWriter) WriteDraft(ctx context.Context, req DraftRequest) error {
	f.called++
	f.req = req
	return nil
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
