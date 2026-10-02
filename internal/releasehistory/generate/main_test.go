// SPDX-License-Identifier: AGPL-3.0-only

package main

import "testing"

func TestCandidateRunIsActualAndUnfinished(t *testing.T) {
	values := map[string]string{"GITHUB_RUN_ID": "42", "GITHUB_WORKFLOW": "Release rehearsal"}
	get := func(key string) string { return values[key] }
	run := candidateRun("inspr-at/paimos", get)
	if run == nil || run.URL != "https://github.com/inspr-at/paimos/actions/runs/42" || run.Name != "Release rehearsal" || run.Status != "in_progress" || run.Conclusion != "" {
		t.Fatalf("candidate run: %+v", run)
	}
	delete(values, "GITHUB_WORKFLOW")
	if candidateRun("inspr-at/paimos", get) != nil {
		t.Fatal("missing workflow identity must stay pending")
	}
	values["GITHUB_WORKFLOW"] = "Release rehearsal"
	for _, id := range []string{"", "0", "pending", "42?token=bad", "999999999999999999999"} {
		values["GITHUB_RUN_ID"] = id
		if candidateRun("inspr-at/paimos", get) != nil {
			t.Fatalf("invalid run ID accepted: %q", id)
		}
	}
	values["GITHUB_RUN_ID"] = "42"
	for _, server := range []string{"http://github.com", "https://user:pass@github.com", "https://github.com?token=bad", "https://github.com/path"} {
		values["GITHUB_SERVER_URL"] = server
		if candidateRun("inspr-at/paimos", get) != nil {
			t.Fatal("invalid server accepted")
		}
	}
}
