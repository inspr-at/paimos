// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDrainCloseNeverStopsOwnedChild(t *testing.T) {
	s, a, p := testSupervisor(t)
	if err := s.StartRun(t.Context(), a.run); err != nil {
		t.Fatal(err)
	}
	status, err := s.Drain(DrainRequest{DaemonID: s.DaemonID(), AccountID: "account"})
	if err != nil || status.State != "draining" || len(status.ActiveRunIDs) != 1 {
		t.Fatalf("drain status: %v %s", err, status.State)
	}
	if err = s.Close(t.Context()); !errors.Is(err, ErrDraining) {
		t.Fatal("Close did not preserve ownership while draining")
	}
	select {
	case <-p.stopped:
		t.Fatal("drain killed owned process")
	default:
	}
	if err = s.StartRun(t.Context(), Run{ID: "new", AccountID: "account"}); !errors.Is(err, ErrDraining) {
		t.Fatal("new work admitted after drain")
	}
	// Only the test child itself exits. No supervisor Stop call is needed.
	p.once.Do(func() { close(p.stopped) })
	<-s.runs[a.run.ID].monitorDone
	if st := s.Lifecycle(""); st.State != "drained" {
		t.Fatalf("exit not observed: %s", st.State)
	}
	if err = s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type failingReportAPI struct {
	*fakeAPI
	mu2  sync.Mutex
	fail bool
}

func (a *failingReportAPI) Report(ctx context.Context, id string, t Telemetry) error {
	a.mu2.Lock()
	fail := a.fail
	a.mu2.Unlock()
	if fail {
		return errors.New("offline")
	}
	return a.fakeAPI.Report(ctx, id, t)
}

func TestOutagePreservesProcessAndPersistsExactSettlement(t *testing.T) {
	s, a, p := testSupervisor(t)
	offline := &failingReportAPI{fakeAPI: a, fail: true}
	s.api = offline
	if err := s.StartRun(t.Context(), a.run); err == nil {
		t.Fatal("outage hidden")
	}
	select {
	case <-p.stopped:
		t.Fatal("telemetry outage killed owned process")
	default:
	}
	s.observe(s.runs[a.run.ID], AdapterEvent{Kind: "usage", InputTokensDelta: 7})
	select {
	case <-p.stopped:
		t.Fatal("observer failure killed owned process")
	default:
	}
	if len(s.Lifecycle("").SettlementPendingRunIDs) != 1 {
		t.Fatal("settlement evidence lost")
	}
	offline.mu2.Lock()
	offline.fail = false
	offline.mu2.Unlock()
	s.settlePending(t.Context())
	a.mu.Lock()
	if len(a.reports) != 2 || a.reports[0].Sequence != 1 || a.reports[1].Sequence != 2 || a.reports[1].InputTokensDelta != 7 {
		t.Error("persisted sequences did not settle exactly")
	}
	a.mu.Unlock()
	if len(s.Lifecycle("").SettlementPendingRunIDs) != 0 {
		t.Fatal("settled outbox not acknowledged")
	}
}

type probeFenceAPI struct {
	*fakeAPI
	probed []string
}

func (a *probeFenceAPI) Probe(_ context.Context, id, _, _ string, _ bool) error {
	a.probed = append(a.probed, id)
	if id == "revoked" {
		return errors.New("revoked")
	}
	return nil
}
func TestRevokedProbeDoesNotStarveOtherAccount(t *testing.T) {
	s, a, _ := testSupervisor(t)
	api := &probeFenceAPI{fakeAPI: a}
	s.api = api
	s.accounts = append([]EnrolledAccount{{ID: "revoked", Key: "revoked-local", Harness: Codex}}, s.accounts...)
	_ = s.PollOnce(t.Context())
	if len(api.probed) != 2 || a.claims != 1 || len(a.routeAccounts) != 1 || a.routeAccounts[0] != "account" {
		t.Fatal("unavailable enrollment starved healthy enrollment")
	}
}

type verificationFakeAdapter struct {
	fakeAdapter
	starts  int
	request StartRequest
}

func (*verificationFakeAdapter) VerificationSupported() bool { return true }
func (a *verificationFakeAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	a.starts++
	a.request = r
	return a.fakeAdapter.Start(ctx, r, observe)
}
func TestVerificationOneShotNoRepoToolsAndNoDuplicate(t *testing.T) {
	s, a, p := testSupervisor(t)
	adapter := &verificationFakeAdapter{fakeAdapter: fakeAdapter{proc: p}}
	s.adapters[Codex] = adapter
	no := false
	duration := int64(60)
	run := a.run
	run.Purpose = VerificationPurpose
	run.AccountID = "account"
	run.VerificationTask = VerificationTask
	run.MaxDurationSeconds = &duration
	run.ToolsAllowed = &no
	run.RepositoryMutationAllowed = &no
	if err := s.StartRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if adapter.request.Tools != nil || adapter.request.Prompt != VerificationTask || adapter.request.Workspace == s.workspace {
		t.Fatal("verification inherited managed workspace/tools/prompt")
	}
	if err := s.StartRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 1 || a.claims != 1 {
		t.Fatal("verification duplicated")
	}
	if _, err := s.Control(t.Context(), ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: run.ID, Generation: s.generation, CorrelationID: "steer", Operation: "steer", Text: "mutate"}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("verification accepted extra turn")
	}
	if len(s.journal.Snapshot()) != 1 || s.journal.Snapshot()[0].ExecutionMode != VerificationPurpose {
		t.Fatal("verification identity not persisted")
	}
}
func TestUnqualifiedVerificationNeverLaunches(t *testing.T) {
	for _, adapter := range []Adapter{NewCodexAdapter("/unused", nil), NewCursorAdapter("/unused", nil), NewPiAdapter("/unused", nil), NewGrokAdapter()} {
		if _, err := adapter.Start(t.Context(), StartRequest{Run: Run{Purpose: VerificationPurpose}}, func(AdapterEvent) {}); !errors.Is(err, ErrVerificationUnavailable) {
			t.Fatalf("%s did not fail before launch", adapter.Name())
		}
	}
}
func TestUncertainRestartDoesNotRelaunchVerification(t *testing.T) {
	s, a, p := testSupervisor(t)
	if err := s.journal.Put(Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, AccountID: "account", Generation: s.generation, ExecutionMode: VerificationPurpose, State: "starting"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSupervisor(t.Context(), Config{API: a, StateRoot: s.journalDir(), DaemonID: "daemon", Workspace: s.workspace, Adapters: []Adapter{&fakeAdapter{proc: p}}, Accounts: s.accounts, EstimatedUnits: s.estimates})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.StartRun(t.Context(), a.run); err != nil {
		t.Fatal(err)
	}
	if a.claims != 0 || restarted.Lifecycle("").State != "unconfirmed" {
		t.Fatal("uncertain attempt resurrected")
	}
	if err = restarted.Close(context.Background()); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("unconfirmed process accepted as idle")
	}
	// Release test fixture descriptors only; no process is adopted/signalled.
	_ = restarted.lock.Close()
	_ = restarted.state.Close()
}

var _ = time.Second
