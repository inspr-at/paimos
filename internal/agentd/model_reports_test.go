// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/modelreport"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

func TestModelInvalidRequiresExplicitVendorEvidence(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{`{"code":"model_not_found","message":"unavailable"}`, true},
		{`{"message":"The model gpt-example does not exist"}`, true},
		{`{"message":"Unsupported model"}`, true},
		{`{"code":400,"message":"Invalid input"}`, false},
		{`{"message":"Model quota exhausted"}`, false},
		{`{"message":"Model not available due to capacity"}`, false},
		{`malformed`, false},
	} {
		if got := explicitModelInvalid(json.RawMessage(test.raw)); got != test.want {
			t.Fatalf("%s: %v", test.raw, got)
		}
	}
	if !errors.Is(modelStartError("launch refused", errModelInvalid), errModelInvalid) {
		t.Fatal("lost typed evidence")
	}
	if errors.Is(modelStartError("launch refused", errors.New("HTTP 400")), errModelInvalid) {
		t.Fatal("generic error became invalid model")
	}
}

type modelEvidenceAPI struct {
	fakeAPI
	evidence []modelreport.Observation
}

func (a *modelEvidenceAPI) ReportHarnessModels(_ context.Context, _ HarnessSession, in []modelreport.Observation) error {
	a.evidence = append(a.evidence, in...)
	return nil
}

func TestModelHealthNeedsPositiveMatchingOutputAndCoalescesStreams(t *testing.T) {
	api := &modelEvidenceAPI{}
	s := &Supervisor{api: api}
	entry := &owned{record: Record{RunID: "public-run"}, harness: HarnessSession{ID: "public-session", Harness: Codex, Model: "gpt-6.1-sol", ReasoningEffort: "high"}}
	zero, positive := int64(0), int64(12)
	s.observe(entry, AdapterEvent{SessionUsage: &sessionusage.UsageReport{Model: "gpt-6.1-sol", OutputTokens: &zero}})
	s.observe(entry, AdapterEvent{SessionUsage: &sessionusage.UsageReport{Model: "gpt-other", OutputTokens: &positive}})
	if len(api.evidence) != 0 {
		t.Fatal("unconfirmed or zero usage became working evidence")
	}
	ev := AdapterEvent{SessionUsage: &sessionusage.UsageReport{Model: "gpt-6.1-sol", OutputTokens: &positive}}
	s.observe(entry, ev)
	s.observe(entry, ev)
	if len(api.evidence) != 1 || api.evidence[0].Status != "working" {
		t.Fatal("working token stream was not coalesced")
	}
	s.observe(entry, AdapterEvent{ErrorCode: "model_invalid"})
	if len(api.evidence) != 2 || api.evidence[1].Status != "invalid" {
		t.Fatal("invalid evidence was not independent of working")
	}
}

func TestCodexStartupRejectionReportsRequestedPinWithoutPublishingMetadata(t *testing.T) {
	api := &modelEvidenceAPI{}
	s := &Supervisor{api: api}
	entry := &owned{record: Record{RunID: "public-run"}, harness: HarnessSession{ID: "public-session", Harness: Codex}}
	s.reportModelState(entry, "invalid", "gpt-rejected", "high")
	if len(api.evidence) != 1 || api.evidence[0].Model != "gpt-rejected" || api.evidence[0].Status != "invalid" {
		t.Fatal("startup rejection lost requested model")
	}
	if entry.harness.Model != "" || entry.harness.ReasoningEffort != "" {
		t.Fatal("requested model published as confirmed metadata")
	}
}
