// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRawSourceHashBOMCRLF(t *testing.T) {
	plain := "## Git\n- Keep commits small.\n  Why: easy to review.\n"
	raw := "\ufeff" + strings.ReplaceAll(plain, "\n", "\r\n")
	p := writeDoc(t, t.TempDir(), "AGENTS.md", raw)
	got := mustBuild(t, Request{Context: ContextProject, Files: []string{p}})
	if got.Files[0].SHA256 != digest([]byte(raw)) || got.Files[0].Bytes != len(raw) {
		t.Fatal("raw provenance changed")
	}
	r := got.Rules[0]
	if r.Sources[0].FileSHA256 != digest([]byte(raw)) || r.Sources[0].StartLine != 2 || r.Sources[0].EndLine != 3 || r.Sources[0].SHA256 != digest([]byte("- Keep commits small.\n  Why: easy to review.\n")) {
		t.Fatal("normalized line coordinates/raw file hash differ")
	}
	other := mustBuild(t, Request{Context: ContextProject, Files: []string{writeDoc(t, t.TempDir(), "AGENTS.md", plain)}})
	if other.PlanID == got.PlanID || other.Rules[0].ID != r.ID {
		t.Fatal("plan must reflect raw bytes while rule identity reflects parsed content")
	}
}

func TestPrivateMarkersAndAmbiguousTemplates(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "AGENTS-KERNEL.md"} {
		for _, marker := range []string{"<!-- PRIVATE KERNEL -->", "# Personal working agreements", "- **User**: Synthetic Person", "# Personal details", "# Contact\n- synthetic@example.invalid"} {
			t.Run(name+marker[:4], func(t *testing.T) {
				body := "<!-- aeon-context: template -->\n" + marker + "\n- PRIVATE-SYNTHETIC-CONTENT\n"
				path := writeDoc(t, t.TempDir(), name, body)
				var out bytes.Buffer
				err := Run(context.Background(), Options{Request: Request{Context: ContextTemplate, Files: []string{path}}}, &out, nil)
				if !errors.Is(err, ErrMixedContext) || out.Len() != 0 || strings.Contains(err.Error(), "PRIVATE-SYNTHETIC-CONTENT") {
					t.Fatal("private template output/error leaked")
				}
				local := mustBuild(t, Request{Context: ContextPrivate, Files: []string{path}})
				if len(local.Rules) == 0 {
					t.Fatal("private local proposal lost")
				}
			})
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := writeDoc(t, t.TempDir(), name, "- Keep commits small.\n  Why: easy to review.\n")
		_, err := Build(context.Background(), Request{Context: ContextTemplate, Files: []string{path}})
		if !errors.Is(err, ErrMixedContext) {
			t.Fatal("filename alone made public template")
		}
		if len(mustBuild(t, Request{Context: ContextProject, Files: []string{path}}).Rules) != 1 {
			t.Fatal("explicit local context refused")
		}
	}
}

func TestSafetyProhibitionWithReviewKeywordsIsRetained(t *testing.T) {
	body := "- 🔴 Never call Claude for review without owner approval.\n  Why: preserve the gate.\n  Do not ask Codex to review itself.\n"
	p := mustBuild(t, Request{Context: ContextTemplate, Files: []string{writeDoc(t, t.TempDir(), "AGENTS-KERNEL.md", body)}})
	if len(p.Rules) != 1 || p.Rules[0].Strength != StrengthLocked || !strings.Contains(p.Rules[0].Details, "Do not ask Codex") || len(p.Unresolved) != 2 {
		t.Fatal("safety content or routing hints lost")
	}
	if p.Rules[0].Sources[0].EndLine != 3 || p.Rules[0].Sources[0].FileSHA256 != digest([]byte(body)) {
		t.Fatal("source lineage incomplete")
	}
	b, _ := json.Marshal(p)
	if bytes.Contains(b, []byte("owner_policy")) || bytes.Contains(b, []byte("2026-09-28")) {
		t.Fatal("date-bound owner policy embedded")
	}
}

func TestDraftMappingRetainsFieldsAndDuplicateLineage(t *testing.T) {
	body := "## Workflow\n<!-- aeon-rule: workflow.check -->\n- Keep the scoped checks.\n  Why: changes need evidence.\n  Details: Full detail.\n  roles: builder\n  harnesses: codex, claude-code\n  expires: 2027-01-01T12:30:00+02:00\n  source: AEON-250\n"
	a := writeDoc(t, t.TempDir(), "AGENTS.md", body)
	b := writeDoc(t, t.TempDir(), "AGENTS.md", body)
	p := mustBuild(t, Request{Context: ContextProject, Files: []string{a, b}})
	mapped, err := MapDraft(p)
	if err != nil {
		t.Fatal(err)
	}
	r := mapped[0]
	if len(mapped) != 1 || r.Text != p.Rules[0].Text || r.Why != p.Rules[0].Why || r.Strength != "normal" || !r.Enabled || r.Source.Reference != "AEON-250" || r.Source.Identity != "workflow.check" || r.Source.EditedHere || r.ExpiresAt.Format("2006-01-02T15:04:05Z07:00") != "2027-01-01T12:30:00+02:00" || strings.Join(r.Roles, ",") != "builder" || strings.Join(r.Harnesses, ",") != "claude-code,codex" {
		t.Fatal("wire fields changed")
	}
	if r.Identity != "import-"+digest([]byte(p.Rules[0].Identity)) || !strings.HasPrefix(r.Details, "Full detail.\n\n[aeon doctrine lineage]\n") {
		t.Fatal("identity/details lost")
	}
	_, raw, _ := strings.Cut(r.Details, "[aeon doctrine lineage]\n")
	var lineage draftLineage
	if err := json.Unmarshal([]byte(raw), &lineage); err != nil {
		t.Fatal(err)
	}
	if len(lineage.Sources) != 2 || lineage.Identity != p.Rules[0].Identity || lineage.Sources[0].FileSHA256 != digest([]byte(body)) || r.Source.Revision != digest([]byte(raw)) {
		t.Fatal("duplicate lineage lost")
	}
}

func TestUnrepresentableDraftKeepsPlan(t *testing.T) {
	for _, tc := range []struct{ name, body, filename string }{
		{"missing why", "- Keep the checks.\n", "AGENTS.md"},
		{"date expiry", "- Keep the checks.\n  Why: evidence.\n  expires: 2027-01-01\n", "AGENTS.md"},
		{"locked expiry", "- 🔴 Keep the checks.\n  Why: evidence.\n  expires: 2027-01-01T00:00:00Z\n", "AGENTS.md"},
		{"locked off", "- 🔴 [off] Keep the checks.\n  Why: evidence.\n", "AGENTS.md"},
		{"harness", "- Keep the checks.\n  Why: evidence.\n  harness: claude\n", "AGENTS.md"},
		{"role", "- Keep the checks.\n  Why: evidence.\n  roles: sysop\n", "AGENTS.md"},
		{"text bound", "- " + strings.Repeat("x", 513) + "\n  Why: evidence.\n", "AGENTS.md"},
		{"details bound", "- Keep the checks.\n  Why: evidence.\n  " + strings.Repeat("d", 16384) + "\n", "AGENTS.md"},
		{"on demand", "- Keep the checks.\n  Why: evidence.\n", "AGENTS-DOMAIN-DEV.md"},
		{"two sets", "## A\n- Keep A.\n  Why: evidence.\n## B\n- Keep B.\n  Why: evidence.\n", "AGENTS.md"},
		{"contradiction", "## A\n<!-- aeon-rule: keep -->\n- Keep A.\n  Why: evidence.\n\n## A\n<!-- aeon-rule: keep -->\n- Keep B.\n  Why: other evidence.\n", "AGENTS.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trust := ContextProject
			if tc.filename != "AGENTS.md" {
				trust = ContextTemplate
			}
			p := mustBuild(t, Request{Context: trust, Files: []string{writeDoc(t, t.TempDir(), tc.filename, tc.body)}})
			before, _ := json.Marshal(p)
			if _, err := MapDraft(p); !errors.Is(err, ErrDraftUnavailable) {
				t.Fatalf("expected explicit refusal: %v", err)
			}
			after, _ := json.Marshal(p)
			if !bytes.Equal(before, after) || p.Adapter.Ready {
				t.Fatal("refusal changed local plan")
			}
		})
	}
}

func TestDraftEnabledAndLockedAreExplicit(t *testing.T) {
	body := "## Checks\n- 🔴 Keep the safety check.\n  Why: safety floor.\n- [off] Optional check.\n  Why: intentionally disabled.\n"
	p := mustBuild(t, Request{Context: ContextProject, Files: []string{writeDoc(t, t.TempDir(), "AGENTS.md", body)}})
	mapped, err := MapDraft(p)
	if err != nil {
		t.Fatal(err)
	}
	foundLocked, foundOff := false, false
	for _, r := range mapped {
		if r.Strength == StrengthLocked {
			foundLocked = r.Enabled && r.ExpiresAt == nil
		}
		if strings.Contains(r.Text, "[off]") {
			foundOff = !r.Enabled && r.Strength == StrengthNormal
		}
	}
	if !foundLocked || !foundOff {
		t.Fatal("explicit enabled/locked fields lost")
	}
}
