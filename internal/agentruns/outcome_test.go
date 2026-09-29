// SPDX-License-Identifier: AGPL-3.0-only

package agentruns

import "testing"

func TestDeriveOutcomeDetail(t *testing.T) {
	sha := "0123456789abcdef"
	cases := []struct {
		status string
		shas   []string
		refs   []string
		want   string
	}{
		{"completed", []string{sha}, nil, "committed"},
		{"completed", []string{"  " + sha + " "}, nil, "committed"},
		{"completed", nil, []string{"https://example.com/acme/repo/commit/" + sha}, "committed"},
		{"completed", nil, []string{"https://example.com/acme/repo/pull/4"}, "pr_opened"},
		{"completed", []string{sha}, []string{"https://example.com/acme/repo/pull/4"}, "pr_opened"},
		{"completed", nil, []string{"https://example.com/acme/repo/pulls/4"}, "pr_opened"},
		{"completed", nil, []string{"https://example.com/acme/repo/merge_requests/4"}, "pr_opened"},
		{"completed", nil, []string{"https://example.com/acme/repo/pull/4/merge"}, "merged"},
		{"completed", nil, []string{"https://example.com/acme/repo/merge_requests/4/merge"}, "merged"},
		{"completed", nil, []string{"https://example.com/acme/repo/pull/4?merged=true"}, "merged"},
		{"cancelled", nil, nil, "abandoned"},
		{"ownership_lost", nil, nil, "abandoned"},
		{"cancelled", []string{"abc"}, nil, "abandoned"},
		{"completed", nil, nil, "no_commit"},
		{"failed", nil, nil, "no_commit"},
		{"cancelled", []string{sha}, nil, "committed"},
	}
	for _, tc := range cases {
		if got := deriveOutcomeDetail(tc.status, tc.shas, tc.refs); got != tc.want {
			t.Fatalf("status %s shas %v refs %v: got %s want %s", tc.status, tc.shas, tc.refs, got, tc.want)
		}
	}
	if commitSubject(" Add usage") || commitSubject("bad\nsubject") || !commitSubject("Add usage") {
		t.Fatal("commit subject fence")
	}
}
