// SPDX-License-Identifier: AGPL-3.0-only

package plugins

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestGrantNarrowCannotWidenOrMutate(t *testing.T) {
	parent := grantOf([]string{fence.PermStageDeploy, fence.PermStageVerify})
	child := parent.Narrow(fence.PermStageDeploy, fence.PermStageAccessApply)
	if !slices.Equal(child.Names(), []string{fence.PermStageDeploy}) {
		t.Fatalf("narrowed grant = %v", child.Names())
	}
	if child.Narrow(fence.PermStageVerify).Allows(fence.PermStageVerify) {
		t.Fatal("child regained removed permission")
	}
	delete(child.perms, fence.PermStageDeploy)
	if !parent.Allows(fence.PermStageDeploy) || !parent.Allows(fence.PermStageVerify) {
		t.Fatal("child mutation changed parent")
	}
	var empty Grant
	if len(empty.Narrow(fence.PermStageDeploy).Names()) != 0 {
		t.Fatal("zero grant widened")
	}
}

func TestHandoffAuthorizationUsesCompiledBindings(t *testing.T) {
	reg, err := Builtin(testStagePlugin("janus"), testStagePlugin("pharos"))
	if err != nil {
		t.Fatal(err)
	}
	p := tenant.Principal{ID: "worker", TenantID: "tenant", Kind: tenant.Agent}
	for _, tc := range []struct {
		id, operation string
		allowed       bool
	}{
		{"janus", "prepare", true}, {"janus", "apply", true},
		{"pharos", "deploy", true}, {"pharos", "verify", true},
		{"janus", "deploy", false}, {"pharos", "prepare", false},
		{"pharos", fence.PermStageDeploy, false}, {"missing", "deploy", false},
	} {
		t.Run(tc.id+"/"+tc.operation, func(t *testing.T) {
			err := reg.AuthorizeHandoff(t.Context(), p, tc.id, tc.operation)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed = %v, error = %v", tc.allowed, err)
			}
		})
	}
	for _, anonymous := range []tenant.Principal{{}, {ID: p.ID}, {TenantID: p.TenantID}} {
		if err := reg.AuthorizeHandoff(t.Context(), anonymous, "janus", "prepare"); !errors.Is(err, ErrDenied) {
			t.Fatalf("incomplete principal: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := reg.AuthorizeHandoff(ctx, p, "janus", "prepare"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
	if step, ok := reg.Step("janus"); !ok || step == nil {
		t.Fatal("compiled step missing")
	}
	if _, ok := reg.Step("missing"); ok {
		t.Fatal("unknown step found")
	}
	copy, _ := reg.Lookup("janus")
	copy.StepPermissions["deploy"] = fence.PermStageDeploy
	copy.Manifest.Permissions = append(copy.Manifest.Permissions, fence.PermStageDeploy)
	if err := reg.AuthorizeHandoff(t.Context(), p, "janus", "deploy"); !errors.Is(err, ErrDenied) {
		t.Fatalf("lookup mutation changed registry: %v", err)
	}
}

// testStagePlugin is a framework fixture; it contains no deployment/access policy.
func testStagePlugin(id string) func() (Plugin, error) {
	return func() (Plugin, error) {
		bindings := map[string]string{"deploy": fence.PermStageDeploy, "verify": fence.PermStageVerify}
		if id == "janus" {
			bindings = map[string]string{"prepare": fence.PermStageAccessPrepare, "apply": fence.PermStageAccessApply}
		}
		steps := []WorkflowStep{}
		perms := []string{fence.PermStepsEvaluate, fence.PermStepsRequest, fence.PermStepsApply}
		for operation, permission := range bindings {
			steps = append(steps, WorkflowStep{Key: operation})
			perms = append(perms, permission)
		}
		p := Plugin{Manifest: Manifest{ID: id, Version: "1", Owner: "test", Permissions: perms, WorkflowSteps: steps}, StepPermissions: bindings, Steps: testSteps{}}
		digest, err := Digest(p)
		p.Manifest.DigestSHA256 = digest
		return p, err
	}
}

type testSteps struct{}

func (testSteps) Evaluate(context.Context, Call, StepRequest) (StepDecision, error) {
	return StepDecision{Proceed: true}, nil
}
func (testSteps) Request(context.Context, Call, StepRequest) (StepDecision, error) {
	return StepDecision{Proceed: true}, nil
}
func (testSteps) ApplyResult(context.Context, Call, StepResult) (StepDecision, error) {
	return StepDecision{Proceed: true}, nil
}
