// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"
)

func TestCandidateRunWorkflowNameBounds(t *testing.T) {
	for _, test := range []struct {
		name, workflow string
		valid          bool
	}{
		{"255 bytes", strings.Repeat("a", 255), true},
		{"256 bytes", strings.Repeat("a", 256), false},
		{"255 UTF-8 bytes", strings.Repeat("é", 127) + "a", true},
		{"256 UTF-8 bytes", strings.Repeat("é", 128), false},
		{"newline", "Release\nrehearsal", false},
		{"tab", "Release\trehearsal", false},
		{"null", "Release\x00rehearsal", false},
		{"delete", "Release\x7frehearsal", false},
		{"Unicode control", "Release\u0085rehearsal", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := candidateRun("inspr-at/paimos", "42", test.workflow, "")
			if !test.valid {
				if run != nil {
					t.Fatal("invalid workflow name must leave CI pending")
				}
				return
			}
			if run == nil || run.Name != test.workflow || run.Status != "in_progress" || run.Conclusion != "" {
				t.Fatalf("valid workflow identity lost: %+v", run)
			}
		})
	}
}

func TestCandidateRunIsActualAndUnfinished(t *testing.T) {
	run := candidateRun("inspr-at/paimos", "42", "Release rehearsal", "")
	if run == nil || run.URL != "https://github.com/inspr-at/paimos/actions/runs/42" || run.Name != "Release rehearsal" || run.Status != "in_progress" || run.Conclusion != "" {
		t.Fatalf("candidate run: %+v", run)
	}
	if candidateRun("inspr-at/paimos", "42", "", "") != nil {
		t.Fatal("missing workflow identity must stay pending")
	}
	for _, id := range []string{"", "0", "pending", "42?token=bad", "999999999999999999999"} {
		if candidateRun("inspr-at/paimos", id, "Release rehearsal", "") != nil {
			t.Fatalf("invalid run ID accepted: %q", id)
		}
	}
	for _, server := range []string{"http://github.com", "https://user:pass@github.com", "https://github.com?token=bad", "https://github.com/path"} {
		if candidateRun("inspr-at/paimos", "42", "Release rehearsal", server) != nil {
			t.Fatal("invalid server accepted")
		}
	}
}
