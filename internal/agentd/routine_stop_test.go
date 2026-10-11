// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

type routineStopAPI struct {
	*fakeAPI
	tenantID, principalID string
}

func (a *routineStopAPI) Identity(context.Context) (string, string, error) {
	return a.tenantID, a.principalID, nil
}

type routineStopProcess struct {
	*fakeProcess
	identity ownedprocess.Identity
	stops    int
	forces   int
}

func (p *routineStopProcess) Ownership() (ownedprocess.Identity, error) { return p.identity, nil }
func (p *routineStopProcess) ForceStop(_ context.Context, expected ownedprocess.Identity, _ time.Time) error {
	if expected != p.identity {
		return ErrNotOwned
	}
	p.forces++
	return nil
}
func (p *routineStopProcess) GracefulStop(context.Context) error {
	p.stops++
	return errors.New("fixture lost cooperative stop reply")
}

// Risk: a stale/replayed recovery control signals a different process or twice
// after a lost native reply, or blocks a later person-authorized force stop.
// No native CLI or PID signal is used in this test.
func TestRoutineCooperativeStopExactIdentityAndUncertainReplay(t *testing.T) {
	g := routineTestGrant("20")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "work")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	api := &routineStopAPI{fakeAPI: &fakeAPI{}, tenantID: g.TenantID, principalID: g.PrincipalID}
	s, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: filepath.Join(root, "state"), Workspace: workspace, DaemonID: g.DaemonID, Adapters: []Adapter{&fakeAdapter{}}})
	if err != nil {
		t.Fatal(err)
	}
	g.DaemonGeneration = s.generation
	identity := ownedprocess.Identity{DaemonID: s.daemonID, Generation: s.generation, ProcessID: strings.Repeat("8", 32), RootPID: 3456, GroupID: 3456, StartedAt: time.Now().UTC()}
	original := &fakeProcess{stopped: make(chan struct{})}
	proc := &routineStopProcess{fakeProcess: original, identity: identity}
	entry := &owned{record: Record{TenantID: g.TenantID, PrincipalID: g.PrincipalID, RunID: g.AgentRunID, Generation: s.generation, AccountID: g.AccountID, Workspace: workspace, State: "running", LaunchState: launchAttempted, Controls: map[string]replay{}, RoutineWorkspace: &RoutineWorkspace{Grant: g, ID: strings.Repeat("7", 64), Path: workspace, Branch: "routine/" + g.LaunchID}}, process: proc}
	s.runs[g.AgentRunID] = entry
	t.Cleanup(func() {
		_ = original.Stop(context.Background())
		entry.mu.Lock()
		entry.record.State = "completed"
		entry.mu.Unlock()
		_ = s.Close(context.Background())
	})
	expiry := time.Now().Add(time.Minute)
	req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: g.AgentRunID, Generation: s.generation, CorrelationID: "routine-stop", Operation: "stop", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry}
	wrong := identity
	wrong.ProcessID = "another-process"
	req.ExpectedOwnership = &wrong
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrNotOwned) || proc.stops != 0 {
		t.Fatalf("wrong process stop: %v count=%d", err, proc.stops)
	}
	req.ExpectedOwnership = &identity
	proc.identity.Generation = "stale-child-generation"
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrNotOwned) || proc.stops != 0 {
		t.Fatalf("stale current identity stop: %v count=%d", err, proc.stops)
	}
	proc.identity = identity
	entry.harness = HarnessSession{ID: "routine-session", Activity: "idle"}
	api.harnessControls = []HarnessControl{
		{ID: req.CorrelationID, Kind: "stop", ExpectedGeneration: entry.harness.ID, ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry},
		{ID: "person-stop-now", Kind: "stop", RequestPayload: &struct {
			StopNow bool `json:"stop_now"`
		}{true}, ExpectedGeneration: entry.harness.ID, ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry},
	}
	api.harnessCompletionFailures = 1
	if err := s.serviceHarnessWake(t.Context(), entry, false, true); !errors.Is(err, ErrControlUnconfirmed) || proc.stops != 1 || proc.forces != 0 {
		t.Fatalf("lost cooperative stop/completion reply: %v stops=%d forces=%d", err, proc.stops, proc.forces)
	}
	if api.harnessCompletionFailures != 0 || len(entry.pending) != 2 {
		t.Fatal("cooperative stop did not attempt uncertain completion or lost its pending controls")
	}
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrControlUnconfirmed) || proc.stops != 1 {
		t.Fatalf("uncertain replay repeated stop: %v count=%d", err, proc.stops)
	}
	// The uncertainty is durable, rather than only an in-memory replay entry.
	raw, err := os.ReadFile(s.journal.CheckpointPath())
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint struct {
		Records []Record `json:"records"`
	}
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	records := checkpoint.Records
	found := false
	for _, record := range records {
		if record.RunID == g.AgentRunID {
			prior, ok := record.Controls[req.CorrelationID]
			found = ok && prior.Rejected
		}
	}
	if !found {
		t.Fatal("lost stop reply was not journaled before touching the child")
	}
	if err := s.serviceHarnessWake(t.Context(), entry, false, true); err != nil {
		t.Fatalf("uncertain cooperative stop blocked the next person control: %v", err)
	}
	if len(entry.pending) != 0 || proc.stops != 1 || proc.forces != 1 || len(api.harnessCompletions) != 2 || api.harnessCompletions[0] != "routine-stop:rejected:outcome_unconfirmed" || api.harnessCompletions[1] != "person-stop-now:applied:owned_group_signalled_root_exited" {
		t.Fatalf("cooperative replay/force stop results: pending=%d stops=%d forces=%d completions=%v", len(entry.pending), proc.stops, proc.forces, api.harnessCompletions)
	}
	if err := s.serviceHarnessWake(t.Context(), entry, false, true); err != nil || proc.stops != 1 || proc.forces != 1 || len(api.harnessCompletions) != 2 {
		t.Fatalf("settled controls were repeated: %v stops=%d forces=%d completions=%v", err, proc.stops, proc.forces, api.harnessCompletions)
	}
}
