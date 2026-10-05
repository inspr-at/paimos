// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"strings"
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

func TestAttachedHookHelperCrashResumePreservesBinding(t *testing.T) {
	for _, scenario := range []string{"resumed", "expired-binding", "pid-reused", "lost-rebind-reply", "old-helper-live", "old-helper-unobservable", "wrong-lease", "wrong-project", "wrong-harness", "changed-process", "helper-changes-during-verification"} {
		t.Run(scenario, func(t *testing.T) {
			s, api, p := testSupervisor(t)
			a := &recoveryAPI{fakeAPI: api}
			s.api = a
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			m, oldHelper, target, _, _ := attachIdentityFixture(t, Claude)
			s.attachedHookVerifier = m
			observe := m.observe
			newHelper := oldHelper
			newHelper.PID, newHelper.Started = 31, "resumed-helper"
			hook := oldHelper
			hook.PID, hook.Started, hook.Parent = 50, "foreground-hook", target.PID
			oldGone, newChecks := false, 0
			m.observe = func(pid int) (attachObservation, error) {
				if pid == hook.PID {
					return hook, nil
				}
				if pid == newHelper.PID && (oldGone || pid != oldHelper.PID) {
					newChecks++
					if scenario == "helper-changes-during-verification" && newChecks > 1 {
						return attachObservation{}, errAttachExited
					}
					return newHelper, nil
				}
				if pid == oldHelper.PID && oldGone {
					if scenario == "old-helper-unobservable" {
						return attachObservation{}, errors.New("fixture observation denied")
					}
					return attachObservation{}, errAttachExited
				}
				return observe(pid)
			}
			m.ancestry = m.observe
			in := AttachedHookRequest{Origin: m.cfg.Origin, ProjectID: "11111111-1111-4111-8111-111111111111", SessionID: "22222222-2222-4222-8222-222222222222", Harness: Claude, Lease: strings.Repeat("fixture-", 8), OwnerPID: target.PID, Sequence: 2, Activity: "busy"}
			if err := s.bindAttachedHook(t.Context(), oldHelper, in); err != nil {
				t.Fatal(err)
			}
			initial := s.attachedHooks[in.SessionID]
			identity := *initial.session.Ownership
			oldGone = scenario != "old-helper-live"
			if scenario == "pid-reused" {
				newHelper.PID = oldHelper.PID
			}
			if oldGone {
				if _, err := s.serviceAttachedHook(t.Context(), hook, AttachedHookRequest{Operation: "pull", SessionID: in.SessionID}); !errors.Is(err, ErrGeneration) {
					t.Fatalf("dead helper did not invalidate foreground delivery: %v", err)
				}
			}
			if scenario == "expired-binding" {
				initial.touched = time.Now().Add(-11 * time.Minute)
				other := in
				other.SessionID = "33333333-3333-4333-8333-333333333333"
				if err := s.bindAttachedHook(t.Context(), newHelper, other); err != nil {
					t.Fatal(err)
				}
			}
			changed := in
			changed.Sequence++
			switch scenario {
			case "wrong-lease":
				changed.Lease = strings.Repeat("other-", 8)
			case "wrong-project":
				changed.ProjectID = "44444444-4444-4444-8444-444444444444"
			case "wrong-harness":
				changed.Harness = Codex
			case "changed-process":
				target.Started = "reused-external-pid"
			}
			if scenario == "lost-rebind-reply" {
				a.hookReplyLost = true
				if err := s.bindAttachedHook(t.Context(), newHelper, changed); err == nil || err.Error() != "fixture binding reply lost after commit" {
					t.Fatalf("lost rebind reply did not expose uncertainty: %v", err)
				}
				if *s.attachedHooks[in.SessionID].session.Ownership != identity {
					t.Fatal("uncertain rebind replaced server identity")
				}
			}
			err := s.bindAttachedHook(t.Context(), newHelper, changed)
			if scenario != "resumed" && scenario != "expired-binding" && scenario != "pid-reused" && scenario != "lost-rebind-reply" {
				want := ErrGeneration
				if scenario == "wrong-harness" || scenario == "helper-changes-during-verification" {
					want = ErrNotOwned
				}
				if !errors.Is(err, want) {
					t.Fatalf("unsafe replacement helper refusal=%v; want %v", err, want)
				}
				if s.attachedHooks[in.SessionID] != initial || initial.helper != oldHelper.Process || *initial.session.Ownership != identity || initial.session.ActivitySequence != in.Sequence {
					t.Fatal("refused helper changed binding")
				}
				return
			}
			if err != nil {
				t.Fatalf("verified resumed helper refused: %v", err)
			}
			binding := s.attachedHooks[in.SessionID]
			if binding.helper != newHelper.Process || binding.process != target.Process || *binding.session.Ownership != identity || binding.session.Lease != in.Lease {
				t.Fatal("resuming helper replaced external process authority")
			}
			q := RecoveryRequest{ID: "resumed-reconnect", SessionID: in.SessionID, ProjectID: in.ProjectID, Action: "reconnect", Ownership: identity, deadline: time.Now().Add(time.Minute)}
			a.requests = []RecoveryRequest{q}
			if err := s.recoverAgents(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(a.completions) != 1 || a.completions[0].Outcome != "reconnected" || a.probes != 1 {
				t.Fatal("resumed binding could not reconnect")
			}
			delivery := HarnessDelivery{ID: "55555555-5555-4555-8555-555555555555", Cursor: 1, Body: "pending foreground input"}
			a.harnessDeliveries = []HarnessDelivery{delivery}
			for i := 0; i < 2; i++ {
				out, err := s.serviceAttachedHook(t.Context(), hook, AttachedHookRequest{Operation: "pull", SessionID: in.SessionID})
				if err != nil || len(out) != 1 || out[0] != delivery {
					t.Fatalf("resumed foreground inbox unavailable: %v", err)
				}
			}
			if a.harnessDeliveryCompletions != 0 {
				t.Fatal("reconnect acknowledged pending delivery")
			}
			if _, err := s.serviceAttachedHook(t.Context(), hook, AttachedHookRequest{Operation: "complete", SessionID: in.SessionID, DeliveryID: delivery.ID, Cursor: delivery.Cursor}); err != nil {
				t.Fatal(err)
			}
			for _, beat := range a.harnessBeatSessions {
				if beat.ID == in.SessionID && (beat.Ownership == nil || *beat.Ownership != identity) {
					t.Fatal("heartbeat changed server ownership")
				}
			}
			if a.claims != 0 || a.harnessDeliveryCompletions != 1 || len(m.sessions) != 0 {
				t.Fatal("helper resume launched work, revived consent, or acknowledged twice")
			}
			select {
			case <-p.stopped:
				t.Fatal("helper resume signalled external process")
			default:
			}
		})
	}
}
