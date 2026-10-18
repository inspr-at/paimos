// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"strings"
	"testing"
	"time"
)

func TestLeadDecisionFrozenGateEvidence(t *testing.T) {
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, state, freshness, outcome string
		age                             time.Duration
		absent                          bool
	}{
		{"ready", "ready", "fresh", "selected", 120 * time.Second, false},
		{"stale", "ready", "stale", "wait", 120*time.Second + time.Nanosecond, false},
		{"future", "ready", "stale", "wait", -time.Nanosecond, false},
		{"full", "full", "fresh", "wait", 0, false},
		{"unreadable", "unreadable", "unreadable", "wait", 0, false},
		{"missing observation", "ready", "unreadable", "wait", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := now.Add(-tc.age)
			gates := []LeadDecisionGate{}
			for _, kind := range []string{"dial", "harness", "account_room", "host_load"} {
				gates = append(gates, LeadDecisionGate{Kind: kind, State: "ready", ObservedAt: &now})
			}
			gates[2].State = tc.state
			gates[2].ObservedAt = &at
			if tc.absent {
				gates[2].ObservedAt = nil
			}
			in := LeadDecisionWrite{Stage: "admission", Outcome: "selected", ReasonCodes: []string{"gates_ready"}, Gates: gates}
			out := freezeLeadDecision(in, "project", "generation", now)
			if out.Outcome != tc.outcome || out.GateFreshness[2].Freshness != tc.freshness || out.AuthorityGranted || out.Evidence != "coordinator_reported" || !out.RecordedAt.Equal(now) {
				t.Fatalf("incorrect evidence: %+v", out)
			}
			if tc.outcome == "wait" && (containsCode(out.ReasonCodes, "gates_ready") || len(out.ReasonCodes) != 1 || !strings.HasPrefix(out.ReasonCodes[0], "account_room_")) {
				t.Fatalf("misleading reasons: %v", out.ReasonCodes)
			}
			if len(in.ReasonCodes) != 1 || in.ReasonCodes[0] != "gates_ready" {
				t.Fatal("modified input evidence")
			}
		})
	}
	handoff := freezeLeadDecision(LeadDecisionWrite{Stage: "release_handoff", Outcome: "handoff", ReasonCodes: []string{"release_handoff"}}, "project", "generation", now)
	if !containsCode(handoff.ReasonCodes, "person_gate_required") || handoff.AuthorityGranted {
		t.Fatal("handoff implied publication authority")
	}
}

func TestLeadDecisionClosedVocabulary(t *testing.T) {
	valid := func() LeadDecisionWrite {
		return LeadDecisionWrite{RequestID: "11111111-1111-1111-1111-111111111111", Stage: "queue", Outcome: "selected", ReasonCodes: []string{"manual_order", "oldest_eligible"}, PolicySource: "project", PolicyRevision: 2, Attempt: 1}
	}
	for _, change := range []func(*LeadDecisionWrite){
		func(in *LeadDecisionWrite) { in.ReasonCodes = []string{"prompt text"} },
		func(in *LeadDecisionWrite) { in.ReasonCodes = []string{"manual_order", "manual_order"} },
		func(in *LeadDecisionWrite) { in.PolicySource = "private account" },
		func(in *LeadDecisionWrite) { in.PolicyRevision = 0 },
		func(in *LeadDecisionWrite) { in.PolicyRevision = 2147483648 },
		func(in *LeadDecisionWrite) { in.Attempt = 33 },
		func(in *LeadDecisionWrite) { in.Stage = "admission" },
		func(in *LeadDecisionWrite) { in.Outcome = "merged" },
		func(in *LeadDecisionWrite) { in.Outcome = "partial" },
		func(in *LeadDecisionWrite) {
			in.Gates = []LeadDecisionGate{{Kind: "dial", State: "ready"}, {Kind: "dial", State: "ready"}}
		},
		func(in *LeadDecisionWrite) { in.Gates = []LeadDecisionGate{{Kind: "owner_login", State: "ready"}} },
		func(in *LeadDecisionWrite) { in.Results = []LeadDecisionLink{{Kind: "url", ID: in.RequestID}} },
		func(in *LeadDecisionWrite) {
			in.Results = []LeadDecisionLink{{Kind: "review", ID: in.RequestID, HeadSHA: "abc"}}
		},
		func(in *LeadDecisionWrite) { in.Results = make([]LeadDecisionLink, 9) },
	} {
		in := valid()
		change(&in)
		if _, err := NormalizeLeadDecision(in); err == nil {
			t.Fatalf("accepted invalid evidence %+v", in)
		}
	}
	in := valid()
	normalized, err := NormalizeLeadDecision(in)
	if err != nil || strings.Join(normalized.ReasonCodes, ",") != "manual_order,oldest_eligible" || normalized.Gates == nil || normalized.Results == nil {
		t.Fatalf("normalization: %+v %v", normalized, err)
	}
	for _, code := range []string{"forecast_unavailable", "model_escalated", "login_selected", "connection_broken", "process_broken", "capacity_unavailable", "capacity_full", "partial_result"} {
		in = valid()
		in.ReasonCodes = []string{code}
		if code == "partial_result" {
			in.Outcome = "partial"
		}
		if _, err := NormalizeLeadDecision(in); err != nil {
			t.Fatalf("distinct cause %s rejected: %v", code, err)
		}
	}
}
