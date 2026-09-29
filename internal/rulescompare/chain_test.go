// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_rulesimport_unsupported

package rulescompare

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func canary(t *testing.T, path string) {
	t.Helper()
	writeFixture(t, path, "SYNTHETIC_SECRET_251")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
}

func gitDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func logicals(chain Chain) []string {
	out := make([]string, len(chain.Files))
	for i, file := range chain.Files {
		out[i] = file.Logical
	}
	return out
}

func texts(chain Chain) string {
	var b strings.Builder
	for _, file := range chain.Files {
		b.WriteString(file.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestClaudeChainOrderFallbackAndBoundaries(t *testing.T) {
	base := t.TempDir()
	home := t.TempDir()
	repo := filepath.Join(base, "fixture")
	launch := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(launch, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, repo)
	canary(t, filepath.Join(home, ".claude", "credentials.json"))
	canary(t, filepath.Join(home, ".claude", "settings.json"))
	writeFixture(t, filepath.Join(base, "CLAUDE.md"), "SYNTHETIC_OUTSIDE_251")
	writeFixture(t, filepath.Join(home, ".claude", "CLAUDE.md"), "user\n")
	writeFixture(t, filepath.Join(repo, "CLAUDE.md"), "root\n")
	writeFixture(t, filepath.Join(launch, "CLAUDE.md"), "leaf\n")
	writeFixture(t, filepath.Join(launch, "CLAUDE.local.md"), "local\n")
	writeFixture(t, filepath.Join(repo, "AGENTS.md"), "not used\n")
	side := filepath.Join(repo, "side.md")
	writeFixture(t, side, "SYNTHETIC_OUTSIDE_251 imported")
	writeFixture(t, filepath.Join(repo, "CLAUDE.md"), "root\n@import ./side.md\n")

	chain, err := LoadChain("claude-code", home, launch)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user/CLAUDE.md", "repo/CLAUDE.md", "repo/pkg/CLAUDE.md", "repo/pkg/CLAUDE.local.md"}
	if strings.Join(logicals(chain), ",") != strings.Join(want, ",") {
		t.Fatalf("files %v", logicals(chain))
	}
	if strings.Contains(texts(chain), "SYNTHETIC_OUTSIDE_251") || strings.Contains(texts(chain), "SYNTHETIC_SECRET_251") || strings.Contains(texts(chain), "not used") {
		t.Fatal("chain included a file it must not read")
	}
	for _, gap := range []string{"managed_policy_not_read", "imports_not_followed", "parents_above_repository_not_read"} {
		if !strings.Contains(strings.Join(chain.Gaps, ","), gap) {
			t.Fatal(chain.Gaps)
		}
	}

	emptyRoot := filepath.Join(t.TempDir(), "empty-root")
	if err = os.MkdirAll(emptyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, emptyRoot)
	writeFixture(t, filepath.Join(emptyRoot, "CLAUDE.md"), "")
	writeFixture(t, filepath.Join(emptyRoot, ".claude", "CLAUDE.md"), "SYNTHETIC_OUTSIDE_251 nested")
	empty, err := LoadChain("claude-code", "", emptyRoot)
	if err != nil || len(empty.Files) != 1 || empty.Files[0].Bytes != 0 || strings.Contains(texts(empty), "SYNTHETIC_OUTSIDE_251") {
		t.Fatalf("%v %+v", err, empty.Files)
	}

	fallback := filepath.Join(t.TempDir(), "agents-fallback")
	if err = os.MkdirAll(fallback, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, fallback)
	userHome := t.TempDir()
	writeFixture(t, filepath.Join(userHome, ".claude", "CLAUDE.md"), "")
	writeFixture(t, filepath.Join(fallback, "AGENTS.md"), "agents fallback\n")
	got, err := LoadChain("claude-code", userHome, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(logicals(got), ",") != "user/CLAUDE.md,repo/AGENTS.md" {
		t.Fatal(logicals(got))
	}
}

func TestClaudeLocalAtEachLevelSuppressesAgentsFallback(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "fixture")
	leaf := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, repo)
	writeFixture(t, filepath.Join(repo, "CLAUDE.local.md"), doc("root-local", "Root local.", "Synthetic."))
	writeFixture(t, filepath.Join(leaf, "CLAUDE.md"), doc("leaf", "Leaf.", "Synthetic."))
	writeFixture(t, filepath.Join(repo, "AGENTS.md"), "SYNTHETIC_OUTSIDE_251 agents\n")
	got, err := LoadChain("claude-code", "", leaf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(logicals(got), ",") != "repo/CLAUDE.local.md,repo/pkg/CLAUDE.md" {
		t.Fatalf("ancestor local omitted: %v", logicals(got))
	}
	if strings.Contains(texts(got), "SYNTHETIC_OUTSIDE_251") {
		t.Fatal("agents fallback loaded beside an ancestor local file")
	}

	localOnly := filepath.Join(t.TempDir(), "local-only")
	if err = os.MkdirAll(localOnly, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, localOnly)
	writeFixture(t, filepath.Join(localOnly, "CLAUDE.local.md"), doc("local", "Local.", "Synthetic."))
	writeFixture(t, filepath.Join(localOnly, "AGENTS.md"), doc("agents", "Agents.", "Synthetic."))
	got, err = LoadChain("claude-code", "", localOnly)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(logicals(got), ",") != "repo/CLAUDE.local.md" {
		t.Fatalf("wrong fallback chain: %v", logicals(got))
	}

	nested := filepath.Join(t.TempDir(), "nested-local")
	nestedLeaf := filepath.Join(nested, "pkg")
	if err = os.MkdirAll(nestedLeaf, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, nested)
	writeFixture(t, filepath.Join(nested, "AGENTS.md"), "SYNTHETIC_OUTSIDE_251\n")
	writeFixture(t, filepath.Join(nestedLeaf, "CLAUDE.local.md"), "leaf local\n")
	got, err = LoadChain("claude-code", "", nestedLeaf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(logicals(got), ",") != "repo/pkg/CLAUDE.local.md" || strings.Contains(texts(got), "SYNTHETIC_OUTSIDE_251") {
		t.Fatalf("nested local did not suppress fallback: %v", logicals(got))
	}
}

func TestClaudeSymlinkAndGitRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "fixture")
	launch := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(launch, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, repo)
	writeFixture(t, filepath.Join(base, "CLAUDE.md"), "SYNTHETIC_OUTSIDE_251")
	writeFixture(t, filepath.Join(repo, "CLAUDE.md"), "root\n")
	target := filepath.Join(launch, "target.md")
	writeFixture(t, target, "SYNTHETIC_SECRET_251")
	if err := os.Symlink(target, filepath.Join(launch, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadChain("claude-code", "", launch); err == nil || !strings.Contains(err.Error(), "CLAUDE.md") || strings.Contains(err.Error(), target) || strings.Contains(err.Error(), "SYNTHETIC_SECRET_251") {
		t.Fatal(err)
	}

	linked := filepath.Join(t.TempDir(), "linked-git")
	if err := os.MkdirAll(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, filepath.Join(linked, ".git")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(linked, "CLAUDE.md"), "only launch\n")
	chain, err := LoadChain("claude-code", "", linked)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(logicals(chain), ",") != "repo/CLAUDE.md" || strings.Contains(texts(chain), "root") {
		t.Fatalf("symlinked git was treated as a root: %v", logicals(chain))
	}

	if _, err = LoadChain("claude-code", "", filepath.Join(t.TempDir(), ".ssh", "repo")); !errorsIsProhibited(err) {
		t.Fatal(err)
	}
	if _, err = LoadChain("grok", "", repo); err == nil {
		t.Fatal("grok was accepted")
	}
}

func errorsIsProhibited(err error) bool {
	return errors.Is(err, rulesimport.ErrProhibitedPath)
}

func TestCodexChainPrefersOverrideSkipsEmptyAndCapsBytes(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(t.TempDir(), "fixture")
	launch := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(launch, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, repo)
	canary(t, filepath.Join(home, ".codex", "credentials.json"))
	writeFixture(t, filepath.Join(home, ".codex", "AGENTS.override.md"), "")
	writeFixture(t, filepath.Join(home, ".codex", "AGENTS.md"), "global\n")
	writeFixture(t, filepath.Join(repo, "AGENTS.override.md"), " \n")
	writeFixture(t, filepath.Join(repo, "AGENTS.md"), "skipped root\n")
	writeFixture(t, filepath.Join(launch, "AGENTS.md"), "leaf\n")
	chain, err := LoadChain("codex", home, launch)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user/AGENTS.md", "repo/AGENTS.override.md", "repo/pkg/AGENTS.md"}
	if strings.Join(logicals(chain), ",") != strings.Join(want, ",") || strings.Contains(texts(chain), "skipped root") || strings.Contains(texts(chain), "SYNTHETIC_SECRET_251") {
		t.Fatalf("%v %q", logicals(chain), texts(chain))
	}
	if !strings.Contains(strings.Join(chain.Gaps, ","), "codex_fallback_filenames_not_applied") || strings.Contains(strings.Join(chain.Gaps, ","), "codex_byte_cap") {
		t.Fatal(chain.Gaps)
	}

	cappedHome := t.TempDir()
	cappedRepo := filepath.Join(t.TempDir(), "capped")
	if err = os.MkdirAll(cappedRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, cappedRepo)
	writeFixture(t, filepath.Join(cappedHome, ".codex", "AGENTS.md"), "x")
	writeFixture(t, filepath.Join(cappedRepo, "AGENTS.md"), strings.Repeat("a", projectDocMaxBytes))
	capped, err := LoadChain("codex", cappedHome, cappedRepo)
	if err != nil || len(capped.Files) != 2 || capped.Files[0].Text != "x" {
		t.Fatalf("%v files %d", err, len(capped.Files))
	}
	prefix := capped.Files[1]
	wantPrefix := strings.Repeat("a", projectDocMaxBytes-1)
	if prefix.Bytes != len(wantPrefix) || prefix.Text != wantPrefix || prefix.SHA256 != sha256Hex(wantPrefix) {
		t.Fatalf("prefix bytes %d", prefix.Bytes)
	}
	if !strings.Contains(strings.Join(capped.Gaps, ","), "codex_byte_cap") {
		t.Fatal(capped.Gaps)
	}
}

func TestCodexRetainsPrefixInsideByteCap(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "fixture")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, repo)
	body := doc("safety", "Keep the floor.", "Synthetic.")
	body += strings.Repeat(" ", projectDocMaxBytes+1-len(body))
	writeFixture(t, filepath.Join(repo, "AGENTS.md"), body)
	got, err := LoadChain("codex", "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Bytes != projectDocMaxBytes || !strings.Contains(strings.Join(got.Gaps, ","), "codex_byte_cap") {
		t.Fatalf("whole %d-byte file discarded, including in-budget first rule; files %d gaps %v", len(body), len(got.Files), got.Gaps)
	}
	parsed, err := rulesimport.ParseLoaded(got.Files[0].Logical, got.Files[0].Text, got.Files[0].SHA256, got.Files[0].Bytes)
	if err != nil || len(parsed.Rules) != 1 || parsed.Rules[0].Text != "Keep the floor." {
		t.Fatalf("prefix lost the rule: %v rules %d", err, len(parsed.Rules))
	}

	split := filepath.Join(t.TempDir(), "split-rune")
	if err = os.MkdirAll(split, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, split)
	raw := strings.Repeat("a", projectDocMaxBytes-1) + "€"
	writeFixture(t, filepath.Join(split, "AGENTS.md"), raw)
	got, err = LoadChain("codex", "", split)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("a", projectDocMaxBytes-1)
	if len(got.Files) != 1 || got.Files[0].Text != want || got.Files[0].SHA256 != sha256Hex(want) {
		t.Fatalf("rune split kept %d bytes", len(got.Files[0].Text))
	}
}

func TestClaudeImportExpandsInsideRootAndRefusesSecrets(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "fixture")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, repo)
	writeFixture(t, filepath.Join(base, "outside.md"), "SYNTHETIC_OUTSIDE_251")
	writeFixture(t, filepath.Join(base, "id_ed25519"), "SYNTHETIC_SECRET_251")
	writeFixture(t, filepath.Join(repo, ".env"), "SYNTHETIC_SECRET_251")
	writeFixture(t, filepath.Join(repo, ".ssh", "id_ed25519"), "SYNTHETIC_SECRET_251")
	writeFixture(t, filepath.Join(repo, "id_rsa"), "SYNTHETIC_SECRET_251")
	writeFixture(t, filepath.Join(repo, "secrets.key"), "SYNTHETIC_SECRET_251")
	writeFixture(t, filepath.Join(repo, "credentials"), "SYNTHETIC_SECRET_251")
	writeFixture(t, filepath.Join(home, ".ssh", "id_ed25519"), "SYNTHETIC_SECRET_251")
	writeFixture(t, filepath.Join(repo, "AGENTS.md"), doc("safety", "Keep the floor.", "Synthetic."))
	link := filepath.Join(repo, "linked.md")
	if err := os.Symlink(filepath.Join(repo, ".env"), link); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n@../outside.md\n@.env\n@.ssh/id_ed25519\n@id_rsa\n@secrets.key\n@credentials\n@linked.md\n@~/../id_ed25519\n`@AGENTS.md`\n```\n@AGENTS.md\n```\n")

	chain, err := LoadChain("claude-code", home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(logicals(chain), ",") != "repo/CLAUDE.md,repo/AGENTS.md" {
		t.Fatalf("files %v", logicals(chain))
	}
	if strings.Contains(texts(chain), "SYNTHETIC_SECRET_251") || strings.Contains(texts(chain), "SYNTHETIC_OUTSIDE_251") {
		t.Fatal("import read a secret or a file outside the root")
	}
	if !strings.Contains(strings.Join(chain.Gaps, ","), "imports_not_followed") {
		t.Fatal(chain.Gaps)
	}
	merged := rules.Merged{Version: "260929120000.0.0", SHA256: strings.Repeat("ab", 32), Rules: []rules.Rule{{
		Identity: "safety", Text: "Keep the floor.", Why: "Synthetic.", Strength: "normal", Enabled: true,
	}}}
	report, err := DiffChain(chain, "builder", "10000000-0000-4000-8000-000000000002", merged)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts.OnlyMerged != 0 || report.Counts.Both != 1 {
		t.Fatalf("imported rule missing from the comparison: %+v", report.Rules)
	}
	if !strings.Contains(strings.Join(report.Gaps, ","), "imports_not_followed") {
		t.Fatal(report.Gaps)
	}

	writeFixture(t, filepath.Join(base, "nope.md"), "SYNTHETIC_OUTSIDE_251")
	writeFixture(t, filepath.Join(home, ".claude", "extra.md"), "home note\n")
	writeFixture(t, filepath.Join(home, ".claude", "CLAUDE.md"), "@extra.md\n@../../nope.md\n")
	userChain, err := LoadChain("claude-code", home, repo)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(logicals(userChain), ",")
	if !strings.Contains(joined, "user/.claude/extra.md") || strings.Contains(texts(userChain), "SYNTHETIC_OUTSIDE_251") || !strings.Contains(strings.Join(userChain.Gaps, ","), "imports_not_followed") {
		t.Fatalf("home import files %v gaps %v", logicals(userChain), userChain.Gaps)
	}
}

func TestClaudeImportBoundsDepthCountAndBytes(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "fixture")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, repo)
	writeFixture(t, filepath.Join(repo, "CLAUDE.md"), "@a.md\n")
	writeFixture(t, filepath.Join(repo, "a.md"), "@b.md\n")
	writeFixture(t, filepath.Join(repo, "b.md"), "@c.md\n")
	writeFixture(t, filepath.Join(repo, "c.md"), "@d.md\n")
	writeFixture(t, filepath.Join(repo, "d.md"), "@e.md\n")
	writeFixture(t, filepath.Join(repo, "e.md"), "SYNTHETIC_DEPTH_251\n")
	got, err := LoadChain("claude-code", "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(texts(got), "SYNTHETIC_DEPTH_251") || len(got.Files) != 5 {
		t.Fatalf("depth bound files %v", logicals(got))
	}
	if !strings.Contains(strings.Join(got.Gaps, ","), "imports_not_followed") {
		t.Fatal(got.Gaps)
	}

	wide := filepath.Join(t.TempDir(), "wide")
	if err = os.MkdirAll(wide, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, wide)
	var refs strings.Builder
	for i := 0; i <= maxImportFiles; i++ {
		name := fmt.Sprintf("n%02d.md", i)
		writeFixture(t, filepath.Join(wide, name), "note\n")
		fmt.Fprintf(&refs, "@%s\n", name)
	}
	writeFixture(t, filepath.Join(wide, "CLAUDE.md"), refs.String())
	counted, err := LoadChain("claude-code", "", wide)
	if err != nil {
		t.Fatal(err)
	}
	if len(counted.Files) != 1+maxImportFiles || !strings.Contains(strings.Join(counted.Gaps, ","), "imports_not_followed") {
		t.Fatalf("count bound files %d gaps %v", len(counted.Files), counted.Gaps)
	}

	huge := filepath.Join(t.TempDir(), "huge")
	if err = os.MkdirAll(huge, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir(t, huge)
	writeFixture(t, filepath.Join(huge, "big.md"), strings.Repeat("x", maxImportBytes+1))
	writeFixture(t, filepath.Join(huge, "CLAUDE.md"), "@big.md\n")
	capped, err := LoadChain("claude-code", "", huge)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Files) != 1 || strings.Contains(texts(capped), "xxx") || !strings.Contains(strings.Join(capped.Gaps, ","), "imports_not_followed") {
		t.Fatalf("byte bound files %d gaps %v", len(capped.Files), capped.Gaps)
	}
}
