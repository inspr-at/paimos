// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/models"
)

type nativeFixtureProcess struct {
	done            chan struct{}
	once            sync.Once
	inboxes, steers int
}

func (p *nativeFixtureProcess) PID() int         { return 4242 }
func (p *nativeFixtureProcess) InboxReady() bool { return p.inboxes == 0 }
func (p *nativeFixtureProcess) Wait() error      { <-p.done; return nil }
func (p *nativeFixtureProcess) Inbox(_ context.Context, r agentd.ControlRequest) (agentd.ControlEffect, error) {
	p.inboxes++
	return agentd.ControlEffect{Primitive: "codex queue --thread", CorrelationID: r.CorrelationID}, nil
}
func (p *nativeFixtureProcess) Steer(_ context.Context, r agentd.ControlRequest) (agentd.ControlEffect, error) {
	p.steers++
	return agentd.ControlEffect{Primitive: "codex app-server turn/steer", CorrelationID: r.CorrelationID}, nil
}
func (p *nativeFixtureProcess) Interrupt(context.Context, agentd.ControlRequest) (agentd.ControlEffect, error) {
	return agentd.ControlEffect{}, errors.New("unused")
}
func (p *nativeFixtureProcess) Stop(_ context.Context, r agentd.ControlRequest) (agentd.ControlEffect, error) {
	p.once.Do(func() { close(p.done) })
	return agentd.ControlEffect{Primitive: "owned process stop", CorrelationID: r.CorrelationID}, nil
}

type nativeFixtureAdapter struct{ process *nativeFixtureProcess }

func (nativeFixtureAdapter) Name() string { return "codex" }
func (nativeFixtureAdapter) Capabilities() []agentd.Capability {
	return []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilitySteer, agentd.CapabilityStop}
}
func (a nativeFixtureAdapter) Start(_ context.Context, _ agentd.StartRequest, observe func(agentd.AdapterEvent)) (agentd.Process, error) {
	observe(agentd.AdapterEvent{Kind: agentd.EventSessionStarted, HarnessSessionID: "fixture-vendor-ref"})
	observe(agentd.AdapterEvent{Kind: agentd.EventTurnStarted})
	return a.process, nil
}

func TestNativeConsumersUsePrivateWorkerLeaseAndExactCanonicalFIFO(t *testing.T) {
	root := t.TempDir()
	process := &nativeFixtureProcess{done: make(chan struct{})}
	controller, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []agentd.Adapter{nativeFixtureAdapter{process}}})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())
	session, err := controller.Start(context.Background(), agentd.StartRequest{Adapter: "codex", Identity: "codex:worker", ProjectID: 42, Workspace: t.TempDir(), Prompt: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	public, target := uuid.NewString(), uuid.NewString()
	if err := controller.CheckpointReporter(context.Background(), session.ID, agentd.ControlRequest{Instance: "fixture", ProjectID: 42, Identity: "codex:worker"}, agentd.ReporterState{PublicSessionID: public, Capabilities: []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilitySteer, agentd.CapabilityStop}}); err != nil {
		t.Fatal(err)
	}
	remote := models.HarnessSession{ID: public, ProjectID: 42, AgentName: "worker", Harness: "codex", Host: "fixture-host", ManagementMode: "managed", MessageTargetID: target, Phase: "working"}
	remote.Capabilities.Inbox = true
	remote.Capabilities.Steer = true
	pending := []agentmessage.Envelope{}
	for i, level := range []string{"simple", "steer"} {
		pending = append(pending, agentmessage.Envelope{Cursor: int64(i + 1), MessageID: uuid.NewString(), To: "codex:worker", Parts: []agentmessage.TextPart{{Kind: "text", Text: "private fixture message"}}, DeliveryWork: &agentmessage.DeliveryWork{DeliveryID: uuid.NewString(), Instance: "fixture", ProjectID: 42, State: "leased", Adapter: agentmessage.AdapterManagedHarness, RequestedLevel: level, MaximumLevel: "steer"}})
	}
	completed := 0
	legacy := false
	reporter, err := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", []string{"PAIMOS_API_KEY_FILE=/fixture/protected"}, func(_ context.Context, path string, args, env []string, stdin io.Reader) ([]byte, error) {
		if path != "/fixture/paimos" || !slices.Equal(env, []string{"PAIMOS_API_KEY_FILE=/fixture/protected"}) {
			t.Fatal("reporter configuration changed")
		}
		if args[1] == "curl" {
			adapter := agentmessage.AdapterManagedHarness
			id := target
			if legacy {
				adapter = "agentd_codex"
				id = uuid.NewString()
			}
			return json.Marshal(map[string]any{"targets": []agentmessage.Target{{ID: id, Instance: "fixture", ProjectID: 42, Address: "codex:worker", Adapter: adapter, Enabled: true, Role: "primary", Version: 1}}})
		}
		operation := args[2]
		if operation == "status" {
			return json.Marshal(remote)
		}
		if !slices.Contains(args, "--worker-lease-file") || stdin == nil {
			t.Fatal("worker operation missing private lease input")
		}
		raw, _ := io.ReadAll(stdin)
		if len(raw) != 43 {
			t.Fatal("invalid lease input")
		}
		for _, arg := range args {
			if arg == string(raw) || strings.Contains(arg, "private fixture") {
				t.Fatal("private data leaked to reporter argv")
			}
		}
		switch operation {
		case "drain":
			messages := pending
			if len(messages) > 1 {
				messages = messages[:1]
			}
			return json.Marshal(agentmessage.InboxPage{Address: "codex:worker", Messages: messages})
		case "complete-delivery":
			level := args[slices.Index(args, "--effective-level")+1]
			if level != pending[0].DeliveryWork.RequestedLevel {
				t.Fatal("delivery level fabricated")
			}
			cursor := pending[0].Cursor
			pending = pending[1:]
			completed++
			return json.Marshal(agentmessage.CursorState{Address: "codex:worker", Cursor: cursor})
		}
		return nil, errors.New("unexpected fixture command")
	}, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	consumers, err := newNativeConsumers(root, "fixture", reporter)
	if err != nil {
		t.Fatal(err)
	}
	defer consumers.supervisor.Stop()
	consumers.controller = controller
	consumers.reconcile(context.Background())
	consumers.reconcile(context.Background())
	if completed != 2 || process.inboxes != 1 || process.steers != 1 {
		t.Fatal("native FIFO did not execute exact simple and steer effects")
	}
	consumers.reconcile(context.Background())
	if completed != 2 || process.inboxes != 1 || process.steers != 1 {
		t.Fatal("consumer repeated completed work")
	}
	raw, _ := json.Marshal(consumers.Snapshot())
	if strings.Contains(string(raw), "private fixture") || strings.Contains(string(raw), "fixture-host") || strings.Contains(string(raw), target) {
		t.Fatal("consumer evidence leaked private data")
	}
	for _, e := range consumers.Snapshot() {
		if e.Kind == "primary" && e.State != "ready" {
			t.Fatal("primary did not report observed readiness")
		}
		if e.Kind == "fallback" && e.Reason != "authority_unavailable" {
			t.Fatal("unfenced fallback claimed ready")
		}
	}
	legacy = true
	consumers.reconcile(context.Background())
	found := false
	for _, e := range consumers.Snapshot() {
		if e.Kind == "primary" && e.Reason == "legacy_handoff_required" {
			found = true
		}
	}
	if !found {
		t.Fatal("legacy receiver conflict was hidden")
	}
}
