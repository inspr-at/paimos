// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/markdownsource/testfixture"
)

func TestDeliveryRequiresCompleteActiveInstruction(t *testing.T) {
	release := PinnedRelease{Repository: "org/repo", Commit: strings.Repeat("a", 40), State: "ready", Rules: []PinnedRule{{Identity: "org/repo/kernel#safe", Text: "Preserve safety."}}}
	cases := map[string]string{
		"comment":           "<!-- Preserve safety. -->",
		"multiline comment": "<!--\n- Preserve safety.\n-->",
		"qualified":         "- Optionally: Preserve safety.",
		"suffix":            "- Preserve safety. Unless inconvenient.",
		"quote":             "> Preserve safety.",
	}
	for name, block := range testfixture.Blocks() {
		cases[name] = strings.ReplaceAll(block, "Example instruction.", "Preserve safety.")
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if status, _ := DeliveryReport([]HarnessFile{{Harness: "codex", Text: body}}, []PinnedRelease{release}, nil); status != "fail" {
				t.Fatal("non-instruction certified as matching doctrine")
			}
			if doubles := sessionDoubles(HarnessFile{Harness: "codex", Session: true, Text: "# Aeon session rules\n\n" + body}, []PinnedRelease{release}); len(doubles) != 0 {
				t.Fatalf("non-instruction certified as duplicate: %v", doubles)
			}
		})
	}
}

func TestDeliveryReportOneChannel(t *testing.T) {
	const (
		identity = "inspr-at/fixture-doctrine/docs/AGENTS-KERNEL.md#secrets"
		text     = "Never print the environment."
	)
	release := PinnedRelease{
		State: "ready", Repository: "inspr-at/fixture-doctrine", Ref: "v260922101217.0.0", Commit: strings.Repeat("ab", 20),
		Rules: []PinnedRule{{Identity: identity, Key: "no-env-dump", Text: text}},
	}
	matched := "# Kernel\n\n## Secrets\n\n<!-- aeon-rule: no-env-dump -->\n- Never print the environment.\n"
	status, detail := DeliveryReport([]HarnessFile{{Harness: "claude", Text: matched}}, []PinnedRelease{release}, nil)
	if status != "ok" || detail != "no rule served twice" {
		t.Fatalf("clean %s %q", status, detail)
	}
	drifted := "# Kernel\n\n## Secrets\n\n<!-- aeon-rule: no-env-dump -->\n- Sometimes print the environment.\n"
	status, detail = DeliveryReport([]HarnessFile{{Harness: "claude", Text: drifted}}, []PinnedRelease{release}, nil)
	if status != "fail" || !strings.Contains(detail, "harness file drifted from inspr-at/fixture-doctrine@v260922101217.0.0") || !strings.Contains(detail, identity) || strings.Contains(detail, "Sometimes print") {
		t.Fatalf("drift %s %q", status, detail)
	}
	doubled := "# Aeon session rules\n\n- [copied] Never print the environment.\n"
	status, detail = DeliveryReport([]HarnessFile{{Harness: "claude", Text: doubled}}, []PinnedRelease{release}, nil)
	if status != "fail" || !strings.Contains(detail, "rule served twice") || !strings.Contains(detail, "Propose a change") || strings.Contains(detail, "no rule served twice") {
		t.Fatalf("session double %s %q", status, detail)
	}
	status, detail = DeliveryReport([]HarnessFile{{Harness: "codex", Text: matched}}, []PinnedRelease{release}, []ServedDuplicate{{Identity: "copied", Doctrine: identity}})
	if status != "fail" || !strings.Contains(detail, "copied duplicates "+identity) || !strings.Contains(detail, "Propose a change") {
		t.Fatalf("published double %s %q", status, detail)
	}
	status, detail = DeliveryReport([]HarnessFile{{Harness: "claude", Missing: true}}, []PinnedRelease{release}, nil)
	if status != "warn" || !strings.Contains(detail, "rendered harness file missing") {
		t.Fatalf("missing %s %q", status, detail)
	}
	status, detail = DeliveryReport(nil, nil, nil)
	if status != "ok" || detail != "no rule served twice" {
		t.Fatalf("empty %s %q", status, detail)
	}
}

func TestDeliveryReportRejectsUnverifiedAndRemovedRules(t *testing.T) {
	release := PinnedRelease{Repository: "org/repo", Commit: strings.Repeat("a", 40), State: "ready", Rules: []PinnedRule{{Identity: "org/repo/kernel#safe", Key: "safe", Text: "Preserve safety."}}}
	for _, text := range []string{"", "# Kernel\n", "<!-- aeon-rule: safe -->\nChanged."} {
		status, detail := DeliveryReport([]HarnessFile{{Harness: "codex", Text: text}}, []PinnedRelease{release}, nil)
		if status != "fail" || !strings.Contains(detail, "harness file drifted") || strings.Contains(detail, "no rule served twice") {
			t.Fatalf("removed rule: %s %q", status, detail)
		}
	}
	for _, state := range []string{"", "failed", "not_indexed", "unknown"} {
		release.State = state
		status, _ := DeliveryReport([]HarnessFile{{Harness: "codex", Text: "Preserve safety."}}, []PinnedRelease{release}, nil)
		if status == "ok" {
			t.Fatalf("certified state %q", state)
		}
	}
	release.State, release.Error = "ready", "index failed"
	if status, _ := DeliveryReport(nil, []PinnedRelease{release}, nil); status == "ok" {
		t.Fatal("certified index error")
	}
	release.Error = ""
	if status, _ := DeliveryReport(nil, []PinnedRelease{release}, nil); status == "ok" {
		t.Fatal("certified missing files")
	}
	for _, file := range []HarnessFile{
		{Harness: "codex", Session: true, Missing: true},
		{Harness: "codex", Session: true},
		{Harness: "codex", Session: true, Text: "unrecognized file"},
	} {
		if status, _ := DeliveryReport([]HarnessFile{file}, nil, nil); status == "ok" {
			t.Fatal("certified unverified session")
		}
	}
	files := []HarnessFile{{Harness: "codex", Text: "Preserve safety."}, {Harness: "codex", Session: true, Path: "received.txt", Text: "# Aeon session rules\n\n- [local] Use local style.\n"}}
	if status, _ := DeliveryReport(files, []PinnedRelease{release}, nil); status != "ok" {
		t.Fatal("clean separate channels refused")
	}
	files[1].Text += "- [copy] Preserve safety.\n"
	if status, detail := DeliveryReport(files, []PinnedRelease{release}, nil); status != "fail" || !strings.Contains(detail, "received.txt") {
		t.Fatalf("missed actual session: %s %s", status, detail)
	}
}

func TestDeliveryReportNormalizesWhitespaceAndRefusesUnresolvedImports(t *testing.T) {
	release := PinnedRelease{Repository: "org/repo", Commit: strings.Repeat("a", 40), State: "ready", Rules: []PinnedRule{{Identity: "org/repo/kernel#safe", Text: "Preserve\n  safety and privacy."}}}
	file := HarnessFile{Harness: "claude", Text: "- Preserve safety\n\tand   privacy."}
	if status, detail := DeliveryReport([]HarnessFile{file}, []PinnedRelease{release}, nil); status != "ok" {
		t.Fatalf("rewrapped rule: %s %s", status, detail)
	}
	file.Session, file.Text = true, "# Aeon session rules\n\n- Preserve safety\n  and privacy."
	if status, detail := DeliveryReport([]HarnessFile{file}, []PinnedRelease{release}, nil); status != "fail" || !strings.Contains(detail, "rule served twice") {
		t.Fatalf("rewrapped duplicate: %s %s", status, detail)
	}
	file.Session, file.Unverified, file.Text = false, true, "@missing.md"
	if status, detail := DeliveryReport([]HarnessFile{file}, []PinnedRelease{release}, nil); status != "warn" || !strings.Contains(detail, "unverified") || strings.Contains(detail, "drift") {
		t.Fatalf("unresolved import: %s %s", status, detail)
	}
	duplicates := []ServedDuplicate{{Identity: "copy", Doctrine: release.Rules[0].Identity}}
	if status, _ := DeliveryReport([]HarnessFile{file}, []PinnedRelease{release}, duplicates); status != "fail" {
		t.Fatal("unresolved imports hid a known duplicate")
	}
}
