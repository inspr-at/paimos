// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
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

type routineQualifiedFixture struct{ fakeAdapter }

func (*routineQualifiedFixture) RoutineExecutionSupported(agentsetup.RoutineGrant) bool { return true }
