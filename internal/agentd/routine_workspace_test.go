// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/client"
)

func routineTestGrant(n string) agentsetup.RoutineGrant {
	id := func(s string) string { return "10000000-0000-4000-8000-0000000000" + s }
	return agentsetup.RoutineGrant{LedgerGeneration: id("10"), LaunchID: id(n), TenantID: id("01"), OwnerID: id("02"), ProjectID: id("03"), RoutineRunID: id("04"), AgentRunID: id(n), PrincipalID: id("05"), ExecutionPrincipalID: id("02"), AccountID: id("06"), ComputerID: id("07"), DaemonID: "daemon", DaemonGeneration: "generation", AssignmentDigest: strings.Repeat("a", 64), DefinitionRevision: 1, ConsentRevision: 1, ExecutionRevision: 1, QualificationID: id("08"), DaemonDigest: strings.Repeat("b", 64), CapabilityDigest: strings.Repeat("c", 64), HostMappingDigest: strings.Repeat("d", 64), Capability: agentsetup.RoutineExecutionCapability, RuntimeCapability: "routine_native_coding_v1", BudgetMode: "off", BudgetGrantID: id("09"), ExpiresAt: time.Now().Add(time.Minute)}
}

// Fixed leases exercise the integration contract; the resource allocator is
// AEON-1089's independently owned implementation, not duplicated by this test.
type routineLeaseFixture struct {
	environment []string
	finished    []bool
}

func (l *routineLeaseFixture) Environment() []string { return l.environment }
func (*routineLeaseFixture) HandoffPorts()           {}
func (*routineLeaseFixture) Running(int) error       { return nil }
func (l *routineLeaseFixture) Finish(confirmed bool) error {
	l.finished = append(l.finished, confirmed)
	return nil
}

// Risks: concurrent admitted assignments sharing a checkout; duplicate writers
// on replay; crash/restart or a changed map clearing disputed ownership. The
// acquisition barrier proves overlap without sleep/timing assertions. Real Git
// worktrees and the existing OS/store fences are exercised, without model calls.
func TestRoutineWorkspacesSeparateAndRetainWriters(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository, trees := filepath.Join(root, "repository"), filepath.Join(root, "trees")
	for _, dir := range []string{repository, trees} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Routine fixture")
	git("config", "user.email", "routine@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-qm", "fixture")
	// Repository helpers must never run during pre-launch allocation. The
	// filter is installed after committing the fixture so setup cannot run it.
	helperMarker := filepath.Join(root, "unadmitted-helper")
	if err := os.WriteFile(filepath.Join(repository, ".gitattributes"), []byte("tracked.txt filter=unadmitted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".gitattributes")
	git("commit", "-qm", "attribute fixture")
	git("config", "filter.unadmitted.smudge", "touch "+helperMarker)
	git("config", "filter.unadmitted.required", "true")
	g1, g2 := routineTestGrant("11"), routineTestGrant("12")
	base := RoutineRepository{TenantID: g1.TenantID, OwnerID: g1.OwnerID, ProjectID: g1.ProjectID, AccountID: g1.AccountID, ComputerID: g1.ComputerID, Repository: repository, Root: trees, BaseCommit: git("rev-parse", "HEAD"), DaemonDigest: g1.DaemonDigest, CapabilityDigest: g1.CapabilityDigest, HostMappingDigest: g1.HostMappingDigest}
	entered, release := make(chan RoutineResourceOwner, 2), make(chan struct{})
	temp1, temp2 := filepath.Join(root, "temp1"), filepath.Join(root, "temp2")
	for _, dir := range []string{temp1, temp2} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	leases := map[string]*routineLeaseFixture{}
	for _, item := range []struct {
		g                agentsetup.RoutineGrant
		tmp, db, app, pg string
	}{{g1, temp1, "aeon_run_" + strings.Repeat("a", 32), "31001", "31002"}, {g2, temp2, "aeon_run_" + strings.Repeat("b", 32), "31003", "31004"}} {
		leases[item.g.LaunchID] = &routineLeaseFixture{environment: []string{"AEON_RUN_ID=" + item.g.LaunchID, "AEON_RUN_DATABASE=" + item.db, "AEON_RUN_APP_PORT=" + item.app, "AEON_RUN_POSTGRES_PORT=" + item.pg, "AEON_RUN_TEMP_DIR=" + item.tmp, "TMPDIR=" + item.tmp, "TMP=" + item.tmp, "TEMP=" + item.tmp}}
	}
	config := &RoutineWorkspaceConfig{Repositories: []RoutineRepository{base}, AcquireResources: func(ctx context.Context, owner RoutineResourceOwner) (RoutineResourceLease, error) {
		entered <- owner
		select {
		case <-release:
			return leases[owner.RunID], nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	newSupervisor := func(state string) *Supervisor {
		t.Helper()
		store, err := agentsetup.OpenStore(filepath.Join(root, state), true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return &Supervisor{tenantID: g1.TenantID, principalID: g1.PrincipalID, daemonID: g1.DaemonID, generation: g1.DaemonGeneration, ledgerBinding: ledgerBinding{Generation: g1.LedgerGeneration}, state: store, routineConfig: config, routineCheckouts: map[string]*agentsetup.Store{}, routineResources: map[string]RoutineResourceLease{}, runs: map[string]*owned{}, capacityNow: time.Now}
	}
	s1, s2 := newSupervisor("state1"), newSupervisor("state2")
	if _, err := s1.routineRepository(Run{AccountID: g1.AccountID}, g1, &routineQualifiedFixture{}); err != nil {
		t.Fatal("valid qualified map refused", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	type result struct {
		w   *RoutineWorkspace
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, item := range []struct {
		s *Supervisor
		g agentsetup.RoutineGrant
	}{{s1, g1}, {s2, g2}} {
		wg.Add(1)
		go func(s *Supervisor, g agentsetup.RoutineGrant) {
			defer wg.Done()
			w, err := s.prepareRoutineWorkspace(ctx, Run{ID: g.AgentRunID}, g, base)
			results <- result{w, err}
		}(item.s, item.g)
	}
	owners := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case owner := <-entered:
			if owner.TenantID != g1.TenantID || owner.AccountID != g1.AccountID || owner.HostID != g1.ComputerID {
				t.Fatal("mixed resource owner")
			}
			owners[owner.RunID] = true
		case <-ctx.Done():
			t.Fatal("allocation barrier did not overlap")
		}
	}
	close(release)
	wg.Wait()
	if len(owners) != 2 {
		t.Fatal("assignments shared resource ownership")
	}
	workspaces := map[string]*RoutineWorkspace{}
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		workspaces[r.w.Grant.AgentRunID] = r.w
	}
	w1, w2 := workspaces[g1.AgentRunID], workspaces[g2.AgentRunID]
	if w1.ID == w2.ID || w1.Path == w2.Path || w1.Branch == w2.Branch {
		t.Fatal("distinct jobs shared checkout")
	}
	if _, err := os.Stat(helperMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("allocation ran repository filter before admitted launch")
	}
	for _, item := range []struct {
		s *Supervisor
		g agentsetup.RoutineGrant
		w *RoutineWorkspace
	}{{s1, g1, w1}, {s2, g2, w2}} {
		if err := validateRoutineCheckout(ctx, base, *item.w); err != nil {
			t.Fatal(err)
		}
		if _, err := item.s.routineEnvironment(item.g.AgentRunID, item.g); err != nil {
			t.Fatal(err)
		}
		if _, err := item.s.prepareRoutineWorkspace(ctx, Run{ID: item.g.AgentRunID}, item.g, base); !errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatalf("replayed unjournaled allocation: %v", err)
		}
		item.s.runs[item.g.AgentRunID] = &owned{record: Record{RoutineWorkspace: item.w, Workspace: item.w.Path, Generation: item.g.DaemonGeneration, LaunchState: launchAttempted, State: "ownership_lost"}}
		raw, _ := json.Marshal(map[string]any{"routine_launch": item.g, "routine_run_id": item.g.RoutineRunID})
		stale := Run{ID: item.g.AgentRunID, WorkOrderID: item.g.ProjectID, AgentPrincipalID: item.g.PrincipalID, Status: "queued", AccountID: item.g.AccountID, RoutineLaunchID: &item.g.LaunchID, Trace: raw}
		if err := item.s.StartRun(ctx, stale); !errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatalf("unknown prior launch acknowledged as started: %v", err)
		}
		// Closing the OS handle simulates crash; durable owner must survive.
		if err := item.s.routineCheckouts[item.g.AgentRunID].Close(); err != nil {
			t.Fatal(err)
		}
		delete(item.s.routineCheckouts, item.g.AgentRunID)
		other := newSupervisor("other-" + item.g.LaunchID)
		if err := other.acquireRoutineCheckout(item.g.AgentRunID, *item.w); !errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatalf("crash erased owner fence: %v", err)
		}
		if err := item.s.releaseRoutineCheckouts(); !errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatalf("unknown fixture cleanup: %v", err)
		}
		if got := leases[item.g.LaunchID].finished; len(got) != 0 || item.s.routineResources[item.g.AgentRunID] == nil {
			t.Fatal("unknown fixture exit released resource ownership")
		}
		if _, err := os.Stat(item.w.Path); err != nil {
			t.Fatal("worktree lost on recovery")
		}
	}
	// An ordinary adapter never acquires a routine grant through its manual API.
	if _, err := s1.routineRepository(Run{AccountID: g1.AccountID}, g1, &fakeAdapter{}); !errors.Is(err, ErrRoutineUnavailable) {
		t.Fatal("manual adapter acquired routine authority")
	}
	for _, mutate := range []func(*agentsetup.RoutineGrant){func(g *agentsetup.RoutineGrant) { g.Capability = "old" }, func(g *agentsetup.RoutineGrant) { g.TenantID = g.OwnerID }, func(g *agentsetup.RoutineGrant) { g.ComputerID = g.OwnerID }, func(g *agentsetup.RoutineGrant) { g.AccountID = g.OwnerID }} {
		g := g1
		mutate(&g)
		raw, _ := json.Marshal(map[string]any{"routine_launch": g, "routine_run_id": g.RoutineRunID})
		run := Run{ID: g.AgentRunID, RoutineLaunchID: &g.LaunchID, Trace: raw, AccountID: g.AccountID}
		parsed, err := routineGrant(run)
		if err == nil {
			_, err = s1.routineRepository(run, *parsed, &routineQualifiedFixture{})
		}
		if !errors.Is(err, ErrRoutineUnavailable) {
			t.Fatalf("changed binding accepted: %v", err)
		}
	}
}

type routineQualifiedFixture struct {
	fakeAdapter
	requests []StartRequest
}

func (a *routineQualifiedFixture) Start(ctx context.Context, request StartRequest, observe func(AdapterEvent)) (Process, error) {
	a.requests = append(a.requests, request)
	return a.fakeAdapter.Start(ctx, request, observe)
}

func (*routineQualifiedFixture) RoutineExecutionSupported(agentsetup.RoutineGrant) bool { return true }

func routineGitFixture(t *testing.T) (string, string, func(...string) string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Routine fixture")
	git("config", "user.email", "routine@example.invalid")
	for name, content := range map[string]string{"tracked.txt": "base\n", ".gitattributes": "tracked.txt filter=unadmitted\n"} {
		if err := os.WriteFile(filepath.Join(repository, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "--", "tracked.txt", ".gitattributes")
	git("commit", "-qm", "fixture")
	return root, repository, git
}

// Risk: included, conditional and worktree config can execute a helper before
// admission. A real checkout must preserve the file without executing it.
func TestRoutineCheckoutDisablesEffectiveFilterConfiguration(t *testing.T) {
	for _, source := range []string{"include", "worktree", "destination-include"} {
		t.Run(source, func(t *testing.T) {
			root, repository, git := routineGitFixture(t)
			marker := filepath.Join(root, "helper-ran")
			config := filepath.Join(root, "filters.conf")
			if err := os.WriteFile(config, []byte("[filter \"unadmitted\"]\n smudge = touch '"+marker+"'; cat\n required = true\n"), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "checkout")
			switch source {
			case "include":
				git("config", "include.path", config)
			case "worktree":
				git("config", "extensions.worktreeConfig", "true")
				git("config", "--worktree", "include.path", config)
			case "destination-include":
				git("config", "includeIf.gitdir:"+filepath.Join(repository, ".git", "worktrees", "checkout")+".path", config)
			}
			if _, err := routineGit(t.Context(), repository, "worktree", "add", "-b", "routine/fixture", destination, "HEAD"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("checkout executed an unadmitted helper", err)
			}
			if data, err := os.ReadFile(filepath.Join(destination, "tracked.txt")); err != nil || string(data) != "base\n" {
				t.Fatal("checkout lost tracked content", err)
			}
		})
	}
}

type routineReplayAPI struct {
	*ledgerFixtureAPI
	grant   agentsetup.RoutineGrant
	pickups []agentsetup.RoutinePickup
	claims  []agentsetup.RoutinePickup
	lost    bool
	refusal error
}

func (a *routineReplayAPI) Identity(context.Context) (string, string, error) {
	return a.grant.TenantID, a.grant.PrincipalID, nil
}
func (a *routineReplayAPI) Route(context.Context, string, string, []string, map[string]int64) (Route, error) {
	return Route{}, &client.StatusError{Status: 409, Message: "routine pickup required"}
}
func (a *routineReplayAPI) RouteRoutine(_ context.Context, run, daemon string, ids []string, _ map[string]int64, pickup agentsetup.RoutinePickup) (Route, error) {
	if run != a.grant.AgentRunID || daemon != a.grant.DaemonID || !reflect.DeepEqual(ids, []string{a.grant.AccountID}) || pickup.LaunchID != a.grant.LaunchID {
		return Route{}, ErrScope
	}
	a.pickups = append(a.pickups, pickup)
	a.run.AccountID = a.grant.AccountID // server commit precedes the lost response
	if a.refusal != nil {
		return Route{}, a.refusal
	}
	if a.lost {
		a.lost = false
		return Route{}, errors.New("fixture lost routine route response")
	}
	return Route{AccountID: a.grant.AccountID, AccountKey: "local", DaemonID: daemon, Reservations: []Reservation{{ID: "reservation"}}}, nil
}
func (a *routineReplayAPI) ClaimRoutine(_ context.Context, run, daemon, generation string, _ []string, _ *WorkerPickup, pickup agentsetup.RoutinePickup) error {
	if run != a.grant.AgentRunID || daemon != a.grant.DaemonID || generation != a.grant.DaemonGeneration {
		return ErrScope
	}
	a.claims = append(a.claims, pickup)
	return nil
}

func routineSupervisorFixture(t *testing.T) (*Supervisor, *routineReplayAPI, *fakeProcess, RoutineRepository, *int) {
	t.Helper()
	root, repository, git := routineGitFixture(t)
	grant := routineTestGrant("21")
	process := &fakeProcess{stopped: make(chan struct{})}
	api := &routineReplayAPI{grant: grant, ledgerFixtureAPI: &ledgerFixtureAPI{fakeAPI: &fakeAPI{profile: Profile{ID: "profile", Harness: Codex, Model: "model"}}, view: agentsetup.View{ServerCapabilities: []string{agentsetup.LedgerCapability}}, maximum: 2}}
	trees := filepath.Join(root, "trees")
	if err := os.Mkdir(trees, 0700); err != nil {
		t.Fatal(err)
	}
	base := RoutineRepository{TenantID: grant.TenantID, OwnerID: grant.OwnerID, ProjectID: grant.ProjectID, AccountID: grant.AccountID, ComputerID: grant.ComputerID, Repository: repository, Root: trees, BaseCommit: git("rev-parse", "HEAD"), DaemonDigest: grant.DaemonDigest, CapabilityDigest: grant.CapabilityDigest, HostMappingDigest: grant.HostMappingDigest}
	allocations := new(int)
	config := &RoutineWorkspaceConfig{Repositories: []RoutineRepository{base}, AcquireResources: func(_ context.Context, owner RoutineResourceOwner) (RoutineResourceLease, error) {
		*allocations++
		tmp := filepath.Join(root, "temp-"+owner.RunID)
		if err := os.MkdirAll(tmp, 0700); err != nil {
			return nil, err
		}
		return &routineLeaseFixture{environment: []string{"AEON_RUN_ID=" + owner.RunID, "AEON_RUN_DATABASE=aeon_run_" + strings.Repeat("a", 32), "AEON_RUN_APP_PORT=31001", "AEON_RUN_POSTGRES_PORT=31002", "AEON_RUN_TEMP_DIR=" + tmp, "TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp}}, nil
	}}
	s, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: filepath.Join(root, "state"), DaemonID: grant.DaemonID, Workspace: repository, Adapters: []Adapter{&routineQualifiedFixture{fakeAdapter: fakeAdapter{proc: process}}}, Accounts: []EnrolledAccount{{ID: grant.AccountID, Key: "local", Harness: Codex, VerifiedIdentity: "routine-login"}}, EstimatedUnits: map[string]int64{"requests": 1}, RoutineWorkspaces: config})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableLedger(t.Context(), LedgerConfig{Path: filepath.Join(root, "ledger"), Label: "cm.aeon.agentd", Root: root, Origin: "https://routine.example.invalid"}); err != nil {
		t.Fatal(err)
	}
	api.grant.DaemonGeneration, api.grant.LedgerGeneration = s.generation, s.ledgerBinding.Generation
	raw, _ := json.Marshal(map[string]any{"routine_launch": api.grant, "routine_run_id": api.grant.RoutineRunID})
	api.run = Run{ID: api.grant.AgentRunID, WorkOrderID: "order", AgentPrincipalID: api.grant.PrincipalID, ModelProfileID: "profile", Status: "queued", Purpose: "managed", RequestedAccountID: grant.AccountID, RoutineLaunchID: &api.grant.LaunchID, Trace: raw}
	t.Cleanup(func() {
		_ = process.Stop(context.Background())
		for _, entry := range s.runs {
			entry.mu.Lock()
			done := entry.monitorDone
			entry.mu.Unlock()
			if done != nil {
				<-done
			}
		}
		_ = s.Close(context.Background())
		for _, store := range s.routineCheckouts {
			_ = store.Close()
		}
		if s.lock != nil {
			_ = s.lock.Close()
		}
		_ = s.state.Close()
		if s.ledger != nil {
			_ = s.ledger.Close()
		}
	})
	return s, api, process, base, allocations
}

// Risk: a committed route with a lost response must keep its pickup, checkout,
// resource lease and single ledger hold, rather than deleting the allocation.
func TestRoutineSupervisorReplaysInterruptedRouting(t *testing.T) {
	s, api, _, _, allocations := routineSupervisorFixture(t)
	api.lost = true
	if err := s.StartRun(t.Context(), api.run); err == nil || !strings.Contains(err.Error(), "lost routine route response") {
		t.Fatal("missing interrupted route", err)
	}
	intent := s.journal.Snapshot()[0]
	lease := s.routineResources[api.run.ID]
	api.refusal = &client.StatusError{Status: 409, Message: "fixture current authority unavailable"}
	if err := s.StartRun(t.Context(), api.run); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("refused routine replay reported success", err)
	}
	if records := s.journal.Snapshot(); !reflect.DeepEqual(records, []Record{intent}) || s.routineResources[api.run.ID] != lease {
		t.Fatal("refused replay discarded routine provenance", records)
	}
	api.refusal = nil
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal("routine route replay failed", err)
	}
	if len(api.pickups) != 3 || api.pickups[0] != api.pickups[1] || api.pickups[0] != api.pickups[2] || len(api.claims) != 2 || api.claims[0] != api.pickups[0] || api.claims[1] != api.pickups[0] {
		t.Fatal("route/claim replay changed persisted pickup", api.pickups, api.claims)
	}
	entry := s.runs[api.run.ID]
	entry.mu.Lock()
	got := entry.record
	entry.mu.Unlock()
	data, _, err := s.ledger.Snapshot()
	if err != nil || len(data.Groups) != 1 || got.LedgerGroup != intent.LedgerGroup || !reflect.DeepEqual(got.RoutineWorkspace, intent.RoutineWorkspace) || got.Workspace != intent.Workspace || *allocations != 1 || s.routineResources[api.run.ID] != lease {
		t.Fatal("replay lost allocation provenance", got, data, err)
	}
}

// Risk: capacity continuation must retain committed and uncommitted work and
// refuse a changed lineage or unknown fixture/process exit before allocation.
func TestRoutineContinuationRetainsOriginalCheckout(t *testing.T) {
	s, api, _, repository, _ := routineSupervisorFixture(t)
	oldRun, oldGrant := api.run, api.grant
	w, err := s.prepareRoutineWorkspace(t.Context(), oldRun, oldGrant, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "retained-commit.txt"), []byte("committed work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "--", "retained-commit.txt"}, {"commit", "-qm", "retained commit"}} {
		if out, err := exec.Command("git", append([]string{"-C", w.Path}, args...)...).CombinedOutput(); err != nil {
			t.Fatal("retained commit fixture", err, string(out))
		}
	}
	retainedHEAD, err := routineGit(t.Context(), w.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "retained.txt"), []byte("retained work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	record := Record{RoutineWorkspace: w, TenantID: s.tenantID, PrincipalID: s.principalID, RunID: oldRun.ID, WorkOrderID: oldRun.WorkOrderID, Generation: s.generation, Workspace: w.Path, LaunchBranch: w.Branch, AccountID: oldGrant.AccountID, LaunchState: launchAttempted, State: "failed", ExitObserved: true}
	prior := &owned{record: record, process: &routineExitedFixture{fakeProcess: &fakeProcess{}, fixturesExited: true}}
	s.runs[oldRun.ID] = prior
	if err := s.journal.Put(record); err != nil {
		t.Fatal(err)
	}
	nextGrant := oldGrant
	nextGrant.LaunchID, nextGrant.AgentRunID = routineTestGrant("22").LaunchID, routineTestGrant("22").AgentRunID
	next := oldRun
	next.ID, next.RetryOfRunID, next.CapacityHandoff = nextGrant.AgentRunID, oldRun.ID, true
	// Refusals must precede resource acquisition and ownership transfer.
	for _, mutate := range []func(){func() { prior.record.ExitObserved = false }, func() { prior.process.(*routineExitedFixture).fixturesExited = false }, func() { prior.record.RoutineWorkspace = nil }} {
		mutate()
		if _, err := s.prepareRoutineWorkspace(t.Context(), next, nextGrant, repository); !errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatal("unsafe continuation was not refused", err)
		}
		prior.record = record
		prior.process.(*routineExitedFixture).fixturesExited = true
	}
	prior.process = nil
	lease := s.routineResources[oldRun.ID]
	delete(s.routineResources, oldRun.ID)
	if _, err := s.prepareRoutineWorkspace(t.Context(), next, nextGrant, repository); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("missing restart lease manufactured fixture exit", err)
	}
	prior.process = &routineExitedFixture{fakeProcess: &fakeProcess{}, fixturesExited: true}
	s.routineResources[oldRun.ID] = lease
	changedGrant := nextGrant
	changedGrant.OwnerID = changedGrant.PrincipalID
	if _, err := s.prepareRoutineWorkspace(t.Context(), next, changedGrant, repository); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("changed owner adopted retained checkout", err)
	}
	api.grant = nextGrant
	next.RoutineLaunchID = &api.grant.LaunchID
	next.Trace, _ = json.Marshal(map[string]any{"routine_launch": nextGrant, "routine_run_id": nextGrant.RoutineRunID})
	api.run = next
	if err := s.StartRun(t.Context(), next); err != nil {
		t.Fatal("supervisor continuation failed", err)
	}
	entry := s.runs[next.ID]
	entry.mu.Lock()
	recovered := entry.record.RoutineWorkspace
	entry.mu.Unlock()
	requests := s.adapters[Codex].(*routineQualifiedFixture).requests
	if recovered == nil || len(requests) != 1 || requests[0].Workspace != w.Path {
		t.Fatal("adapter did not launch in retained workspace", requests)
	}
	if recovered.Path != w.Path || recovered.ID != w.ID || recovered.Branch != w.Branch || recovered.Grant.LaunchID != nextGrant.LaunchID {
		t.Fatal("continuation replaced retained checkout", recovered)
	}
	if data, err := os.ReadFile(filepath.Join(recovered.Path, "retained.txt")); err != nil || string(data) != "retained work\n" {
		t.Fatal("continuation lost work", err)
	}
	if head, err := routineGit(t.Context(), recovered.Path, "rev-parse", "HEAD"); err != nil || head != retainedHEAD {
		t.Fatal("continuation lost retained commit", err)
	}
	if err := s.validateCapacityHandoff(next, recovered.Branch); err != nil {
		t.Fatal("retained handoff refused", err)
	}
	if err := validateLaunchRecord(Record{RoutineWorkspace: recovered, TenantID: s.tenantID, PrincipalID: s.principalID, RunID: next.ID, Generation: s.generation, Workspace: recovered.Path, LaunchState: launchAttempted}); err != nil {
		t.Fatal("retained workspace cannot be journaled", err)
	}
	if err := validateRoutineCheckout(t.Context(), repository, *recovered); err != nil {
		t.Fatal(err)
	}
}

type routineExitedFixture struct {
	*fakeProcess
	fixturesExited bool
}

func (p *routineExitedFixture) RoutineFixturesExited() bool { return p.fixturesExited }
