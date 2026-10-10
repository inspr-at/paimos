// SPDX-License-Identifier: AGPL-3.0-only
package routineguard

import (
	"strings"
	"testing"
)

// Risks R1/R12/R15: silent scope loosening, mutable hard rules/script hashes,
// malicious artifact paths and missing required evaluation authorizing effects.
func TestGuardrailFloorScopeEvidenceAndBoundedCheckpoints(t *testing.T) {
	const id = "10000000-0000-4000-8000-000000000001"
	admin := Actor{ID: id, Person: true, Administrator: true, Reason: "Approved scoped exception"}
	sensitive := recommendedRules()[0]
	sensitive.Result = Block
	tenant, err := Change(nil, Source{Scope: Scope{Kind: "tenant", ID: id}}, []Rule{sensitive}, 0, admin)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Checkpoint: "action", Action: "work.update", Text: "Update password handling", PayloadBytes: 25}
	d, err := Evaluate([]Source{tenant}, c)
	if err != nil || d.Result != Block || d.DeterministicResult != Block || len(d.Findings) != 7 || d.RequiredEvaluation == nil || d.CanExecute() {
		t.Fatalf("all rules/strictest outcome: %+v %v", d, err)
	}
	weakened := sensitive
	weakened.Result = Allow
	for _, actor := range []Actor{{ID: id, Person: true}, {ID: id, Person: true, Administrator: true}, {ID: id, Administrator: true, Reason: "agent claimed admin"}} {
		if _, err := Change([]Source{tenant}, Source{Scope: Scope{Kind: "project", ID: id}}, []Rule{weakened}, 0, actor); err == nil {
			t.Fatal("unauthorized loosening allowed")
		}
	}
	project, err := Change([]Source{tenant}, Source{Scope: Scope{Kind: "project", ID: id}}, []Rule{weakened}, 0, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Loosenings) != 1 || project.Loosenings[0].ActorID != id || project.Loosenings[0].Reason != admin.Reason || project.Loosenings[0].ExpectedRevision != 0 {
		t.Fatal("override lost identity/reason/revision")
	}
	d, err = Evaluate([]Source{tenant, project}, c)
	if err != nil || d.DeterministicResult != Allow || d.Result != NeedsPerson || d.RequiredEvaluation == nil || d.RequiredEvaluation.PolicyDigest != d.PolicyDigest || d.RequiredEvaluation.ContextDigest != d.ContextDigest || d.CanExecute() {
		t.Fatalf("mandatory hard evaluation was bypassed: %+v %v", d, err)
	}
	changed := tenant
	changed.Revision++
	if _, err := Evaluate([]Source{changed, project}, c); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale inherited override accepted: %v", err)
	}
	if _, err := Change(nil, tenant, nil, 1, Actor{ID: id, Person: true}); err == nil {
		t.Fatal("ordinary person removed restriction")
	}
	if _, err := Change(nil, tenant, nil, 0, admin); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("stale edit accepted: %v", err)
	}
	for _, rule := range []Rule{hardRules()[0], {ID: "custom.script", Checkpoint: "action", Method: "template", Template: "protected_paths_v1", ScriptHash: strings.Repeat("a", 64), Result: Allow}, {ID: "custom.shell", Checkpoint: "both", Method: "shell", Values: []string{"echo allowed"}, Result: Allow}} {
		if err := ValidateRules([]Rule{rule}); err == nil {
			t.Fatal("mutable hard rule/hash or arbitrary script accepted")
		}
	}
	for _, checkpoint := range []string{"save", "action"} {
		d, err := Evaluate(nil, Context{Checkpoint: checkpoint, Action: "pr.open", Artifacts: []ArtifactChange{{BeforePath: "internal/routineguard/policy.go", AfterPath: "ordinary/code.go"}}})
		if err != nil || d.Result != Block || d.CanExecute() {
			t.Fatalf("own rule artifact modification allowed: %+v %v", d, err)
		}
	}
	for _, context := range []Context{{Checkpoint: "action", Action: "deploy"}, {Checkpoint: "action", Action: "work.update", ChangesGuardrails: true}, {Checkpoint: "save", Text: "steal credentials"}} {
		d, err := Evaluate(nil, context)
		if err != nil || d.Result != Block {
			t.Fatalf("hard block not enforced: %+v %v", d, err)
		}
	}
	for _, context := range []Context{{Checkpoint: "action", Text: strings.Repeat("x", MaxTextBytes+1)}, {Checkpoint: "action", PayloadBytes: MaxTextBytes + 1}, {Checkpoint: "action", Paths: []string{"../rules"}}, {Checkpoint: "action", Paths: []string{"ordinary/../../gate"}}, {Checkpoint: "action", Paths: []string{"scripts\\gate.sh"}}, {Checkpoint: "action", Paths: []string{strings.Repeat("x", MaxPathBytes+1)}}, {Checkpoint: "action", Paths: make([]string, MaxPaths+1)}} {
		d, err := Evaluate(nil, context)
		if err == nil || d.Result != Block || len(d.Findings) != 0 {
			t.Fatalf("unbounded/unsafe input reached matching: %+v %v", d, err)
		}
	}
	d, err = Evaluate(nil, Context{Checkpoint: "save", Text: "Review ordinary code"})
	if err != nil || d.Result != Allow || d.CanExecute() {
		t.Fatal("save result granted action execution")
	}
	if (Decision{Result: Allow}).CanExecute() {
		t.Fatal("absent hard evidence authorized execution")
	}
}
