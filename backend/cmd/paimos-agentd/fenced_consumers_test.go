// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
)

func TestFencedConsumerForeignPrivateReferenceNeverInvokesReceiver(t *testing.T) {
	// No controller/executable exists in this fixture. A foreign private target
	// must be rejected before any owned inbox or external process can be invoked.
	p := &projectLifecycle{owner: &daemonLifecycle{reporter: &cliReporter{instance: "fixture"}}}
	s := agentd.Session{ID: uuid.NewString(), ProjectID: 42, Identity: "codex:worker", HarnessSessionID: "owned-thread"}
	target := agentmessage.Target{Adapter: "codex", TargetKind: "codex_thread"}
	a := &agentmessage.ConsumerAttempt{ID: uuid.NewString(), ResourceID: uuid.NewString(), Cursor: 7}
	page := agentmessage.ConsumerPage{SchemaVersion: 1, Attempt: a, Delivery: &agentmessage.ConsumerDelivery{Envelope: &agentmessage.Envelope{Cursor: 7, To: s.Identity, Parts: []agentmessage.TextPart{{Kind: "text", Text: "private fixture"}}}, Work: agentmessage.DeliveryWork{DeliveryID: a.ResourceID, Instance: "fixture", ProjectID: 42, State: "leased", MaximumLevel: "simple", Adapter: "codex", TargetKind: "codex_thread", TargetRef: "foreign-unmanaged-thread"}}}
	if _, e := p.deliverConsumer(context.Background(), s, target, "fallback", page); !errors.Is(e, lifecycleclient.ErrOwnership) {
		t.Fatal("foreign target was accepted", e)
	}
	page.Delivery = nil
	page.Attention = &agentmessage.AttentionPage{Address: s.Identity, NextCursor: 7, Frame: "content-free fixture attention", Work: &agentmessage.AttentionDeliveryWork{BatchID: a.ResourceID, Instance: "fixture", ProjectID: 42, State: "leased", MaximumLevel: "simple", Adapter: "codex", TargetKind: "codex_thread", TargetRef: "foreign-unmanaged-thread"}}
	if _, e := p.deliverConsumer(context.Background(), s, target, "attention", page); !errors.Is(e, lifecycleclient.ErrOwnership) {
		t.Fatal("foreign attention target was accepted", e)
	}
}

func TestRuntimeHealthRequiresFreshEvidenceForSelectedProject(t *testing.T) {
	now := time.Now()
	fresh := runtimeconsumer.Evidence{Kind: "primary", ProjectID: 42, State: "ready", LastSuccess: now}
	for _, test := range []struct {
		name    string
		layers  []runtimeconsumer.Evidence
		healthy bool
	}{
		{"missing", nil, false},
		{"fresh", []runtimeconsumer.Evidence{fresh}, true},
		{"other project", []runtimeconsumer.Evidence{{Kind: "primary", ProjectID: 99, State: "ready", LastSuccess: now}}, false},
		{"stale", []runtimeconsumer.Evidence{{Kind: "primary", ProjectID: 42, State: "ready", LastSuccess: now.Add(-time.Minute)}}, false},
		{"future", []runtimeconsumer.Evidence{{Kind: "primary", ProjectID: 42, State: "ready", LastSuccess: now.Add(time.Minute)}}, false},
		{"foreign failure isolated", []runtimeconsumer.Evidence{fresh, {Kind: "primary", ProjectID: 99, State: "circuit_open"}}, true},
		{"own failure", []runtimeconsumer.Evidence{fresh, {Kind: "primary", ProjectID: 42, State: "circuit_open"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, _, _ := healthEvidence("primary", 42, test.layers, now)
			if (state == "healthy") != test.healthy {
				t.Fatal("incorrect project health", state)
			}
		})
	}
}

func TestReceiverReferenceRequiresExactCurrentOwnedGeneration(t *testing.T) {
	id := uuid.NewString()
	s := agentd.Session{ID: id, ProjectID: 42, Identity: "codex:worker", HarnessSessionID: "private-fixture-reference", State: agentd.StateRunning, PID: 42, Managed: true, Reporter: agentd.ReporterState{PublicSessionID: uuid.NewString()}}
	status := agentd.Status{DaemonID: uuid.NewString(), Instance: "fixture", Sessions: []agentd.Session{s}}
	if value, e := ownedReceiverReference(status, "fixture", id, s.Identity, 42); e != nil || value != s.HarnessSessionID {
		t.Fatal("owned generation refused")
	}
	if _, e := ownedReceiverReference(status, "fixture", id, s.Identity, 0); e == nil {
		t.Fatal("missing project scope accepted")
	}
	if _, e := ownedReceiverReference(status, "other", id, s.Identity, 42); e == nil {
		t.Fatal("foreign instance accepted")
	}
	if _, e := ownedReceiverReference(status, "fixture", id, s.Identity, 99); e == nil {
		t.Fatal("foreign project accepted")
	}
	if _, e := ownedReceiverReference(status, "fixture", uuid.NewString(), s.Identity, 42); e == nil {
		t.Fatal("public/foreign generation accepted")
	}
	status.Sessions[0].State = agentd.StateOwnershipLost
	if _, e := ownedReceiverReference(status, "fixture", id, s.Identity, 42); e == nil {
		t.Fatal("orphan reference accepted")
	}
}

func TestAttentionChoosesServerOrderedTargetButKeepsSimpleDelivery(t *testing.T) {
	s := agentd.Session{ProjectID: 42, Identity: "codex:root", Role: "coordinator"}
	targets := []agentmessage.Target{
		{ID: "fallback", Instance: "fixture", ProjectID: 42, Address: s.Identity, Enabled: true, Adapter: "codex", Role: "simple_fallback", MaximumLevel: "simple", Version: 99},
		{ID: "new-primary", Instance: "fixture", ProjectID: 42, Address: s.Identity, Enabled: true, Adapter: "codex", Role: "primary", MaximumLevel: "steer", Version: 2},
		{ID: "old-primary", Instance: "fixture", ProjectID: 42, Address: s.Identity, Enabled: true, Adapter: "codex", Role: "primary", MaximumLevel: "simple", Version: 1},
	}
	selected, e := selectOwnedConsumerTarget(targets, "fixture", s, "attention")
	if e != nil || selected == nil || selected.ID != "new-primary" {
		t.Fatal("attention target differs from server priority")
	}
	selected, e = selectOwnedConsumerTarget(targets, "fixture", s, "fallback")
	if e != nil || selected == nil || selected.ID != "fallback" {
		t.Fatal("steer primary admitted as simple fallback")
	}
}
