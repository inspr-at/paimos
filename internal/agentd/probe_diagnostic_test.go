// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

type diagnosticProbeAdapter struct {
	*fakeAdapter
	statuses map[string]ProbeStatus
}

func (a *diagnosticProbeAdapter) ProbeStatus(_ context.Context, key string) ProbeStatus {
	return a.statuses[key]
}

func TestProbeDiagnosticReachesLifecycleJSONAndClearsOnRecovery(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.api = &readinessQueueAPI{fakeAPI: api}
	s.accounts[0].Harness = Claude
	adapter := &diagnosticProbeAdapter{fakeAdapter: &fakeAdapter{}, statuses: map[string]ProbeStatus{"local": {Failure: ProbeUnavailable, ReasonDetail: agentsetup.ProbeClaudeDefaultPrivate}}}
	s.adapters[Claude] = adapter
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	status := s.Lifecycle("")
	for _, got := range []agentsetup.HarnessDetail{status.HarnessDetails[Claude], status.AccountStatuses["account"]} {
		if got.State != "blocked" || got.Reason != "probe_failed" || got.ReasonDetail != agentsetup.ProbeClaudeDefaultPrivate || got.Fix.Command != `chmod 700 "$HOME/.claude"` {
			t.Fatalf("diagnostic lost: %+v", got)
		}
	}
	raw, err := json.Marshal(status)
	if err != nil || strings.Count(string(raw), `"reason_detail"`) != 2 {
		t.Fatalf("lifecycle JSON lost diagnostic: %s, %v", raw, err)
	}
	adapter.statuses["local"] = ProbeStatus{OK: true, ReasonDetail: agentsetup.ProbeClaudeDefaultPrivate}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(s.Lifecycle(""))
	if err != nil || strings.Contains(string(raw), "reason_detail") || strings.Contains(string(raw), "chmod") {
		t.Fatalf("recovered diagnostic persisted: %s, %v", raw, err)
	}
	if _, ok := s.probeReasonDetails["account"]; ok {
		t.Fatal("recovery retained local diagnostic")
	}
	adapter.statuses["local"] = ProbeStatus{Failure: ProbeUnavailable, ReasonDetail: "/private/profile: raw diagnostic"}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(s.Lifecycle(""))
	if err != nil || strings.Contains(string(raw), "reason_detail") || strings.Contains(string(raw), "/private/profile") {
		t.Fatalf("raw diagnostic escaped: %s, %v", raw, err)
	}
	s.probeReasonDetails["account"] = agentsetup.ProbeOutputInvalid
	s.beginAccountProbe("account", time.Now())
	if _, ok := s.probeReasonDetails["account"]; ok {
		t.Fatal("new probe inherited an old cause")
	}
}

func TestReadySiblingRetainsOnlyBlockedAccountProbeDetail(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.api = &readinessQueueAPI{fakeAPI: api}
	s.accounts = []EnrolledAccount{{ID: "blocked", Key: "bad", Harness: Claude}, {ID: "healthy", Key: "good", Harness: Claude}}
	s.adapters[Claude] = &diagnosticProbeAdapter{fakeAdapter: &fakeAdapter{}, statuses: map[string]ProbeStatus{
		"bad": {Failure: ProbeUnavailable, ReasonDetail: agentsetup.ProbeOutputInvalid}, "good": {OK: true},
	}}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.Lifecycle("").HarnessDetails[Claude]
	if got.State != "ready" || got.ReasonDetail != "" || got.Fix.Command != "" || len(got.Attention) != 1 || got.Attention[0].AccountID != "blocked" || got.Attention[0].ReasonDetail != agentsetup.ProbeOutputInvalid {
		t.Fatalf("sibling diagnosis lost: %+v", got)
	}
}
