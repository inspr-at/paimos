// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

type claimFaultAPI struct {
	*fakeAPI
	lock                        sync.Mutex
	server                      Run
	claimErr, getErr, reportErr error
	claimCommitted              bool
	routes, profiles            int
	claimIDs                    [][]string
	claimGeneration             string
	accepted                    map[int64]Telemetry
	reportGenerations           []string
	probes                      []string
}

func claimFixture(t *testing.T) (*Supervisor, *claimFaultAPI, *verificationFakeAdapter) {
	t.Helper()
	s, base, proc := testSupervisor(t)
	base.run.AccountID = "account"
	api := &claimFaultAPI{fakeAPI: base, server: base.run, accepted: map[int64]Telemetry{}}
	s.api = api
	adapter := &verificationFakeAdapter{fakeAdapter: fakeAdapter{proc: proc}}
	s.adapters[Codex] = adapter
	return s, api, adapter
}

func verificationClaim(api *claimFaultAPI) {
	no, duration := false, int64(60)
	api.run.Purpose, api.run.VerificationTask, api.run.VerificationPolicy = VerificationPurpose, VerificationTask, "read_only"
	api.run.RepositoryMutationAllowed, api.run.MaxDurationSeconds = &no, &duration
	api.server = api.run
}

func (a *claimFaultAPI) Profiles(context.Context) ([]Profile, error) {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.profiles++
	return []Profile{a.profile}, nil
}
func (a *claimFaultAPI) Route(ctx context.Context, id, daemon string, accounts []string, estimates map[string]int64) (Route, error) {
	a.lock.Lock()
	a.routes++
	a.lock.Unlock()
	return a.fakeAPI.Route(ctx, id, daemon, accounts, estimates)
}
func (a *claimFaultAPI) GetRun(context.Context, string) (Run, error) {
	a.lock.Lock()
	defer a.lock.Unlock()
	return a.server, a.getErr
}
func (a *claimFaultAPI) Queued(context.Context) ([]Run, error) {
	a.lock.Lock()
	defer a.lock.Unlock()
	if a.server.Status == "queued" {
		return []Run{a.server}, nil
	}
	return nil, nil
}
func (a *claimFaultAPI) Claim(_ context.Context, _, _, generation string, ids []string) error {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.claimIDs = append(a.claimIDs, append([]string(nil), ids...))
	if a.claimErr == nil || a.claimCommitted {
		a.server.Status = "starting"
		a.claimGeneration = generation
	}
	return a.claimErr
}
func (a *claimFaultAPI) ReportForClaim(_ context.Context, _, _, generation string, report Telemetry) error {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.reportGenerations = append(a.reportGenerations, generation)
	if generation != a.claimGeneration {
		return ErrGeneration
	}
	if previous, exists := a.accepted[report.Sequence]; exists && !reflect.DeepEqual(previous, report) {
		return ErrTelemetryProtocol
	}
	a.accepted[report.Sequence] = report
	if report.Status != "" {
		a.server.Status = report.Status
	}
	return a.reportErr
}
func (a *claimFaultAPI) Probe(_ context.Context, _, _, generation string, _ bool) error {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.probes = append(a.probes, generation)
	return nil
}

func TestClaimResponseLossSettlesNeverLaunchedExactlyOnce(t *testing.T) {
	s, api, adapter := claimFixture(t)
	verificationClaim(api)
	api.claimErr = errors.New("claim response lost")
	api.claimCommitted = true
	api.reportErr = errors.New("terminal response lost")
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("lost response hidden")
	}
	if adapter.starts != 0 || api.routes != 1 || len(api.claimIDs) != 1 || len(api.accepted) != 1 {
		t.Fatal("lost claim response launched or lost terminal settlement")
	}
	report := api.accepted[1]
	if report.Kind != "finished" || report.Status != "failed" || report.TurnCountDelta != 0 || report.InputTokensDelta != 0 {
		t.Fatal("never-launched run reported invented work")
	}
	if st := s.Lifecycle(""); st.State != "drained" || len(st.SettlementPendingRunIDs) != 1 {
		t.Fatal("no-launch proof confused with unsettled accounting")
	}
	api.reportErr = nil
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(api.accepted) != 1 || len(api.reportGenerations) != 2 || len(s.Lifecycle("").SettlementPendingRunIDs) != 0 || adapter.starts != 0 {
		t.Fatal("terminal replay was not exact or relaunched verification")
	}
}

func TestClaimRejectedQueuedRetriesSameReservation(t *testing.T) {
	s, api, adapter := claimFixture(t)
	verificationClaim(api)
	s.prepareScratch = func(string) (string, error) { return t.TempDir(), nil }
	api.claimErr = errors.New("claim rejected before commit")
	if err := s.PollOnce(t.Context()); err == nil {
		t.Fatal("rejected claim hidden")
	}
	if adapter.starts != 0 || len(api.accepted) != 0 || s.runs[api.run.ID].record.State != "claim_pending" {
		t.Fatal("queued rejection was treated as a claimed/started run")
	}
	api.claimErr = nil
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 1 || api.routes != 1 || len(api.claimIDs) != 2 || !reflect.DeepEqual(api.claimIDs[0], api.claimIDs[1]) {
		t.Fatal("claim retry routed/reserved again or skipped the same run")
	}
}

func TestClaimLookupOutageRecoversOutsideQueuedFeed(t *testing.T) {
	s, api, adapter := claimFixture(t)
	verificationClaim(api)
	api.claimErr, api.getErr = errors.New("claim response lost"), errors.New("offline")
	api.claimCommitted = true
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("uncertain claim hidden")
	}
	if len(api.accepted) != 0 || adapter.starts != 0 {
		t.Fatal("uncertain server state guessed")
	}
	api.getErr = nil
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if api.server.Status != "failed" || adapter.starts != 0 || len(api.claimIDs) != 1 {
		t.Fatal("claimed run disappeared from queue without recovery")
	}
}

func TestUnlaunchedClaimDoesNotBlockOtherEnrollmentVerification(t *testing.T) {
	s, api, adapter := claimFixture(t)
	verificationClaim(api)
	s.prepareScratch = func(string) (string, error) { return t.TempDir(), nil }
	s.accounts = append(s.accounts, EnrolledAccount{ID: "other-account", Key: "other-local", Harness: Codex})
	r := Record{LaunchState: launchPrepared, ExecutionMode: VerificationPurpose, AccountID: "other-account",
		TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "other-run", WorkOrderID: "other-order",
		Generation: "prior-generation", State: "claim_pending",
		ClaimRoute: &Route{AccountID: "other-account", Reservations: []Reservation{{ID: "other-reservation"}}}}
	if err := s.journal.Put(r); err != nil {
		t.Fatal(err)
	}
	s.runs[r.RunID] = &owned{record: r}
	if err := s.StartRun(t.Context(), api.run); err != nil || adapter.starts != 1 {
		t.Fatal("proven unlaunched claim starved another enrollment")
	}
}

func TestPostClaimPreparationFailureSettlesWithoutLaunch(t *testing.T) {
	for _, failure := range []string{"scratch", "ref", "lease_a", "lease_b"} {
		t.Run(failure, func(t *testing.T) {
			s, api, adapter := claimFixture(t)
			if failure == "scratch" {
				no, duration := false, int64(60)
				api.run.Purpose, api.run.VerificationTask, api.run.VerificationPolicy = VerificationPurpose, VerificationTask, "read_only"
				api.run.RepositoryMutationAllowed, api.run.MaxDurationSeconds = &no, &duration
				s.prepareScratch = func(string) (string, error) { return "", errors.New("scratch failed") }
			} else {
				failAt := map[string]int{"ref": 1, "lease_a": 2, "lease_b": 3}[failure]
				calls := 0
				s.newHarnessID = func() (string, error) {
					calls++
					if calls == failAt {
						return "", errors.New("random source failed")
					}
					return "test-reference", nil
				}
			}
			api.server = api.run
			if err := s.StartRun(t.Context(), api.run); err == nil {
				t.Fatal("preparation failure hidden")
			}
			if adapter.starts != 0 || api.server.Status != "failed" || len(api.accepted) != 1 || s.Lifecycle("").State != "drained" {
				t.Fatal("claimed run leaked a hold or invented a process")
			}
			if err := s.PollOnce(t.Context()); err != nil || len(api.claimIDs) != 1 {
				t.Fatal("failed preparation repeated claim")
			}
		})
	}
}

func restartClaimFixture(t *testing.T, s *Supervisor, api *claimFaultAPI, adapter Adapter) *Supervisor {
	t.Helper()
	// Simulate loss of the prior daemon without claiming its processes exited.
	_ = s.lock.Close()
	_ = s.state.Close()
	next, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: s.journalDir(), DaemonID: s.daemonID, Workspace: s.workspace, Adapters: []Adapter{adapter}, Accounts: s.accounts, EstimatedUnits: s.estimates})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.lock.Close(); _ = next.state.Close() })
	return next
}

func TestRestartRecoversOnlyDurablyUnlaunchedClaim(t *testing.T) {
	for _, tc := range []struct{ launch, state string }{
		{launchPrepared, "claim_pending"}, {launchAttempted, "starting"},
		{"", "starting"}, {"", "failed"}, {"", "completed"},
	} {
		t.Run(tc.launch+"_"+tc.state, func(t *testing.T) {
			s, api, adapter := claimFixture(t)
			oldGeneration := s.generation
			r := Record{LaunchState: tc.launch, AccountID: "account", TenantID: s.tenantID, PrincipalID: s.principalID, RunID: api.run.ID, WorkOrderID: api.run.WorkOrderID, Generation: oldGeneration, State: tc.state}
			if tc.launch == launchPrepared {
				r.ClaimRoute = &Route{AccountID: "account", AccountKey: "local", DaemonID: s.daemonID, Reservations: []Reservation{{ID: "reservation"}}}
			}
			if err := s.journal.Put(r); err != nil {
				t.Fatal(err)
			}
			api.server.Status, api.claimGeneration = "starting", oldGeneration
			next := restartClaimFixture(t, s, api, adapter)
			if err := next.PollOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if adapter.starts != 0 {
				t.Fatal("restart launched an uncertain run")
			}
			if tc.launch == launchPrepared {
				if api.server.Status != "failed" || len(api.reportGenerations) != 1 || api.reportGenerations[0] != oldGeneration || next.Lifecycle("").State != "drained" {
					t.Fatal("no-launch recovery lost the original claim binding")
				}
			} else if len(api.accepted) != 0 || next.Lifecycle("").State != "unconfirmed" {
				t.Fatal("legacy/launch-intent journal guessed that no child existed")
			}
		})
	}
}

type launchErrorAdapter struct {
	fakeAdapter
	s *Supervisor
	t *testing.T
}

func (a *launchErrorAdapter) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	if records := a.s.journal.Snapshot(); len(records) != 1 || records[0].LaunchState != launchAttempted {
		a.t.Fatal("adapter entered before durable launch intent")
	}
	return nil, errors.New("fork outcome unknown")
}

func TestLaunchErrorCannotProveNoFork(t *testing.T) {
	s, api, adapter := claimFixture(t)
	s.adapters[Codex] = &launchErrorAdapter{fakeAdapter: adapter.fakeAdapter, s: s, t: t}
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("launch error hidden")
	}
	if len(api.accepted) != 0 || api.server.Status != "starting" || s.Lifecycle("").State != "unconfirmed" {
		t.Fatal("unknown fork outcome falsely settled/freed the server run")
	}
}

func TestVerificationRefusalPersistsAndDoesNotPollAgain(t *testing.T) {
	s, api, adapter := claimFixture(t)
	api.run.Purpose = VerificationPurpose
	api.server = api.run
	s.adapters[Codex] = &fakeAdapter{proc: adapter.proc}
	if err := s.PollOnce(t.Context()); !errors.Is(err, ErrVerificationUnavailable) {
		t.Fatal("missing typed verification refusal")
	}
	if err := s.PollOnce(t.Context()); err != nil || api.profiles != 1 || api.routes != 0 || len(api.claimIDs) != 0 {
		t.Fatal("same refused verification retried or reserved")
	}
	// A later capability change never retries the refused immutable run ID.
	next := restartClaimFixture(t, s, api, adapter)
	if err := next.PollOnce(t.Context()); err != nil || api.profiles != 1 || adapter.starts != 0 || len(next.Lifecycle("").VerificationUnavailable) != 1 {
		t.Fatal("restart/capability change resurrected refused verification")
	}
}

func TestRestartQueuedClaimRemainsPendingUntilServerConfirms(t *testing.T) {
	s, api, adapter := claimFixture(t)
	api.claimErr = errors.New("claim response lost")
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("claim error hidden")
	}
	next := restartClaimFixture(t, s, api, adapter)
	if err := next.PollOnce(t.Context()); !errors.Is(err, errClaimUnconfirmed) {
		t.Fatal("old in-flight claim guessed safe to replace")
	}
	if len(api.probes) != 0 || len(api.claimIDs) != 1 || adapter.starts != 0 {
		t.Fatal("restart replaced generation or launched unresolved prior claim")
	}
	st := next.Lifecycle("")
	if st.State != "drained" || len(st.SettlementPendingRunIDs) != 1 {
		t.Fatal("durable no-fork proof confused with server settlement")
	}
	// Server owns expiry/cancellation of unclaimed queued verification.
	api.server.Status = "cancelled"
	if err := next.PollOnce(t.Context()); err != nil || len(next.Lifecycle("").SettlementPendingRunIDs) != 0 || len(api.probes) != 1 {
		t.Fatal("authoritative queued cancellation did not resolve claim uncertainty")
	}
}

func TestOfflineNoForkEvidenceRequiresExplicitMarker(t *testing.T) {
	for _, launch := range []string{launchPrepared, launchAttempted, ""} {
		t.Run("launch_"+launch, func(t *testing.T) {
			s, api, _ := claimFixture(t)
			r := Record{LaunchState: launch, AccountID: "account", TenantID: s.tenantID, PrincipalID: s.principalID, RunID: api.run.ID, WorkOrderID: api.run.WorkOrderID, Generation: s.generation, State: "starting"}
			if launch == launchPrepared {
				r.State = "claim_pending"
				r.ClaimRoute = &Route{AccountID: "account", Reservations: []Reservation{{ID: "reservation"}}}
			}
			if err := s.journal.Put(r); err != nil {
				t.Fatal(err)
			}
			if err := PersistFence(s.journalDir(), s.daemonID, ""); err != nil {
				t.Fatal(err)
			}
			_ = s.lock.Close()
			_ = s.state.Close()
			st, err := OfflineLifecycle(s.journalDir(), s.daemonID, s.tenantID, s.principalID, "")
			if err != nil {
				t.Fatal(err)
			}
			if launch == launchPrepared {
				if st.State != "drained" || len(st.SettlementPendingRunIDs) != 1 {
					t.Fatal("no-launch proof lost pending server accounting")
				}
			} else if st.State != "unconfirmed" {
				t.Fatal("missing PID incorrectly proved no fork")
			}
		})
	}
}
