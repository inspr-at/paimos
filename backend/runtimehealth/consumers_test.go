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
