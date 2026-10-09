// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/client"
)

type ledgerFixtureAPI struct {
	*fakeAPI
	view             agentsetup.View
	maximum          int
	ledgerGeneration string
	enrollmentErr    error
	routeErr         error
	routeCommitted   bool
	routeCalls       int
	offered          [][]string
	barrier          chan struct{}
	release          chan struct{}
	once             sync.Once
}

func (a *ledgerFixtureAPI) LedgerView(context.Context) (agentsetup.View, int, error) {
	return a.view, a.maximum, nil
}
func (a *ledgerFixtureAPI) EnrollLedger(_ context.Context, generation string) error {
	if a.enrollmentErr != nil {
		return a.enrollmentErr
	}
	a.view.LedgerMode = true
	a.view.LedgerGeneration = &generation
	return nil
}
func (a *ledgerFixtureAPI) SetLedgerGeneration(generation string) { a.ledgerGeneration = generation }
func (a *ledgerFixtureAPI) Route(ctx context.Context, runID, daemon string, ids []string, units map[string]int64) (Route, error) {
	a.routeCalls++
	a.routeCommitted = true
	a.offered = append(a.offered, slices.Clone(ids))
	if a.barrier != nil {
		a.once.Do(func() { close(a.barrier); <-a.release })
	}
	a.routeCommitted = true
	if a.routeErr != nil {
		return Route{}, a.routeErr
	}
	route, err := a.fakeAPI.Route(ctx, runID, daemon, ids, units)
	if len(ids) > 0 {
		route.AccountID = ids[0]
		route.AccountKey = "local"
		if ids[0] == "B" {
			route.AccountKey = "other"
		}
	}
	return route, err
}
func enableLedgerFixture(t *testing.T, s *Supervisor, a *fakeAPI) (*ledgerFixtureAPI, LedgerConfig) {
	t.Helper()
	api := &ledgerFixtureAPI{fakeAPI: a, view: agentsetup.View{ServerCapabilities: []string{agentsetup.LedgerCapability}}, maximum: 2}
	s.api = api
	s.accounts[0].VerifiedIdentity = "login-A"
	config := LedgerConfig{Path: filepath.Join(filepath.Dir(s.state.Path()), "ledger"), Label: "cm.aeon.agentd", Root: filepath.Dir(s.state.Path()), Origin: "https://ppm.example.invalid"}
	if err := s.EnableLedger(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	return api, config
}
func fixtureLedgerPeer(t *testing.T, s *Supervisor, cap int) agentsetup.LedgerMember {
	t.Helper()
	id, err := agentsetup.LedgerID()
	if err != nil {
		t.Fatal(err)
	}
	m := agentsetup.LedgerMember{ID: id, Label: "cm.aeon.agentd.pma", Root: filepath.Join(filepath.Dir(s.state.Path()), "peer"), JoinedAt: time.Unix(10, 0).UTC()}
	if err = s.ledger.Register(m); err != nil {
		t.Fatal(err)
	}
	generation := s.ledgerBinding.Generation
	_, err = s.ledger.Import(id, agentsetup.LedgerInstance{Fingerprint: agentsetup.LedgerFingerprint(generation, "instance", "peer"), PID: os.Getpid(), StartedAt: time.Unix(11, 0).UTC(), MaximumAgents: cap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ledger.Enrolled(id, generation); err != nil {
		t.Fatal(err)
	}
	return m
}

// Risk: handover can miss a possible fork in either launch-write window. The
// import request is established by a barrier while dispatchMu is held.
func TestSharedLedgerHandoverWaitsAcrossLaunchWindows(t *testing.T) {
	for _, window := range []string{"launching", "attempted"} {
		t.Run(window, func(t *testing.T) {
			s, a, p := testSupervisor(t)
			_, config := enableLedgerFixture(t, s, a)
			s.probedAccounts["account"] = true
			reached, proceed, requested := make(chan struct{}), make(chan struct{}), make(chan struct{})
			s.ledgerBarrier = func(stage string) {
				if stage == window {
					close(reached)
					<-proceed
				}
				if stage == "import_requested" {
					close(requested)
				}
			}
			started := make(chan error, 1)
			go func() { started <- s.StartRun(t.Context(), a.run) }()
			<-reached
			if s.dispatchMu.TryLock() {
				s.dispatchMu.Unlock()
				t.Fatal("dispatch did not hold launch fence")
			}
			imported := make(chan error, 1)
			go func() { imported <- s.EnableLedger(t.Context(), config) }()
			<-requested
			select {
			case err := <-imported:
				t.Fatal("handover crossed dispatch fence", err)
			default:
			}
			d, _, err := s.ledger.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Groups) != 1 {
				t.Fatal("missing launch occupancy")
			}
			for _, g := range d.Groups {
				if g.State != "launching" {
					t.Fatal("wrong barrier state", g.State)
				}
			}
			close(proceed)
			if err = <-started; err != nil {
				t.Fatal(err)
			}
			if err = <-imported; err != nil {
				t.Fatal(err)
			}
			d, _, err = s.ledger.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range d.Groups {
				if g.State != "running" || g.PID != p.PID() {
					t.Fatal("import forgot possible fork", g)
				}
			}
			s.ledgerBarrier = nil
		})
	}
}

// Risk: a server-committed Route with a lost response can be deleted by a peer,
// or a retry can allocate a second machine slot. Owner restart replays it.
func TestSharedLedgerRouteCommitCrashOwnerOnlyReplay(t *testing.T) {
	s, a, _ := testSupervisor(t)
	api, config := enableLedgerFixture(t, s, a)
	peer := fixtureLedgerPeer(t, s, 1)
	s.probedAccounts["account"] = true
	api.routeErr = errors.New("fixture lost response")
	api.barrier = make(chan struct{})
	api.release = make(chan struct{})
	dispatched := make(chan error, 1)
	go func() { dispatched <- s.StartRun(t.Context(), a.run) }()
	<-api.barrier
	records := s.journal.Snapshot()
	if len(records) != 1 || records[0].LaunchState != launchRoutePending {
		t.Fatal("Route preceded attempt marker", records)
	}
	intent := records[0]
	if err := s.ledger.Release(peer.ID, intent.LedgerGeneration, intent.LedgerGroup); !errors.Is(err, agentsetup.ErrLedgerOwner) {
		t.Fatal("peer released owner intent", err)
	}
	d, _, err := s.ledger.Snapshot()
	if err != nil || len(d.Groups) != 1 {
		t.Fatal("peer damaged group", err)
	}
	close(api.release)
	if err = <-dispatched; err == nil {
		t.Fatal("lost response claimed success")
	}
	if !api.routeCommitted {
		t.Fatal("fixture did not commit Route")
	}
	// Simulate owner crash: release only its OS resources, preserving its journal.
	root, workspace := s.state.Path(), s.workspace
	s.lock.Close()
	s.lock = nil
	s.state.Close()
	next, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, DaemonID: "daemon", Workspace: workspace, Adapters: []Adapter{&fakeAdapter{proc: &fakeProcess{stopped: make(chan struct{})}}}, Accounts: s.accounts, EstimatedUnits: map[string]int64{"requests": 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		next.lock.Close()
		next.state.Close()
		if next.ledger != nil {
			next.ledger.Close()
		}
	})
	api.routeErr = nil
	api.barrier = nil
	if err = next.EnableLedger(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	got := next.journal.Snapshot()[0]
	if got.LedgerGroup != intent.LedgerGroup || got.LaunchState != launchPrepared || got.ClaimRoute == nil {
		t.Fatal("restart failed exact owner reconciliation", got)
	}
	d, _, err = next.ledger.Snapshot()
	if err != nil || len(d.Groups) != 1 || d.Groups[intent.LedgerGroup].State != "claimed" {
		t.Fatal("replay lost machine slot", d, err)
	}
	if api.routeCalls != 2 || !slices.Equal(api.offered[0], api.offered[1]) {
		t.Fatal("replay changed subset", api.offered)
	}
}

// Risk: a crash before the attempt marker cannot invent a Route; after the
// marker, an unreachable server must keep both group and private evidence.
func TestSharedLedgerCrashBeforeIntentAndUnreachableRecovery(t *testing.T) {
	for _, window := range []string{"group", "intent"} {
		t.Run(window, func(t *testing.T) {
			s, a, _ := testSupervisor(t)
			api, config := enableLedgerFixture(t, s, a)
			gid, err := agentsetup.LedgerID()
			if err != nil {
				t.Fatal(err)
			}
			subset, err := s.ledger.Acquire(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, gid, s.ledgerCandidates([]string{"account"}, s.ledgerBinding.Generation))
			if err != nil {
				t.Fatal(err)
			}
			if window == "intent" {
				r := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", WorkOrderID: "order", Generation: s.generation, ExecutionMode: "managed", State: "route_pending", LaunchState: launchRoutePending, LedgerGroup: gid, LedgerGeneration: s.ledgerBinding.Generation, RouteCandidates: subset}
				if err = s.journal.Put(r); err != nil {
					t.Fatal(err)
				}
				s.runs[r.RunID] = &owned{record: r}
				api.routeErr = errors.New("fixture unreachable")
			}
			err = s.EnableLedger(t.Context(), config)
			d, _, readErr := s.ledger.Snapshot()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if window == "group" {
				if err != nil || len(d.Groups) != 0 || api.routeCalls != 0 {
					t.Fatal("pre-intent orphan was routed", err, d)
				}
			} else {
				if err == nil || len(d.Groups) != 1 || s.journal.Snapshot()[0].LaunchState != launchRoutePending {
					t.Fatal("unreachable server forgot attempt", err, d)
				}
				peer := fixtureLedgerPeer(t, s, 2)
				try, _ := agentsetup.LedgerID()
				if _, err = s.ledger.Acquire(peer.ID, s.ledgerBinding.Generation, try, subset); !errors.Is(err, agentsetup.ErrLedgerOccupied) {
					t.Fatal("unreachable attempt did not occupy login", err)
				}
			}
		})
	}
}

// Risk: the offer sent to Route may include a login held by a peer, or a pin
// may silently select a free alternative. Both are tested on actual dispatch.
func TestSharedLedgerDispatchPrunesRouteOfferAndKeepsPin(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "free-B", true: "pinned-A"}[pinned], func(t *testing.T) {
			s, a, _ := testSupervisor(t)
			api, _ := enableLedgerFixture(t, s, a)
			peer := fixtureLedgerPeer(t, s, 2)
			s.accounts = append(s.accounts, EnrolledAccount{ID: "B", Key: "other", Harness: Codex, VerifiedIdentity: "login-B"})
			s.probedAccounts["account"], s.probedAccounts["B"] = true, true
			fixture, err := agentsetup.LedgerID()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ledger.Acquire(peer.ID, s.ledgerBinding.Generation, fixture, s.ledgerCandidates([]string{"account"}, s.ledgerBinding.Generation)); err != nil {
				t.Fatal(err)
			}
			if pinned {
				a.run.RequestedAccountID = "account"
			}
			err = s.StartRun(t.Context(), a.run)
			if pinned {
				if !errors.Is(err, agentsetup.ErrLedgerOccupied) || api.routeCalls != 0 {
					t.Fatal("pin silently rerouted", err, api.offered)
				}
			} else {
				if err != nil || len(api.offered) != 1 || !slices.Equal(api.offered[0], []string{"B"}) {
					t.Fatal("Route offered occupied A", err, api.offered)
				}
			}
		})
	}
}

// Risk: generation replacement can let a dispatch fork using a vanished group.
// A barrier rebuilds while the current dispatch retains its old group ID.
func TestSharedLedgerRebuildRefusesOldDispatchBeforeLaunching(t *testing.T) {
	s, a, _ := testSupervisor(t)
	_, _ = enableLedgerFixture(t, s, a)
	s.probedAccounts["account"] = true
	reached, proceed := make(chan struct{}), make(chan struct{})
	s.ledgerBarrier = func(stage string) {
		if stage == "intent" {
			close(reached)
			<-proceed
		}
	}
	dispatched := make(chan error, 1)
	go func() { dispatched <- s.StartRun(t.Context(), a.run) }()
	<-reached
	old := s.ledgerBinding.Generation
	generation, err := s.ledger.Rebuild()
	if err != nil || generation == old {
		t.Fatal(err)
	}
	close(proceed)
	if err = <-dispatched; !errors.Is(err, agentsetup.ErrLedgerGeneration) {
		t.Fatal("stale dispatch passed launching fence", err)
	}
	r := s.journal.Snapshot()[0]
	if r.LaunchState != launchRoutePending || r.PID != 0 {
		t.Fatal("old dispatch crossed possible-fork boundary", r)
	}
	s.ledgerBarrier = nil
}

// Risk: a failed enrolment must retain imported occupancy and prohibit all
// further launches. An old server must be refused before any member is written.
func TestSharedLedgerEnrollmentCrashAndOldServerFailClosed(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{true: "old-server", false: "enrollment-crash"}[old], func(t *testing.T) {
			s, a, _ := testSupervisor(t)
			api := &ledgerFixtureAPI{fakeAPI: a, view: agentsetup.View{ServerCapabilities: []string{agentsetup.LedgerCapability}}, maximum: 1, enrollmentErr: errors.New("fixture enrollment response lost")}
			if old {
				api.view.ServerCapabilities = nil
			}
			s.api = api
			config := LedgerConfig{Path: filepath.Join(filepath.Dir(s.state.Path()), "ledger"), Label: "cm.aeon.agentd", Root: filepath.Dir(s.state.Path()), Origin: "https://ppm.example.invalid"}
			if err := s.EnableLedger(t.Context(), config); err == nil {
				t.Fatal("unsupported transition succeeded")
			}
			if old {
				if _, err := os.Lstat(config.Path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("old server changed ledger", err)
				}
			} else {
				s.probedAccounts["account"] = true
				if err := s.StartRun(t.Context(), a.run); !errors.Is(err, agentsetup.ErrLedgerUnavailable) {
					t.Fatal("failed enrollment resumed legacy starts", err)
				}
				api.enrollmentErr = nil
				if err := s.EnableLedger(t.Context(), config); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Risk: a generic server conflict does not prove that a reservation never
// committed. Capacity/grant/generation conflicts must retain the attempt.
func TestSharedLedgerGenericConflictKeepsAttempt(t *testing.T) {
	s, a, _ := testSupervisor(t)
	api, _ := enableLedgerFixture(t, s, a)
	s.probedAccounts["account"] = true
	api.routeErr = &client.StatusError{Status: 409, Message: "fixture capacity wait"}
	if err := s.StartRun(t.Context(), a.run); err == nil {
		t.Fatal("conflict succeeded")
	}
	if err := s.recoverUnlaunched(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshLedger(t.Context(), s.ledgerBinding.Config); err == nil {
		t.Fatal("conflict silently cleared")
	}
	d, _, err := s.ledger.Snapshot()
	if err != nil || len(d.Groups) != 1 || s.journal.Snapshot()[0].LaunchState != launchRoutePending {
		t.Fatal("conflict lost reservation evidence", err, d)
	}
}
