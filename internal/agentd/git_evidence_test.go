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
	if len(got) != 1 || !fullSHA(got[0].SHA) || got[0].SHA == head || got[0].Subject != "Add usage" || got[0].Parents != 1 || !got[0].OnDefaultBranch {
		t.Fatalf("commits since launch: %+v", got)
	}
}

func TestMergeCommitOnDefaultBranch(t *testing.T) {
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
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-m", "base")
	launch := workspaceHEAD(t.Context(), dir)
	git("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "b.txt")
	git("commit", "-m", "feature")
	git("checkout", "main")
	git("merge", "--no-ff", "feature", "-m", "Merge feature")
	got := commitsSince(t.Context(), dir, launch)
	var merged bool
	for _, c := range got {
		if c.Parents >= 2 && c.OnDefaultBranch && c.Subject == "Merge feature" {
			merged = true
		}
	}
	if !merged {
		t.Fatalf("default-branch merge: %+v", got)
	}
	git("checkout", "-b", "other", launch)
	git("checkout", "-b", "side")
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "c.txt")
	git("commit", "-m", "side")
	git("checkout", "other")
	git("merge", "--no-ff", "side", "-m", "Merge side")
	got = commitsSince(t.Context(), dir, launch)
	var side bool
	for _, c := range got {
		if c.Subject == "Merge side" && c.Parents >= 2 && !c.OnDefaultBranch {
			side = true
		}
		if c.Parents >= 2 && c.OnDefaultBranch {
			t.Fatalf("side history counted as merged: %+v", got)
		}
	}
	if !side {
		t.Fatalf("side merge: %+v", got)
	}
}

func evidenceRepo(t *testing.T) (string, func(dir string, args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return t.TempDir(), git
}

// Review case: no origin and no main/master. The worktree's own branch is
// not the default branch, so a feature-only merge is never on it.
func TestUnknownDefaultBranchIsNotHEAD(t *testing.T) {
	dir, git := evidenceRepo(t)
	git(dir, "init", "-b", "trunk")
	git(dir, "commit", "--allow-empty", "-m", "base")
	start := workspaceHEAD(t.Context(), dir)
	git(dir, "checkout", "-b", "feature")
	git(dir, "commit", "--allow-empty", "-m", "feature")
	git(dir, "checkout", "-b", "side", start)
	git(dir, "commit", "--allow-empty", "-m", "side")
	git(dir, "checkout", "feature")
	git(dir, "merge", "--no-ff", "side", "-m", "side into feature")
	if ref := defaultBranchRef(t.Context(), dir); ref != "" {
		t.Fatalf("default branch resolved to %q", ref)
	}
	for _, c := range commitsSince(t.Context(), dir, start) {
		if c.OnDefaultBranch {
			t.Fatalf("commit claimed on an unknown default branch: %+v", c)
		}
	}
	// A repository-configured default is honoured.
	git(dir, "config", "--local", "init.defaultBranch", "trunk")
	if ref := defaultBranchRef(t.Context(), dir); ref != "trunk" {
		t.Fatalf("configured default: %q", ref)
	}
}

// Review case: an unrelated upstream merge synced into the worker branch is
// on the default branch, but the worker's own change is not.
func TestUpstreamMergeLeavesWorkerChangeOffDefault(t *testing.T) {
	dir, git := evidenceRepo(t)
	git(dir, "init", "-b", "main")
	git(dir, "commit", "--allow-empty", "-m", "base")
	start := workspaceHEAD(t.Context(), dir)
	git(dir, "checkout", "-b", "feature")
	git(dir, "commit", "--allow-empty", "-m", "worker change")
	git(dir, "checkout", "-b", "upstream", start)
	git(dir, "commit", "--allow-empty", "-m", "unrelated")
	git(dir, "checkout", "main")
	git(dir, "merge", "--no-ff", "upstream", "-m", "upstream merge")
	git(dir, "checkout", "feature")
	git(dir, "merge", "--no-ff", "main", "-m", "sync main")
	var worker, upstream bool
	for _, c := range commitsSince(t.Context(), dir, start) {
		switch c.Subject {
		case "worker change":
			worker = !c.OnDefaultBranch
		case "upstream merge":
			upstream = c.OnDefaultBranch && c.Parents >= 2
		}
	}
	if !worker || !upstream {
		t.Fatalf("fixture: worker off default=%t upstream merge on default=%t", worker, upstream)
	}
}

// origin/HEAD wins over local branches: the worker's change merged on the
// remote default branch is on it; a local-only main commit is not.
func TestOriginHEADIsTheDefaultBranch(t *testing.T) {
	root, git := evidenceRepo(t)
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	git(root, "init", "--bare", "-b", "trunk", origin)
	git(root, "clone", "-q", origin, work)
	git(work, "commit", "--allow-empty", "-m", "base")
	git(work, "push", "-q", "origin", "HEAD:trunk")
	git(work, "remote", "set-head", "origin", "trunk")
	start := workspaceHEAD(t.Context(), work)
	git(work, "checkout", "-b", "main")
	git(work, "commit", "--allow-empty", "-m", "local main only")
	git(work, "checkout", "-b", "feature", start)
	git(work, "commit", "--allow-empty", "-m", "worker change")
	git(work, "push", "-q", "origin", "HEAD:trunk")
	git(work, "fetch", "-q", "origin")
	if ref := defaultBranchRef(t.Context(), work); ref != "origin/trunk" {
		t.Fatalf("default branch %q", ref)
	}
	got := commitsSince(t.Context(), work, start)
	if len(got) != 1 || got[0].Subject != "worker change" || !got[0].OnDefaultBranch {
		t.Fatalf("worker change on origin default: %+v", got)
	}
	git(work, "checkout", "main")
	for _, c := range commitsSince(t.Context(), work, start) {
		if c.Subject == "local main only" && c.OnDefaultBranch {
			t.Fatal("a local main commit counted as on the remote default branch")
		}
	}
}
