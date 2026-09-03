// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInspectWorkspaceDistinguishesPrimaryAndLinkedWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	linked := filepath.Join(root, "linked")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init", "-b", "main")
	runTestGit(t, repo, "config", "user.name", "PAIMOS Test")
	runTestGit(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "tracked")
	runTestGit(t, repo, "commit", "-m", "fixture")
	runTestGit(t, repo, "worktree", "add", "-b", "feat/profile/test", linked)

	primary, err := inspectWorkspace(context.Background(), repo, WorkspaceExclusive)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := inspectWorkspace(context.Background(), linked, WorkspaceExclusive)
	if err != nil {
		t.Fatal(err)
	}
	if primary.Kind != WorkspacePrimary || primary.GitBranch != "main" {
		t.Fatalf("primary provenance = %#v", primary)
	}
	if worktree.Kind != WorkspaceWorktree || worktree.GitBranch != "feat/profile/test" {
		t.Fatalf("worktree provenance = %#v", worktree)
	}
	if primary.Identity == worktree.Identity || primary.GitTopLevel == worktree.GitTopLevel {
		t.Fatalf("worktree identities were not isolated: primary=%#v linked=%#v", primary, worktree)
	}
	runTestGit(t, repo, "checkout", "--detach")
	detached, err := inspectWorkspace(context.Background(), repo, WorkspaceExclusive)
	if err != nil || detached.GitBranch != "detached" || detached.Kind != WorkspacePrimary {
		t.Fatalf("detached provenance = %#v err=%v", detached, err)
	}
}

func TestInspectWorkspaceFallsBackToDirectoryWithoutGitClaims(t *testing.T) {
	directory := t.TempDir()
	got, err := inspectWorkspace(context.Background(), directory, WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != WorkspaceDirectory || got.Mode != WorkspaceShared || got.GitTopLevel != "" || got.GitBranch != "" || len(got.Identity) != 64 {
		t.Fatalf("directory provenance = %#v", got)
	}
}

func TestInspectWorkspaceDoesNotRelabelBrokenGitRepository(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectWorkspace(context.Background(), directory, WorkspaceExclusive); err == nil {
		t.Fatal("broken Git repository was relabelled as an ordinary directory")
	}
}

func runTestGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
