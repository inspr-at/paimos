// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

var errLeadReplyLost = errors.New("fixture claim reply lost")

type leadFixtureAPI struct {
	*ledgerFixtureAPI
	demand       agentsetup.LeadDemand
	grant        agentsetup.RoutineGrant
	launch       *agentsetup.LeadLaunch
	claimCalls   int
	lost         bool
	windowClosed bool
	started      chan struct{}
	release      chan struct{}
}

func (a *leadFixtureAPI) LeadDemand(context.Context, string) (*agentsetup.LeadDemand, error) {
	return &a.demand, nil
}
func (a *leadFixtureAPI) ClaimLead(_ context.Context, in agentsetup.LeadPickup) (agentsetup.LeadLaunch, error) {
	a.claimCalls++
	if a.launch == nil {
		g := a.grant
		g.DaemonGeneration = in.DaemonGeneration
		g.LedgerGeneration = in.LedgerGeneration
		g.DaemonID = in.DaemonID
		launch := agentsetup.LeadLaunch{Demand: in.Demand, Grant: g, SessionID: uidLead("20"), Generation: in.Demand.Generation + 1, ProfileID: uidLead("21"), Harness: Codex, Model: "fixture", Effort: "high", Host: "fixture", MaxDurationSeconds: 60}
		a.launch = &launch
	}
	if a.started != nil {
		close(a.started)
		<-a.release
		a.started = nil
	}
	if a.lost {
		a.lost = false
		return agentsetup.LeadLaunch{}, errLeadReplyLost
	}
	return *a.launch, nil
}
func (a *leadFixtureAPI) ReportLead(_ context.Context, _ agentsetup.LeadPickup, kind string, _ *ownedprocess.Identity) (agentsetup.LeadLaunch, error) {
	if kind == "preflight" && a.windowClosed {
		return agentsetup.LeadLaunch{}, ErrDraining
	}
	return *a.launch, nil
}
func uidLead(s string) string { return "10000000-0000-4000-8000-0000000000" + s }

type leadFixtureAdapter struct {
	*fakeAdapter
	starts   atomic.Int32
	exit     chan struct{}
	exited   atomic.Bool
	fail     bool
	identity ownedprocess.Identity
	serviced atomic.Int32
}

func (*leadFixtureAdapter) RoutineExecutionSupported(agentsetup.RoutineGrant) bool { return true }
func (a *leadFixtureAdapter) LeadExitConfirmed(Process) bool                       { return a.exited.Load() }
func (a *leadFixtureAdapter) ServiceLead(context.Context, Process, *HarnessPause) error {
	a.serviced.Add(1)
	return nil
}
func (a *leadFixtureAdapter) StartLead(_ context.Context, in LeadStartRequest) (Process, error) {
	a.starts.Add(1)
	if a.fail {
		return nil, errors.New("fixture uncertain native start")
	}
	a.identity = ownedprocess.Identity{DaemonID: in.Launch.Grant.DaemonID, Generation: in.Launch.Grant.DaemonGeneration, ProcessID: strings.Repeat("a", 32), RootPID: 99, GroupID: 99, StartedAt: time.Unix(100, 0).UTC()}
	return &leadFixtureProcess{adapter: a}, nil
}

type leadFixtureProcess struct{ adapter *leadFixtureAdapter }

func (p *leadFixtureProcess) PID() int                                    { return 99 }
func (p *leadFixtureProcess) Wait() error                                 { <-p.adapter.exit; p.adapter.exited.Store(true); return nil }
func (*leadFixtureProcess) Control(context.Context, string, string) error { return nil }
func (*leadFixtureProcess) Stop(context.Context) error                    { return ErrUnsupported }
func (p *leadFixtureProcess) Ownership() (ownedprocess.Identity, error) {
	return p.adapter.identity, nil
}
func (*leadFixtureProcess) ForceStop(context.Context, ownedprocess.Identity, time.Time) error {
	return ErrUnsupported
}
func (p *leadFixtureProcess) RoutineFixturesExited() bool { return p.adapter.exited.Load() }

// Risks: duplicate/lost replies starting two native leads, an old generation
// relaunching a retained claim, or a window/turn completion releasing a slot.
// The claim barrier establishes concurrent pickup; Wait is released explicitly.
func TestLeadRuntimePersistsOneNativeAttemptAndRequiresObservedExit(t *testing.T) {
	for _, mode := range []string{"lost_claim", "window", "uncertain_start"} {
		t.Run(mode, func(t *testing.T) {
			s, a, _ := testSupervisor(t)
			g := routineTestGrant("11")
			now := time.Unix(200, 0).UTC()
			g.ExpiresAt = now.Add(time.Minute)
			s.tenantID, s.principalID = g.TenantID, g.PrincipalID
			s.capacityNow = func() time.Time { return now }
			s.accounts[0].ID = g.AccountID
			ledger, _ := enableLedgerFixture(t, s, a)
			api := &leadFixtureAPI{ledgerFixtureAPI: ledger, grant: g, demand: agentsetup.LeadDemand{ID: g.LaunchID, ProjectID: g.ProjectID, RoutineRunID: g.RoutineRunID, OwnerID: g.OwnerID, Revision: 1}}
			s.api = api
			adapter := &leadFixtureAdapter{fakeAdapter: &fakeAdapter{}, exit: make(chan struct{}), fail: mode == "uncertain_start"}
			s.adapters[Codex] = adapter
			root := filepath.Join(filepath.Dir(s.workspace), "lead-trees")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			repository := RoutineRepository{TenantID: g.TenantID, OwnerID: g.OwnerID, ProjectID: g.ProjectID, AccountID: g.AccountID, ComputerID: g.ComputerID, Repository: s.workspace, Root: root, BaseCommit: strings.Repeat("e", 40), DaemonDigest: g.DaemonDigest, CapabilityDigest: g.CapabilityDigest, HostMappingDigest: g.HostMappingDigest}
			tmp := t.TempDir()
			lease := &routineLeaseFixture{environment: []string{"AEON_RUN_ID=" + g.LaunchID, "AEON_RUN_DATABASE=aeon_run_" + strings.Repeat("a", 32), "AEON_RUN_APP_PORT=31001", "AEON_RUN_POSTGRES_PORT=31002", "AEON_RUN_TEMP_DIR=" + tmp, "TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp}}
			s.leads.config = &LeadRuntimeConfig{Repositories: []RoutineRepository{repository}, Prepare: func(_ context.Context, d agentsetup.LeadDemand, r RoutineRepository) (RoutineWorkspace, RoutineResourceLease, error) {
				id := LeadWorktreeID(d, r)
				path := filepath.Join(root, id)
				if err := os.Mkdir(path, 0700); err != nil {
					return RoutineWorkspace{}, nil, err
				}
				return RoutineWorkspace{ID: id, Path: path}, lease, nil
			}}
			var closeOnce sync.Once
			t.Cleanup(func() { closeOnce.Do(func() { close(adapter.exit) }) })
			if mode == "lost_claim" {
				api.lost = true
				api.started = make(chan struct{})
				api.release = make(chan struct{})
				first, second := make(chan error, 1), make(chan error, 1)
				go func() { first <- s.PollLeadRuntime(t.Context()) }()
				<-api.started
				go func() { second <- s.PollLeadRuntime(t.Context()) }()
				close(api.release)
				if err := <-first; !errors.Is(err, errLeadReplyLost) {
					t.Fatal(err)
				}
				if err := <-second; err != nil {
					t.Fatal(err)
				}
				if adapter.starts.Load() != 1 || api.claimCalls != 2 {
					t.Fatal("lost claim spawned duplicate", adapter.starts.Load(), api.claimCalls)
				}
				data, _, err := s.ledger.Snapshot()
				if err != nil || len(data.Groups) != 1 {
					t.Fatal("root not counted in shared dial", err, data)
				}
				if err := s.PollLeadRuntime(t.Context()); !errors.Is(err, ErrProcessesUnconfirmed) {
					t.Fatal("attempted pickup was replayed", err)
				}
				if adapter.starts.Load() != 1 || len(lease.finished) != 0 {
					t.Fatal("live lead released or replaced")
				}
				if adapter.serviced.Load() != 1 || a.harnessBeats != 1 || a.harnessBeatSessions[0].ID != api.launch.SessionID || a.harnessBeatSessions[0].Ownership == nil {
					t.Fatal("live lead did not service its exact owned session")
				}
				// A restart cannot infer absence from a PID or reclaim an older daemon.
				if err := checkRetainedLeads(s.state); !errors.Is(err, ErrProcessesUnconfirmed) {
					t.Fatal("restart accepted unknown exit", err)
				}
				local := s.leads.leads[g.LaunchID]
				if err := s.Close(t.Context()); !errors.Is(err, ErrDraining) {
					t.Fatal("shutdown accepted an active native lead", err)
				}
				closeOnce.Do(func() { close(adapter.exit) })
				select {
				case <-local.done:
				case <-t.Context().Done():
					t.Fatal("native Wait did not finish")
				}
				// Shutdown reconciles exit without discovery or another native start.
				if err := s.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				data, _, err = s.ledger.Snapshot()
				if err != nil || len(data.Groups) != 0 || len(lease.finished) != 1 || !lease.finished[0] {
					t.Fatal("confirmed exit did not release exact slot", data, lease.finished, err)
				}
				store, err := agentsetup.OpenStore(s.state.Path(), false)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if err := checkRetainedLeads(store); err != nil {
					t.Fatal(err)
				}
			} else {
				api.windowClosed = mode == "window"
				err := s.PollLeadRuntime(t.Context())
				if mode == "window" && !errors.Is(err, ErrDraining) || mode == "uncertain_start" && !errors.Is(err, ErrProcessesUnconfirmed) {
					t.Fatal(err)
				}
				expected := int32(0)
				if mode == "uncertain_start" {
					expected = 1
				}
				if adapter.starts.Load() != expected {
					t.Fatal("native call crossed admission barrier")
				}
				if mode == "uncertain_start" {
					if err = s.PollLeadRuntime(t.Context()); !errors.Is(err, ErrProcessesUnconfirmed) {
						t.Fatal("uncertain start was relaunched", err)
					}
				}
				raw, err := s.state.Read("lead-runtime-"+g.LaunchID+".json", 32768)
				if err != nil {
					t.Fatal(err)
				}
				var saved leadRecord
				if json.Unmarshal(raw, &saved) != nil || saved.ExitReported || saved.ExitObserved || saved.Attempted != (mode == "uncertain_start") {
					t.Fatal("lost durable state", saved)
				}
				// An old daemon cannot use an unattempted claim after restart either.
				previous := s.generation
				s.generation = "new-daemon-generation"
				if err = s.PollLeadRuntime(t.Context()); !errors.Is(err, ErrProcessesUnconfirmed) {
					t.Fatal("old claim crossed daemon generation", err)
				}
				s.generation = previous
				if adapter.starts.Load() != expected || len(lease.finished) != 0 {
					t.Fatal("unknown process released its owners")
				}
			}
		})
	}
}
