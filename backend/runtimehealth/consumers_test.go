// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"context"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
	"testing"
	"time"
)

func TestConsumerHealthRequiresFreshExactGenerationEvidence(t *testing.T) {
	s := agentd.RuntimeStatus{DaemonID: "fixture"}
	if ConsumerExtensions(context.Background(), s)[0].State != Unknown {
		t.Fatal("missing consumer evidence became ready")
	}
	for _, kind := range []string{"primary", "fallback", "attention"} {
		s.Consumers = append(s.Consumers, runtimeconsumer.Evidence{Kind: kind, State: "ready", Generation: s.DaemonID, LastSuccess: time.Now()})
	}
	if ConsumerExtensions(context.Background(), s)[0].State != Known {
		t.Fatal("fresh evidence not recognized")
	}
	s.Consumers[2].Generation = "old"
	if ConsumerExtensions(context.Background(), s)[0].State != ActionRequired {
		t.Fatal("stale generation accepted")
	}
	s.Consumers[2].Generation = s.DaemonID
	s.Consumers[2].State = "unavailable"
	s.Consumers[2].Reason = "authority_unavailable"
	if got := ConsumerExtensions(context.Background(), s)[0]; got.State != ActionRequired || got.Code != "consumer_authority_unavailable" {
		t.Fatal("missing server fence hidden")
	}
	s.Consumers[0].State = "circuit_open"
	if ConsumerExtensions(context.Background(), s)[0].Code != "consumer_circuit_open" {
		t.Fatal("circuit hidden")
	}
}

func TestLifecycleReadinessIsIndependentAndNeverStaticSuccess(t *testing.T) {
	s := agentd.RuntimeStatus{DaemonID: "fixture"}
	if got := ConsumerExtensions(context.Background(), s)[1]; got.Name != "browser_intents" || got.State != Unknown {
		t.Fatal("absent executor ready")
	}
	s.Consumers = []runtimeconsumer.Evidence{{Kind: "intents", State: "ready", Generation: s.DaemonID, LastSuccess: time.Now()}}
	if got := ConsumerExtensions(context.Background(), s); got[0].State != Unknown || got[1].State != Known {
		t.Fatal("intent and inbox evidence conflated")
	}
	s.Consumers[0].State = "unavailable"
	s.Consumers[0].Reason = "ownership_lost"
	if got := ConsumerExtensions(context.Background(), s)[1]; got.State != ActionRequired || got.Code != "runtime_authority_expired" {
		t.Fatal("expired authority masked")
	}
}

func TestPrimaryInboxRejectsFutureEvidence(t *testing.T) {
	s := agentd.RuntimeStatus{DaemonID: "fixture", Consumers: []runtimeconsumer.Evidence{{Kind: "primary", Generation: "fixture", State: "ready", LastSuccess: time.Now().Add(time.Minute)}}}
	if got := ConsumerExtensions(context.Background(), s)[2]; got.Name != "primary_inbox" || got.State != Unknown {
		t.Fatal("future primary evidence became ready")
	}
}

func TestTargetAttestationRequiresFreshExactProjectConsumers(t *testing.T) {
	now := time.Now()
	s := agentd.RuntimeStatus{DaemonID: "fixture"}
	for _, kind := range []string{"fallback", "attention"} {
		s.Consumers = append(s.Consumers, runtimeconsumer.Evidence{Kind: kind, ProjectID: 42, Generation: s.DaemonID, State: "ready", LastSuccess: now})
	}
	if !attestedTargets(s, 42, now) || attestedTargets(s, 99, now) || attestedTargets(s, 0, now) {
		t.Fatal("target project scope was not enforced")
	}
	s.Consumers[1].LastSuccess = now.Add(-time.Minute)
	if attestedTargets(s, 42, now) {
		t.Fatal("stale attention proved target ownership")
	}
	s.Consumers[1].LastSuccess = now
	s.Consumers[1].Generation = "old"
	if attestedTargets(s, 42, now) {
		t.Fatal("foreign generation proved target ownership")
	}
}
