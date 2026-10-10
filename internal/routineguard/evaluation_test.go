// SPDX-License-Identifier: AGPL-3.0-only
package routineguard

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

// Risks R1/R15/R16/R17: false approval from a spoofed provider, missing verdict,
// changed policy/action/head, or evaluator self-admission without a spend hold.
// Existing deterministic floor and review pin/family helpers are reused.
func TestIndependentEvaluationBindsEvidenceAndCannotBootstrap(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	id := func(n string) string { return "10000000-0000-4000-8000-00000000000" + n }
	b := ActionBinding{TenantID: id("1"), RunID: id("2"), ActionID: id("3"), AuthorRunID: id("4"), ProjectID: id("5"), TargetID: id("6"), TargetRevision: 7, Kind: "pr.open", PayloadDigest: strings.Repeat("a", 64), HeadSHA: strings.Repeat("b", 40)}
	b.TargetUpdatedAt = now
	c := Context{Checkpoint: "action", Action: b.Kind, Text: "Update the assigned source", Paths: []string{"ordinary/source.go"}, PayloadBytes: 100}
	d, err := Evaluate(nil, c)
	if err != nil {
		t.Fatal(err)
	}
	r := EvaluationRequest{Binding: b, ContextDigest: d.ContextDigest, PolicyDigest: d.PolicyDigest, FamilyPolicy: reviewgate.DefaultFamilyPolicy(), Author: modelregistry.VerifiedModel{Harness: "codex", RequestedModel: "gpt-6.1-sol", EffectiveModel: "gpt-6.1-sol", Evidence: "vendor_reported"}, ProfileID: id("7"), AccountID: id("8"), Harness: "claude", Model: "opus", Family: "anthropic", Capability: EvaluationCapability, CreatedAt: now, Deadline: now.Add(MaxEvaluationTime)}
	r.Digest = r.digest()
	evidence := modelregistry.VerifiedModel{Harness: "claude", RequestedModel: "opus", EffectiveModel: "claude-opus-5-5", Evidence: "vendor_reported"}
	v := StructuredVerdict{RequestDigest: r.Digest, Verdict: Allow, Reason: "The bounded action satisfies the supplied rules."}
	verify := func(request EvaluationRequest, binding ActionBinding, context Context, model modelregistry.VerifiedModel, verdict StructuredVerdict, policy reviewgate.FamilyPolicy, at time.Time, status string) (Decision, error) {
		return VerifyEvaluation(nil, context, binding, request, status, model, verdict, policy, at)
	}
	accepted, err := verify(r, b, c, evidence, v, r.FamilyPolicy, now, "completed")
	if err != nil || !accepted.CanExecuteFor(r) {
		t.Fatalf("independent exact result: %+v %v", accepted, err)
	}
	requestJSON, _ := json.Marshal(r)
	var persisted EvaluationRequest
	if err := json.Unmarshal(requestJSON, &persisted); err != nil {
		t.Fatal(err)
	}
	localBinding := b
	localBinding.TargetUpdatedAt = now.In(time.FixedZone("fixture", 2*60*60))
	if got, err := verify(persisted, localBinding, c, evidence, v, r.FamilyPolicy, now, "completed"); err != nil || !got.CanExecuteFor(persisted) {
		t.Fatalf("equivalent persisted timestamp lost exact binding: %v", err)
	}
	for name, mutate := range map[string]func(*EvaluationRequest, *ActionBinding, *Context, *modelregistry.VerifiedModel, *StructuredVerdict, *reviewgate.FamilyPolicy, *time.Time, *string){
		"same provider": func(r *EvaluationRequest, _ *ActionBinding, _ *Context, e *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			r.Author = *e
			r.Digest = r.digest()
		},
		"wrong effective provider": func(_ *EvaluationRequest, _ *ActionBinding, _ *Context, e *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			e.Harness = "cursor"
			e.EffectiveModel = "gpt-6.1-sol"
		},
		"unverified model": func(_ *EvaluationRequest, _ *ActionBinding, _ *Context, e *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			e.Evidence = "requested"
		},
		"weaker pin": func(_ *EvaluationRequest, _ *ActionBinding, _ *Context, e *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			e.EffectiveModel = "claude-sonnet-5-5"
		},
		"missing verdict": func(_ *EvaluationRequest, _ *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, v *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			v.Verdict = ""
		},
		"wrong request": func(_ *EvaluationRequest, _ *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, v *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			v.RequestDigest = strings.Repeat("c", 64)
		},
		"timeout": func(_ *EvaluationRequest, _ *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, at *time.Time, _ *string) {
			*at = r.Deadline
		},
		"unfinished": func(_ *EvaluationRequest, _ *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, status *string) {
			*status = "running"
		},
		"changed target": func(_ *EvaluationRequest, b *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			b.TargetID = id("9")
		},
		"changed revision": func(_ *EvaluationRequest, b *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			b.TargetRevision++
		},
		"changed timestamp": func(_ *EvaluationRequest, b *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			b.TargetUpdatedAt = b.TargetUpdatedAt.Add(time.Second)
		},
		"changed head": func(_ *EvaluationRequest, b *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			b.HeadSHA = strings.Repeat("c", 40)
		},
		"changed payload": func(_ *EvaluationRequest, b *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			b.PayloadDigest = strings.Repeat("c", 64)
		},
		"changed context": func(_ *EvaluationRequest, _ *ActionBinding, c *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, _ *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			c.Text += " changed"
		},
		"changed family policy": func(_ *EvaluationRequest, _ *ActionBinding, _ *Context, _ *modelregistry.VerifiedModel, _ *StructuredVerdict, p *reviewgate.FamilyPolicy, _ *time.Time, _ *string) {
			*p = reviewgate.FamilyPolicy{Mode: "allowlist", AllowedFamilies: []string{"xai"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			req, binding, context, model, verdict, policy, at, status := r, b, c, evidence, v, r.FamilyPolicy, now, "completed"
			mutate(&req, &binding, &context, &model, &verdict, &policy, &at, &status)
			got, err := verify(req, binding, context, model, verdict, policy, at, status)
			want := "binding or policy changed"
			switch name {
			case "wrong effective provider", "unverified model", "weaker pin":
				want = "effective model or family is unverified"
			case "missing verdict", "wrong request":
				want = "explicit bound structured result"
			case "timeout", "unfinished":
				want = "incomplete or timed out"
			}
			if err == nil || !strings.Contains(err.Error(), want) || got.CanExecute() || got.Result == Allow {
				t.Fatalf("false approval: %+v %v", got, err)
			}
		})
	}
	changedPolicy := Source{Scope: Scope{Kind: "tenant", ID: b.TenantID}, Revision: 1, Rules: []Rule{{ID: "custom.limit", Checkpoint: "action", Method: "size", Limit: 1000, Result: Block}}}
	if got, err := VerifyEvaluation([]Source{changedPolicy}, c, b, r, "completed", evidence, v, r.FamilyPolicy, now); err == nil || got.CanExecute() {
		t.Fatal("stale rule digest allowed")
	}
	for _, outcome := range []Outcome{NeedsPerson, Block} {
		verdict := v
		verdict.Verdict = outcome
		got, err := verify(r, b, c, evidence, verdict, r.FamilyPolicy, now, "completed")
		if err != nil || got.Result != outcome || got.CanExecute() {
			t.Fatalf("strict evaluator outcome: %+v %v", got, err)
		}
		got.Result = Allow
		if got.CanExecute() {
			t.Fatal("changing public metadata forged approval")
		}
	}
	off := r
	off.FamilyPolicy = reviewgate.FamilyPolicy{Mode: "off", AllowedFamilies: []string{}}
	off.Digest = off.digest()
	offVerdict := v
	offVerdict.RequestDigest = off.Digest
	if got, err := verify(off, b, c, evidence, offVerdict, off.FamilyPolicy, now, "completed"); err != nil || !got.CanExecuteFor(off) {
		t.Fatalf("off policy lost compiled floor: %v", err)
	}
	off.Author = evidence
	off.Digest = off.digest()
	offVerdict.RequestDigest = off.Digest
	if got, err := verify(off, b, c, evidence, offVerdict, off.FamilyPolicy, now, "completed"); err == nil || got.CanExecute() {
		t.Fatal("ordinary policy off skipped independence")
	}
	raw, _ := json.Marshal(v)
	if parsed, err := ParseEvaluationResult(raw); err != nil || parsed != v {
		t.Fatalf("explicit parser: %+v %v", parsed, err)
	}
	for _, raw := range []string{"VERDICT: ok", "{}", `{"verdict":"allow"}`, string(raw) + ` {}`, strings.Replace(string(raw), `"verdict":"allow"`, `"verdict":"block","verdict":"allow"`, 1), strings.Replace(string(raw), `"verdict":"allow"`, `"verdict":"allow","tools":true`, 1), strings.Repeat("x", MaxEvaluationOutput+1)} {
		if _, err := ParseEvaluationResult([]byte(raw)); err == nil {
			t.Fatal("ambiguous or missing structured result accepted")
		}
	}
	grant := EvaluationGrant{HoldID: id("9"), RequestDigest: r.Digest, Capability: EvaluationCapability, InputTokens: MaxEvaluationInputTokens, OutputTokens: MaxEvaluationOutputTokens, Deadline: r.Deadline}
	if err := AdmitEvaluation(r, grant, now); !errors.Is(err, ErrEvaluationBudgetUnavailable) {
		t.Fatalf("evaluation escaped default-off provider enforcement: %v", err)
	}
	grant.Capability = "routine_write_v1"
	if err := AdmitEvaluation(r, grant, now); err == nil || errors.Is(err, ErrEvaluationBudgetUnavailable) {
		t.Fatal("outward-capable evaluator reservation accepted")
	}
	for _, model := range []modelregistry.VerifiedModel{{Harness: "opencode", RequestedModel: "openrouter/auto", EffectiveModel: "openrouter/auto", Evidence: "vendor_reported"}, {Harness: "claude", RequestedModel: "opus", EffectiveModel: "claude-opus-5-5", Evidence: "requested"}} {
		if _, err := model.Family(); err == nil {
			t.Fatal("unknown/unverified author was independent")
		}
	}
	serialized, _ := json.Marshal(accepted)
	var restored Decision
	if err := json.Unmarshal(serialized, &restored); err != nil || restored.CanExecute() {
		t.Fatal("serialized decision minted execution latch")
	}
}
