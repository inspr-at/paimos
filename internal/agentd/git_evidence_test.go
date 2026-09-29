// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

// launchedRepo is a repository on main with one base commit, captured the
// way the supervisor captures a launch.
type launchedRepo struct {
	dir                   string
	git                   func(dir string, args ...string)
	launch, launchDefault string
}

func newLaunchedRepo(t *testing.T) *launchedRepo {
	t.Helper()
	dir, git := evidenceRepo(t)
	git(dir, "init", "-b", "main")
	git(dir, "commit", "--allow-empty", "-m", "base")
	return &launchedRepo{dir: dir, git: git}
}

func (r *launchedRepo) run(args ...string) { r.git(r.dir, args...) }

func (r *launchedRepo) launchNow(t *testing.T) {
	t.Helper()
	r.launch, r.launchDefault = workspaceHEAD(t.Context(), r.dir), launchDefaultRev(t.Context(), r.dir)
}

func (r *launchedRepo) evidence(t *testing.T) string {
	t.Helper()
	var parts []string
	for _, c := range runCommits(t.Context(), r.dir, r.launch, r.launchDefault) {
		if !fullSHA(c.SHA) || c.Parents != 1 || c.OnDefaultBranch {
			t.Fatalf("malformed evidence %+v", c)
		}
		parts = append(parts, c.Subject)
	}
	return strings.Join(parts, ",")
}

func TestRunCommitsSinceLaunch(t *testing.T) {
	r := newLaunchedRepo(t)
	r.launchNow(t)
	if got := r.evidence(t); got != "" {
		t.Fatalf("existing history listed: %s", got)
	}
	if runCommits(t.Context(), r.dir, "", r.launchDefault) != nil || runCommits(t.Context(), "", r.launch, r.launchDefault) != nil {
		t.Fatal("missing workspace or launch revision listed commits")
	}
	if err := os.WriteFile(filepath.Join(r.dir, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "b.txt")
	r.run("commit", "-m", "Add usage\x07")
	if got := r.evidence(t); got != "Add usage" {
		t.Fatalf("own commit: %s", got)
	}
}

// Round 4 review cases, per the lead decision: local git yields committed or
// no_commit only.
func TestRunCommitsRound4Reproductions(t *testing.T) {
	t.Run("reset backwards then fast-forward is no_commit", func(t *testing.T) {
		r := newLaunchedRepo(t)
		base := workspaceHEAD(t.Context(), r.dir)
		r.run("checkout", "-b", "worker")
		r.run("commit", "--allow-empty", "-m", "PREVIOUS RUN")
		old := workspaceHEAD(t.Context(), r.dir)
		r.run("branch", "-f", "main", old)
		r.run("reset", "--soft", base)
		r.launchNow(t)
		r.run("merge", "--ff-only", "main")
		if got := r.evidence(t); got != "" {
			t.Fatalf("history from before launch credited: %s", got)
		}
	})
	t.Run("upstream sync merge is no_commit", func(t *testing.T) {
		r := newLaunchedRepo(t)
		r.launchNow(t)
		external := filepath.Join(t.TempDir(), "external")
		r.git(r.dir, "clone", "-q", r.dir, external)
		r.git(external, "commit", "--allow-empty", "-m", "external upstream")
		r.run("remote", "add", "origin", external)
		r.run("fetch", "-q", "origin")
		r.run("merge", "--no-ff", "origin/main", "-m", "sync upstream")
		if got := r.evidence(t); got != "" {
			t.Fatalf("upstream commits credited: %s", got)
		}
	})
	t.Run("ff-only merge of run work is committed", func(t *testing.T) {
		r := newLaunchedRepo(t)
		r.launchNow(t)
		r.run("checkout", "-b", "worker")
		r.run("commit", "--allow-empty", "-m", "THIS RUN")
		r.run("checkout", "main")
		r.run("merge", "--ff-only", "worker")
		if got := r.evidence(t); got != "THIS RUN" {
			t.Fatalf("run work lost: %s", got)
		}
	})
	t.Run("detached reset to history that existed at launch is no_commit", func(t *testing.T) {
		r := newLaunchedRepo(t)
		base := workspaceHEAD(t.Context(), r.dir)
		r.run("checkout", "-b", "elsewhere")
		r.run("commit", "--allow-empty", "-m", "EXISTING ELSEWHERE")
		old := workspaceHEAD(t.Context(), r.dir)
		r.run("branch", "-f", "main", old)
		r.run("checkout", "--detach", base)
		r.launchNow(t)
		r.run("reset", "--soft", old)
		if got := r.evidence(t); got != "" {
			t.Fatalf("history from before launch credited: %s", got)
		}
	})
}

// Lead decision: a fast-forward onto upstream commits fetched during the run,
// with no work of the run's own, is no_commit; own work on top still counts,
// including after it is pushed to a feature branch.
func TestRunCommitsFastForwardOntoUpstream(t *testing.T) {
	root, git := evidenceRepo(t)
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	upstream := filepath.Join(root, "upstream")
	git(root, "init", "--bare", "-b", "main", origin)
	git(root, "clone", "-q", origin, work)
	git(work, "commit", "--allow-empty", "-m", "base")
	git(work, "push", "-q", "origin", "HEAD:main")
	git(work, "remote", "set-head", "origin", "main")
	r := &launchedRepo{dir: work, git: git}
	r.launchNow(t)
	git(root, "clone", "-q", origin, upstream)
	git(upstream, "commit", "--allow-empty", "-m", "new upstream 1")
	git(upstream, "commit", "--allow-empty", "-m", "new upstream 2")
	git(upstream, "push", "-q", "origin", "HEAD:main")
	r.run("pull", "-q", "--ff-only", "origin", "main")
	if got := r.evidence(t); got != "" {
		t.Fatalf("fast-forward onto upstream credited: %s", got)
	}
	r.run("checkout", "-b", "feature")
	r.run("commit", "--allow-empty", "-m", "own work")
	r.run("push", "-q", "origin", "feature")
	if got := r.evidence(t); got != "own work" {
		t.Fatalf("own work on top of upstream: %s", got)
	}
}

// Round 5 review case: no origin/HEAD and a configured local trunk must not
// shadow origin/main; a fast-forward onto its upstream commits is no_commit.
func TestRemoteDefaultNotShadowedByLocalFallback(t *testing.T) {
	root, git := evidenceRepo(t)
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	upstream := filepath.Join(root, "upstream")
	git(root, "init", "--bare", "-b", "main", origin)
	git(root, "clone", "-q", origin, work)
	git(work, "commit", "--allow-empty", "-m", "base")
	git(work, "push", "-q", "origin", "HEAD:main")
	git(work, "fetch", "-q", "origin")
	git(work, "remote", "set-head", "origin", "-d")
	git(work, "branch", "trunk")
	git(work, "config", "--local", "init.defaultBranch", "trunk")
	if ref := defaultBranchRef(t.Context(), work); ref != "trunk" {
		t.Fatalf("fixture: default %q", ref)
	}
	r := &launchedRepo{dir: work, git: git}
	r.launchNow(t)
	git(root, "clone", "-q", origin, upstream)
	git(upstream, "commit", "--allow-empty", "-m", "foreign upstream")
	git(upstream, "push", "-q", "origin", "HEAD:main")
	r.run("fetch", "-q", "origin")
	r.run("merge", "--ff-only", "origin/main")
	if got := r.evidence(t); got != "" {
		t.Fatalf("foreign upstream credited: %s", got)
	}
	r.run("commit", "--allow-empty", "-m", "own work")
	if got := r.evidence(t); got != "own work" {
		t.Fatalf("own work: %s", got)
	}
}

// Earlier review cases keep their answers under the simpler rule.
func TestRunCommitsEarlierReproductions(t *testing.T) {
	t.Run("upstream merge synced beside unmerged work is committed", func(t *testing.T) {
		r := newLaunchedRepo(t)
		r.launchNow(t)
		r.run("checkout", "-b", "feature")
		r.run("commit", "--allow-empty", "-m", "worker change")
		r.run("checkout", "-b", "upstream", r.launch)
		r.run("commit", "--allow-empty", "-m", "unrelated")
		r.run("checkout", "main")
		r.run("merge", "--no-ff", "upstream", "-m", "upstream merge")
		r.run("checkout", "feature")
		r.run("merge", "--no-ff", "main", "-m", "sync main")
		if got := r.evidence(t); got != "worker change" {
			t.Fatalf("evidence: %s", got)
		}
	})
	t.Run("fast-forward to an upstream merge only is no_commit", func(t *testing.T) {
		r := newLaunchedRepo(t)
		r.launchNow(t)
		r.run("checkout", "-b", "worker")
		r.run("checkout", "-b", "upstream")
		r.run("commit", "--allow-empty", "-m", "unrelated upstream change")
		r.run("checkout", "main")
		r.run("merge", "--no-ff", "upstream", "-m", "unrelated upstream merge")
		r.run("checkout", "worker")
		r.run("merge", "--ff-only", "main")
		if got := r.evidence(t); got != "" {
			t.Fatalf("upstream credited: %s", got)
		}
	})
	t.Run("many upstream commits do not hide own work", func(t *testing.T) {
		r := newLaunchedRepo(t)
		r.launchNow(t)
		r.run("checkout", "-b", "worker")
		r.run("commit", "--allow-empty", "-m", "OWN UNMERGED")
		r.run("checkout", "-b", "upstream", r.launch)
		for i := 0; i < 22; i++ {
			r.run("commit", "--allow-empty", "-m", fmt.Sprint("unrelated ", i))
		}
		r.run("checkout", "main")
		r.run("merge", "--no-ff", "upstream", "-m", "upstream merge")
		r.run("checkout", "worker")
		r.run("merge", "--no-ff", "main", "-m", "sync main")
		if got := r.evidence(t); got != "OWN UNMERGED" {
			t.Fatalf("evidence: %s", got)
		}
	})
	t.Run("wire cap", func(t *testing.T) {
		r := newLaunchedRepo(t)
		r.launchNow(t)
		for i := 0; i < 25; i++ {
			r.run("commit", "--allow-empty", "-m", fmt.Sprint("own ", i))
		}
		if got := runCommits(t.Context(), r.dir, r.launch, r.launchDefault); len(got) != gitCommitsWire || got[0].Subject != "own 24" {
			t.Fatalf("cap: %d", len(got))
		}
	})
}

// No origin and no main/master: the worktree's own branch is not the
// default branch.
func TestUnknownDefaultBranchIsNotHEAD(t *testing.T) {
	dir, git := evidenceRepo(t)
	git(dir, "init", "-b", "trunk")
	git(dir, "commit", "--allow-empty", "-m", "base")
	if ref := defaultBranchRef(t.Context(), dir); ref != "" {
		t.Fatalf("default branch resolved to %q", ref)
	}
	if rev := launchDefaultRev(t.Context(), dir); rev != "" {
		t.Fatalf("launch default %q", rev)
	}
	git(dir, "config", "--local", "init.defaultBranch", "trunk")
	if ref := defaultBranchRef(t.Context(), dir); ref != "trunk" {
		t.Fatalf("configured default: %q", ref)
	}
}

// origin/HEAD wins over local branches.
func TestOriginHEADIsTheDefaultBranch(t *testing.T) {
	root, git := evidenceRepo(t)
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	git(root, "init", "--bare", "-b", "trunk", origin)
	git(root, "clone", "-q", origin, work)
	git(work, "commit", "--allow-empty", "-m", "base")
	git(work, "push", "-q", "origin", "HEAD:trunk")
	git(work, "remote", "set-head", "origin", "trunk")
	tip := workspaceHEAD(t.Context(), work)
	git(work, "checkout", "-b", "main")
	git(work, "commit", "--allow-empty", "-m", "local main only")
	if ref := defaultBranchRef(t.Context(), work); ref != "origin/trunk" {
		t.Fatalf("default branch %q", ref)
	}
	if rev := launchDefaultRev(t.Context(), work); rev != tip {
		t.Fatalf("launch default %q want %q", rev, tip)
	}
}

// The supervisor persists the launch revisions with the run and reports
// only the run's own commits when it finishes.
func TestSupervisorPersistsLaunchAndReportsRunCommits(t *testing.T) {
	s, a, p := testSupervisor(t)
	_, git := evidenceRepo(t)
	git(s.workspace, "init", "-b", "main")
	git(s.workspace, "commit", "--allow-empty", "-m", "base")
	launch := workspaceHEAD(t.Context(), s.workspace)
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var persisted bool
	for _, rec := range s.journal.Snapshot() {
		persisted = persisted || (rec.RunID == "run" && rec.LaunchRev == launch && rec.LaunchDefaultRev == launch)
	}
	if !persisted {
		t.Fatalf("launch revisions not in the journal: %+v", s.journal.Snapshot())
	}
	git(s.workspace, "commit", "--allow-empty", "-m", "run work")
	if err := p.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry := s.runs["run"]
	entry.mu.Lock()
	done := entry.monitorDone
	entry.mu.Unlock()
	<-done
	a.mu.Lock()
	defer a.mu.Unlock()
	var finished *Telemetry
	for i := range a.reports {
		if a.reports[i].Kind == "finished" {
			finished = &a.reports[i]
		}
	}
	if finished == nil || len(finished.GitCommits) != 1 || finished.GitCommits[0].Subject != "run work" || finished.GitCommits[0].OnDefaultBranch {
		t.Fatalf("finished evidence: %+v", finished)
	}
}
