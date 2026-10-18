// SPDX-License-Identifier: AGPL-3.0-only
package escalation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDistinctFailureEvidence(t *testing.T) {
	s := State{Status: "observing"}
	apply := func(kind, body string) {
		t.Helper()
		if err := s.apply(kind, json.RawMessage(body)); err != nil {
			t.Fatal(err)
		}
	}
	apply("fix_round", `{"round":1}`)
	apply("fix_round", `{"round":1}`)
	apply("fix_round", `{"round":0}`)
	if s.FixRounds != 1 || s.Status != "observing" {
		t.Fatal(s)
	}
	apply("fix_round", `{"round":2}`)
	apply("fix_round", `{"round":3}`)
	if s.Status != "stuck" || s.Reason != "failed_fix_rounds" {
		t.Fatal(s)
	}
	apply("review_verdict", `{"verdict":"ok"}`)
	if s.Status != "resolved" || s.PlannedProfile != "" {
		t.Fatal(s)
	}
	s = State{Status: "observing"}
	finding := strings.Repeat("a", 64)
	apply("review_verdict", fmt.Sprintf(`{"verdict":"changes","round":1,"findings_fingerprint":%q}`, finding))
	apply("review_verdict", fmt.Sprintf(`{"verdict":"changes","round":1,"findings_fingerprint":%q}`, finding))
	if s.Status != "observing" {
		t.Fatal("same round counted twice")
	}
	apply("review_verdict", fmt.Sprintf(`{"verdict":"changes","round":2,"findings_fingerprint":%q}`, strings.Repeat("b", 64)))
	if s.Status != "observing" {
		t.Fatal("different findings counted as a repetition")
	}
	apply("review_verdict", fmt.Sprintf(`{"verdict":"changes","round":3,"findings_fingerprint":%q}`, finding))
	if s.Reason != "repeated_findings" {
		t.Fatal(s)
	}
}
func TestCICheckIdentityAndBounds(t *testing.T) {
	s := State{Status: "observing"}
	send := func(repo string, pr int, name, result, attempt string) {
		t.Helper()
		raw, _ := json.Marshal(Signal{Repo: repo, Number: pr, Name: name, Result: result, AttemptID: attempt})
		if err := s.apply("ci_result", raw); err != nil {
			t.Fatal(err)
		}
	}
	send("a/b", 1, "tests", "fail", "one")
	send("a/b", 1, "tests", "fail", "one")
	send("a/b", 2, "tests", "fail", "two")
	send("a/b", 1, "lint", "fail", "two")
	send("c/d", 1, "tests", "fail", "two")
	send("a/b", 1, "tests", "fail", "")
	if s.Status != "observing" {
		t.Fatal("different execution identities conflated", s)
	}
	send("a/b", 1, "tests", "pass", "three")
	send("a/b", 1, "tests", "fail", "four")
	if s.Status != "observing" {
		t.Fatal("pass did not reset check")
	}
	send("a/b", 1, "tests", "fail", "five")
	if s.Reason != "repeated_ci_failure" {
		t.Fatal(s)
	}
	s = State{Status: "observing"}
	for i := 0; i < 33; i++ {
		send("a/b", 1, fmt.Sprint(i), "fail", "one")
	}
	if len(s.Checks) != 32 || s.Reason != "evidence_limit" {
		t.Fatal("unbounded evidence", s)
	}
	raw, _ := json.Marshal(s.Public())
	if strings.Contains(string(raw), "checks") || strings.Contains(string(raw), "last_finding") {
		t.Fatal("private evidence leaked", string(raw))
	}
}
