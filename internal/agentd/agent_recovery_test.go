// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

type recoveryAPI struct {
	*fakeAPI
	recoveryMu    sync.Mutex
	requests      []RecoveryRequest
	completions   []RecoveryReport
	fail          bool
	probeError    error
	hookReplyLost bool
	probes        int
}

func (a *recoveryAPI) ClaimAgentRecoveries(context.Context, string, string) ([]RecoveryRequest, error) {
	a.recoveryMu.Lock()
	defer a.recoveryMu.Unlock()
	out := a.requests
	a.requests = nil
	return out, nil
}
func (a *recoveryAPI) CompleteAgentRecovery(_ context.Context, _, _ string, r RecoveryReport) error {
	a.recoveryMu.Lock()
	defer a.recoveryMu.Unlock()
	a.completions = append(a.completions, r)
	if a.fail {
		a.fail = false
		return errors.New("fixture completion unavailable")
	}
	return nil
}

type recoveryOwnedProcess struct {
	*fakeProcess
	identity ownedprocess.Identity
}

func (p *recoveryOwnedProcess) Ownership() (ownedprocess.Identity, error) { return p.identity, nil }
func (p *recoveryOwnedProcess) ForceStop(context.Context, ownedprocess.Identity, time.Time) error {
	return errors.New("force must never be called")
}
func recoverySupervisor(t *testing.T) (*Supervisor, *recoveryAPI, *fakeProcess, RecoveryRequest) {
	t.Helper()
	s, api, p := testSupervisor(t)
	a := &recoveryAPI{fakeAPI: api}
	s.api = a
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry := s.runs["run"]
	identity := ownedprocess.Identity{DaemonID: s.daemonID, Generation: s.generation, ProcessID: "12345678901234567890123456789012", RootPID: 3456, GroupID: 3456, StartedAt: time.Now().UTC()}
	entry.mu.Lock()
	entry.process = &recoveryOwnedProcess{p, identity}
	entry.harness.Ownership = &identity
	entry.managedPolicy = true
	entry.mu.Unlock()
	run := "run"
	q := RecoveryRequest{ID: "recovery-1", SessionID: "session", ProjectID: "project", RunID: &run, Action: "restart", Ownership: identity, ExpiresAt: time.Now().Add(time.Minute), deadline: time.Now().Add(time.Minute)}
	t.Cleanup(func() {
		_ = p.Stop(context.Background())
		select {
		case <-entry.monitorDone:
		case <-time.After(3 * time.Second):
			t.Error("fixture monitor did not exit")
		}
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s, a, p, q
}

func TestAgentRecoveryRestartWaitsForExitAndRetriesOnlyOutcome(t *testing.T) {
	s, a, p, q := recoverySupervisor(t)
	exitObserved, releaseExit := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(releaseExit) }) })
	p.onExit = func() { close(exitObserved); <-releaseExit }
	a.requests = []RecoveryRequest{q}
	a.fail = true
	finished := make(chan error, 1)
	go func() { finished <- s.recoverAgents(t.Context()) }()
	select {
	case <-exitObserved:
	case <-time.After(3 * time.Second):
		t.Fatal("owned process never reached exit barrier")
	}
	a.recoveryMu.Lock()
	count := len(a.completions)
	a.recoveryMu.Unlock()
	if count != 0 {
		t.Fatal("continuation was requested before process exit")
	}
	release.Do(func() { close(releaseExit) })
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("fixture lost response was reported successful")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not settle")
	}
	entry := s.runs["run"]
	entry.mu.Lock()
	reports := len(entry.record.RecoveryReports)
	entry.mu.Unlock()
	if reports != 1 {
		t.Fatal("outcome uncertainty was not journaled")
	}
	if err := s.recoverAgents(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.recoveryMu.Lock()
	defer a.recoveryMu.Unlock()
	if len(a.completions) != 2 || a.completions[0] != a.completions[1] || a.completions[0].Outcome != "exited" {
		t.Fatalf("outcome retries=%+v", a.completions)
	}
	a.mu.Lock()
	claims := a.claims
	a.mu.Unlock()
	if claims != 1 {
		t.Fatal("recovery launched another local process instead of normal dispatch")
	}
}

func TestAgentRecoveryGenerationIdentityAndDeadlineRefuseSignals(t *testing.T) {
	s, _, p, q := recoverySupervisor(t)
	for _, mutate := range []func(*RecoveryRequest){func(q *RecoveryRequest) { q.Ownership.Generation = "old" }, func(q *RecoveryRequest) { q.Ownership.ProcessID = "different" }, func(q *RecoveryRequest) { q.SessionID = "another-session" }, func(q *RecoveryRequest) { q.deadline = time.Now().Add(-time.Second) }} {
		wrong := q
		mutate(&wrong)
		if out := s.applyAgentRecovery(t.Context(), s.runs["run"], wrong); out != "rejected" {
			t.Fatalf("unsafe action=%s", out)
		}
		select {
		case <-p.stopped:
			t.Fatal("refused recovery signalled the process")
		default:
		}
	}
	entry := s.runs["run"]
	entry.mu.Lock()
	entry.record.ExitObserved = true
	entry.mu.Unlock()
	wrong := q
	wrong.Ownership.ProcessID = "another-exited-process"
	if out := s.applyAgentRecovery(t.Context(), entry, wrong); out != "rejected" {
		t.Fatal("exit observation authorized a different process identity")
	}
	entry.mu.Lock()
	entry.record.ExitObserved = false
	entry.mu.Unlock()
}

func TestAgentRecoveryReconnectRefreshesReportingWithoutStopping(t *testing.T) {
	s, a, p, q := recoverySupervisor(t)
	q.Action = "reconnect"
	a.mu.Lock()
	beats := a.harnessBeats
	a.harnessControls = []HarnessControl{{ID: "pending-stop", Kind: "stop"}}
	a.harnessDeliveries = []HarnessDelivery{{ID: "pending-message", Body: "queued input"}}
	a.mu.Unlock()
	if out := s.applyAgentRecovery(t.Context(), s.runs["run"], q); out != "reconnected" {
		t.Fatalf("reconnect=%s", out)
	}
	a.mu.Lock()
	updated := a.harnessBeats > beats
	untouched := len(a.harnessControls) == 1 && len(a.harnessDeliveries) == 1 && a.harnessDeliveryCompletions == 0
	a.mu.Unlock()
	p.mu.Lock()
	injected := p.calls
	p.mu.Unlock()
	if !untouched || injected != 0 {
		t.Fatal("reconnect consumed a control or acted on an inbox message")
	}
	if !updated {
		t.Fatal("reconnect did not refresh heartbeat")
	}
	select {
	case <-p.stopped:
		t.Fatal("reconnect stopped the process")
	default:
	}
}

func (a *recoveryAPI) ProbeAttachedInbox(context.Context, HarnessSession) error {
	a.probes++
	return a.probeError
}

func (a *recoveryAPI) HeartbeatHarness(ctx context.Context, session HarnessSession, phase string) error {
	err := a.fakeAPI.HeartbeatHarness(ctx, session, phase)
	if session.AttachedHook && a.hookReplyLost {
		a.hookReplyLost = false
		return errors.New("fixture binding reply lost after commit")
	}
	return err
}
