// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProcess struct {
	pid        int
	waited     chan error
	stopOnce   sync.Once
	mu         sync.Mutex
	steers     []ControlRequest
	interrupts []ControlRequest
	stops      int
	steerErr   error
	stopErr    error
}

func newFakeProcess(pid int) *fakeProcess { return &fakeProcess{pid: pid, waited: make(chan error, 1)} }
func (p *fakeProcess) PID() int           { return p.pid }
func (p *fakeProcess) Wait() error        { return <-p.waited }
func (p *fakeProcess) Steer(_ context.Context, request ControlRequest) (ControlEffect, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steers = append(p.steers, request)
	return ControlEffect{Primitive: "codex app-server turn/steer", CorrelationID: request.CorrelationID, VendorMessageID: "turn-live"}, p.steerErr
}
func (p *fakeProcess) Interrupt(_ context.Context, request ControlRequest) (ControlEffect, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interrupts = append(p.interrupts, request)
	return ControlEffect{Primitive: "fake turn/interrupt", CorrelationID: request.CorrelationID}, nil
}
func (p *fakeProcess) Stop(_ context.Context, request ControlRequest) (ControlEffect, error) {
	p.mu.Lock()
	p.stops++
	p.mu.Unlock()
	p.stopOnce.Do(func() { p.waited <- nil })
	return ControlEffect{Primitive: "fake process stop", CorrelationID: request.CorrelationID}, p.stopErr
}

type fakeAdapter struct {
	name         string
	process      Process
	threadID     string
	startRequest StartRequest
	mu           sync.Mutex
}

type blockingControlProcess struct {
	*fakeProcess
	entered chan string
	release chan struct{}
}

func (p *blockingControlProcess) Steer(_ context.Context, request ControlRequest) (ControlEffect, error) {
	p.entered <- request.CorrelationID
	<-p.release
	return ControlEffect{Primitive: "codex app-server turn/steer", CorrelationID: request.CorrelationID}, nil
}

type fakeReporter struct{ reports chan Status }

type closeRaceAdapter struct {
	process *fakeProcess
	started chan struct{}
}

func (*closeRaceAdapter) Name() string { return AdapterCodex }
func (*closeRaceAdapter) Capabilities() []Capability {
	return []Capability{CapabilityInbox, CapabilityStatus, CapabilitySteer, CapabilityInterrupt, CapabilityStop}
}
func (a *closeRaceAdapter) Start(ctx context.Context, _ StartRequest, observe func(AdapterEvent)) (Process, error) {
	close(a.started)
	<-ctx.Done()
	observe(AdapterEvent{Kind: EventSessionStarted, HarnessSessionID: "thread-close-race"})
	return a.process, nil
}

func (r *fakeReporter) ReportStatus(_ context.Context, status Status) error {
	select {
	case r.reports <- status:
	default:
	}
	return nil
}

func (a *fakeAdapter) Name() string { return a.name }
func (a *fakeAdapter) Capabilities() []Capability {
	return []Capability{CapabilityInbox, CapabilityStatus, CapabilitySteer, CapabilityInterrupt, CapabilityStop}
}
func (a *fakeAdapter) Start(_ context.Context, request StartRequest, observe func(AdapterEvent)) (Process, error) {
	a.mu.Lock()
	a.startRequest = request
	a.mu.Unlock()
	observe(AdapterEvent{Kind: EventSessionStarted, HarnessSessionID: a.threadID})
	return a.process, nil
}

func TestSupervisorListsAndControlsOnlyOwnedChildren(t *testing.T) {
	adapter := &fakeAdapter{name: AdapterCodex, process: newFakeProcess(1234), threadID: "thread-owned"}
	supervisor, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{adapter}, HeartbeatInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })

	if _, err := supervisor.Steer(context.Background(), "thread-someone-else", ControlRequest{CorrelationID: "delivery-foreign", Text: "do this"}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unowned steer error=%v, want ErrSessionNotFound", err)
	}
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "build the ticket", Identity: "codex:worker-849",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !session.Managed || session.HarnessSessionID != "thread-owned" || !session.Steerable {
		t.Fatalf("owned session=%+v", session)
	}
	if adapter.startRequest.Prompt != "build the ticket" || adapter.startRequest.Identity != "codex:worker-849" {
		t.Fatalf("start request=%+v", adapter.startRequest)
	}

	receipt, err := supervisor.Steer(context.Background(), session.ID, ControlRequest{CorrelationID: "delivery-live-123", Text: "tighten tests"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Identity != "codex:worker-849" || receipt.RequestedLevel != "steer" ||
		receipt.EffectiveLevel != "steer" || receipt.FallbackReason != "" || receipt.CorrelationID != "delivery-live-123" || receipt.VendorMessageID != "turn-live" {
		t.Fatalf("steer receipt=%+v", receipt)
	}
	status := supervisor.Status()
	if len(status.Sessions) != 1 || status.Sessions[0].ID != session.ID {
		t.Fatalf("status leaked or invented a session: %+v", status)
	}
}

func TestSupervisorSerializesControlsOnExactLiveProcess(t *testing.T) {
	process := &blockingControlProcess{fakeProcess: newFakeProcess(2234), entered: make(chan string, 2), release: make(chan struct{})}
	supervisor, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-serial"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:serial"})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		_, callErr := supervisor.Steer(context.Background(), session.ID, ControlRequest{CorrelationID: "delivery-one", Text: "one"})
		results <- callErr
	}()
	if got := <-process.entered; got != "delivery-one" {
		t.Fatalf("first control=%q", got)
	}
	go func() {
		_, callErr := supervisor.Steer(context.Background(), session.ID, ControlRequest{CorrelationID: "delivery-two", Text: "two"})
		results <- callErr
	}()
	select {
	case got := <-process.entered:
		t.Fatalf("concurrent control reached live process: %q", got)
	case <-time.After(25 * time.Millisecond):
	}
	close(process.release)
	if got := <-process.entered; got != "delivery-two" {
		t.Fatalf("second control=%q", got)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSupervisorCloseCancelsAndReapsStartingProcess(t *testing.T) {
	process := newFakeProcess(3234)
	adapter := &closeRaceAdapter{process: process, started: make(chan struct{})}
	supervisor, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	startDone := make(chan error, 1)
	go func() {
		_, startErr := supervisor.Start(context.Background(), StartRequest{
			Adapter: AdapterCodex, Workspace: workspace, Prompt: "work", Identity: "codex:close-race",
		})
		startDone <- startErr
	}()
	<-adapter.started
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; err == nil {
		t.Fatal("start succeeded after supervisor closed")
	}
	process.mu.Lock()
	stops := process.stops
	process.mu.Unlock()
	if stops != 1 || len(supervisor.Status().Sessions) != 0 {
		t.Fatalf("stops=%d status=%+v", stops, supervisor.Status())
	}
}

func TestSupervisorStopRaceWithNaturalExitDoesNotOverwriteTerminalState(t *testing.T) {
	process := newFakeProcess(4234)
	process.stopErr = ErrSessionNotRunning
	supervisor, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-stop-race"}}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:stop-race",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Stop(context.Background(), session.ID, ControlRequest{CorrelationID: "control-stop-race"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got := supervisor.Status().Sessions[0]
		if got.State == StateStopped {
			if got.LastErrorCode == ErrorChildStopFailed || got.ExitedAt == nil {
				t.Fatalf("terminal state overwritten: %+v", got)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status=%+v", supervisor.Status())
}

func TestSupervisorTerminalPersistenceBarrierHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitSessionFinalized(ctx, &sessionEntry{monitorDone: make(chan struct{})})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("terminal persistence wait error=%v, want context cancellation", err)
	}
}

func TestSupervisorKeepsLiveProcessControlAcrossAdapterReplacement(t *testing.T) {
	process := newFakeProcess(4321)
	process.steerErr = errors.New("proxy restart")
	first := &fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-restart"}
	supervisor, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{first}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Steer(context.Background(), session.ID, ControlRequest{CorrelationID: "delivery-first", Text: "first"}); err == nil {
		t.Fatal("first adapter failure was hidden")
	}

	restarted := &fakeAdapter{name: AdapterCodex, process: first.process, threadID: first.threadID}
	if err := supervisor.RegisterAdapter(restarted); err != nil {
		t.Fatal(err)
	}
	process.mu.Lock()
	process.steerErr = nil
	process.mu.Unlock()
	if _, err := supervisor.Steer(context.Background(), session.ID, ControlRequest{CorrelationID: "delivery-second", Text: "second"}); err != nil {
		t.Fatalf("steer after adapter restart: %v", err)
	}
	if got := supervisor.Status().Sessions[0]; !got.Steerable || got.HarnessSessionID != "thread-restart" {
		t.Fatalf("ownership lost after adapter restart: %+v", got)
	}
}

func TestSupervisorRestartReconcilesPersistedChildrenToOwnershipLost(t *testing.T) {
	root := t.TempDir()
	process := newFakeProcess(7777)
	first, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-persisted"}}, StateRoot: root, Instance: "ppm-prod"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := first.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "TOP SECRET prompt 849", Identity: "codex:worker"})
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err := InstanceStateDir(root, "ppm-prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sessions.journal", "sessions.checkpoint.json"} {
		raw, readErr := os.ReadFile(filepath.Join(stateDir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, forbidden := range []string{"\"prompt\"", "\"pid\"", "Bearer ", "api_key", "vendor token", "TOP SECRET prompt 849"} {
			if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(forbidden)) {
				t.Fatalf("%s leaked %q: %s", name, forbidden, raw)
			}
		}
	}

	// Simulate the daemon disappearing without calling Close. A new daemon may
	// read history but must never adopt, signal, or control the old PID.
	second, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{NewUnsupportedAdapter(AdapterClaude)}, StateRoot: root, Instance: "ppm-prod"})
	if err != nil {
		t.Fatal(err)
	}
	got := second.Status().Sessions
	if len(got) != 1 || got[0].ID != session.ID || got[0].State != StateOwnershipLost || got[0].PID != 0 || got[0].Steerable {
		t.Fatalf("recovered session=%+v", got)
	}
	if len(got[0].Capabilities) != 2 || got[0].Capabilities[0] != CapabilityInbox || got[0].Capabilities[1] != CapabilityStatus {
		t.Fatalf("recovered capabilities=%v", got[0].Capabilities)
	}
	if _, err := second.Stop(context.Background(), session.ID, ControlRequest{CorrelationID: "control-after-restart"}); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("stop after restart error=%v", err)
	}
	process.mu.Lock()
	stops := process.stops
	process.mu.Unlock()
	if stops != 0 {
		t.Fatalf("new daemon signalled an unproven PID %d times", stops)
	}
	// The assertion above is the daemon-disappearance boundary: the restarted
	// supervisor never controlled the foreign child. Only now close the original
	// owner so its monitor finishes terminal persistence before TempDir cleanup.
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorStateIsSeparatedByPPMInstance(t *testing.T) {
	root := t.TempDir()
	one, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: newFakeProcess(8888), threadID: "thread-one"}}, StateRoot: root, Instance: "ppm-one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := one.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:one"}); err != nil {
		t.Fatal(err)
	}
	two, err := NewSupervisor(SupervisorConfig{StateRoot: root, Instance: "ppm-two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(two.Status().Sessions) != 0 {
		t.Fatal("PPM instances shared agentd state")
	}
	_ = one.Close(context.Background())
}

func TestSupervisorPrunesOwnershipLostHistoryBeforeActiveBound(t *testing.T) {
	root := t.TempDir()
	journal, err := openRegistryJournal(root, "ppm-prune", 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldSession := Session{ID: "019d1234-1234-7123-8123-123456789abc", Identity: "codex:old", Adapter: AdapterCodex,
		Workspace: t.TempDir(), Capabilities: []Capability{CapabilityInbox, CapabilityStatus}, Managed: true,
		State: StateOwnershipLost, StartedAt: now.Add(-time.Hour), HeartbeatAt: now, LastErrorCode: "ownership_lost"}
	if err := journal.put(oldSession); err != nil {
		t.Fatal(err)
	}
	newProcess := newFakeProcess(9002)
	second, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: newProcess, threadID: "thread-new"}}, StateRoot: root, Instance: "ppm-prune", MaxSessions: 1})
	if err != nil {
		t.Fatal(err)
	}
	newSession, err := second.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "new", Identity: "codex:new"})
	if err != nil {
		t.Fatal(err)
	}
	status := second.Status()
	if len(status.Sessions) != 1 || status.Sessions[0].ID != newSession.ID || status.Sessions[0].ID == oldSession.ID {
		t.Fatalf("status=%+v", status)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		records := second.journal.journal.Snapshot()
		if len(records) == 1 && records[0].Session.State == StateStopped {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("new session did not stop before cleanup")
}

func TestClaudeBoundaryStaysUnsupportedUntilOwnedAdapterExists(t *testing.T) {
	supervisor, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{NewUnsupportedAdapter(AdapterClaude)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Prompt: "work", Identity: "claude:worker",
	})
	if !errors.Is(err, ErrAdapterUnsupported) {
		t.Fatalf("claude start error=%v, want ErrAdapterUnsupported", err)
	}
}

func TestSupervisorReportsHeartbeatWithoutOwnedSessions(t *testing.T) {
	reporter := &fakeReporter{reports: make(chan Status, 4)}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-heartbeat", Reporter: reporter, HeartbeatInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	select {
	case status := <-reporter.reports:
		if status.Instance != "ppm-heartbeat" || len(status.Sessions) != 0 || status.DaemonID == "" {
			t.Fatalf("status=%+v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon emitted no empty heartbeat")
	}
}
