// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	heartbeatCommitWalk = 200
	heartbeatReflogWalk = 2000
)

func heartbeatCommits(ctx context.Context, o heartbeatOptions, dep heartbeatDeps, disk *heartbeatDisk) []heartbeatCommit {
	since := disk.CommitCursor
	if !validCommitSHA(since) {
		since = disk.StartRev
	}
	if dep.commits != nil {
		found, err := dep.commits(o.Worktree, since)
		if err != nil {
			return nil
		}
		return selectHeartbeatCommits(found, nil)
	}
	batch, skipped := localHeartbeatCommits(ctx, o.Worktree, disk)
	if len(batch) == 0 && validCommitSHA(skipped) && skipped != disk.CommitCursor {
		disk.CommitCursor = skipped
	}
	return batch
}

func selectHeartbeatCommits(found []heartbeatCommit, sent map[string]bool) []heartbeatCommit {
	out := make([]heartbeatCommit, 0, len(found))
	seen := map[string]bool{}
	for _, c := range found {
		sha := strings.ToLower(c.SHA)
		subject := heartbeatText(c.Subject, 200)
		if subject == "" || !validCommitSHA(sha) || sent[sha] || seen[sha] {
			continue
		}
		seen[sha] = true
		out = append(out, heartbeatCommit{SHA: sha, Subject: subject})
		if len(out) == heartbeatMaxCommits {
			break
		}
	}
	return out
}

// localHeartbeatCommits reports commits created on the bound branch after the
// helper started. Reflog actions distinguish those commits from history that
// arrived by fast-forward, merge, or a rebase that only moved the ref.
// Replayed rebase commits come from HEAD. The cursor walks oldest-first so a
// long history cannot crowd out a later commit.
func localHeartbeatCommits(ctx context.Context, worktree string, disk *heartbeatDisk) (batch []heartbeatCommit, skipped string) {
	if strings.TrimSpace(worktree) == "" || disk == nil || filepath.Clean(worktree) != filepath.Clean(disk.BoundWorktree) {
		return nil, ""
	}
	if disk.StartedUnix <= 0 || !validHeartbeatBranch(disk.BoundBranch) || !validCommitSHA(disk.StartRev) {
		return nil, ""
	}
	branch, err := gitLine(ctx, worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch != disk.BoundBranch {
		return nil, ""
	}
	cursor := disk.CommitCursor
	if !validCommitSHA(cursor) {
		cursor = disk.StartRev
	}
	local, err := localReflogSHAs(ctx, worktree, disk)
	if err != nil {
		return nil, ""
	}
	lines, err := gitLines(ctx, worktree, heartbeatCommitWalk, "log", "--first-parent", "--reverse", "--format=%H%x1f%s", cursor+"..HEAD")
	if err != nil {
		return nil, ""
	}
	var found []heartbeatCommit
	for _, line := range lines {
		sha, subject, ok := strings.Cut(line, "\x1f")
		sha = strings.ToLower(strings.TrimSpace(sha))
		if !ok || !validCommitSHA(sha) {
			continue
		}
		if !local[sha] {
			if len(found) == 0 {
				skipped = sha
			}
			continue
		}
		found = append(found, heartbeatCommit{SHA: sha, Subject: subject})
		if len(found) == heartbeatMaxCommits {
			break
		}
	}
	return selectHeartbeatCommits(found, nil), skipped
}

func localReflogSHAs(ctx context.Context, worktree string, disk *heartbeatDisk) (map[string]bool, error) {
	local := map[string]bool{}
	if err := collectReflog(ctx, worktree, disk, disk.BoundBranch, local, false); err != nil {
		return nil, err
	}
	// Replayed commits are named on HEAD (`rebase (pick)`). The branch reflog
	// only records `rebase (finish)`, which also matches a fast-forward.
	_ = collectReflog(ctx, worktree, disk, "HEAD", local, true)
	return local, nil
}

func collectReflog(ctx context.Context, worktree string, disk *heartbeatDisk, ref string, local map[string]bool, rebaseOnly bool) error {
	lines, err := gitLines(ctx, worktree, heartbeatReflogWalk, "reflog", "show", "--date=unix", "--format=%H%x1f%gd%x1f%gs", ref)
	if err != nil {
		return err
	}
	for _, line := range lines {
		sha, rest, ok := strings.Cut(line, "\x1f")
		selector, action, ok2 := strings.Cut(rest, "\x1f")
		sha = strings.ToLower(strings.TrimSpace(sha))
		if !ok || !ok2 || !validCommitSHA(sha) || !acceptReflogAction(action, rebaseOnly) {
			continue
		}
		when, ok := reflogUnix(selector)
		if !ok || when < disk.StartedUnix {
			continue
		}
		local[sha] = true
	}
	return nil
}

func acceptReflogAction(msg string, rebaseOnly bool) bool {
	if rebaseOnly {
		return rebaseCreatesCommit(msg)
	}
	return localReflogAction(msg)
}

func localReflogAction(msg string) bool {
	msg = strings.TrimSpace(msg)
	if strings.Contains(msg, "Fast-forward") {
		return false
	}
	switch {
	case strings.HasPrefix(msg, "commit"):
		return true
	case strings.HasPrefix(msg, "cherry-pick"):
		return true
	case rebaseCreatesCommit(msg):
		return true
	case strings.HasPrefix(msg, "merge"):
		return true
	default:
		return false
	}
}

// rebaseCreatesCommit is true for a rebase step that writes a commit.
// start, finish and abort only move a ref onto a commit that already exists.
func rebaseCreatesCommit(msg string) bool {
	msg = strings.TrimSpace(msg)
	const prefix = "rebase ("
	if !strings.HasPrefix(msg, prefix) {
		return false
	}
	op, _, ok := strings.Cut(msg[len(prefix):], ")")
	if !ok {
		return false
	}
	switch op {
	case "pick", "reword", "edit", "squash", "fixup", "continue":
		return true
	default:
		return false
	}
}

func reflogUnix(selector string) (int64, bool) {
	i := strings.LastIndexByte(selector, '{')
	j := strings.LastIndexByte(selector, '}')
	if i < 0 || j <= i+1 {
		return 0, false
	}
	n, err := strconv.ParseInt(selector[i+1:j], 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func validHeartbeatBranch(branch string) bool {
	if branch == "" || branch == "HEAD" || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") || len(branch) > 200 {
		return false
	}
	if strings.ContainsAny(branch, " \t\r\n\\~^:?*[") {
		return false
	}
	return true
}

func gitHEAD(ctx context.Context, worktree string) (string, error) {
	if strings.TrimSpace(worktree) == "" {
		return "", nil
	}
	sha, err := gitLine(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha = strings.ToLower(sha)
	if !validCommitSHA(sha) {
		return "", errors.New("invalid HEAD")
	}
	return sha, nil
}

func gitLine(ctx context.Context, worktree string, args ...string) (string, error) {
	lines, err := gitLines(ctx, worktree, 1, args...)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", errors.New("empty git output")
	}
	return lines[0], nil
}

func gitLines(ctx context.Context, worktree string, maxLines int, args ...string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...)
	cmd.Dir = worktree
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_PAGER=cat")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stdout, 1<<20))
	_ = stdout.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if len(raw) == 1<<20 {
		if i := strings.LastIndexByte(string(raw), '\n'); i >= 0 {
			raw = raw[:i]
		}
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if maxLines > 0 && len(lines) >= maxLines {
			break
		}
	}
	return lines, nil
}

func validCommitSHA(sha string) bool {
	if len(sha) < 7 || len(sha) > 40 {
		return false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
