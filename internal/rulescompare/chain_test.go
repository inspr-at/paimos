// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_rulesimport_unsupported

package rulescompare

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if err != nil || len(capped.Files) != 1 || !strings.Contains(strings.Join(capped.Gaps, ","), "codex_byte_cap") {
		t.Fatalf("%v files %d gaps %v", err, len(capped.Files), capped.Gaps)
	}
}
