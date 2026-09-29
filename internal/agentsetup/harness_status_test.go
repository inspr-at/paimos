// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// recoveryFixture is shared with web/tests/agent-pairing.test.ts so the web
// labels and fix commands cannot drift from the daemon and server mapping.
// Regenerate with UPDATE_HARNESS_RECOVERY=1 after an intentional change.
const recoveryFixture = "testdata/harness_recovery.json"

type recoveryTable struct {
	Harnesses []string                          `json:"harnesses"`
	Reasons   []string                          `json:"reasons"`
	Fixes     map[string]map[string]*HarnessFix `json:"fixes"`
}

func TestRecoveryFixIsOneSharedVocabulary(t *testing.T) {
	table := recoveryTable{Harnesses: []string{"claude", "codex", "cursor", "grok", "pi"}, Reasons: append(append([]string(nil), HarnessReasons...), "future_reason"), Fixes: map[string]map[string]*HarnessFix{}}
	kinds := map[string]string{"dependency_invalid": FixAddHarness, PinMissing: FixAddHarness, PinPartial: FixAddHarness, PinDrifted: FixAddHarness, PinInvalid: FixAddHarness, PinUnsafe: FixAddHarness, "login_required": FixLogin, "harness_failed": FixRestart, "cli_unavailable": FixRestart, "profile_permissions": FixRestart}
	for _, harness := range table.Harnesses {
		table.Fixes[harness] = map[string]*HarnessFix{}
		for _, reason := range table.Reasons {
			fix := RecoveryFix(harness, reason)
			want := kinds[reason]
			if harness == "claude" && want == FixAddHarness {
				want = FixRepin
			}
			if fix.Kind != want || (want == "") != (fix.Command == "") {
				t.Fatalf("%s/%s: fix %+v, want kind %q", harness, reason, fix, want)
			}
			if fix.Kind != "" {
				table.Fixes[harness][reason] = &fix
			}
			// Every blocked report keeps its code; only known codes get a fix.
			detail, ok := HarnessReport(harness, "blocked", reason)
			if !ok || detail.Reason != reason || detail.Fix != fix {
				t.Fatalf("%s/%s: report %+v %v", harness, reason, detail, ok)
			}
		}
	}
	if fix := RecoveryFix("gemini", PinDrifted); fix != (HarnessFix{}) {
		t.Fatal("unknown harness received a command", fix)
	}
	for _, report := range [][3]string{{"claude", "future_state", "future_reason"}, {"codex", "checking", "starting"}, {"pi", "login_required", ""}} {
		if detail, ok := HarnessReport(report[0], report[1], report[2]); !ok || detail.State != report[1] {
			t.Fatalf("report %v dropped: %+v", report, detail)
		}
	}
	for _, report := range [][3]string{{"claude", "ready", PinDrifted}, {"claude", "blocked", "Local Diagnostic"}, {"claude", "Blocked", ""}, {"gemini", "blocked", PinDrifted}} {
		if _, ok := HarnessReport(report[0], report[1], report[2]); ok {
			t.Fatalf("inconsistent or malformed report accepted: %v", report)
		}
	}
	raw, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := filepath.FromSlash(recoveryFixture)
	if os.Getenv("UPDATE_HARNESS_RECOVERY") == "1" {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("%s is stale; rerun with UPDATE_HARNESS_RECOVERY=1 and update the web mapping: %v", recoveryFixture, err)
	}
}
