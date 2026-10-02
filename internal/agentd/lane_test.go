// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/laneprotocol"
)

const laneProject = "00000000-0000-4000-8000-000000000001"
const laneEnvelope = "00000000-0000-4000-8000-000000000002"
const laneRunA = "00000000-0000-4000-8000-000000000003"
const laneRunB = "00000000-0000-4000-8000-000000000004"

type laneTestAPI struct {
	*fakeAPI
	grant *laneprotocol.Grant
}

func (a *laneTestAPI) ClaimLane(_ context.Context, id, daemon, generation string, _ []string, workspace string) (Run, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claims++
	run := a.run
	run.Status = "starting"
	run.LaneGrant = &laneprotocol.Grant{EnvelopeID: laneEnvelope, RunID: id, ProjectID: laneProject, WorkspaceID: workspace, DaemonID: daemon, Generation: generation, RemainingMS: 60000, StopAllowanceMS: 2000, ExpiresAt: time.Now().Add(time.Minute)}
	if a.grant != nil {
		run.LaneGrant = a.grant
	}
	return run, nil
}
func (a *laneTestAPI) Route(ctx context.Context, id, daemon string, accounts []string, estimates map[string]int64) (Route, error) {
	r, e := a.fakeAPI.Route(ctx, id, daemon, accounts, estimates)
	r.BillingMode = "subscription"
	return r, e
}
func (a *laneTestAPI) ProjectForNode(context.Context, string) (string, error) {
	return laneProject, nil
}

type laneTestAdapter struct {
	fakeAdapter
	request StartRequest
	starts  int
}

func (*laneTestAdapter) LaneExecutionSupported() bool { return true }
func (a *laneTestAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	a.request = r
	a.starts++
	return a.fakeAdapter.Start(ctx, r, observe)
}
func laneFixture(t *testing.T) (*Supervisor, *laneTestAPI, *laneTestAdapter) {
	t.Helper()
	s, base, proc := testSupervisor(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", s.workspace}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "fixture")
	head := git("rev-parse", "HEAD")
	s.laneRepositories = map[string]LaneRepository{laneProject: {Path: s.workspace, BaseRevision: head}}
	base.run.ID = laneRunA
	base.run.LaneEnvelopeID = laneEnvelope
	base.run.LaneProjectID = laneProject
	api := &laneTestAPI{fakeAPI: base}
	adapter := &laneTestAdapter{fakeAdapter: fakeAdapter{proc: proc}}
	s.api = api
	s.adapters[Codex] = adapter
	return s, api, adapter
}
func TestLaneWorkspacesNeverShareCheckout(t *testing.T) {
	s, a, adapter := laneFixture(t)
	path, branch, err := s.laneWorkspace(t.Context(), a.run, adapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := a.run
	other.ID = laneRunB
	second, otherBranch, err := s.laneWorkspace(t.Context(), other, adapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path == second || path == s.workspace || branch == otherBranch {
		t.Fatal("shared lane checkout")
	}
	if err = os.WriteFile(filepath.Join(path, "private-work"), []byte("one run"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(second, "private-work")); !os.IsNotExist(err) {
		t.Fatal("worktree file leaked")
	}
	if _, _, err = s.laneWorkspace(t.Context(), a.run, adapter, nil); err == nil {
		t.Fatal("reused unknown checkout")
	}
	delete(s.laneRepositories, laneProject)
	if _, _, err = s.laneWorkspace(t.Context(), other, adapter, nil); err == nil {
		t.Fatal("unmapped repository accepted")
	}
}
func TestLaneStartGrantAndConfirmedSettlement(t *testing.T) {
	s, a, adapter := laneFixture(t)
	if err := s.StartRun(t.Context(), a.run); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 1 || adapter.request.Workspace == s.workspace || !strings.HasSuffix(adapter.request.Workspace, laneRunA) {
		t.Fatalf("wrong workspace: %s", adapter.request.Workspace)
	}
	entry := s.runs[laneRunA]
	entry.mu.Lock()
	record, done := entry.record, entry.monitorDone
	entry.mu.Unlock()
	if record.Workspace != adapter.request.Workspace || record.AutomaticReview {
		t.Fatal("journal or coordinator-review contract")
	}
	if err := s.StartRun(t.Context(), a.run); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 1 {
		t.Fatal("duplicate process")
	}
	_ = adapter.proc.Stop(t.Context())
	<-done
	a.mu.Lock()
	defer a.mu.Unlock()
	found := false
	for _, r := range a.reports {
		if r.Kind == "finished" {
			found = true
			if r.LaneSettlement == nil || !r.LaneSettlement.ExitConfirmed || r.LaneSettlement.ElapsedMS < 0 {
				t.Fatal("missing confirmed accounting")
			}
		}
	}
	if !found {
		t.Fatal("no finish report")
	}
}
func TestLaneUnsupportedAdapterRefusesBeforeRouting(t *testing.T) {
	s, a, adapter := laneFixture(t)
	s.adapters[Codex] = &adapter.fakeAdapter
	if err := s.StartRun(t.Context(), a.run); err == nil {
		t.Fatal("unqualified adapter launched")
	}
	if a.claims != 0 || len(a.routeAccounts) != 0 || adapter.starts != 0 {
		t.Fatal("refusal consumed a claim")
	}
}
func TestLaneDeadlineUsesRequestClockAndRemainingGrant(t *testing.T) {
	run := Run{ID: laneRunA, LaneEnvelopeID: laneEnvelope, LaneProjectID: laneProject}
	sent := time.Now()
	g := &laneprotocol.Grant{EnvelopeID: laneEnvelope, RunID: laneRunA, ProjectID: laneProject, WorkspaceID: laneRunA, DaemonID: "d", Generation: "g", RemainingMS: 12000, StopAllowanceMS: 2000, ExpiresAt: time.Now().Add(24 * time.Hour)}
	deadline, err := grantDeadline(g, run, "d", "g", laneRunA, sent)
	if err != nil || !deadline.Equal(sent.Add(10*time.Second)) {
		t.Fatalf("deadline %v %v", deadline, err)
	}
	// Server wall clock is deliberately a day ahead; it cannot extend local time.
	g.RemainingMS = 2000
	if _, err = grantDeadline(g, run, "d", "g", laneRunA, sent); err == nil {
		t.Fatal("no stop allowance left")
	}
	g.RemainingMS = 12000
	g.Generation = "stale"
	if _, err = grantDeadline(g, run, "d", "g", laneRunA, sent); err == nil {
		t.Fatal("stale generation grant")
	}
}
