// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
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

func commitsSince(ctx context.Context, workspace, start string) []GitCommit {
	if strings.TrimSpace(workspace) == "" || !fullSHA(start) {
		return nil
	}
	out, err := gitOutput(ctx, workspace, "log", "--format=%H%x1f%P%x1f%s", start+"..HEAD")
	if err != nil {
		return nil
	}
	def := defaultBranchRef(ctx, workspace)
	var commits []GitCommit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha, rest, ok := strings.Cut(line, "\x1f")
		parents, subject, ok2 := strings.Cut(rest, "\x1f")
		sha = strings.ToLower(strings.TrimSpace(sha))
		if !ok || !ok2 || !fullSHA(sha) {
			continue
		}
		count := 0
		for _, parent := range strings.Fields(parents) {
			if fullSHA(strings.ToLower(parent)) {
				count++
			}
		}
		commits = append(commits, GitCommit{
			SHA: sha, Subject: cleanCommitSubject(subject),
			Parents: count, OnDefaultBranch: commitOnDefaultBranch(ctx, workspace, sha, def),
		})
		if len(commits) == 20 {
			break
		}
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

func commitOnDefaultBranch(ctx context.Context, workspace, sha, ref string) bool {
	if ref == "" || !fullSHA(sha) {
		return false
	}
	_, err := gitOutput(ctx, workspace, "merge-base", "--is-ancestor", sha, ref)
	return err == nil
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
