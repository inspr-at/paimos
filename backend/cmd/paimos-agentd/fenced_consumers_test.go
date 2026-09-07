// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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

func TestFencedConsumerInventoryFailureCannotHideBehindHealthySession(t *testing.T) {
	testFencedConsumerInventoryCoverage(t, false)
}
func TestFencedConsumerHealthySessionProgressesWhileInventoryStalls(t *testing.T) {
	testFencedConsumerInventoryCoverage(t, true)
}
func testFencedConsumerInventoryCoverage(t *testing.T, stall bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, healthy := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var healthyOnce sync.Once
	process := &nativeFixtureProcess{done: make(chan struct{})}
	controller, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "fixture", StateRoot: t.TempDir(), Adapters: []agentd.Adapter{nativeFixtureAdapter{process}}})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(ctx)
	bound := map[string]bool{}
	for _, name := range []string{"failed", "healthy"} {
		session, err := controller.Start(ctx, agentd.StartRequest{Adapter: "codex", Identity: "codex:" + name, Role: "coordinator", ProjectID: 42, Workspace: t.TempDir(), Prompt: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		bound[session.ID] = true
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/message-targets"):
			if r.URL.Query().Get("address") == "codex:failed" {
				if stall {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				http.Error(w, "unavailable", 503)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"targets": []agentmessage.Target{{ID: uuid.NewString(), Instance: "fixture", ProjectID: 42, Address: "codex:healthy", Adapter: "codex", Enabled: true, Role: "simple_fallback", MaximumLevel: "simple", Version: 1}}})
		case strings.HasSuffix(r.URL.Path, "/streams"):
			if stall {
				select {
				case <-entered:
				case <-r.Context().Done():
					return
				}
				healthyOnce.Do(func() { close(healthy) })
			}
			var in agentmessage.ConsumerRegistration
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(agentmessage.ConsumerStream{SchemaVersion: 1, ID: uuid.NewString(), Revision: 1, Generation: in.Generation, Kind: in.Kind, ExpiresAt: time.Now().Add(5 * time.Minute).Format(time.RFC3339Nano)})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			_ = json.NewEncoder(w).Encode(agentmessage.ConsumerPage{SchemaVersion: 1})
		default:
			t.Error("unexpected consumer operation")
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	defer cancel()
	proof, _ := lifecycleclient.NewProof()
	authority, err := lifecycleclient.NewHTTP(server.URL, 42, proof, func() (string, error) { return "fixture", nil })
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	consumers, err := lifecycleclient.NewConsumers(dir, authority)
	if err != nil {
		t.Fatal(err)
	}
	p := &projectLifecycle{owner: &daemonLifecycle{supervisor: controller, reporter: &cliReporter{instance: "fixture"}}, config: configuredProject{ProjectID: 42}, bound: bound, authority: authority, consumers: consumers}
	done := make(chan error, 1)
	go func() { done <- p.consume(ctx) }()
	if stall {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("inventory never entered")
		}
		select {
		case <-healthy:
		case <-time.After(3 * time.Second):
			t.Fatal("healthy receiver blocked behind stalled inventory")
		}
		close(release)
	}
	if err := <-done; err == nil {
		t.Fatal("inventory failure not returned")
	}
	for _, kind := range []string{"fallback", "attention"} {
		ready, unavailable := 0, 0
		for _, e := range p.consumerEvidence {
			if e.Kind != kind {
				continue
			}
			if e.State == "ready" {
				ready++
			}
			if e.State == "unavailable" && e.Reason == "transport_unavailable" {
				unavailable++
			}
		}
		if ready != 1 || unavailable != 1 {
			t.Errorf("%s coverage: ready=%d unavailable=%d", kind, ready, unavailable)
		}
		if state, _, _ := healthEvidence(kind, 42, p.consumerEvidence, time.Now()); state != "unhealthy" {
			t.Errorf("%s partial inventory reported %s", kind, state)
		}
	}
}
