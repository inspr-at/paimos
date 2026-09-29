// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/piprobe"
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

type detailedProbeAdapter struct {
	fakeAdapter
	err error
}

func (a *detailedProbeAdapter) ProbeStatus(context.Context, string) (bool, error) {
	return false, a.err
}

func TestProbeStartupFailureIsNotLoginRequired(t *testing.T) {
	s, api, process := testSupervisor(t)
	adapter := &detailedProbeAdapter{fakeAdapter: fakeAdapter{proc: process}, err: errors.New("synthetic startup failure")}
	s.adapters[Codex] = adapter
	_ = s.PollOnce(t.Context())
	status := s.Lifecycle("")
	if !status.HarnessFailed || status.LoginRequired || status.Ready || api.claims != 0 {
		t.Fatal("startup failure reported as login required or allowed work")
	}
	adapter.err = piprobe.ErrPrivateProfile
	_ = s.PollOnce(t.Context())
	status = s.Lifecycle("")
	if !status.ProfilePermissions || !status.HarnessFailed || status.LoginRequired {
		t.Fatal("permissions classification lost")
	}
	adapter.err = nil
	_ = s.PollOnce(t.Context())
	status = s.Lifecycle("")
	if status.ProfilePermissions || status.HarnessFailed || !status.LoginRequired || status.Ready || api.claims != 0 {
		t.Fatal("missing login not distinguished from startup failure")
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
	run.VerificationPolicy = "read_only"
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
	for _, adapter := range []Adapter{NewCodexAdapter("/unused", nil), NewPiAdapter("/unused", nil), NewCursorAdapter("/unused", nil)} {
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

// Readiness describes usable accounts, never cleanup or process ownership.
func TestLifecycleReadinessIsPerHarness(t *testing.T) {
	s, _, _ := testSupervisor(t)
	if status := s.Lifecycle(""); status.Ready || status.HarnessStatuses[Codex] != "checking" || status.HarnessDetails[Codex].Reason != "starting" {
		t.Fatalf("unprobed account is ready: %+v", status)
	}
	s.probedAccounts["account"] = true
	if status := s.Lifecycle(""); !status.Ready || status.HarnessStatuses[Codex] != "ready" {
		t.Fatalf("healthy account not ready: %+v", status)
	}
	s.SetHarnessHoldWithReason(Codex, "repin_pending", "waiting for active runs")
	if status := s.Lifecycle(""); status.Ready || status.HarnessStatuses[Codex] != "blocked" || status.HarnessDetails[Codex].Reason != "repin_pending" || status.HarnessDetails[Codex].Fix.Command != "" {
		t.Fatalf("held account is ready: %+v", status)
	}
	s.SetHarnessHold(Codex, "")
	s.loginRequired["account"] = true
	if status := s.Lifecycle(""); status.Ready || !status.LoginRequired || status.HarnessStatuses[Codex] != "login_required" || status.HarnessDetails[Codex].Fix.Command != "codex login" {
		t.Fatalf("unsigned account is ready: %+v", status)
	}
	s.loginRequired["account"] = false
	if _, err := s.Drain(DrainRequest{DaemonID: s.daemonID}); err != nil {
		t.Fatal(err)
	}
	if status := s.Lifecycle(""); status.Ready || status.HarnessStatuses[Codex] != "draining" || !status.AllFenced {
		t.Fatalf("fenced account is ready: %+v", status)
	}
}

func TestReadyAccountDoesNotHideBlockedSibling(t *testing.T) {
	s, _, _ := testSupervisor(t)
	s.mu.Lock()
	s.accounts = append(s.accounts, EnrolledAccount{ID: "blocked", Key: "blocked-local", Harness: Codex, DependencyBlocked: true, PinReason: agentsetup.PinDrifted})
	s.blockedAccounts["blocked"] = true
	s.probedAccounts["account"] = true
	s.mu.Unlock()
	status := s.Lifecycle("")
	detail := status.HarnessDetails[Codex]
	if !status.Ready || status.HarnessFailed || status.HarnessStatuses[Codex] != "ready" || detail.State != "ready" || detail.Reason != "" || detail.Fix.Command != "" || len(detail.Attention) != 1 || detail.Attention[0].AccountID != "blocked" || detail.Attention[0].Reason != agentsetup.PinDrifted || detail.AttentionCount != 1 || detail.AttentionTruncated {
		t.Fatalf("ready sibling erased the block: %+v ready=%v", detail, status.Ready)
	}
	if len(status.BlockedAccounts) != 1 || status.BlockedAccounts[0].AccountID != "blocked" || status.BlockedAccounts[0].Reason != agentsetup.PinDrifted {
		t.Fatalf("local blocked account lost: %+v", status.BlockedAccounts)
	}
	for _, item := range detail.Attention {
		if item.AccountID == "account" {
			t.Fatal("healthy account listed as needing attention")
		}
	}
}

func TestReadyAccountKeepsSixBlockedSiblings(t *testing.T) {
	s, _, _ := testSupervisor(t)
	s.mu.Lock()
	s.probedAccounts["account"] = true
	for i := 1; i <= 6; i++ {
		id := "b" + string(rune('0'+i))
		s.accounts = append(s.accounts, EnrolledAccount{ID: id, Key: id, Harness: Codex, DependencyBlocked: true, PinReason: agentsetup.PinDrifted})
	}
	s.mu.Unlock()
	detail := s.Lifecycle("").HarnessDetails[Codex]
	if detail.State != "ready" || detail.AttentionCount != 6 || detail.AttentionTruncated || len(detail.Attention) != 6 {
		t.Fatalf("six blocked siblings: %+v", detail)
	}
	seen := map[string]bool{}
	for _, item := range detail.Attention {
		if item.AccountID == "account" || item.Reason != agentsetup.PinDrifted {
			t.Fatalf("blocked set %+v", detail.Attention)
		}
		seen[item.AccountID] = true
	}
	if len(seen) != 6 {
		t.Fatalf("blocked set %+v", detail.Attention)
	}
}
