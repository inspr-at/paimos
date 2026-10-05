// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/workorders"
)

type handoffFaultAPI struct {
	*claimFaultAPI
	s       *Supervisor
	t       *testing.T
	pickups []WorkerPickup
}

func (a *handoffFaultAPI) WorkOrder(context.Context, string) (WorkOrder, error) {
	return WorkOrder{NodeID: "order", Status: "ready", Revision: 1}, nil
}

func (a *handoffFaultAPI) Route(ctx context.Context, id, daemon string, accounts []string, estimates map[string]int64) (Route, error) {
	route, err := a.claimFaultAPI.Route(ctx, id, daemon, accounts, estimates)
	route.DaemonID = daemon
	return route, err
}

func (a *handoffFaultAPI) ClaimHandoff(ctx context.Context, id, daemon, generation string, ids []string, pickup WorkerPickup) error {
	records := a.s.journal.Snapshot()
	if len(records) != 1 || records[0].WorkerPickup == nil || !reflect.DeepEqual(*records[0].WorkerPickup, pickup) || records[0].LaunchState != launchPrepared {
		a.t.Fatal("assignment pickup not journaled before network claim")
	}
	a.pickups = append(a.pickups, pickup)
	return a.claimFaultAPI.Claim(ctx, id, daemon, generation, ids)
}

func assignedClaimFixture(t *testing.T) (*Supervisor, *handoffFaultAPI, *verificationFakeAdapter) {
	t.Helper()
	s, base, adapter := claimFixture(t)
	digest, err := workorders.BriefDigest("Do work", "Criteria", []string{})
	if err != nil {
		t.Fatal(err)
	}
	base.run.Trace, err = json.Marshal(map[string]any{"worker_assignment": map[string]any{
		"run_id": base.run.ID, "ticket_revision": time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), "work_order_revision": 1, "brief_sha256": digest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	base.server = base.run
	api := &handoffFaultAPI{claimFaultAPI: base, s: s, t: t}
	s.api = api
	return s, api, adapter
}

func TestWorkerAssignmentClaimResponseLossUsesJournalProof(t *testing.T) {
	s, api, adapter := assignedClaimFixture(t)
	api.claimErr, api.reportErr = errors.New("lost claim response"), errors.New("lost report response")
	api.claimCommitted = true
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("response loss hidden")
	}
	if adapter.starts != 0 || len(api.pickups) != 1 || api.accepted[1].ProcessState != "not_attempted" {
		t.Fatal("uncertain claim launched or failed to retain no-launch evidence")
	}
	records := s.journal.Snapshot()
	if records[0].WorkerPickup == nil || strings.Contains(records[0].WorkerPickup.WorktreeID, s.workspace) || len(records[0].WorkerPickup.WorktreeID) != 64 {
		t.Fatal("pickup lost opaque worktree binding")
	}
	api.reportErr = nil
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 0 || len(api.accepted) != 1 || api.accepted[1].ProcessState != "not_attempted" {
		t.Fatal("reconciliation changed proof or launched a second process")
	}
}

type handoffLaunchError struct {
	fakeAdapter
	starts int
}

func (a *handoffLaunchError) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	a.starts++
	return nil, errors.New("unknown fork outcome")
}

func TestWorkerAssignmentUnknownForkNeverRetries(t *testing.T) {
	s, api, _ := assignedClaimFixture(t)
	adapter := &handoffLaunchError{}
	s.adapters[Codex] = adapter
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("unknown fork hidden")
	}
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal(err)
	}
	records := s.journal.Snapshot()
	if adapter.starts != 1 || records[0].LaunchState != launchAttempted || records[0].ExitObserved || records[0].WorkerPickup == nil {
		t.Fatal("unknown launch was retried or declared stopped")
	}
	for _, report := range api.accepted {
		if report.ProcessState == "exited" || report.ProcessState == "not_attempted" {
			t.Fatal("unknown fork fabricated exit evidence")
		}
	}
}

func TestWorkerAssignmentChangedBriefCannotClaim(t *testing.T) {
	s, api, adapter := assignedClaimFixture(t)
	api.run.Trace = json.RawMessage(`{"worker_assignment":{"run_id":"run","ticket_revision":"2026-10-06T00:00:00Z","work_order_revision":1,"brief_sha256":"` + strings.Repeat("a", 64) + `"}}`)
	if err := s.StartRun(t.Context(), api.run); err == nil || !strings.Contains(err.Error(), "assignment brief changed") {
		t.Fatalf("changed brief: %v", err)
	}
	if api.routes != 0 || len(api.pickups) != 0 || adapter.starts != 0 || len(s.journal.Snapshot()) != 0 {
		t.Fatal("changed brief reserved or started work")
	}
}

func TestWorkerAssignmentRemoteClaimCarriesExactPickup(t *testing.T) {
	pickup := WorkerPickup{TicketRevision: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), WorkOrderRevision: 3, BriefSHA256: strings.Repeat("a", 64), WorktreeID: strings.Repeat("b", 64)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/runs/run/claim" {
			t.Error("wrong claim route")
		}
		var body struct {
			Daemon       string       `json:"daemon_id"`
			Generation   string       `json:"daemon_generation"`
			Reservations []string     `json:"reservation_ids"`
			Handoff      WorkerPickup `json:"handoff"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Daemon != "daemon" || body.Generation != "generation" || !reflect.DeepEqual(body.Reservations, []string{"reservation"}) || !reflect.DeepEqual(body.Handoff, pickup) {
			t.Error("pickup identity changed over HTTP")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "test-key")
	if err := remote.ClaimHandoff(t.Context(), "run", "daemon", "generation", []string{"reservation"}, pickup); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("claim repeated")
	}
}

func TestWorkerAssignmentWorktreeIdentityIgnoresDaemon(t *testing.T) {
	s, api, _ := assignedClaimFixture(t)
	node, err := api.Node(t.Context(), api.run.WorkOrderID)
	if err != nil {
		t.Fatal(err)
	}
	order, err := api.WorkOrder(t.Context(), api.run.WorkOrderID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.workerPickup(api.run, node, order)
	if err != nil {
		t.Fatal(err)
	}
	s.daemonID = "another-daemon"
	second, err := s.workerPickup(api.run, node, order)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorktreeID != second.WorktreeID {
		t.Fatal("same physical checkout changed identity with daemon")
	}
}

func competingHandoffSupervisor(t *testing.T, s *Supervisor, api *handoffFaultAPI) (*Supervisor, *handoffFaultAPI, *verificationFakeAdapter) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	base := &claimFaultAPI{fakeAPI: api.fakeAPI, server: api.run, accepted: map[int64]Telemetry{}}
	adapter := &verificationFakeAdapter{fakeAdapter: fakeAdapter{proc: &fakeProcess{stopped: make(chan struct{})}}}
	next, err := NewSupervisor(t.Context(), Config{API: base, StateRoot: state, DaemonID: "competing-daemon", Workspace: s.workspace, Adapters: []Adapter{adapter}, Accounts: s.accounts, EstimatedUnits: s.estimates})
	if err != nil {
		t.Fatal(err)
	}
	nextAPI := &handoffFaultAPI{claimFaultAPI: base, s: next, t: t}
	next.api = nextAPI
	t.Cleanup(func() {
		_ = adapter.proc.Stop(context.Background())
		for _, entry := range next.runs {
			entry.mu.Lock()
			done := entry.monitorDone
			entry.mu.Unlock()
			if done != nil {
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("competing fixture monitor did not finish")
					return
				}
			}
		}
		if err := next.Close(context.Background()); err != nil {
			t.Errorf("close competing fixture: %v", err)
		}
	})
	return next, nextAPI, adapter
}

func TestWorkerAssignmentCompetingDaemonCannotClaimCheckout(t *testing.T) {
	s, api, _ := assignedClaimFixture(t)
	s.adapters[Codex] = &handoffLaunchError{}
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("unknown fork hidden")
	}
	if err := s.Close(t.Context()); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatalf("unknown writer released ownership: %v", err)
	}
	next, nextAPI, adapter := competingHandoffSupervisor(t, s, api)
	err := next.StartRun(t.Context(), api.run)
	if err == nil || !strings.Contains(err.Error(), "checkout") {
		t.Fatalf("competing daemon crossed checkout fence: %v", err)
	}
	if nextAPI.routes != 0 || len(nextAPI.pickups) != 0 || adapter.starts != 0 {
		t.Fatal("competing daemon reserved, claimed or launched in held checkout")
	}
}

func TestWorkerAssignmentCheckoutFenceSurvivesDaemonLoss(t *testing.T) {
	if os.Getenv("AEON_TEST_HANDOFF_EXIT_CHILD") == "1" {
		s, api, _ := assignedClaimFixture(t)
		// Parent supplied a private physical checkout. This test process exits with
		// an unknown fork; no Close call may mark its checkout as safely stopped.
		s.workspace = os.Args[len(os.Args)-1]
		s.adapters[Codex] = &handoffLaunchError{}
		if err := s.StartRun(t.Context(), api.run); err == nil {
			t.Fatal("unknown fork hidden")
		}
		if err := s.Close(t.Context()); !errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatal("unknown writer accepted as stopped")
		}
		return
	}
	s, api, _ := assignedClaimFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestWorkerAssignmentCheckoutFenceSurvivesDaemonLoss$", "-test.timeout=10s", "--", s.workspace)
	child.Env = append(os.Environ(), "AEON_TEST_HANDOFF_EXIT_CHILD=1")
	if err := child.Run(); err != nil {
		t.Fatalf("unknown-fork child fixture failed: %v", err)
	}
	next, nextAPI, adapter := competingHandoffSupervisor(t, s, api)
	if err := next.StartRun(t.Context(), api.run); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatalf("daemon loss released uncertain checkout: %v", err)
	}
	if nextAPI.routes != 0 || len(nextAPI.pickups) != 0 || adapter.starts != 0 {
		t.Fatal("durable checkout owner was bypassed")
	}
}

func TestWorkerAssignmentCheckoutReleasesAfterConfirmedExit(t *testing.T) {
	s, api, adapter := assignedClaimFixture(t)
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal(err)
	}
	next, nextAPI, nextAdapter := competingHandoffSupervisor(t, s, api)
	if err := next.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("live checkout allowed competing writer")
	}
	if err := adapter.proc.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry := s.runs[api.run.ID]
	entry.mu.Lock()
	done := entry.monitorDone
	entry.mu.Unlock()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("monitor did not observe exit")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("confirmed exit retained checkout: %v", err)
	}
	if err := next.StartRun(t.Context(), api.run); err != nil {
		t.Fatal(err)
	}
	if len(nextAPI.pickups) != 1 || nextAdapter.starts != 1 {
		t.Fatal("checkout did not transfer after confirmed exit")
	}
}

func TestWorkerAssignmentRestartClearsOwnConfirmedCheckoutFence(t *testing.T) {
	s, api, adapter := assignedClaimFixture(t)
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal(err)
	}
	if err := adapter.proc.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	awaitTelemetryMonitor(t, s.runs[api.run.ID])
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A daemon crash after journaling confirmed exit, before clean shutdown,
	// leaves this owner marker next to the already confirmed-exit journal.
	store, err := agentsetup.OpenStore(filepath.Join(s.workspace, ".aeon-agentd"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := json.Marshal([]string{s.tenantID, s.principalID, s.daemonID, s.journalDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write("owner.json", owner, false); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: s.journalDir(), DaemonID: s.daemonID, Workspace: s.workspace, Adapters: []Adapter{adapter}, Accounts: s.accounts, EstimatedUnits: s.estimates})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	if err := restarted.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	marker, err := store.Read("owner.json", 4096)
	if err != nil || string(marker) != "[]" {
		t.Fatalf("confirmed-exit recovery retained its own checkout fence: %v", err)
	}
}

func TestWorkerAssignmentRejectsOverlappingCheckoutDirectories(t *testing.T) {
	for _, marker := range []string{"directory", "gitdir-file"} {
		t.Run(marker, func(t *testing.T) {
			s, api, _ := assignedClaimFixture(t)
			cmd := exec.Command("git", "init", "--quiet", s.workspace)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("init checkout fixture: %v: %s", err, out)
			}
			if marker == "gitdir-file" {
				// Git accepts a gitdir file as a checkout root (including linked
				// worktrees); the fence must not depend on .git being a directory.
				gitdir := filepath.Join(s.workspace, ".fixture-git")
				if err := os.Rename(filepath.Join(s.workspace, ".git"), gitdir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(s.workspace, ".git"), []byte("gitdir: "+gitdir+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// The checkout root is valid; directories beneath it share its files and
			// Git state and must not acquire their own writer identity or fence.
			root, _, _ := competingHandoffSupervisor(t, s, api)
			if root.workspace != s.workspace {
				t.Fatal("checkout root changed")
			}
			for _, relative := range []string{"work", "work/nested", "other"} {
				t.Run(relative, func(t *testing.T) {
					workspace := filepath.Join(s.workspace, relative)
					if err := os.MkdirAll(workspace, 0700); err != nil {
						t.Fatal(err)
					}
					state, err := filepath.EvalSymlinks(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					child, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: state, DaemonID: "subdirectory-daemon", Workspace: workspace, Adapters: []Adapter{&handoffLaunchError{}}, Accounts: s.accounts, EstimatedUnits: s.estimates})
					if child != nil {
						defer child.Close(context.Background())
					}
					if err == nil || !strings.Contains(err.Error(), "workspace must be the Git checkout root") {
						t.Fatalf("overlapping checkout directory accepted or wrong rejection: %v", err)
					}
					if _, err := os.Stat(filepath.Join(workspace, ".aeon-agentd")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("rejected directory acquired its own fence: %v", err)
					}
				})
			}
		})
	}
}
