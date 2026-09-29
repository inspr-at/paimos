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
	out, err := gitOutput(ctx, workspace, "log", "--format=%H%x1f%s", start+"..HEAD")
	if err != nil {
		return nil
	}
	var commits []GitCommit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha, subject, ok := strings.Cut(line, "\x1f")
		sha = strings.ToLower(strings.TrimSpace(sha))
		if !ok || !fullSHA(sha) {
			continue
		}
		commits = append(commits, GitCommit{SHA: sha, Subject: cleanCommitSubject(subject)})
		if len(commits) == 20 {
			break
		}
	}
	return commits
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
