// SPDX-License-Identifier: AGPL-3.0-only

package agentruns

import "testing"

func TestDeriveOutcomeDetail(t *testing.T) {
	sha := "0123456789abcdef"
	cases := []struct {
		status  string
		commits []GitCommit
		shas    []string
		refs    []string
		want    string
	}{
		{"completed", nil, []string{sha}, nil, "committed"},
		{"completed", nil, []string{"  " + sha + " "}, nil, "committed"},
		{"completed", nil, nil, []string{"https://example.com/acme/repo/commit/" + sha}, "committed"},
		{"completed", nil, nil, []string{"https://example.com/acme/repo/pull/4"}, "pr_opened"},
		{"completed", nil, []string{sha}, []string{"https://example.com/acme/repo/pull/4"}, "pr_opened"},
		{"completed", nil, nil, []string{"https://example.com/acme/repo/pulls/4"}, "pr_opened"},
		{"completed", nil, nil, []string{"https://example.com/acme/repo/merge_requests/4"}, "pr_opened"},
		{"completed", nil, nil, []string{"https://example.com/acme/repo/pull/4/merge"}, "pr_opened"},
		{"completed", nil, nil, []string{"https://example.com/acme/repo/merge_requests/4/merge"}, "pr_opened"},
		{"completed", nil, nil, []string{"https://example.com/acme/repo/pull/4?merged=true"}, "pr_opened"},
		{"completed", nil, nil, []string{"https://github.com/acme/merged-tools/pull/4"}, "pr_opened"},
		// The subject text is never merge evidence.
		{"completed", []GitCommit{{SHA: sha, Subject: "Merge branch 'main'", Parents: 1}}, nil, nil, "committed"},
		{"completed", []GitCommit{{SHA: sha, Subject: "Merge feature", Parents: 2, OnDefaultBranch: false}}, nil, nil, "committed"},
		// A merge commit alone is no positive evidence that the run's own work merged.
		{"completed", []GitCommit{{SHA: sha, Subject: "Add usage", Parents: 2, OnDefaultBranch: true}}, nil, nil, "committed"},
		{"completed", []GitCommit{{SHA: sha, Subject: "Merge feature", Parents: 2, OnDefaultBranch: true}}, nil, []string{"https://github.com/acme/merged-tools/pull/4"}, "pr_opened"},
		// Local git never yields merged; on_default_branch is ignored (lead decision, round 4).
		{"completed", []GitCommit{{SHA: sha, Subject: "own change", Parents: 1, OnDefaultBranch: true}}, nil, nil, "committed"},
		{"completed", []GitCommit{{SHA: sha, Subject: "own change", Parents: 1, OnDefaultBranch: true}}, nil, []string{"https://github.com/acme/merged-tools/pull/4"}, "pr_opened"},
		// Round 3 review case: no own commits is no_commit.
		{"completed", []GitCommit{}, nil, nil, "no_commit"},
		// Round 3 review case: an unmerged own commit keeps the run committed.
		{"completed", []GitCommit{{SHA: "3123456789abcdef", Subject: "OWN UNMERGED", Parents: 1}, {SHA: sha, Subject: "sync main", Parents: 2}}, nil, nil, "committed"},
		// An upstream merge synced into the worktree does not merge the run's own change.
		{"completed", []GitCommit{{SHA: sha, Subject: "sync main", Parents: 2}, {SHA: "1123456789abcdef", Subject: "unrelated", Parents: 1, OnDefaultBranch: true}, {SHA: "2123456789abcdef", Subject: "upstream merge", Parents: 2, OnDefaultBranch: true}, {SHA: "3123456789abcdef", Subject: "worker change", Parents: 1}}, nil, nil, "committed"},
		{"completed", []GitCommit{{SHA: sha, Subject: "Merge feature", Parents: 2, OnDefaultBranch: true}, {SHA: "3123456789abcdef", Subject: "worker change", Parents: 1, OnDefaultBranch: true}}, nil, nil, "committed"},
		{"cancelled", nil, nil, nil, "abandoned"},
		{"ownership_lost", nil, nil, nil, "abandoned"},
		{"cancelled", nil, []string{"abc"}, nil, "abandoned"},
		{"completed", nil, nil, nil, "no_commit"},
		{"failed", nil, nil, nil, "no_commit"},
		{"cancelled", nil, []string{sha}, nil, "committed"},
	}
	for _, tc := range cases {
		if got := deriveOutcomeDetail(tc.status, tc.commits, tc.shas, tc.refs); got != tc.want {
			t.Fatalf("status %s commits %+v shas %v refs %v: got %s want %s", tc.status, tc.commits, tc.shas, tc.refs, got, tc.want)
		}
	}
	if commitSubject(" Add usage") || commitSubject("bad\nsubject") || !commitSubject("Add usage") {
		t.Fatal("commit subject fence")
	}
}
