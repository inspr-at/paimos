// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

func doc(id, text, why string) string {
	return "# Synthetic\n\n## Safety\n\n<!-- aeon-rule: " + id + " -->\n- " + text + "\n  Why: " + why + "\n"
}

func chainFile(t *testing.T, logical, body string) ChainFile {
	t.Helper()
	parsed, err := rulesimport.ParseLoaded(logical, body, strings.Repeat("ab", 32), len(body))
	if err != nil || len(parsed.Rules) != 1 {
		t.Fatalf("parse %s: %v rules %d", logical, err, len(parsed.Rules))
	}
	return ChainFile{Logical: logical, SHA256: strings.Repeat("ab", 32), Bytes: len(body), Text: body}
}

func TestDiffChainMatchesExplicitWhyAndConflict(t *testing.T) {
	floor := doc("safety", "Keep the floor.", "it is the floor.")
	other := doc("local-only", "Stay local.", "it is local.")
	conflictA := doc("safety", "Keep the floor.", "it is the floor.")
	conflictB := doc("safety", "Keep another floor.", "a different reason.")
	chain := Chain{
		Harness: "codex", RepoName: "fixture", RepoSHA256: strings.Repeat("cd", 32),
		Files: []ChainFile{chainFile(t, "repo/AGENTS.md", floor), chainFile(t, "repo/pkg/AGENTS.md", other)},
		Gaps:  []string{"imports_not_followed"},
	}
	parsed, err := rulesimport.ParseLoaded("repo/AGENTS.md", floor, strings.Repeat("ab", 32), len(floor))
	if err != nil {
		t.Fatal(err)
	}
	local := parsed.Rules[0]
	merged := rules.Merged{
		Version: "260929120000.0.0", SHA256: strings.Repeat("ef", 32),
		Rules: []rules.Rule{
			{Identity: "safety", Text: local.Text, Why: "published reason.", Strength: "normal", Enabled: true},
			{Identity: "merged-only", Text: "Published only.", Why: "server.", Strength: "locked", Enabled: true},
			{Identity: importIdentity(local.Identity), Text: "hash loser", Why: "not the explicit match", Strength: "normal", Enabled: true},
		},
	}
	report, err := DiffChain(chain, "builder", "10000000-0000-4000-8000-000000000002", merged)
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != LiveSchema || !report.Limits.OneTime || report.Limits.WaitingWindow || report.Limits.RolloutAuthorized || !report.Limits.FileContentOmitted || report.Limits.ModelLoadVerified {
		t.Fatalf("%+v", report.Limits)
	}
	if report.Summary != "codex · fixture · both 0, only local 1, only merged 2, differs 1" {
		t.Fatal(report.Summary)
	}
	got := map[string]LiveRule{}
	for _, row := range report.Rules {
		got[row.Identity] = row
		if strings.Contains(row.Identity, "Keep") || row.LocalTextSHA256 == local.Text {
			t.Fatalf("row kept prose %+v", row)
		}
	}
	safety := got["safety"]
	if safety.Status != "differs" || strings.Join(safety.Changed, ",") != "why" || safety.LocalTextSHA256 != sha256Hex(local.Text) || safety.MergedTextSHA256 != sha256Hex(merged.Rules[0].Text) {
		t.Fatalf("%+v", safety)
	}
	if got["local-only"].Status != "only_local" || got["local-only"].MergedTextSHA256 != "" || got["merged-only"].Status != "only_merged" || got["merged-only"].LocalTextSHA256 != "" {
		t.Fatalf("%+v", got)
	}
	if got[importIdentity(local.Identity)].Status != "only_merged" {
		t.Fatal("explicit id did not win over the import hash")
	}
	raw := report.Text()
	if strings.Contains(raw, "Keep the floor") || strings.Contains(raw, "published reason") {
		t.Fatal(raw)
	}

	conflict := chain
	conflict.Files = []ChainFile{chainFile(t, "repo/AGENTS.md", conflictA), chainFile(t, "repo/pkg/AGENTS.md", conflictB)}
	conflicted, err := DiffChain(conflict, "builder", report.ProjectID, rules.Merged{Version: "v", SHA256: strings.Repeat("ab", 32), Rules: []rules.Rule{{Identity: "safety", Text: local.Text, Why: local.Why, Strength: "normal", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicted.Rules) != 1 || conflicted.Rules[0].Status != "differs" || strings.Join(conflicted.Rules[0].Changed, ",") != "local_conflict" || conflicted.Rules[0].LocalTextSHA256 != "" || conflicted.Rules[0].MergedTextSHA256 != "" {
		t.Fatalf("%+v", conflicted.Rules)
	}

	if _, err = DiffChain(chain, "builder", report.ProjectID, rules.Merged{Rules: []rules.Rule{{Identity: "safety"}, {Identity: "safety"}}}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatal(err)
	}
}
