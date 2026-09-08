// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
	"github.com/inspr-at/paimos/backend/secretvault"
)

type nativeFixtureProcess struct {
	done            chan struct{}
	once            sync.Once
	inboxes, steers int
	busy, stayReady bool
	effects         []string
}

func (p *nativeFixtureProcess) PID() int         { return 4242 }
func (p *nativeFixtureProcess) InboxReady() bool { return !p.busy && (p.stayReady || p.inboxes == 0) }
func (p *nativeFixtureProcess) Wait() error      { <-p.done; return nil }
func (p *nativeFixtureProcess) Inbox(_ context.Context, r agentd.ControlRequest) (agentd.ControlEffect, error) {
	p.inboxes++
	p.effects = append(p.effects, r.CorrelationID)
	return agentd.ControlEffect{Primitive: "codex queue --thread", CorrelationID: r.CorrelationID}, nil
}

func TestNativeConsumersWaitForBusyReceiverWithoutLeasingFIFO(t *testing.T) {
	previousDB := paimosdb.DB
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "fixture")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	secretvault.ResetForTest()
	t.Cleanup(func() {
		secretvault.ResetForTest()
		if paimosdb.DB != nil {
			_ = paimosdb.DB.Close()
		}
		paimosdb.DB = previousDB
	})
	if err := paimosdb.Open(); err != nil {
		t.Fatal(err)
	}
	projectResult, err := paimosdb.DB.Exec(`INSERT INTO projects(name,key) VALUES('Busy fixture','BSY')`)
	if err != nil {
		t.Fatal(err)
	}
	project, _ := projectResult.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker'),(?,'sender')`, project, project); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	process := &nativeFixtureProcess{done: make(chan struct{}), busy: true, stayReady: true}
	controller, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []agentd.Adapter{nativeFixtureCursorAdapter{process}}})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())
	local, err := controller.Start(context.Background(), agentd.StartRequest{Adapter: "cursor", Identity: "cursor:worker", ProjectID: project, Workspace: t.TempDir(), Prompt: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	server := managedharness.NewService(paimosdb.DB)
	remote, _, err := server.Register(context.Background(), managedharness.RegisterInput{
		ProjectID: project, AgentName: "worker", Harness: "cursor", Host: "fixture-host", SessionRef: "fixture-private-thread",
		WorkerLease: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ManagementMode: managedharness.ManagementManaged,
		Role: managedharness.RoleWorker, SteerMode: managedharness.SteerNone, Capabilities: models.HarnessCapabilities{Inbox: true, Status: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.CheckpointReporter(context.Background(), local.ID, agentd.ControlRequest{Instance: "fixture", ProjectID: project, Identity: "cursor:worker"}, agentd.ReporterState{PublicSessionID: remote.ID, Capabilities: []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus}}); err != nil {
		t.Fatal(err)
	}
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), project, "cursor:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"first", "second", "third"} {
		if _, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: project, Sender: "sender", To: "cursor:worker", Body: body, DeliveryLevel: "simple"}); err != nil {
			t.Fatal(err)
		}
	}
	reporter, err := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		if args[1] == "curl" {
			route, err := url.Parse(args[2])
			if err != nil {
				return nil, err
			}
			switch {
			case strings.HasSuffix(route.Path, "/message-targets"):
				targets, err := bus.ListTargets(context.Background(), project, route.Query().Get("address"))
				if err != nil {
					return nil, err
				}
				return json.Marshal(map[string]any{"targets": targets, "count": len(targets)})
			case strings.HasSuffix(route.Path, "/message-deliveries"):
				deliveries, err := bus.ListDeliveryStatus(context.Background(), project)
				if err != nil {
					return nil, err
				}
				return json.Marshal(map[string]any{"deliveries": deliveries, "count": len(deliveries)})
			}
		}
		switch args[2] {
		case "status":
			return json.Marshal(remote)
		case "drain":
			page, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{ProjectID: project, Address: "cursor:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterManagedHarness, TargetID: remote.MessageTargetID, Limit: 100})
			if err != nil {
				return nil, err
			}
			for i := range page.Messages {
				if page.Messages[i].DeliveryWork != nil {
					page.Messages[i].DeliveryWork.TargetRef = ""
				}
			}
			return json.Marshal(page)
		case "complete-delivery":
			cursor, _ := strconv.ParseInt(args[slices.Index(args, "--cursor")+1], 10, 64)
			state, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{ProjectID: project, Address: "cursor:worker", Agent: "worker", Cursor: cursor, DeliveryID: args[slices.Index(args, "--delivery-id")+1], EffectiveLevel: args[slices.Index(args, "--effective-level")+1], FallbackReason: args[slices.Index(args, "--fallback-reason")+1], TargetID: remote.MessageTargetID})
			if err != nil {
				return nil, err
			}
			return json.Marshal(state)
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
	for range 8 {
		consumers.reconcile(context.Background())
	}
	statuses, err := bus.ListDeliveryStatus(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.State != "pending" || status.AttemptCount != 0 {
			t.Fatalf("busy receiver consumed a server attempt: %#v", status)
		}
	}
	if process.inboxes != 0 || consumers.supervisor.Snapshot()[0].Failures != 0 {
		t.Fatal("busy wait executed an effect or counted a circuit failure")
	}

	process.busy = false // Models an idle transition after the long turn is cancelled.
	for range len(statuses) + 2 {
		consumers.reconcile(context.Background())
	}
	statuses, err = bus.ListDeliveryStatus(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(process.effects) != len(statuses) {
		t.Fatalf("effects=%d deliveries=%d", len(process.effects), len(statuses))
	}
	for i, status := range statuses {
		if status.State != "handed_off" || status.AttemptCount != 1 || process.effects[i] != status.DeliveryID {
			t.Fatalf("delivery %d lost FIFO/exactly-once semantics: status=%#v effects=%#v", i, status, process.effects)
		}
	}

	// Reproduce the durable server shape left by the released implementation:
	// a pre-effect lease on the FIFO head and later rows marked fifo_blocked.
	for _, body := range []string{"poisoned head", "blocked follower"} {
		if _, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: project, Sender: "sender", To: "cursor:worker", Body: body, DeliveryLevel: "simple"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{ProjectID: project, Address: "cursor:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterManagedHarness, TargetID: remote.MessageTargetID, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	before := len(process.effects)
	consumers.reconcile(context.Background())
	if len(process.effects) != before || consumers.supervisor.Snapshot()[0].Failures != 0 {
		t.Fatal("live leased head plus fifo_blocked followers was misclassified as a conflict")
	}
	status := controller.Status()
	binding := runtimeconsumer.Binding{Instance: status.Instance, Machine: reporter.host, Generation: status.DaemonID, Session: local.ID, Address: local.Identity, Project: project, Kind: "primary", Revision: remote.ID}
	if err := consumers.RecoverCircuit(context.Background(), binding, runtimeconsumer.Evidence{Reason: "singleton_conflict"}); err != nil {
		t.Fatal("durable old-bug shape was not accepted for bounded repair", err)
	}
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

type nativeFixtureCursorAdapter struct{ process *nativeFixtureProcess }

func (nativeFixtureCursorAdapter) Name() string { return "cursor" }
func (nativeFixtureCursorAdapter) Capabilities() []agentd.Capability {
	return []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilityStop}
}
func (a nativeFixtureCursorAdapter) Start(_ context.Context, _ agentd.StartRequest, observe func(agentd.AdapterEvent)) (agentd.Process, error) {
	observe(agentd.AdapterEvent{Kind: agentd.EventSessionStarted, HarnessSessionID: "fixture-cursor-ref"})
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

func TestNativeConsumerRepairRefusesUnboundProject(t *testing.T) {
	// Other projects' retained bindings are not authority to repair this project.
	// There is intentionally no driver: a refusal must not verify, poll or clear
	// another project's stream circuit.
	consumers := &nativeConsumers{bindings: map[string]agentd.Session{
		"other-project": {ProjectID: 42},
	}}
	if err := consumers.RepairProject(context.Background(), 43); !errors.Is(err, runtimeconsumer.ErrAuthority) {
		t.Fatalf("repair without an owned binding for the requested project: %v", err)
	}
	if len(consumers.bindings) != 1 || consumers.bindings["other-project"].ProjectID != 42 {
		t.Fatal("refused repair changed another project's binding")
	}
}

func TestNativeConsumersHealthySessionProgressesWhileFirstStalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	process := &nativeFixtureProcess{done: make(chan struct{})}
	controller, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []agentd.Adapter{nativeFixtureAdapter{process}}})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())
	remotes := map[string]models.HarnessSession{}
	targets := map[string]string{}
	for _, name := range []string{"one", "two"} {
		session, err := controller.Start(ctx, agentd.StartRequest{Adapter: "codex", Identity: "codex:" + name, ProjectID: 42, Workspace: t.TempDir(), Prompt: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		public, target := uuid.NewString(), uuid.NewString()
		if err := controller.CheckpointReporter(ctx, session.ID, agentd.ControlRequest{Instance: "fixture", ProjectID: 42, Identity: session.Identity}, agentd.ReporterState{PublicSessionID: public, Capabilities: []agentd.Capability{agentd.CapabilityInbox}}); err != nil {
			t.Fatal(err)
		}
		remotes[public] = models.HarnessSession{ID: public, ProjectID: 42, AgentName: name, Harness: "codex", Host: "fixture-host", ManagementMode: "managed", MessageTargetID: target, Phase: "working", Capabilities: models.HarnessCapabilities{Inbox: true}}
		targets[session.Identity] = target
	}
	first := controller.Status().Sessions[0].Reporter.PublicSessionID
	entered, release, healthy := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enteredOnce, healthyOnce sync.Once
	reporter, err := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(callCtx context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		if args[1] == "curl" {
			route, err := url.Parse(args[2])
			if err != nil {
				return nil, err
			}
			address := route.Query().Get("address")
			return json.Marshal(map[string]any{"targets": []agentmessage.Target{{ID: targets[address], Instance: "fixture", ProjectID: 42, Address: address, Adapter: agentmessage.AdapterManagedHarness, Enabled: true, Role: "primary", Version: 1}}})
		}
		public := args[slices.Index(args, "--session")+1]
		remote := remotes[public]
		switch args[2] {
		case "status":
			if public == first {
				enteredOnce.Do(func() { close(entered) })
				select {
				case <-release:
				case <-callCtx.Done():
					return nil, callCtx.Err()
				}
			} else {
				select {
				case <-entered:
				case <-callCtx.Done():
					return nil, callCtx.Err()
				}
			}
			return json.Marshal(remote)
		case "drain":
			if public != first {
				healthyOnce.Do(func() { close(healthy) })
			}
			return json.Marshal(agentmessage.InboxPage{Address: remote.Harness + ":" + remote.AgentName})
		default:
			return nil, errors.New("unexpected command")
		}
	}, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	c, err := newNativeConsumers(root, "fixture", reporter)
	if err != nil {
		t.Fatal(err)
	}
	c.controller = controller
	defer c.supervisor.Stop()
	// Cancel active driver calls before Stop drains, including on test failure.
	defer cancel()
	done := make(chan struct{})
	go func() { c.reconcile(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first session never entered")
	}
	select {
	case <-healthy:
	case <-time.After(3 * time.Second):
		t.Fatal("healthy session waited behind stalled first session")
	}
	// Concurrent snapshots and repair must not race with per-session map updates.
	_ = c.Snapshot()
	repairCtx, stopRepair := context.WithCancel(ctx)
	stopRepair()
	if err := c.RepairProject(repairCtx, 42); !errors.Is(err, context.Canceled) {
		t.Fatal("repair did not wait for reconcile drain", err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reconcile failed to drain")
	}
	if err := c.RepairProject(ctx, 42); err != nil {
		t.Fatal(err)
	}
}
