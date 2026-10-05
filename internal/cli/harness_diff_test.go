// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"testing"
)

func TestCommitDiffEvidenceFromOwnedGitCommit(t *testing.T) {
	repo, _ := gitRepo(t)
	gitCommit(t, repo, "two")
	head, err := gitHEAD(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	added, deleted, files := gitCommitDiffStats(context.Background(), repo, head)
	if added == nil || deleted == nil || files == nil || *added != 1 || *deleted != 1 || *files != 1 {
		t.Fatalf("diff: %v/%v/%v", added, deleted, files)
	}
	batch := selectHeartbeatCommits([]heartbeatCommit{{SHA: head, Subject: "two", LinesAdded: added, LinesDeleted: deleted, FilesChanged: files}}, nil)
	if len(batch) != 1 || batch[0].LinesAdded == nil || *batch[0].LinesAdded != 1 {
		t.Fatal("selection dropped diff evidence")
	}
}
func TestCommitNumstatNeverTurnsMissingOrBinaryEvidenceIntoZero(t *testing.T) {
	for _, raw := range []string{"-\t-\tbinary\n", "1\t2\tfile", "invalid\n", "1000000001\t0\tfile\n", "-1\t0\tfile\n"} {
		a, d, f := parseCommitNumstat([]byte(raw))
		if a != nil || d != nil || f != nil {
			t.Fatalf("invalid numstat %q became measured", raw)
		}
	}
	a, d, f := parseCommitNumstat(nil)
	if a == nil || d == nil || f == nil || *a != 0 || *d != 0 || *f != 0 {
		t.Fatal("successful empty numstat is measured zero")
	}
}
