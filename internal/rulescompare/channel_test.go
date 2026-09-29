// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"strings"
	"testing"
)

func TestDeliveryReportOneChannel(t *testing.T) {
	const (
		identity = "inspr-at/fixture-doctrine/docs/AGENTS-KERNEL.md#secrets"
		text     = "Never print the environment."
	)
	release := PinnedRelease{
		Repository: "inspr-at/fixture-doctrine", Ref: "v260922101217.0.0", Commit: strings.Repeat("ab", 20),
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
