// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCommitsSinceLaunchRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-m", "base")
	head := workspaceHEAD(t.Context(), dir)
	if !fullSHA(head) {
		t.Fatalf("head %q", head)
	}
	if commitsSince(t.Context(), dir, head) != nil {
		t.Fatal("launch revision listed existing history")
	}
	if commitsSince(t.Context(), dir, "") != nil || commitsSince(t.Context(), "", head) != nil {
		t.Fatal("missing workspace or start revision listed commits")
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "b.txt")
	git("commit", "-m", "Add usage\x07")
	got := commitsSince(t.Context(), dir, head)
	if len(got) != 1 || !fullSHA(got[0].SHA) || got[0].SHA == head || got[0].Subject != "Add usage" {
		t.Fatalf("commits since launch: %+v", got)
	}
}
