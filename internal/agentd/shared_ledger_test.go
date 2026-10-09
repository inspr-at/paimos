// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/localjournal"
)

type ledgerFixtureAPI struct {
	*fakeAPI
	view                agentsetup.View
	maximum             int
	ledgerGeneration    string
	enrollmentErr       error
	enrollmentCommitted bool
	routeErr            error
	routeErrByRun       map[string]error
	runsByID            map[string]Run
	routeCommitted      bool
	routeCalls          int
	offered             [][]string
	barrier             chan struct{}
	release             chan struct{}
	once                sync.Once
}

func (a *ledgerFixtureAPI) LedgerView(context.Context) (agentsetup.View, int, error) {
	return a.view, a.maximum, nil
}
func (a *ledgerFixtureAPI) EnrollLedger(_ context.Context, generation string) error {
	if a.enrollmentErr != nil {
		if a.enrollmentCommitted {
			a.view.LedgerMode = true
			a.view.LedgerGeneration = &generation
		}
		return a.enrollmentErr
	}
	a.view.LedgerMode = true
	a.view.LedgerGeneration = &generation
	return nil
}
func (a *ledgerFixtureAPI) SetLedgerGeneration(generation string) { a.ledgerGeneration = generation }
func (a *ledgerFixtureAPI) GetRun(ctx context.Context, id string) (Run, error) {
	if run, ok := a.runsByID[id]; ok {
		return run, nil
	}
	return a.fakeAPI.GetRun(ctx, id)
}
func (a *ledgerFixtureAPI) Route(ctx context.Context, runID, daemon string, ids []string, units map[string]int64) (Route, error) {
	a.routeCalls++
	a.routeCommitted = true
	if err := a.routeErrByRun[runID]; err != nil {
		return Route{}, err
	}
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

// Risk: a routine refusal for one durable intent freezes other logins and
// suppresses probes. Its hold must survive while an independent run launches.
func TestSharedLedgerUnconfirmedIntentAllowsOtherLoginDispatch(t *testing.T) {
	for _, refusal := range []int{409, 403, 404, 503} {
		t.Run(http.StatusText(refusal), func(t *testing.T) {
			s, a, _ := testSupervisor(t)
			api, config := enableLedgerFixture(t, s, a)
			s.accounts = append(s.accounts, EnrolledAccount{ID: "B", Key: "other", Harness: Codex, VerifiedIdentity: "login-B"})
			s.probedAccounts["account"] = true
			a.run.RequestedAccountID = "account"
			api.routeErrByRun = map[string]error{a.run.ID: &client.StatusError{Status: refusal, Message: "fixture unconfirmed route"}}
			if err := s.StartRun(t.Context(), a.run); err == nil {
				t.Fatal("initial refusal reported success")
			}
			intent := s.journal.Snapshot()[0]
			api.runsByID = map[string]Run{a.run.ID: a.run}
			var reasons []string
			s.pollDiagnostic = func(reason string) { reasons = append(reasons, reason) }
			if err := s.RefreshLedger(t.Context(), config); err != nil {
				t.Fatal("one intent blocked ledger refresh", err)
			}
			a.run.ID, a.run.RequestedAccountID = "other-run", "B"
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal("one intent blocked probes or other-login dispatch", err)
			}
			data, _, err := s.ledger.Snapshot()
			if err != nil || len(data.Groups) != 2 || data.Groups[intent.LedgerGroup].State != "pending" || !reflect.DeepEqual(data.Groups[intent.LedgerGroup].Holds, []agentsetup.LedgerLogin{intent.RouteCandidates[0].Login}) {
				t.Fatal("unconfirmed intent lost its slot or login hold", data, err)
			}
			if got := s.runs[intent.RunID].record; !reflect.DeepEqual(got, intent) {
				t.Fatal("refusal changed private evidence", got)
			}
			other := s.runs["other-run"]
			other.mu.Lock()
			launched := other.record.AccountID == "B" && other.record.LaunchState == launchAttempted && other.process != nil
			other.mu.Unlock()
			if !launched || !s.probedAccounts["B"] || !slices.Equal(api.offered[len(api.offered)-1], []string{"B"}) || !slices.Equal(reasons, []string{"ledger_route_unconfirmed"}) {
				t.Fatal("independent login did not dispatch with one bounded diagnostic", launched, api.offered, reasons)
			}
		})
	}
}

// Risk: reconciliation returns on the first refusal and never confirms a
// later intent. Both holds must remain, with only the confirmed one narrowed.
func TestSharedLedgerUnconfirmedIntentDoesNotStopLaterReconciliation(t *testing.T) {
	s, a, _ := testSupervisor(t)
	api, config := enableLedgerFixture(t, s, a)
	s.accounts = append(s.accounts, EnrolledAccount{ID: "B", Key: "other", Harness: Codex, VerifiedIdentity: "login-B"})
	for i, account := range []string{"account", "B"} {
		gid, err := agentsetup.LedgerID()
		if err != nil {
			t.Fatal(err)
		}
		subset, err := s.ledger.Acquire(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, gid, s.ledgerCandidates([]string{account}, s.ledgerBinding.Generation))
		if err != nil {
			t.Fatal(err)
		}
		id := []string{"a-stuck", "b-confirmable"}[i]
		r := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: id, WorkOrderID: "order", Generation: s.generation, ExecutionMode: "managed", State: "route_pending", LaunchState: launchRoutePending, LedgerGroup: gid, LedgerGeneration: s.ledgerBinding.Generation, RouteCandidates: subset}
		if err := s.journal.Put(r); err != nil {
			t.Fatal(err)
		}
		s.runs[id] = &owned{record: r}
	}
	api.routeErrByRun = map[string]error{"a-stuck": &client.StatusError{Status: 409}}
	if err := s.RefreshLedger(t.Context(), config); err != nil {
		t.Fatal("one intent stopped reconciliation", err)
	}
	if api.routeCalls != 2 || s.runs["a-stuck"].record.State != "route_pending" || s.runs["b-confirmable"].record.State != "claim_pending" || s.runs["b-confirmable"].record.ClaimRoute == nil {
		t.Fatal("later intent was not reconciled", s.journal.Snapshot(), api.routeCalls)
	}
}

// Risk: only terminal conflicts are released, or a mismatched run read releases
// someone else's evidence. Confirm obsolete attempts independently, then restart.
func TestSharedLedgerConflictReleasesOnlyIndependentlyObsoleteAttempt(t *testing.T) {
	for _, observed := range []Run{
		{Status: "starting"}, {Status: "running"}, {Status: "waiting"},
		{Status: "completed"}, {Status: "failed"}, {Status: "cancelled"},
		{Status: "queued", AccountID: "elsewhere"},
		{Status: "queued"}, {Status: ""},
		{Status: "completed", ID: "foreign-run"},
		{Status: "completed", WorkOrderID: "foreign-order"},
		{Status: "completed", AgentPrincipalID: "foreign-agent"},
	} {
		t.Run(observed.Status+"/"+observed.AccountID+observed.ID+observed.WorkOrderID+observed.AgentPrincipalID, func(t *testing.T) {
			s, a, _ := testSupervisor(t)
			api, config := enableLedgerFixture(t, s, a)
			s.probedAccounts["account"] = true
			api.routeErr = &client.StatusError{Status: 409}
			if err := s.StartRun(t.Context(), a.run); err == nil {
				t.Fatal("initial refusal reported success")
			}
			run := a.run
			run.Status, run.AccountID = observed.Status, observed.AccountID
			if observed.ID != "" {
				run.ID = observed.ID
			}
			if observed.WorkOrderID != "" {
				run.WorkOrderID = observed.WorkOrderID
			}
			if observed.AgentPrincipalID != "" {
				run.AgentPrincipalID = observed.AgentPrincipalID
			}
			api.runsByID = map[string]Run{a.run.ID: run}
			if err := s.RefreshLedger(t.Context(), config); err != nil {
				t.Fatal("per-intent conflict blocked refresh", err)
			}
			obsolete := run.ID == a.run.ID && run.WorkOrderID == a.run.WorkOrderID && run.AgentPrincipalID == a.run.AgentPrincipalID && run.Status != "" && (run.Status != "queued" || run.AccountID != "")
			data, _, err := s.ledger.Snapshot()
			got := s.journal.Snapshot()[0]
			if err != nil {
				t.Fatal(err)
			}
			if obsolete {
				if len(data.Groups) != 0 || got.LedgerGroup != "" || len(got.RouteCandidates) != 0 || !noLocalProcess(got) || got.ClaimRoute != nil || got.State == "route_pending" {
					t.Fatal("confirmed obsolete intent retained a slot or invented launch evidence", got, data)
				}
			} else if len(data.Groups) != 1 || got.LedgerGroup == "" || got.State != "route_pending" || len(got.RouteCandidates) != 1 {
				t.Fatal("unconfirmed or foreign read released evidence", got, data)
			}
			s = restartSharedLedgerFixture(t, s, api)
			if err := s.RefreshLedger(t.Context(), config); err != nil {
				t.Fatal("restart rejected durable disposition", err)
			}
			data, _, err = s.ledger.Snapshot()
			if err != nil || (len(data.Groups) == 0) != obsolete {
				t.Fatal("restart changed confirmed occupancy", data, err)
			}
		})
	}
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

func restartSharedLedgerFixture(t *testing.T, previous *Supervisor, api *ledgerFixtureAPI) *Supervisor {
	t.Helper()
	root := previous.state.Path()
	previous.lock.Close()
	previous.lock = nil
	previous.state.Close()
	if previous.ledger != nil {
		previous.ledger.Close()
		previous.ledger = nil
	}
	next, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, DaemonID: previous.daemonID, Workspace: previous.workspace, Accounts: previous.accounts, EstimatedUnits: previous.estimates, Adapters: []Adapter{&fakeAdapter{proc: &fakeProcess{stopped: make(chan struct{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if next.lock != nil {
			next.lock.Close()
		}
		next.state.Close()
		if next.ledger != nil {
			next.ledger.Close()
		}
	})
	return next
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
			s = restartSharedLedgerFixture(t, s, api)
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

// Risk: a response loss while the owner remains alive can allocate another
// group on retry. Exact replay must keep the machine slot until confirmed.
func TestSharedLedgerLostRouteResponseSameOwnerKeepsGroup(t *testing.T) {
	s, a, _ := testSupervisor(t)
	api, _ := enableLedgerFixture(t, s, a)
	fixtureLedgerPeer(t, s, 1)
	s.probedAccounts["account"] = true
	api.routeErr = errors.New("fixture lost response")
	if err := s.StartRun(t.Context(), a.run); err == nil {
		t.Fatal("response loss succeeded")
	}
	attempt := s.journal.Snapshot()[0]
	api.routeErr = nil
	// Independent server read reflects the reservation committed before loss.
	api.fakeAPI.run.AccountID = "account"
	if err := s.StartRun(t.Context(), a.run); err != nil {
		t.Fatal(err)
	}
	d, _, err := s.ledger.Snapshot()
	if err != nil || len(d.Groups) != 1 || s.journal.Snapshot()[0].LedgerGroup != attempt.LedgerGroup || api.routeCalls != 2 {
		t.Fatal("retry replaced the occupied slot", d, err, api.routeCalls)
	}
}

// Risk: uninstall can forget a surviving worker after service removal and
// ledger loss. Only the owner's independently verified exit can clear it.
func TestSharedLedgerRemovedServiceRebuildRequiresOwnerExitProof(t *testing.T) {
	s, a, _ := testSupervisor(t)
	_, config := enableLedgerFixture(t, s, a)
	peer := fixtureLedgerPeer(t, s, 2)
	generation := s.ledgerBinding.Generation
	gid, err := agentsetup.LedgerID()
	if err != nil {
		t.Fatal(err)
	}
	// This record is retained after the daemon crashed, with no adopted process.
	r := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "survivor", WorkOrderID: "order", Generation: "previous-owner", ExecutionMode: "managed", AccountID: "account", State: "ownership_lost", LaunchState: launchAttempted, LedgerGroup: gid, LedgerGeneration: generation, PID: 3456, ProcessGroupID: 3456, ProcessStartedAt: time.Unix(10, 0).UTC()}
	if err = s.journal.Put(r); err != nil {
		t.Fatal(err)
	}
	s.runs[r.RunID] = &owned{record: r}
	if err = os.Remove(filepath.Join(config.Path, "ledger.json")); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ledger.Rebuild()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ledger.Import(peer.ID, agentsetup.LedgerInstance{Fingerprint: agentsetup.LedgerFingerprint(fresh, "instance", "peer"), PID: os.Getpid(), StartedAt: time.Unix(11, 0).UTC(), MaximumAgents: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ledger.Enrolled(peer.ID, fresh); err != nil {
		t.Fatal(err)
	}
	try, _ := agentsetup.LedgerID()
	if _, err = s.ledger.Acquire(peer.ID, fresh, try, s.ledgerCandidates([]string{"account"}, fresh)); !errors.Is(err, agentsetup.ErrLedgerUnavailable) {
		t.Fatal("rebuild forgot removed owner", err)
	}
	s.ledgerExitProof = func(Record) bool { return false }
	if err = s.EnableLedger(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Drain(DrainRequest{DaemonID: s.daemonID}); err != nil {
		t.Fatal(err)
	}
	if err = s.LeaveLedger(t.Context()); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("uninstall released surviving worker", err)
	}
	d, members, err := s.ledger.Snapshot()
	if err != nil || len(d.Groups) != 1 || len(members) != 2 {
		t.Fatal("failed uninstall lost occupancy", err, d)
	}
	s.ledgerExitProof = func(record Record) bool { return record.PID == 3456 && record.ProcessGroupID == 3456 }
	if err = s.LeaveLedger(t.Context()); err != nil {
		t.Fatal(err)
	}
	ledger, openErr := agentsetup.OpenSharedLedger(config.Path, false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer ledger.Close()
	d, members, err = ledger.Snapshot()
	if err != nil || len(d.Groups) != 0 || len(members) != 1 || members[0].ID != peer.ID {
		t.Fatal("owner did not delete tombstone last", err, d, members)
	}
}

// Risk: enrolment can be sent to a different trust context, or admission
// requests can omit the generation header. Optional fields require negotiation.
func TestSharedLedgerRemoteHeadersAndNegotiatedTelemetry(t *testing.T) {
	generation := "01234567-89ab-4cde-8fab-0123456789ab"
	advertised := false
	capacityReports := 0
	routes := map[string]int{}
	var fixtureMu sync.Mutex
	reports := func() int { fixtureMu.Lock(); defer fixtureMu.Unlock(); return capacityReports }
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixtureMu.Lock()
		defer fixtureMu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-runtime" {
			t.Error("wrong owner authority")
		}
		routes[r.URL.Path]++
		switch r.URL.Path {
		case "/api/agent-pairing/self":
			capabilities := []string{agentsetup.LedgerCapability}
			if advertised {
				capabilities = append(capabilities, "peer-running-v1")
			}
			_ = json.NewEncoder(w).Encode(agentsetup.View{ServerCapabilities: capabilities})
		case "/api/agent-pairing/self/ledger":
			var in struct {
				Generation string `json:"generation"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Generation != generation {
				t.Error("wrong enrolled generation")
			}
			_ = json.NewEncoder(w).Encode(agentsetup.View{LedgerMode: true, LedgerGeneration: &generation})
		case "/api/runs/queued", "/api/agent-accounts/route", "/api/runs/run/claim":
			if r.Header.Get(agentsetup.LedgerGenerationHeader) != generation {
				t.Error("admission omitted enrolled generation")
			}
			if r.URL.Path == "/api/runs/queued" {
				_ = json.NewEncoder(w).Encode([]Run{})
			} else if r.URL.Path == "/api/agent-accounts/route" {
				_ = json.NewEncoder(w).Encode(Route{})
			} else {
				w.WriteHeader(204)
			}
		case "/api/agent-pairing/self/capacity":
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["peer_running"] != float64(1) || !advertised {
				t.Error("unnegotiated peer telemetry")
			}
			capacityReports++
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "fixture-runtime")
	remote.Client.HTTP = server.Client()
	if _, _, err := remote.LedgerView(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := remote.EnrollLedger(t.Context(), generation); err != nil {
		t.Fatal(err)
	}
	remote.SetLedgerGeneration(generation)
	if _, err := remote.Queued(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Route(t.Context(), "run", "daemon", []string{"account"}, map[string]int64{"requests": 1}); err != nil {
		t.Fatal(err)
	}
	if err := remote.Claim(t.Context(), "run", "daemon", "claim-generation", []string{"reservation"}); err != nil {
		t.Fatal(err)
	}
	if err := remote.PeerRunning(t.Context(), 1); err != nil || reports() != 0 {
		t.Fatal("strict old server received unknown field", err)
	}
	fixtureMu.Lock()
	advertised = true
	fixtureMu.Unlock()
	if _, _, err := remote.LedgerView(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := remote.PeerRunning(t.Context(), 1); err != nil || reports() != 1 {
		t.Fatal("negotiated telemetry missing", err, reports())
	}
	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	for _, path := range []string{"/api/agent-pairing/self/ledger", "/api/runs/queued", "/api/agent-accounts/route", "/api/runs/run/claim"} {
		if routes[path] != 1 {
			t.Fatal("route not exercised", path)
		}
	}
}

// Risk: a crash after server enrolment, before the local success receipt,
// leaves a restarted owner dispatching before re-import and re-enrolment.
func TestSharedLedgerEnrollmentResponseLossRequiresRestartImport(t *testing.T) {
	s, a, _ := testSupervisor(t)
	api := &ledgerFixtureAPI{fakeAPI: a, view: agentsetup.View{ServerCapabilities: []string{agentsetup.LedgerCapability}}, maximum: 1, enrollmentErr: errors.New("fixture enrollment receipt lost"), enrollmentCommitted: true}
	s.api = api
	config := LedgerConfig{Path: filepath.Join(filepath.Dir(s.state.Path()), "ledger"), Label: "cm.aeon.agentd", Root: filepath.Dir(s.state.Path()), Origin: "https://ppm.example.invalid"}
	if err := s.EnableLedger(t.Context(), config); err == nil || !api.view.LedgerMode || api.view.LedgerGeneration == nil {
		t.Fatal("fixture did not crash after server enrollment", err)
	}
	root, workspace := s.state.Path(), s.workspace
	s.lock.Close()
	s.lock = nil
	s.state.Close()
	next, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, DaemonID: "daemon", Workspace: workspace, Adapters: []Adapter{&fakeAdapter{proc: &fakeProcess{stopped: make(chan struct{})}}}, Accounts: s.accounts, EstimatedUnits: map[string]int64{"requests": 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		next.Close(context.Background())
		if next.ledger != nil {
			next.ledger.Close()
		}
	})
	if err = next.StartRun(t.Context(), a.run); !errors.Is(err, agentsetup.ErrLedgerUnavailable) || api.routeCalls != 0 {
		t.Fatal("restart dispatched before import", err)
	}
	api.enrollmentErr = nil
	if err = next.RefreshLedger(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if api.ledgerGeneration != *api.view.LedgerGeneration {
		t.Fatal("re-enrollment lost generation")
	}
}

// Risk: re-import skips exited groups but leaves old-generation private
// coordinates, causing recovery to fail forever after a ledger rebuild.
func TestSharedLedgerRebuildClearsExitedOldGenerationCoordinates(t *testing.T) {
	s, a, _ := testSupervisor(t)
	_, config := enableLedgerFixture(t, s, a)
	gid, err := agentsetup.LedgerID()
	if err != nil {
		t.Fatal(err)
	}
	r := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "exited", WorkOrderID: "order", Generation: "previous-owner", ExecutionMode: "managed", AccountID: "account", State: "completed", LaunchState: launchAttempted, LedgerGroup: gid, LedgerGeneration: s.ledgerBinding.Generation, ExitObserved: true, PID: 3456}
	if err = s.journal.Put(r); err != nil {
		t.Fatal(err)
	}
	s.runs[r.RunID] = &owned{record: r}
	if _, err = s.ledger.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if err = s.EnableLedger(t.Context(), config); err != nil {
		t.Fatal("proven-exited record vetoed rebuild", err)
	}
	got := s.journal.Snapshot()[0]
	if got.LedgerGroup != "" || got.LedgerGeneration != "" || !got.ExitObserved || got.PID != r.PID || got.Generation != r.Generation {
		t.Fatal("coordinate cleanup damaged provenance", got)
	}
}

// Risk: offline uninstall uses a revoked key or guesses that an unpublished
// possible fork exited. Owner cleanup uses its fenced journal, without a key.
func TestSharedLedgerOfflineOwnerLeaveNeedsExitAndKeepsUnpublishedFork(t *testing.T) {
	for _, exited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpublished-fork", true: "observed-exit"}[exited], func(t *testing.T) {
			s, a, _ := testSupervisor(t)
			_, config := enableLedgerFixture(t, s, a)
			peer := fixtureLedgerPeer(t, s, 2)
			gid, err := agentsetup.LedgerID()
			if err != nil {
				t.Fatal(err)
			}
			subset, err := s.ledger.Acquire(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, gid, s.ledgerCandidates([]string{"account"}, s.ledgerBinding.Generation))
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ledger.Claimed(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, gid, subset[0].Login); err != nil {
				t.Fatal(err)
			}
			if err = s.ledger.Launching(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, gid); err != nil {
				t.Fatal(err)
			}
			r := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "survivor", WorkOrderID: "order", Generation: "previous-owner", ExecutionMode: "managed", AccountID: "account", State: "ownership_lost", LaunchState: launchAttempted, LedgerGroup: gid, LedgerGeneration: s.ledgerBinding.Generation, ExitObserved: exited}
			if err = s.journal.Put(r); err != nil {
				t.Fatal(err)
			}
			s.runs[r.RunID] = &owned{record: r}
			if err = PersistFence(s.state.Path(), s.daemonID, ""); err != nil {
				t.Fatal(err)
			}
			root := s.state.Path()
			s.lock.Close()
			s.lock = nil
			s.state.Close()
			err = OfflineLeaveLedger(root, s.daemonID, s.tenantID, s.principalID)
			ledger, openErr := agentsetup.OpenSharedLedger(config.Path, false)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer ledger.Close()
			d, members, readErr := ledger.Snapshot()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if exited {
				if err != nil || len(members) != 1 || members[0].ID != peer.ID || len(d.Groups) != 0 {
					t.Fatal("owner cleanup failed without key", err, d)
				}
			} else if !errors.Is(err, ErrProcessesUnconfirmed) || len(members) != 2 || len(d.Groups) != 1 {
				t.Fatal("unpublished fork forgotten", err, d)
			}
		})
	}
}

// Risk: upgrading a v3 owner erases a possible process or silently lets a
// downgraded writer overwrite private ledger attempt evidence in v4.
func TestSharedLedgerJournalV3MigrationPreservesEvidenceAndRejectsDowngrade(t *testing.T) {
	s, api, proc := testSupervisor(t)
	root, workspace := filepath.Join(filepath.Dir(s.state.Path()), "v3-owner"), s.workspace
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	oldConfig := localjournal.Config[Record]{Directory: root, Prefix: "aeon-agentd-daemon", Version: 3, MaxBytes: 4 << 20, MaxRecords: 4096, Key: func(r Record) (string, error) { return r.RunID, nil }, Validate: validateLaunchRecord}
	old, err := localjournal.Open(oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	want := Record{RunID: "prior-v3", WorkOrderID: "order", TenantID: "tenant", PrincipalID: "agent", Generation: "previous-owner", ExecutionMode: "managed", LaunchState: launchAttempted, State: "running", PID: 54321, Pending: []Telemetry{{Sequence: 1, Kind: "started"}}, Sequence: 1}
	if err = old.Put(want); err != nil {
		t.Fatal(err)
	}
	next, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, DaemonID: "daemon", Workspace: workspace, Adapters: []Adapter{&fakeAdapter{proc: proc}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.lock.Close(); _ = next.state.Close() })
	got := next.journal.Snapshot()
	if len(got) != 1 || got[0].RunID != want.RunID || got[0].PID != want.PID || got[0].Generation != want.Generation || got[0].State != "ownership_lost" || got[0].ExitObserved || !reflect.DeepEqual(got[0].Pending, want.Pending) || proc.calls != 0 {
		t.Fatal("v3 migration lost or adopted prior evidence", got)
	}
	paths := []string{filepath.Join(root, "aeon-agentd-daemon.checkpoint.json"), filepath.Join(root, "aeon-agentd-daemon.journal")}
	before := make([][]byte, len(paths))
	for i, path := range paths {
		before[i], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = localjournal.Open(oldConfig)
	var versionErr *localjournal.SchemaVersionError
	if !errors.As(err, &versionErr) || versionErr.Stored != RecordSchemaVersion || versionErr.Supported != 3 {
		t.Fatal("v3 downgrade did not refuse for the schema boundary", err)
	}
	for i, path := range paths {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before[i], after) {
			t.Fatal("downgrade damaged retained evidence", path, err)
		}
	}
}

// Risk: restarting after a deleted/corrupt ledger treats lost data as an empty
// machine, sends Route too early, or loses the exact surviving owner intent.
func TestSharedLedgerDamagedLedgerRestartWaitsForEveryMember(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt"} {
		t.Run(damage, func(t *testing.T) {
			s, a, _ := testSupervisor(t)
			api, config := enableLedgerFixture(t, s, a)
			peer := fixtureLedgerPeer(t, s, 2)
			old := s.ledgerBinding.Generation
			gid, err := agentsetup.LedgerID()
			if err != nil {
				t.Fatal(err)
			}
			subset, err := s.ledger.Acquire(s.ledgerBinding.Member.ID, old, gid, s.ledgerCandidates([]string{"account"}, old))
			if err != nil {
				t.Fatal(err)
			}
			r := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, WorkOrderID: a.run.WorkOrderID, Generation: s.generation, ExecutionMode: "managed", State: "route_pending", LaunchState: launchRoutePending, LedgerGroup: gid, LedgerGeneration: old, RouteCandidates: subset}
			if err = s.journal.Put(r); err != nil {
				t.Fatal(err)
			}
			s.runs[r.RunID] = &owned{record: r}
			path := filepath.Join(config.Path, "ledger.json")
			if damage == "missing" {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte("corrupt"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			s = restartSharedLedgerFixture(t, s, api)
			if err = s.RefreshLedger(t.Context(), config); !errors.Is(err, agentsetup.ErrLedgerUnavailable) || api.routeCalls != 0 {
				t.Fatal("damaged ledger was recreated or routed", err)
			}
			ledger, err := agentsetup.OpenSharedLedger(config.Path, false)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := ledger.Rebuild()
			ledger.Close()
			if err != nil || fresh == old {
				t.Fatal("rebuild reused generation", err)
			}
			if err = s.EnableLedger(t.Context(), config); err != nil || api.routeCalls != 0 {
				t.Fatal("owner handover failed while waiting for the peer", err)
			}
			if err = s.StartRun(t.Context(), a.run); !errors.Is(err, agentsetup.ErrLedgerUnavailable) || api.routeCalls != 0 {
				t.Fatal("owner admission preceded every member import", err)
			}
			data, members, err := s.ledger.Snapshot()
			if err != nil || !data.Rebuilding || len(members) != 2 || len(data.Groups) != 1 || data.Groups[gid].Generation != fresh {
				t.Fatal("restart lost surviving intent or tombstone", data, err)
			}
			if err = s.ledger.Launching(s.ledgerBinding.Member.ID, old, gid); !errors.Is(err, agentsetup.ErrLedgerGeneration) {
				t.Fatal("stale launch survived restart", err)
			}
			_, err = s.ledger.Import(peer.ID, agentsetup.LedgerInstance{Fingerprint: agentsetup.LedgerFingerprint(fresh, "instance", "peer"), PID: os.Getpid(), StartedAt: time.Unix(11, 0).UTC(), MaximumAgents: 2}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ledger.Enrolled(peer.ID, fresh); err != nil {
				t.Fatal(err)
			}
			if err = s.RefreshLedger(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			got := s.journal.Snapshot()[0]
			if got.LedgerGroup != gid || got.LedgerGeneration != fresh || got.LaunchState != launchPrepared || got.ClaimRoute == nil || api.routeCalls != 1 || !slices.Equal(api.offered[0], []string{"account"}) {
				t.Fatal("post-rebuild replay lost owner attempt", got)
			}
		})
	}
}

// Risk: a released cached route survives a restart as a second, permanent
// machine slot. Independent queued/unbound state invalidates only that route;
// the new attempt keeps the explicit pin and retains the old claim provenance.
func TestSharedLedgerCachedRouteReleaseAfterRestartReacquiresOneSlot(t *testing.T) {
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
	if err = s.ledger.Claimed(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, gid, subset[0].Login); err != nil {
		t.Fatal(err)
	}
	route, err := a.Route(t.Context(), a.run.ID, s.daemonID, []string{"account"}, s.estimates)
	if err != nil {
		t.Fatal(err)
	}
	r := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, WorkOrderID: a.run.WorkOrderID, Generation: s.generation, ExecutionMode: "managed", AccountID: route.AccountID, State: "claim_pending", LaunchState: launchPrepared, ClaimRoute: &route, LedgerGroup: gid, LedgerGeneration: s.ledgerBinding.Generation, RouteCandidates: subset}
	if err = s.journal.Put(r); err != nil {
		t.Fatal(err)
	}
	s.runs[r.RunID] = &owned{record: r}
	s = restartSharedLedgerFixture(t, s, api)
	if err = s.EnableLedger(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	a.run.AccountID = ""
	a.run.RequestedAccountID = "account"
	if err = s.reconcileUnlaunched(t.Context(), s.runs[r.RunID]); err != nil {
		t.Fatal("confirmed release could not retire the cached hold", err)
	}
	got := s.journal.Snapshot()[0]
	data, _, err := s.ledger.Snapshot()
	if err != nil || !got.RouteReleased || got.LedgerGroup != "" || got.Generation != r.Generation || len(data.Groups) != 0 || s.hasUnresolvedOldClaim("account") {
		t.Fatal("confirmed release retained a slot or changed claim evidence", got, data, err)
	}
	s.probedAccounts["account"] = true
	api.routeErr = errors.New("fixture new route response lost")
	if err = s.StartRun(t.Context(), a.run); !errors.Is(err, api.routeErr) {
		t.Fatal("fresh pinned attempt could not reacquire", err)
	}
	got = s.journal.Snapshot()[0]
	data, _, err = s.ledger.Snapshot()
	if err != nil || got.LedgerGroup == gid || got.LedgerGroup == "" || got.LaunchState != launchRoutePending || len(data.Groups) != 1 || api.routeCalls != 1 || !slices.Equal(api.offered[0], []string{"account"}) {
		t.Fatal("cached-route retry lost its pin or occupied two slots", got, data, err)
	}
}
