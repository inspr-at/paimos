// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeAPI struct {
	mu                         sync.Mutex
	run                        Run
	profile                    Profile
	claims                     int
	reports                    []Telemetry
	acks                       int
	page                       InboxPage
	pageFn                     func(int64) InboxPage
	harnessRegistration        HarnessSession
	harnessCaps                []string
	harnessBeats               int
	harnessControls            []HarnessControl
	harnessCompletions         []string
	harnessDeliveries          []HarnessDelivery
	harnessDeliveryCompletions int
	harnessStops               []string
	routeDaemon                string
	routeAccounts              []string
	metadataReports            []AccountMetadata
}

func (*fakeAPI) Identity(context.Context) (string, string, error) { return "tenant", "agent", nil }
func (a *fakeAPI) Queued(context.Context) ([]Run, error)          { return []Run{a.run}, nil }
func (a *fakeAPI) GetRun(context.Context, string) (Run, error)    { return a.run, nil }
func (a *fakeAPI) Profiles(context.Context) ([]Profile, error)    { return []Profile{a.profile}, nil }
func (*fakeAPI) Node(context.Context, string) (Node, error) {
	return Node{ID: "order", Key: "TASK-1", Title: "Do work", Body: "Criteria"}, nil
}
func (*fakeAPI) WorkOrder(context.Context, string) (WorkOrder, error) {
	return WorkOrder{NodeID: "order", Status: "ready"}, nil
}
func (a *fakeAPI) Route(_ context.Context, _ string, daemonID string, accountIDs []string, _ map[string]int64) (Route, error) {
	a.routeDaemon = daemonID
	a.routeAccounts = append([]string(nil), accountIDs...)
	return Route{AccountID: "account", AccountKey: "local", AccountLabel: "Work subscription", DaemonID: "daemon", Reservations: []Reservation{{ID: "reservation"}}}, nil
}
func (a *fakeAPI) Claim(context.Context, string, string, string, []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claims++
	return nil
}
func (a *fakeAPI) Report(_ context.Context, _ string, t Telemetry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reports = append(a.reports, t)
	return nil
}
func (a *fakeAPI) Inbox(_ context.Context, after int64) (InboxPage, error) {
	if a.pageFn != nil {
		return a.pageFn(after), nil
	}
	return a.page, nil
}
func (a *fakeAPI) Ack(context.Context, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks++
	return nil
}
func (*fakeAPI) AddEvidence(context.Context, string, string, string) error { return nil }
func (*fakeAPI) Probe(context.Context, string, string, string, bool) error { return nil }
func (*fakeAPI) ProjectForNode(context.Context, string) (string, error)    { return "project", nil }
func (a *fakeAPI) RegisterHarness(_ context.Context, s HarnessSession, _, _, _, _, _ string, caps []string) (HarnessSession, error) {
	s.ID = "session"
	a.mu.Lock()
	a.harnessRegistration = s
	a.harnessCaps = append([]string(nil), caps...)
	a.mu.Unlock()
	return s, nil
}
func (a *fakeAPI) HeartbeatHarness(context.Context, HarnessSession, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.harnessBeats++
	return nil
}
func (a *fakeAPI) YieldHarness(context.Context, HarnessSession) ([]HarnessControl, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := a.harnessControls
	a.harnessControls = nil
	return result, nil
}
func (a *fakeAPI) DrainHarness(context.Context, HarnessSession) ([]HarnessDelivery, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.harnessDeliveries, nil
}
func (a *fakeAPI) CompleteHarnessControl(_ context.Context, _ HarnessSession, id, outcome, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.harnessCompletions = append(a.harnessCompletions, id+":"+outcome+":"+reason)
	return nil
}
func (a *fakeAPI) CompleteHarnessDelivery(context.Context, HarnessSession, HarnessDelivery) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.harnessDeliveryCompletions++
	a.harnessDeliveries = nil
	return nil
}
func (a *fakeAPI) StopHarness(_ context.Context, _ HarnessSession, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.harnessStops = append(a.harnessStops, reason)
	return nil
}

type fakeAdapter struct{ proc *fakeProcess }

func (*fakeAdapter) Name() string                       { return Codex }
func (*fakeAdapter) Probe(context.Context, string) bool { return true }
func (a *fakeAdapter) Start(_ context.Context, _ StartRequest, _ func(AdapterEvent)) (Process, error) {
	return a.proc, nil
}

type fakeProcess struct {
	mu      sync.Mutex
	calls   int
	stopped chan struct{}
	once    sync.Once
}

func (*fakeProcess) PID() int      { return 3456 }
func (p *fakeProcess) Wait() error { <-p.stopped; return nil }
func (p *fakeProcess) Control(_ context.Context, _, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return nil
}
func (p *fakeProcess) GracefulStop(ctx context.Context) error { return p.Stop(ctx) }
func (p *fakeProcess) Stop(context.Context) error             { p.once.Do(func() { close(p.stopped) }); return nil }

func testSupervisor(t *testing.T, metadata ...*AccountMetadata) (*Supervisor, *fakeAPI, *fakeProcess) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "work")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	a := &fakeAPI{run: Run{ID: "run", WorkOrderID: "order", AgentPrincipalID: "agent", ModelProfileID: "profile", Status: "queued"}, profile: Profile{ID: "profile", Harness: Codex, Model: "model", Effort: "high"}}
	p := &fakeProcess{stopped: make(chan struct{})}
	var publicMetadata *AccountMetadata
	if len(metadata) > 0 {
		publicMetadata = metadata[0]
	}
	s, err := NewSupervisor(context.Background(), Config{API: a, StateRoot: state, DaemonID: "daemon", Workspace: workspace,
		Adapters: []Adapter{&fakeAdapter{proc: p}}, EstimatedUnits: map[string]int64{"requests": 1}, Accounts: []EnrolledAccount{{ID: "account", Key: "local", Harness: Codex, Metadata: publicMetadata}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Stop(context.Background())
		for _, e := range s.runs {
			e.mu.Lock()
			done := e.monitorDone
			e.mu.Unlock()
			if done != nil {
				<-done
			}
		}
		_ = s.Close(context.Background())
	})
	return s, a, p
}

func TestManagedHarnessLifecycleAndControls(t *testing.T) {
	s, a, p := testSupervisor(t)
	defer s.Close(context.Background())
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	registration := a.harnessRegistration
	caps := append([]string(nil), a.harnessCaps...)
	a.harnessControls = []HarnessControl{{ID: "interrupt-1", Kind: "interrupt"}}
	a.harnessDeliveries = []HarnessDelivery{{ID: "delivery-1", Cursor: 1, Body: "Please check this"}}
	a.mu.Unlock()
	if registration.ID != "session" || registration.ProjectID != "project" || len(registration.Lease) < 32 || len(caps) != 5 {
		t.Fatalf("managed registration binding or capabilities invalid: %v", caps)
	}
	if registration.Model != a.profile.Model || registration.ReasoningEffort != a.profile.Effort || registration.AccountLabel != "Work subscription" {
		t.Fatal("managed registration lost chosen model, effort or account label")
	}
	if err := s.serviceHarness(t.Context(), s.runs["run"]); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	calls := p.calls
	p.mu.Unlock()
	a.mu.Lock()
	completions := append([]string(nil), a.harnessCompletions...)
	deliveries := a.harnessDeliveryCompletions
	beats := a.harnessBeats
	a.mu.Unlock()
	if calls != 2 || len(completions) != 1 || completions[0] != "interrupt-1:applied:agentd_applied" || deliveries != 1 || beats < 2 {
		t.Fatalf("control/drain: calls=%d completions=%v deliveries=%d beats=%d", calls, completions, deliveries, beats)
	}
	a.mu.Lock()
	a.harnessControls = []HarnessControl{{ID: "stop-1", Kind: "stop"}}
	a.mu.Unlock()
	if err := s.serviceHarness(t.Context(), s.runs["run"]); err != nil {
		t.Fatal(err)
	}
	entry := s.runs["run"]
	entry.mu.Lock()
	done := entry.monitorDone
	entry.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("owned child did not stop")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.harnessCompletions) != 2 || a.harnessCompletions[1] != "stop-1:applied:agentd_applied" || len(a.harnessStops) != 1 || a.harnessStops[0] != "stopped" {
		t.Fatalf("stop completion: controls=%v stops=%v", a.harnessCompletions, a.harnessStops)
	}
}

func TestSupervisorClaimControlReplayAndScope(t *testing.T) {
	s, a, p := testSupervisor(t)
	ctx := context.Background()
	if err := s.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if a.claims != 1 || len(a.reports) != 1 || a.reports[0].Kind != "started" {
		t.Fatalf("claim/report: %d %#v", a.claims, a.reports)
	}
	if a.routeDaemon != "daemon" || len(a.routeAccounts) != 1 || a.routeAccounts[0] != "account" {
		t.Fatalf("route enrollment: daemon=%q accounts=%v", a.routeDaemon, a.routeAccounts)
	}
	req := ControlRequest{TenantID: "tenant", PrincipalID: "agent", RunID: "run", Generation: s.Generation(), CorrelationID: "control-1", Operation: "steer", Text: "next"}
	first, err := s.Control(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Control(ctx, req)
	if err != nil || second != first {
		t.Fatalf("replay: %#v %v", second, err)
	}
	if p.calls != 1 {
		t.Fatalf("control called %d times", p.calls)
	}
	req.Text = "different"
	if _, err := s.Control(ctx, req); !errors.Is(err, ErrReplay) {
		t.Fatalf("divergent replay: %v", err)
	}
	req.TenantID = "foreign"
	if _, err := s.Control(ctx, req); !errors.Is(err, ErrScope) {
		t.Fatalf("foreign tenant: %v", err)
	}
	req.TenantID = "tenant"
	req.Generation = "old"
	if _, err := s.Control(ctx, req); !errors.Is(err, ErrScope) {
		t.Fatalf("stale generation: %v", err)
	}
	_ = s.Close(ctx)
}

func TestInboxAckAfterControlAndForeignSenderPending(t *testing.T) {
	s, a, p := testSupervisor(t)
	ctx := context.Background()
	if err := s.StartRun(ctx, a.run); err != nil {
		t.Fatal(err)
	}
	a.page = InboxPage{Items: []InboxMessage{
		{ID: "foreign", SenderPrincipalID: "person", Body: `{"run_id":"run","operation":"steer","text":"unsafe"}`},
		{ID: "message", SenderPrincipalID: "agent", Body: `{"run_id":"run","operation":"steer","text":"safe","generation":"` + s.Generation() + `"}`},
	}}
	if err := s.DeliverInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if a.acks != 1 || p.calls != 1 {
		t.Fatalf("ack=%d calls=%d", a.acks, p.calls)
	}
	if err := s.DeliverInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatalf("duplicate effect: %d", p.calls)
	}
	_ = s.Close(ctx)
}

func TestInboxPagesPastForeignMessages(t *testing.T) {
	s, a, p := testSupervisor(t)
	ctx := context.Background()
	if err := s.StartRun(ctx, a.run); err != nil {
		t.Fatal(err)
	}
	foreign := make([]InboxMessage, 100)
	for i := range foreign {
		foreign[i] = InboxMessage{ID: "foreign", SenderPrincipalID: "person", SentEventID: int64(i + 1)}
	}
	a.pageFn = func(after int64) InboxPage {
		if after == 0 {
			return InboxPage{Items: foreign, NextAfter: 100}
		}
		return InboxPage{Items: []InboxMessage{{ID: "message", SenderPrincipalID: "agent", SentEventID: 101,
			Body: `{"run_id":"run","operation":"steer","text":"safe","generation":"` + s.Generation() + `"}`}}, NextAfter: 101}
	}
	if err := s.DeliverInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if a.acks != 1 || p.calls != 1 {
		t.Fatalf("ack=%d calls=%d", a.acks, p.calls)
	}
	_ = s.Close(ctx)
}

func TestRestartDoesNotAdoptPersistedPID(t *testing.T) {
	s, a, p := testSupervisor(t)
	ctx := context.Background()
	if err := s.journal.Put(Record{TenantID: "tenant", PrincipalID: "agent", RunID: "run", Generation: s.Generation(),
		Workspace: s.workspace, PID: 3456, State: "running", Controls: map[string]replay{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSupervisor(ctx, Config{API: a, StateRoot: s.journalDir(), DaemonID: "daemon", Workspace: s.workspace,
		Adapters: []Adapter{&fakeAdapter{proc: p}}, EstimatedUnits: map[string]int64{"requests": 1}, Accounts: s.accounts})
	if err != nil {
		t.Fatal(err)
	}
	status := restarted.Status()
	if len(status) != 1 || status[0].State != "ownership_lost" {
		t.Fatalf("recovered status: %#v", status)
	}
	if _, err := restarted.Control(ctx, ControlRequest{TenantID: "tenant", PrincipalID: "agent", RunID: "run", Generation: restarted.Generation(), CorrelationID: "x", Operation: "stop"}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("stale PID control: %v", err)
	}
	_ = restarted.Close(ctx)
}

func TestOneDaemonGenerationPerStateRoot(t *testing.T) {
	s, a, p := testSupervisor(t)
	_, err := NewSupervisor(context.Background(), Config{API: a, StateRoot: s.journalDir(), DaemonID: "daemon", Workspace: s.workspace,
		Adapters: []Adapter{&fakeAdapter{proc: p}}, EstimatedUnits: map[string]int64{"requests": 1}, Accounts: s.accounts})
	if err == nil {
		t.Fatal("second daemon acquired the same instance")
	}
	_ = s.Close(context.Background())
}

func TestHeartbeatAndChildDeadline(t *testing.T) {
	s, a, p := testSupervisor(t)
	s.heartbeatInterval = time.Second
	s.maxRunDuration = 1500 * time.Millisecond
	if err := s.StartRun(context.Background(), a.run); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.stopped:
		<-s.runs[a.run.ID].monitorDone
	case <-time.After(3 * time.Second):
		t.Fatal("child exceeded deadline")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	heartbeat, cancelled := false, false
	for _, report := range a.reports {
		if report.Kind == "heartbeat" {
			heartbeat = true
		}
		if report.Kind == "finished" && report.Status == "cancelled" {
			cancelled = true
		}
	}
	if !heartbeat || !cancelled {
		t.Fatalf("heartbeat=%v cancelled=%v", heartbeat, cancelled)
	}
}

func (s *Supervisor) journalDir() string { return filepath.Dir(s.journal.JournalPath()) }
