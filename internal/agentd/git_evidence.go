// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

func workspaceHEAD(ctx context.Context, workspace string) string {
	out, err := gitOutput(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	rev := strings.ToLower(strings.TrimSpace(out))
	if !fullSHA(rev) {
		return ""
	}
	return rev
}

// gitCommitsWire is the telemetry contract's cap on git_commits.
const gitCommitsWire = 20

// launchDefaultRev is the commit the default branch named at launch, or ""
// when the repository has no resolvable default branch.
func launchDefaultRev(ctx context.Context, workspace string) string {
	ref := defaultBranchRef(ctx, workspace)
	if ref == "" {
		return ""
	}
	full := "refs/heads/" + ref
	if strings.HasPrefix(ref, "origin/") {
		full = "refs/remotes/" + ref
	}
	out, err := gitOutput(ctx, workspace, "rev-parse", "--verify", "--quiet", full+"^{commit}")
	if err != nil {
		return ""
	}
	rev := strings.ToLower(strings.TrimSpace(out))
	if !fullSHA(rev) {
		return ""
	}
	return rev
}

// remoteDefaultRev is the commit the remote default branch (origin/HEAD or
// its fallbacks) names now, or "" when the default branch is not a remote ref.
func remoteDefaultRev(ctx context.Context, workspace string) string {
	ref := defaultBranchRef(ctx, workspace)
	if !strings.HasPrefix(ref, "origin/") {
		return ""
	}
	out, err := gitOutput(ctx, workspace, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref+"^{commit}")
	if err != nil {
		return ""
	}
	rev := strings.ToLower(strings.TrimSpace(out))
	if !fullSHA(rev) {
		return ""
	}
	return rev
}

// runCommits lists the run's commit evidence: non-merge commits on the first-
// parent history of the final HEAD that neither the launch commit nor the
// default branch as it was at launch contains. A merge brings its other
// parents' history in without making it the run's work, so an upstream sync
// adds nothing; a fast-forward to history that existed at launch, or that
// the remote default branch holds at the end, adds nothing either. The server derives only committed or no_commit from this;
// merged and pr_opened come from forge or release evidence, never from
// local git.
func runCommits(ctx context.Context, workspace, launch, launchDefault string) []GitCommit {
	if strings.TrimSpace(workspace) == "" || !fullSHA(launch) {
		return nil
	}
	args := []string{"log", "--first-parent", "--no-merges", "--format=%H%x1f%s", fmt.Sprintf("--max-count=%d", gitCommitsWire), "HEAD", "^" + launch}
	if fullSHA(launchDefault) {
		args = append(args, "^"+launchDefault)
	}
	// Upstream work fetched during the run and fast-forwarded onto is on the
	// remote default branch now; it is not the run's work either.
	if remote := remoteDefaultRev(ctx, workspace); fullSHA(remote) {
		args = append(args, "^"+remote)
	}
	out, err := gitOutput(ctx, workspace, append(args, "--")...)
	if err != nil {
		return nil
	}
	var commits []GitCommit
	for _, line := range strings.Split(out, "\n") {
		sha, subject, ok := strings.Cut(strings.TrimSpace(line), "\x1f")
		sha = strings.ToLower(strings.TrimSpace(sha))
		if !ok || !fullSHA(sha) {
			continue
		}
		commits = append(commits, GitCommit{SHA: sha, Subject: cleanCommitSubject(subject), Parents: 1})
	}
	return commits
}

// defaultBranchRef names the repository's default branch: origin/HEAD, then
// the repository's own init.defaultBranch, then origin/main|master, then a
// local main|master. It never falls back to HEAD: a worktree's own branch is
// not the default branch, so an unknown default yields "" and nothing is
// reported as on the default branch.
func defaultBranchRef(ctx context.Context, workspace string) string {
	if out, err := gitOutput(ctx, workspace, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimSpace(out); branchRefName(name) && refExists(ctx, workspace, name) {
			return name
		}
	}
	var candidates []string
	if out, err := gitOutput(ctx, workspace, "config", "--local", "--get", "init.defaultBranch"); err == nil {
		if name := strings.TrimSpace(out); branchRefName(name) {
			candidates = append(candidates, "origin/"+name, name)
		}
	}
	candidates = append(candidates, "origin/main", "origin/master", "main", "master")
	for _, name := range candidates {
		if refExists(ctx, workspace, name) {
			return name
		}
	}
	return ""
}

func refExists(ctx context.Context, workspace, name string) bool {
	full := "refs/heads/" + name
	if strings.HasPrefix(name, "origin/") {
		full = "refs/remotes/" + name
	}
	_, err := gitOutput(ctx, workspace, "rev-parse", "--verify", "--quiet", full+"^{commit}")
	return err == nil
}

func branchRefName(name string) bool {
	if name == "" || name == "HEAD" || strings.HasSuffix(name, "/HEAD") || len(name) > 200 || strings.HasPrefix(name, "-") || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	return true
}

func gitOutput(ctx context.Context, workspace string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...)
	cmd.Dir = workspace
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_PAGER=cat")
	raw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func fullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if r < '0' || (r > '9' && r < 'a') || r > 'f' {
			return false
		}
	}
	return true
}

func cleanCommitSubject(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if utf8.RuneCountInString(b.String()) == 200 {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "commit"
	}
	return b.String()
}
