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

type fakeRunPower struct {
	mu              sync.Mutex
	taken, released int
	releaseDone     chan struct{}
}

func (p *fakeRunPower) PreventIdleSleep() (func(), error) {
	p.mu.Lock()
	p.taken++
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		p.released++
		p.mu.Unlock()
		p.releaseDone <- struct{}{}
	}, nil
}

func (p *fakeRunPower) check(t *testing.T, taken, released int) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.taken != taken || p.released != released {
		t.Fatalf("sleep assertion taken/released = %d/%d, want %d/%d", p.taken, p.released, taken, released)
	}
}

type powerRunAdapter struct {
	*fakeAdapter
	power             *fakeRunPower
	t                 *testing.T
	startErr, exitErr error
}

func (a *powerRunAdapter) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	a.power.check(a.t, 1, 0)
	if a.startErr != nil {
		return nil, a.startErr
	}
	return &powerRunProcess{fakeProcess: a.proc, err: a.exitErr}, nil
}

type powerRunProcess struct {
	*fakeProcess
	err error
}

func (p *powerRunProcess) Wait() error       { _ = p.fakeProcess.Wait(); return p.err }
func (*powerRunProcess) ProcessExited() bool { return true }

func powerSupervisor(t *testing.T, lifetime context.Context) (*Supervisor, *fakeAPI, *powerRunAdapter) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{run: Run{ID: "run", WorkOrderID: "order", AgentPrincipalID: "agent", ModelProfileID: "profile", Status: "queued"}, profile: Profile{ID: "profile", Harness: Codex, Model: "model", Effort: "high"}}
	power := &fakeRunPower{releaseDone: make(chan struct{}, 4)}
	adapter := &powerRunAdapter{fakeAdapter: &fakeAdapter{proc: &fakeProcess{stopped: make(chan struct{})}}, power: power, t: t}
	config := Config{API: api, StateRoot: state, DaemonID: "daemon", Workspace: root, Adapters: []Adapter{adapter}, EstimatedUnits: map[string]int64{"requests": 1}, Accounts: []EnrolledAccount{{ID: "account", Key: "local", Harness: Codex}}}
	config.Power = power
	s, err := NewSupervisor(lifetime, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = adapter.proc.Stop(context.Background())
		if entry := s.runs[api.run.ID]; entry != nil && entry.monitorDone != nil {
			<-entry.monitorDone
		}
		_ = s.Close(context.Background())
	})
	return s, api, adapter
}

// Risk: idle sleep interrupts active work, or a completed/failed/cancelled run
// leaves a sleep assertion behind. Child-exit and monitor barriers prove scope.
func TestRunIdleSleepAssertionLifecycle(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "cancelled", "start failed"} {
		t.Run(outcome, func(t *testing.T) {
			s, api, adapter := powerSupervisor(t, t.Context())
			adapter.power.check(t, 0, 0)
			if outcome == "failed" {
				adapter.exitErr = errors.New("fixture child failed")
			}
			if outcome == "start failed" {
				adapter.startErr = errors.New("fixture launch failed")
			}
			err := s.StartRun(t.Context(), api.run)
			if outcome == "start failed" {
				if !errors.Is(err, adapter.startErr) {
					t.Fatal("wrong startup result", err)
				}
				adapter.power.check(t, 1, 1)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			adapter.power.check(t, 1, 0)
			entry := s.runs[api.run.ID]
			if outcome == "cancelled" {
				if _, err := s.Control(t.Context(), ControlRequest{TenantID: s.TenantID(), PrincipalID: s.PrincipalID(), RunID: api.run.ID, Generation: s.Generation(), CorrelationID: "cancel", Operation: "stop"}); err != nil {
					t.Fatal("cancel control failed", err)
				}
			} else {
				_ = adapter.proc.Stop(t.Context())
			}
			<-entry.monitorDone
			adapter.power.check(t, 1, 1)
			api.mu.Lock()
			defer api.mu.Unlock()
			last := api.reports[len(api.reports)-1]
			if last.Kind != "finished" || last.Status != outcome {
				t.Fatal("wrong terminal report", last)
			}
		})
	}
}

// Risk: daemon cancellation leaks a live assertion, or a drain releases it
// while the daemon still owns work. Close must continue to preserve the child.
func TestRunIdleSleepAssertionDaemonShutdown(t *testing.T) {
	lifetime, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, api, adapter := powerSupervisor(t, lifetime)
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); !errors.Is(err, ErrDraining) {
		t.Fatal("active close changed ownership", err)
	}
	adapter.power.check(t, 1, 0)
	cancel()
	select {
	case <-adapter.power.releaseDone:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon shutdown leaked the assertion")
	}
	adapter.power.check(t, 1, 1)
	select {
	case <-adapter.proc.stopped:
		t.Fatal("power cleanup stopped the child")
	default:
	}
	_ = adapter.proc.Stop(t.Context())
	<-s.runs[api.run.ID].monitorDone
	adapter.power.check(t, 1, 1)
}
