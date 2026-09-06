// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
)

type listenerRepairProcess struct {
	nativeFixtureProcess
	effects int
}

func (p *listenerRepairProcess) Inbox(context.Context, agentd.ControlRequest) (agentd.ControlEffect, error) {
	p.effects++
	return agentd.ControlEffect{}, errors.New("fixture effect outcome unknown")
}

type listenerRepairAdapter struct{ process *listenerRepairProcess }

func (listenerRepairAdapter) Name() string { return "codex" }
func (listenerRepairAdapter) Capabilities() []agentd.Capability {
	return []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilityStop}
}
func (a listenerRepairAdapter) Start(_ context.Context, _ agentd.StartRequest, observe func(agentd.AdapterEvent)) (agentd.Process, error) {
	observe(agentd.AdapterEvent{Kind: agentd.EventSessionStarted, HarnessSessionID: "fixture-reference"})
	observe(agentd.AdapterEvent{Kind: agentd.EventTurnCompleted})
	return a.process, nil
}

func TestLifecycleListenerRepairPreservesOptionalAndEffectBoundaries(t *testing.T) {
	for _, mode := range []string{"unconfigured", "configured_unhealthy", "unknown_effect", "no_live_binding"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			process := &listenerRepairProcess{nativeFixtureProcess: nativeFixtureProcess{done: make(chan struct{})}}
			controller, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []agentd.Adapter{listenerRepairAdapter{process}}})
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close(ctx)
			session, err := controller.Start(ctx, agentd.StartRequest{Adapter: "codex", Identity: "codex:worker", ProjectID: 42, Workspace: t.TempDir(), Prompt: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			public, target := uuid.NewString(), uuid.NewString()
			request := agentd.ControlRequest{Instance: "fixture", ProjectID: 42, Identity: "codex:worker"}
			if err = controller.CheckpointReporter(ctx, session.ID, request, agentd.ReporterState{PublicSessionID: public, Capabilities: []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilityStop}}); err != nil {
				t.Fatal(err)
			}
			remote := models.HarnessSession{ID: public, ProjectID: 42, AgentName: "worker", Harness: "codex", Host: "fixture-host", ManagementMode: "managed", MessageTargetID: target, Phase: "working", Capabilities: models.HarnessCapabilities{Inbox: true}}
			ownedTarget := agentmessage.Target{ID: target, Instance: "fixture", ProjectID: 42, Address: "codex:worker", Adapter: agentmessage.AdapterManagedHarness, Role: "primary", Enabled: true}
			pending := mode == "unknown_effect"
			reporter, err := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
				if args[1] == "curl" {
					return json.Marshal(map[string]any{"targets": []agentmessage.Target{ownedTarget}})
				}
				switch args[2] {
				case "status":
					return json.Marshal(remote)
				case "drain":
					page := agentmessage.InboxPage{Address: "codex:worker", Messages: []agentmessage.Envelope{}}
					if pending {
						page.Messages = append(page.Messages, agentmessage.Envelope{Cursor: 1, To: "codex:worker", Parts: []agentmessage.TextPart{{Kind: "text", Text: "fixture"}}, DeliveryWork: &agentmessage.DeliveryWork{DeliveryID: uuid.NewString(), Instance: "fixture", ProjectID: 42, State: "leased", Adapter: agentmessage.AdapterManagedHarness, RequestedLevel: "simple", MaximumLevel: "simple"}})
					}
					return json.Marshal(page)
				default:
					t.Fatalf("unexpected primary operation %s", args[2])
					return nil, errors.New("unexpected operation")
				}
			}, newMemoryReporterLeaseStore())
			if err != nil {
				t.Fatal(err)
			}
			primary, err := newNativeConsumers(root, "fixture", reporter)
			if err != nil {
				t.Fatal(err)
			}
			defer primary.supervisor.Stop()
			primary.controller = controller
			primary.reconcile(ctx)
			if mode == "no_live_binding" {
				request.CorrelationID = uuid.NewString()
				if _, err = controller.Stop(ctx, session.ID, request); err != nil {
					t.Fatal(err)
				}
				primary.reconcile(ctx)
			}
			var optionalAttempts atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message-targets") {
					targets := []agentmessage.Target{ownedTarget}
					if mode == "configured_unhealthy" {
						targets = append(targets, agentmessage.Target{ID: uuid.NewString(), Instance: "fixture", ProjectID: 42, Address: "codex:worker", Adapter: "codex", MaximumLevel: "simple", Role: "simple_fallback", Enabled: true, Version: 1})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"targets": targets})
					return
				}
				optionalAttempts.Add(1)
				http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			proof, _ := lifecycleclient.NewProof()
			authority, err := lifecycleclient.NewHTTP(server.URL, 42, proof, func() (string, error) { return "fixture-key", nil })
			if err != nil {
				t.Fatal(err)
			}
			optionalDir := t.TempDir()
			if err = os.Chmod(optionalDir, 0700); err != nil {
				t.Fatal(err)
			}
			optional, err := lifecycleclient.NewConsumers(optionalDir, authority)
			if err != nil {
				t.Fatal(err)
			}
			p := &projectLifecycle{owner: &daemonLifecycle{supervisor: controller, primary: primary, reporter: reporter}, config: configuredProject{ProjectID: 42}, authority: authority, consumers: optional, bound: map[string]bool{session.ID: true}, record: runtimeRegistrationRecord{Runtime: lifecycleintents.Runtime{ID: uuid.NewString(), Generation: controller.Status().DaemonID}}}
			directory, _ := agentd.InstanceStateDir(root, "fixture")
			receipts := func() []byte {
				var out []byte
				for _, name := range []string{"consumer-effects.journal", "consumer-effects.checkpoint.json"} {
					raw, readErr := os.ReadFile(filepath.Join(directory, name))
					if readErr != nil && !os.IsNotExist(readErr) {
						t.Fatal(readErr)
					}
					out = append(out, raw...)
				}
				return out
			}
			before := receipts()
			result, err := p.Execute(ctx, lifecycleintents.Intent{Request: lifecycleintents.Request{Operation: "repair", RepairLayer: "listeners"}})
			switch mode {
			case "unconfigured":
				if err != nil || result.Reason != "applied" || optionalAttempts.Load() != 0 {
					t.Fatalf("primary repair required optional receivers: result=%+v error=%v requests=%d", result, err, optionalAttempts.Load())
				}
				for _, kind := range []string{"fallback", "attention"} {
					if !p.optionalReceiverUnconfigured(kind) {
						t.Fatalf("%s lost explicit unconfigured evidence", kind)
					}
				}
				absent := runtimeconsumer.Evidence{Kind: "fallback", ProjectID: 42, Generation: controller.Status().DaemonID, State: "unavailable", Reason: "receiver_not_configured"}
				stale, foreign, unhealthy := absent, absent, absent
				stale.Generation = uuid.NewString()
				foreign.ProjectID++
				unhealthy.State, unhealthy.Reason = "backoff", "transport_unavailable"
				for name, evidence := range map[string][]runtimeconsumer.Evidence{"missing": nil, "stale": {stale}, "foreign": {foreign}, "mixed": {absent, unhealthy}} {
					p.consumerEvidence = evidence
					if p.optionalReceiverUnconfigured("fallback") {
						t.Fatalf("%s evidence treated as an absent optional receiver", name)
					}
				}
			case "configured_unhealthy":
				if err == nil || optionalAttempts.Load() == 0 || p.optionalReceiverUnconfigured("fallback") {
					t.Fatal("configured unhealthy receiver treated as absent")
				}
			case "unknown_effect":
				after := receipts()
				if !errors.Is(err, runtimeconsumer.ErrUnknown) || process.effects != 1 || !bytes.Contains(before, []byte("pending")) || !bytes.Equal(before, after) || optionalAttempts.Load() != 0 {
					t.Fatalf("unknown effect was not preserved: error=%v effects=%d", err, process.effects)
				}
			case "no_live_binding":
				if !errors.Is(err, runtimeconsumer.ErrAuthority) || optionalAttempts.Load() != 0 {
					t.Fatalf("cold repair bypassed owned binding: %v", err)
				}
			}
		})
	}
}
