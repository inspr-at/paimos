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

// gitEvidenceLimit bounds every git listing behind run evidence. Hitting it
// means the evidence is incomplete, and nothing is reported as merged.
const gitEvidenceLimit = 2000

// gitCommitsWire is the telemetry contract's cap on git_commits.
const gitCommitsWire = 20

type rangeCommit struct {
	sha, subject string
	parents      int
}

// commitsSince reports the commits this run created after its launch
// revision: commits in start..HEAD that the branch the run ends on recorded
// as created in its reflog (commit, merge commit, cherry-pick, revert), plus
// rebase picks from the HEAD reflog. Upstream commits synced in by a merge
// or fast-forward are not the run's own. When the run ends on the default
// branch itself, the commits its own merges brought in count too.
// on_default_branch is set only when the evidence is complete; unmerged
// non-merge commits are listed first so the 20-item cap never hides one.
func commitsSince(ctx context.Context, workspace, start string) []GitCommit {
	return commitsSinceBounded(ctx, workspace, start, gitEvidenceLimit)
}

func commitsSinceBounded(ctx context.Context, workspace, start string, limit int) []GitCommit {
	if strings.TrimSpace(workspace) == "" || !fullSHA(start) || limit <= 0 {
		return nil
	}
	out, err := gitOutput(ctx, workspace, "log", "--format=%H%x1f%P%x1f%s", fmt.Sprintf("--max-count=%d", limit+1), start+"..HEAD")
	if err != nil {
		return nil
	}
	var span []rangeCommit
	inRange := map[string]int{}
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
		inRange[sha] = count
		span = append(span, rangeCommit{sha: sha, subject: subject, parents: count})
	}
	complete := len(span) <= limit
	if len(span) == 0 {
		return nil
	}
	def := defaultBranchRef(ctx, workspace)
	own, ownComplete := ownCommits(ctx, workspace, def, inRange, limit)
	complete = complete && ownComplete
	unmerged, unmergedComplete := unmergedSince(ctx, workspace, start, def, limit)
	complete = complete && unmergedComplete
	var first, rest []GitCommit
	for _, c := range span {
		if !own[c.sha] {
			continue
		}
		gc := GitCommit{SHA: c.sha, Subject: cleanCommitSubject(c.subject), Parents: c.parents, OnDefaultBranch: complete && def != "" && !unmerged[c.sha]}
		if c.parents < 2 && !gc.OnDefaultBranch {
			first = append(first, gc)
		} else {
			rest = append(rest, gc)
		}
	}
	commits := append(first, rest...)
	if len(commits) > gitCommitsWire {
		commits = commits[:gitCommitsWire]
	}
	return commits
}

// ownCommits returns the commits of inRange that this workspace created on
// the branch HEAD names (HEAD itself when detached). complete is false when
// a reflog listing hit the evidence bound or could not be read.
func ownCommits(ctx context.Context, workspace, def string, inRange map[string]int, limit int) (map[string]bool, bool) {
	own := map[string]bool{}
	ref, branch := "HEAD", ""
	if out, err := gitOutput(ctx, workspace, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		if name := strings.TrimSpace(out); branchRefName(name) {
			ref, branch = "refs/heads/"+name, name
		}
	}
	complete := true
	note := func(ref string, created func(string) bool) {
		out, err := gitOutput(ctx, workspace, "reflog", "show", "--format=%H%x1f%gs", fmt.Sprintf("-n%d", limit+1), ref, "--")
		if err != nil {
			complete = false
			return
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) > limit {
			complete = false
		}
		for _, line := range lines {
			sha, subject, ok := strings.Cut(strings.TrimSpace(line), "\x1f")
			sha = strings.ToLower(sha)
			if _, in := inRange[sha]; ok && in && created(subject) {
				own[sha] = true
			}
		}
	}
	note(ref, reflogCreated)
	if ref != "HEAD" {
		note("HEAD", reflogRebasePick)
	}
	if branch != "" && (def == branch || def == "origin/"+branch) {
		for sha := range own {
			if inRange[sha] < 2 {
				continue
			}
			out, err := gitOutput(ctx, workspace, "rev-list", fmt.Sprintf("--max-count=%d", limit+1), sha, "^"+sha+"^1")
			if err != nil {
				complete = false
				continue
			}
			side := strings.Fields(out)
			if len(side) > limit {
				complete = false
			}
			for _, c := range side {
				if _, in := inRange[strings.ToLower(c)]; in {
					own[strings.ToLower(c)] = true
				}
			}
		}
	}
	return own, complete
}

// reflogCreated reports whether a branch reflog entry created its commit.
// Fast-forwards, resets, checkouts and branch creation move to an existing
// commit and are not the run's work.
func reflogCreated(subject string) bool {
	action, detail, _ := strings.Cut(subject, ": ")
	switch {
	case strings.HasPrefix(action, "commit"), action == "cherry-pick", action == "revert", action == "am":
		return true
	case strings.HasPrefix(action, "merge ") || action == "pull" || strings.HasPrefix(action, "pull "):
		return strings.HasPrefix(detail, "Merge made by")
	}
	return false
}

// reflogRebasePick reports a HEAD reflog entry that rewrote one of the
// run's commits during a rebase.
func reflogRebasePick(subject string) bool {
	action, _, _ := strings.Cut(subject, ": ")
	if !strings.HasPrefix(action, "rebase") && !strings.HasPrefix(action, "pull --rebase") {
		return false
	}
	for _, step := range []string{"(pick)", "(squash)", "(fixup)", "(reword)", "(edit)"} {
		if strings.HasSuffix(action, step) {
			return true
		}
	}
	return false
}

// unmergedSince lists commits in start..HEAD that the default branch does
// not contain. complete is false when the default is unknown or the listing
// hit the evidence bound.
func unmergedSince(ctx context.Context, workspace, start, def string, limit int) (map[string]bool, bool) {
	unmerged := map[string]bool{}
	if def == "" {
		return unmerged, false
	}
	out, err := gitOutput(ctx, workspace, "rev-list", fmt.Sprintf("--max-count=%d", limit+1), "HEAD", "^"+start, "^"+def, "--")
	if err != nil {
		return unmerged, false
	}
	shas := strings.Fields(out)
	for _, sha := range shas {
		unmerged[strings.ToLower(sha)] = true
	}
	return unmerged, len(shas) <= limit
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
