// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPreflightPolicyRejectsPoolStaleAttemptWriteAndLayoutChanges(t *testing.T) {
	// Risk: a new manual workflow slips into the Mac pool, borrows CI's
	// identity/concurrency, receives secrets or narrows the browser gate.
	body := readPolicyWorkflows(t)["ci-preflight.yml"]
	if problems, err := checkWorkflow("ci-preflight.yml", body); err != nil || len(problems) != 0 {
		t.Fatalf("draft rejected: %v %v", problems, err)
	}
	for _, change := range []func(map[string]any){
		func(w map[string]any) {
			mapping(mapping(w["jobs"])["browser"])["if"] = "github.ref == 'refs/heads/main'"
		},
		func(w map[string]any) { mapping(mapping(w["jobs"])["browser"])["runs-on"] = "self-hosted" },
		func(w map[string]any) { mapping(w["permissions"])["actions"] = "write" },
		func(w map[string]any) { mapping(w["on"])["pull_request"] = nil },
		func(w map[string]any) { mapping(w["concurrency"])["group"] = ciConcurrencyGroup },
		func(w map[string]any) {
			mapping(mapping(mapping(w["jobs"])["browser"])["strategy"])["matrix"] = map[string]any{"group": []any{"browser-1"}}
		},
	} {
		var w map[string]any
		if err := yaml.Unmarshal(body, &w); err != nil {
			t.Fatal(err)
		}
		change(w)
		raw, err := yaml.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		if problems, err := checkWorkflow("ci-preflight.yml", raw); err != nil || len(problems) == 0 {
			t.Fatalf("unsafe mutation not refused: %v %v", problems, err)
		}
	}
}
