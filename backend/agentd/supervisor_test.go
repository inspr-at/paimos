// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"fmt"
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

func TestSupervisorPrunesOnlyAfterDurableReporterClosure(t *testing.T) {
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-prune-reporter", MaxSessions: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	old := &sessionEntry{session: Session{ID: "11111111-1111-4111-8111-111111111111", Managed: true, State: StateOwnershipLost,
		StartedAt: time.Now().Add(-time.Hour), Reporter: ReporterState{PublicSessionID: "22222222-2222-4222-8222-222222222222", RemoteClosed: true}}}
	supervisor.sessions[old.session.ID] = old
	newEntry := &sessionEntry{session: Session{ID: "33333333-3333-4333-8333-333333333333", Managed: true, State: StateStarting, StartedAt: time.Now()}}
	if err := supervisor.reserveSession(newEntry); err == nil {
		t.Fatal("remote-closed session was pruned before durable lease release")
	}
	old.mu.Lock()
	old.session.Reporter.Closed = true
	old.mu.Unlock()
	if err := supervisor.reserveSession(newEntry); err != nil {
		t.Fatalf("durably closed session remained unprunable: %v", err)
	}
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

type failedStopProcess struct {
	*fakeProcess
	failure error
}

type ownedStopFailure struct{ detail string }

func (e *ownedStopFailure) Error() string { return e.detail }

func (p *failedStopProcess) Stop(_ context.Context, _ ControlRequest) (ControlEffect, error) {
	p.mu.Lock()
	p.stops++
	p.mu.Unlock()
	return ControlEffect{}, p.failure
}

func (p *blockingControlProcess) Steer(_ context.Context, request ControlRequest) (ControlEffect, error) {
	p.entered <- request.CorrelationID
	<-p.release
	return ControlEffect{Primitive: "codex app-server turn/steer", CorrelationID: request.CorrelationID}, nil
}

type fakeReporter struct{ reports chan Status }
type failingStatusReporter struct{}

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

func (failingStatusReporter) ReportStatus(context.Context, Status) error {
	return errors.New("private upstream detail must not surface")
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

func scopedControl(supervisor *Supervisor, session Session, correlationID, text string) ControlRequest {
	return ControlRequest{
		Instance: supervisor.instance, ProjectID: session.ProjectID, Identity: session.Identity,
		CorrelationID: correlationID, Text: text,
	}
}

func TestSupervisorListsAndControlsOnlyOwnedChildren(t *testing.T) {
	adapter := &fakeAdapter{name: AdapterCodex, process: newFakeProcess(1234), threadID: "thread-owned"}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-test", Adapters: []Adapter{adapter}, HeartbeatInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })

	if _, err := supervisor.Steer(context.Background(), "thread-someone-else", ControlRequest{CorrelationID: "delivery-foreign", Text: "do this"}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unowned steer error=%v, want ErrSessionNotFound", err)
	}
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "build the ticket", Identity: "codex:worker-849", ProjectID: 849,
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

	receipt, err := supervisor.Steer(context.Background(), session.ID, scopedControl(supervisor, session, "delivery-live-123", "tighten tests"))
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
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-test", Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-serial"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:serial", ProjectID: 849})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		_, callErr := supervisor.Steer(context.Background(), session.ID, scopedControl(supervisor, session, "delivery-one", "one"))
		results <- callErr
	}()
	if got := <-process.entered; got != "delivery-one" {
		t.Fatalf("first control=%q", got)
	}
	go func() {
		_, callErr := supervisor.Steer(context.Background(), session.ID, scopedControl(supervisor, session, "delivery-two", "two"))
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
			Adapter: AdapterCodex, Workspace: workspace, Prompt: "work", Identity: "codex:close-race", ProjectID: 849,
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
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-test", Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-stop-race"}}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:stop-race", ProjectID: 849,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Stop(context.Background(), session.ID, scopedControl(supervisor, session, "control-stop-race", "")); err != nil {
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

func TestSupervisorFailedStopReplayPreservesExactErrorAfterChildFinalizes(t *testing.T) {
	stopFailure := &ownedStopFailure{detail: "ordinary owned stop failure"}
	process := &failedStopProcess{fakeProcess: newFakeProcess(4250), failure: stopFailure}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-stop-replay", Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-stop-replay"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:stop-replay", ProjectID: 870,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := scopedControl(supervisor, session, "control-failed-stop-replay", "")
	firstReceipt, firstErr := supervisor.Stop(context.Background(), session.ID, request)
	if firstReceipt != (Receipt{}) || firstErr != stopFailure {
		t.Fatalf("first stop receipt=%+v error=%v want exact failure", firstReceipt, firstErr)
	}

	// The process exits after the failed stop attempt, making monitorDone close
	// successfully before the same correlation is retried. This is the window
	// that previously overwrote the memoized failure with nil.
	process.waited <- nil
	entry, err := supervisor.get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entry.monitorDone:
	case <-time.After(time.Second):
		t.Fatal("child finalization did not complete")
	}
	final := supervisor.Status().Sessions[0]
	if final.LastErrorCode != ErrorChildStopFailed {
		t.Fatalf("failed stop lost its terminal error invariant: %+v", final)
	}
	replayReceipt, replayErr := supervisor.Stop(context.Background(), session.ID, request)
	if replayReceipt != (Receipt{}) || replayErr != stopFailure {
		t.Fatalf("failed replay receipt=%+v error=%v want exact memoized failure", replayReceipt, replayErr)
	}
	process.mu.Lock()
	stops := process.stops
	process.mu.Unlock()
	if stops != 1 {
		t.Fatalf("failed stop replay reached vendor %d times, want once", stops)
	}
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
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-test", Adapters: []Adapter{first}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:worker", ProjectID: 849,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Steer(context.Background(), session.ID, scopedControl(supervisor, session, "delivery-first", "first")); err == nil {
		t.Fatal("first adapter failure was hidden")
	}
	if _, err := supervisor.Steer(context.Background(), session.ID, scopedControl(supervisor, session, "delivery-first", "first")); err == nil {
		t.Fatal("failed control replay was hidden")
	}
	process.mu.Lock()
	failedAttempts := len(process.steers)
	process.mu.Unlock()
	if failedAttempts != 1 {
		t.Fatalf("ambiguous failed control reached process %d times, want once", failedAttempts)
	}

	restarted := &fakeAdapter{name: AdapterCodex, process: first.process, threadID: first.threadID}
	if err := supervisor.RegisterAdapter(restarted); err != nil {
		t.Fatal(err)
	}
	process.mu.Lock()
	process.steerErr = nil
	process.mu.Unlock()
	if _, err := supervisor.Steer(context.Background(), session.ID, scopedControl(supervisor, session, "delivery-second", "second")); err != nil {
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
	session, err := first.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "TOP SECRET prompt 849", Identity: "codex:worker", ProjectID: 849})
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
	if _, err := second.Stop(context.Background(), session.ID, scopedControl(second, got[0], "control-after-restart", "")); !errors.Is(err, ErrSessionNotRunning) {
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

func TestSupervisorRestartKeepsLegacyUnscopedHistoryFailClosed(t *testing.T) {
	root := t.TempDir()
	journal, err := openRegistryJournal(root, "ppm-legacy", 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	legacy := Session{
		ID: "019d1234-1234-7123-8123-123456789abc", Identity: "codex:legacy", Adapter: AdapterCodex,
		Workspace: t.TempDir(), Capabilities: []Capability{CapabilityInbox, CapabilityStatus, CapabilitySteer, CapabilityInterrupt, CapabilityStop},
		Managed: true, State: StateRunning, StartedAt: now.Add(-time.Minute), HeartbeatAt: now,
	}
	if err := journal.put(legacy); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(SupervisorConfig{StateRoot: root, Instance: "ppm-legacy", MaxSessions: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := supervisor.Status().Sessions
	if len(got) != 1 || got[0].ProjectID != 0 || got[0].State != StateOwnershipLost || got[0].Steerable || got[0].PID != 0 {
		t.Fatalf("legacy recovery=%+v", got)
	}
	if _, err := supervisor.Steer(context.Background(), got[0].ID, ControlRequest{
		Instance: "ppm-legacy", ProjectID: 870, Identity: got[0].Identity,
		CorrelationID: "legacy-must-fail", Text: "do not apply",
	}); !errors.Is(err, ErrControlScopeMismatch) {
		t.Fatalf("legacy control error=%v, want ErrControlScopeMismatch", err)
	}
}

func TestSupervisorStateIsSeparatedByPPMInstance(t *testing.T) {
	root := t.TempDir()
	one, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: newFakeProcess(8888), threadID: "thread-one"}}, StateRoot: root, Instance: "ppm-one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := one.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:one", ProjectID: 849}); err != nil {
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
	oldSession := Session{ID: "019d1234-1234-7123-8123-123456789abc", Identity: "codex:old", ProjectID: 849, Adapter: AdapterCodex,
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
	newSession, err := second.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "new", Identity: "codex:new", ProjectID: 849})
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
		Adapter: AdapterClaude, Workspace: t.TempDir(), Prompt: "work", Identity: "claude:worker", ProjectID: 850,
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

func TestSupervisorSurfacesSanitizedReporterFailureState(t *testing.T) {
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-reporter-error", Reporter: failingStatusReporter{}, HeartbeatInterval: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	deadline := time.Now().Add(time.Second)
	for {
		status := supervisor.Status()
		if status.ReporterErrorCode == ErrorReporterUnavailable && status.ReporterFailureCount > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reporter failure was not surfaced: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSupervisorRejectsCrossScopeControlBeforeOwnedProcess(t *testing.T) {
	process := newFakeProcess(8701)
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-one",
		Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-scoped"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:scoped", ProjectID: 870,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, request := range []ControlRequest{
		{Instance: "ppm-two", ProjectID: 870, Identity: "codex:scoped", CorrelationID: "wrong-instance", Text: "do not apply"},
		{Instance: "ppm-one", ProjectID: 871, Identity: "codex:scoped", CorrelationID: "wrong-project", Text: "do not apply"},
		{Instance: "ppm-one", ProjectID: 870, Identity: "codex:other", CorrelationID: "wrong-identity", Text: "do not apply"},
	} {
		if _, err := supervisor.Steer(context.Background(), session.ID, request); !errors.Is(err, ErrControlScopeMismatch) {
			t.Fatalf("request=%+v error=%v, want ErrControlScopeMismatch", request, err)
		}
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	if len(process.steers) != 0 {
		t.Fatalf("cross-scope controls reached owned process: %+v", process.steers)
	}
}

func TestSupervisorReplaysCompletedControlReceiptWithoutDuplicateEffect(t *testing.T) {
	process := newFakeProcess(8702)
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-replay",
		Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-replay"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:replay", ProjectID: 870,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ControlRequest{
		Instance: "ppm-replay", ProjectID: 870, Identity: "codex:replay",
		CorrelationID: "delivery-replay-870", Text: "apply exactly once",
	}
	first, err := supervisor.Steer(context.Background(), session.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.Steer(context.Background(), session.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("replayed receipt changed: first=%+v second=%+v", first, second)
	}
	process.mu.Lock()
	steers := len(process.steers)
	process.mu.Unlock()
	if steers != 1 {
		t.Fatalf("completed delivery applied %d times, want once", steers)
	}

	request.Text = "reuse correlation with different input"
	if _, err := supervisor.Steer(context.Background(), session.ID, request); !errors.Is(err, ErrControlReplayConflict) {
		t.Fatalf("correlation reuse error=%v, want ErrControlReplayConflict", err)
	}
	request.Text = "bounded input"
	for i := 1; i < maxControlReplayEntries; i++ {
		request.CorrelationID = fmt.Sprintf("delivery-replay-%03d", i)
		if _, err := supervisor.Steer(context.Background(), session.ID, request); err != nil {
			t.Fatalf("fill replay entry %d: %v", i, err)
		}
	}
	request.CorrelationID = "delivery-over-bound"
	if _, err := supervisor.Steer(context.Background(), session.ID, request); !errors.Is(err, ErrControlReplayCapacity) {
		t.Fatalf("over-bound error=%v, want ErrControlReplayCapacity", err)
	}
	request.CorrelationID = "delivery-replay-870"
	request.Text = "apply exactly once"
	if replayed, err := supervisor.Steer(context.Background(), session.ID, request); err != nil || replayed != first {
		t.Fatalf("old replay after bound receipt=%+v error=%v", replayed, err)
	}
	process.mu.Lock()
	steers = len(process.steers)
	process.mu.Unlock()
	if steers != maxControlReplayEntries {
		t.Fatalf("bounded controls applied %d times, want %d", steers, maxControlReplayEntries)
	}
}
