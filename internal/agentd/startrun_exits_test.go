// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/servicetier"
)

// These fixtures never invoke a model CLI. Faults occur at existing synchronous
// boundaries, so a refusal cannot accidentally pass by failing at an earlier step.
type startRunExitAPI struct {
	*claimFaultAPI
	node                                         Node
	order                                        WorkOrder
	route                                        Route
	profileErr, nodeErr, orderErr, routeErr      error
	projectErr, registerErr, tierErr             error
	heartbeatErr                                 error
	hostErr                                      error
	onNode, onRoute, onClaim, onRegister, onTier func()
	registrations, refusals                      int
	refusalReason                                string
	refusalErr                                   error
	routine                                      *routineReplayAPI
	onStop, onReport                             func(context.Context)
}

func (a *startRunExitAPI) Profiles(ctx context.Context) ([]Profile, error) {
	if a.profileErr != nil {
		return nil, a.profileErr
	}
	return a.claimFaultAPI.Profiles(ctx)
}
func (a *startRunExitAPI) HostCapacity(context.Context) (hostcapacity.View, error) {
	return hostcapacity.View{}, a.hostErr
}
func (a *startRunExitAPI) Node(context.Context, string) (Node, error) {
	if a.onNode != nil {
		a.onNode()
	}
	return a.node, a.nodeErr
}
func (a *startRunExitAPI) WorkOrder(context.Context, string) (WorkOrder, error) {
	return a.order, a.orderErr
}
func (a *startRunExitAPI) Route(ctx context.Context, id, daemon string, accounts []string, estimates map[string]int64) (Route, error) {
	_, _ = a.claimFaultAPI.Route(ctx, id, daemon, accounts, estimates)
	if a.onRoute != nil {
		a.onRoute()
	}
	return a.route, a.routeErr
}
func (a *startRunExitAPI) Claim(ctx context.Context, id, daemon, generation string, ids []string) error {
	err := a.claimFaultAPI.Claim(ctx, id, daemon, generation, ids)
	if a.onClaim != nil {
		a.onClaim()
	}
	return err
}
func (a *startRunExitAPI) ClaimHandoff(ctx context.Context, id, daemon, generation string, ids []string, _ WorkerPickup) error {
	return a.Claim(ctx, id, daemon, generation, ids)
}
func (a *startRunExitAPI) ProjectForNode(context.Context, string) (string, error) {
	return "project", a.projectErr
}
func (a *startRunExitAPI) RegisterHarness(ctx context.Context, session HarnessSession, principal, run, order, harness, host string, caps []string) (HarnessSession, error) {
	a.registrations++
	if a.registerErr != nil {
		return HarnessSession{}, a.registerErr
	}
	result, err := a.fakeAPI.RegisterHarness(ctx, session, principal, run, order, harness, host, caps)
	if a.onRegister != nil {
		a.onRegister()
	}
	return result, err
}
func (a *startRunExitAPI) ReportServiceTiers(context.Context, HarnessSession, []servicetier.Report, *string) error {
	if a.onTier != nil {
		a.onTier()
	}
	return a.tierErr
}
func (a *startRunExitAPI) RefuseVerification(_ context.Context, _, _, _, reason string) error {
	a.refusals++
	a.refusalReason = reason
	return a.refusalErr
}
func (a *startRunExitAPI) HeartbeatHarness(ctx context.Context, session HarnessSession, phase string) error {
	_ = a.fakeAPI.HeartbeatHarness(ctx, session, phase)
	return a.heartbeatErr
}
func (a *startRunExitAPI) StopHarness(ctx context.Context, session HarnessSession, reason string) error {
	if a.onStop != nil {
		a.onStop(ctx)
	}
	return a.fakeAPI.StopHarness(ctx, session, reason)
}
func (a *startRunExitAPI) ReportForClaim(ctx context.Context, run, daemon, generation string, report Telemetry) error {
	if a.onReport != nil {
		a.onReport(ctx)
	}
	return a.claimFaultAPI.ReportForClaim(ctx, run, daemon, generation, report)
}
func (a *startRunExitAPI) RouteRoutine(ctx context.Context, id, daemon string, accounts []string, estimates map[string]int64, pickup agentsetup.RoutinePickup) (Route, error) {
	a.routine.pickups = append(a.routine.pickups, pickup)
	return a.Route(ctx, id, daemon, accounts, estimates)
}
func (a *startRunExitAPI) ClaimRoutine(ctx context.Context, id, daemon, generation string, ids []string, _ *WorkerPickup, pickup agentsetup.RoutinePickup) error {
	a.routine.claims = append(a.routine.claims, pickup)
	return a.Claim(ctx, id, daemon, generation, ids)
}

// Only the tool-enabled variant implements RunToolAPI and its local signer.
type startRunExitToolAPI struct {
	*startRunExitAPI
	RunToolAPI
	credential string
}

func (a *startRunExitToolAPI) WorkOrder(ctx context.Context, id string) (WorkOrder, error) {
	return a.startRunExitAPI.WorkOrder(ctx, id)
}
func (a *startRunExitToolAPI) runCredential(string, string, string, string) string {
	return a.credential
}

type startRunExitAdapter struct {
	fakeAdapter
	harness      string
	verification bool
	managed      bool
	starts       int
	startErr     error
	request      StartRequest
	onStart      func(func(AdapterEvent))
	startProcess func(context.Context, StartRequest, func(AdapterEvent)) (Process, error)
}

func (a *startRunExitAdapter) Name() string                                         { return a.harness }
func (a *startRunExitAdapter) VerificationSupported() bool                          { return a.verification }
func (a *startRunExitAdapter) ManagedControlSupported() bool                        { return a.managed }
func (*startRunExitAdapter) RoutineExecutionSupported(agentsetup.RoutineGrant) bool { return true }
func (*startRunExitAdapter) RoutineToolsSupported(agentsetup.RoutineGrant) bool     { return true }
func (a *startRunExitAdapter) Start(ctx context.Context, request StartRequest, observe func(AdapterEvent)) (Process, error) {
	a.starts++
	a.request = request
	if a.onStart != nil {
		a.onStart(observe)
	}
	if a.startErr != nil {
		return nil, a.startErr
	}
	if a.startProcess != nil {
		return a.startProcess(ctx, request, observe)
	}
	return a.proc, nil
}

type startRunExitFixture struct {
	t       *testing.T
	s       *Supervisor
	api     *startRunExitAPI
	adapter *startRunExitAdapter
	process *fakeProcess
	power   *fakeRunPower
	ctx     context.Context
	run     Run
}

func newStartRunExitFixture(t *testing.T) *startRunExitFixture {
	t.Helper()
	s, base, process := testSupervisor(t)
	base.run.AccountID = "account"
	api := &startRunExitAPI{
		claimFaultAPI: &claimFaultAPI{fakeAPI: base, server: base.run, accepted: map[int64]Telemetry{}},
		node:          Node{ID: "order", Key: "TASK-1", Title: "Do work", Body: "Criteria"},
		order:         WorkOrder{NodeID: "order", Status: "ready"},
		route:         Route{AccountID: "account", AccountKey: "local", DaemonID: "daemon", Reservations: []Reservation{{ID: "reservation"}}},
	}
	adapter := &startRunExitAdapter{fakeAdapter: fakeAdapter{proc: process}, harness: Codex, verification: true}
	s.api, s.adapters[Codex] = api, adapter
	s.prepareScratch = func(string) (string, error) { return t.TempDir(), nil }
	f := &startRunExitFixture{t: t, s: s, api: api, adapter: adapter, process: process, ctx: t.Context(), run: base.run}
	f.power = &fakeRunPower{releaseDone: make(chan struct{}, 4)}
	s.power = f.power
	// Assertions run before fixture cleanup, so a leaked listener still fails
	// the test while a failed assertion cannot leave a server behind.
	t.Cleanup(func() {
		for _, entry := range s.runs {
			_ = entry.tools.Close()
		}
	})
	return f
}

func (f *startRunExitFixture) verification() {
	no, seconds := false, int64(60)
	f.run.Purpose, f.run.VerificationTask, f.run.VerificationPolicy = VerificationPurpose, VerificationTask, "read_only"
	f.run.RepositoryMutationAllowed, f.run.MaxDurationSeconds = &no, &seconds
}
func (f *startRunExitFixture) tools(credential string) {
	f.s.api = &startRunExitToolAPI{startRunExitAPI: f.api, RunToolAPI: &doneToolAPI{}, credential: credential}
}
func (f *startRunExitFixture) managed() {
	f.api.profile.Harness, f.s.accounts[0].Harness = Claude, Claude
	f.adapter.harness, f.adapter.managed = Claude, true
	f.s.adapters[Claude] = f.adapter
}
func (f *startRunExitFixture) saved(state, launch string) *owned {
	r := Record{RunID: f.run.ID, WorkOrderID: f.run.WorkOrderID, TenantID: f.s.tenantID, PrincipalID: f.s.principalID,
		Generation: f.s.generation, AccountID: "account", State: state, LaunchState: launch, ClaimRoute: &f.api.route}
	e := &owned{record: r}
	f.s.runs[f.run.ID] = e
	return e
}

type startRunExitWant struct {
	err                     error
	message                 string
	routes, claims, reports int
	registrations, stops    int
	refusals                int
	refusalReason           string
	starts                  int
	state, launch           string
	tools, chat             string // absent, open, closed
	checkout                bool
	resources, checkouts    int
}

// Risk: moving stage boundaries changes refusal precedence, duplicates claim
// settlement, releases disputed ownership, or changes resource cleanup. All
// assertions below preserve refusal precedence and enforce one cleanup owner.
// Pre-claim refusals have ZERO settlements: inventing one would change behavior.
// A claimed, proven-unlaunched exit has exactly ONE finished report. An unknown
// fork has ZERO final reports and retains its ownership fence.
func TestStartRunExits(t *testing.T) {
	fault := errors.New("fixture boundary failed")
	invalidJournal := "local journal record is invalid"
	claimed := func(message string) startRunExitWant {
		return startRunExitWant{message: message, routes: 1, claims: 1, reports: 1, state: "failed", launch: launchPrepared, checkout: true}
	}
	registered := func(message string) startRunExitWant {
		w := claimed(message)
		w.registrations, w.stops = 1, 1
		return w
	}
	tests := []struct {
		name  string
		setup func(*startRunExitFixture)
		want  startRunExitWant
	}{
		{"dispatch_lock_cancelled", func(f *startRunExitFixture) {
			ctx, cancel := context.WithCancel(f.ctx)
			cancel()
			f.ctx = ctx
		}, startRunExitWant{err: context.Canceled}},
		{"required_ledger_missing", func(f *startRunExitFixture) { f.s.ledgerRequired = true }, startRunExitWant{err: agentsetup.ErrLedgerUnavailable}},
		{"capacity_capture", func(f *startRunExitFixture) { f.s.capacityCapturing = true }, startRunExitWant{err: ErrDraining}},
		{"closing", func(f *startRunExitFixture) { f.s.closing = true }, startRunExitWant{err: ErrDraining}},
		{"account_fenced", func(f *startRunExitFixture) {
			if err := PersistFence(f.s.state.Path(), f.s.daemonID, "account"); err != nil {
				f.t.Fatal(err)
			}
		}, startRunExitWant{err: ErrDraining}},
		{"missing_run", func(f *startRunExitFixture) { f.run.ID = "" }, startRunExitWant{err: ErrScope}},
		{"missing_order", func(f *startRunExitFixture) { f.run.WorkOrderID = "" }, startRunExitWant{err: ErrScope}},
		{"wrong_principal", func(f *startRunExitFixture) { f.run.AgentPrincipalID = "other" }, startRunExitWant{err: ErrScope}},
		{"not_queued", func(f *startRunExitFixture) { f.run.Status = "starting" }, startRunExitWant{err: ErrScope}},
		{"malformed_routine_grant", func(f *startRunExitFixture) { f.run.Trace = json.RawMessage(`{`) }, startRunExitWant{err: ErrRoutineUnavailable}},
		{"existing_run", func(f *startRunExitFixture) { f.saved("completed", launchAttempted).record.ExitObserved = true }, startRunExitWant{state: "completed", launch: launchAttempted}},
		{"existing_refusal", func(f *startRunExitFixture) {
			f.saved("verification_unavailable", launchRefused).record.VerificationReason = "adapter_unsupported"
		}, startRunExitWant{state: "verification_unavailable", launch: launchRefused, refusals: 1, refusalReason: "adapter_unsupported"}},
		{"claim_recovery_offline", func(f *startRunExitFixture) { f.saved("claim_pending", launchPrepared); f.api.getErr = fault }, startRunExitWant{err: errClaimUnconfirmed, state: "claim_pending", launch: launchPrepared}},
		{"claim_recovery_settled", func(f *startRunExitFixture) {
			f.saved("claim_pending", launchPrepared)
			f.api.server.Status = "starting"
			f.api.claimGeneration = f.s.generation
		}, startRunExitWant{reports: 1, state: "failed", launch: launchPrepared}},
		{"claim_recovery_terminal", func(f *startRunExitFixture) {
			f.saved("claim_pending", launchPrepared)
			f.api.server.Status = "cancelled"
		}, startRunExitWant{state: "cancelled", launch: launchPrepared}},
		{"host_capacity", func(f *startRunExitFixture) { f.api.hostErr = fault }, startRunExitWant{err: fault}},
		{"ledger_reconciliation", func(f *startRunExitFixture) {
			_, config := enableLedgerFixture(f.t, f.s, f.api.fakeAPI)
			f.s.api = f.api
			if err := os.WriteFile(filepath.Join(config.Path, "ledger.json"), []byte(`{`), 0600); err != nil {
				f.t.Fatal(err)
			}
		}, startRunExitWant{err: agentsetup.ErrLedgerUnavailable}},
		{"profiles", func(f *startRunExitFixture) { f.api.profileErr = fault }, startRunExitWant{err: fault}},
		{"verification_missing_profile", func(f *startRunExitFixture) { f.verification(); f.api.profile.ID = "other" }, startRunExitWant{state: "verification_unavailable", launch: launchRefused, refusals: 1, refusalReason: "local_binding_missing"}},
		{"verification_unsupported_adapter", func(f *startRunExitFixture) { f.verification(); f.adapter.verification = false }, startRunExitWant{state: "verification_unavailable", launch: launchRefused, refusals: 1, refusalReason: "adapter_unsupported"}},
		{"verification_incomplete_binding", func(f *startRunExitFixture) { f.verification(); f.run.VerificationPolicy = "unsafe" }, startRunExitWant{state: "verification_unavailable", launch: launchRefused, refusals: 1, refusalReason: "binding_incomplete"}},
		{"verification_missing_enrollment", func(f *startRunExitFixture) { f.verification(); f.s.accounts = nil }, startRunExitWant{state: "verification_unavailable", launch: launchRefused, refusals: 1, refusalReason: "local_binding_missing"}},
		{"verification_refusal_journal", func(f *startRunExitFixture) { f.verification(); f.adapter.verification = false; f.s.journal = nil }, startRunExitWant{message: invalidJournal}},
		{"verification_refusal_report", func(f *startRunExitFixture) {
			f.verification()
			f.adapter.verification = false
			f.api.refusalErr = fault
		}, startRunExitWant{err: fault, state: "verification_unavailable", launch: launchRefused, refusals: 1, refusalReason: "adapter_unsupported"}},
		{"harness_hold", func(f *startRunExitFixture) { f.s.SetHarnessHold(Codex, "fixture") }, startRunExitWant{err: ErrDraining}},
		{"adapter_missing", func(f *startRunExitFixture) { delete(f.s.adapters, Codex) }, startRunExitWant{err: ErrUnsupported}},
		{"profile_missing", func(f *startRunExitFixture) { f.api.profile.ID = "other" }, startRunExitWant{err: ErrUnsupported}},
		{"execution_mode", func(f *startRunExitFixture) { f.run.Purpose = "unknown" }, startRunExitWant{err: ErrUnsupported}},
		{"account_unavailable", func(f *startRunExitFixture) { f.s.accounts = nil }, startRunExitWant{err: errAccountUnavailable}},
		{"requested_account_unavailable", func(f *startRunExitFixture) { f.run.AccountID = ""; f.run.RequestedAccountID = "other" }, startRunExitWant{err: errAccountUnavailable}},
		{"budget_enforcement", func(f *startRunExitFixture) { f.run.RoutineBudget = &RoutineProviderBudget{} }, startRunExitWant{err: ErrBudgetEnforcementUnavailable}},
		{"verification_busy", func(f *startRunExitFixture) {
			f.verification()
			f.s.runs["other"] = &owned{record: Record{RunID: "other", ExecutionMode: VerificationPurpose, State: "running", LaunchState: launchAttempted}}
		}, startRunExitWant{err: ErrDraining}},
		{"node_fetch", func(f *startRunExitFixture) { f.api.nodeErr = fault }, startRunExitWant{err: fault}},
		{"node_identity", func(f *startRunExitFixture) { f.api.node.ID = "other" }, startRunExitWant{message: "work order node unavailable"}},
		{"node_title", func(f *startRunExitFixture) { f.api.node.Title = " \t" }, startRunExitWant{message: "work order node unavailable"}},
		{"node_key", func(f *startRunExitFixture) { f.api.node.Key = "" }, startRunExitWant{message: "work order key unavailable"}},
		{"order_fetch", func(f *startRunExitFixture) { f.api.orderErr = fault }, startRunExitWant{err: fault}},
		{"order_not_dispatchable", func(f *startRunExitFixture) { f.api.order.Status = "done" }, startRunExitWant{message: "work order is not dispatchable"}},
		{"order_wrong_node", func(f *startRunExitFixture) { f.api.order.NodeID = "other" }, startRunExitWant{message: "work order is not dispatchable"}},
		{"review_mode_mismatch", func(f *startRunExitFixture) { f.api.order.Kind = "review" }, startRunExitWant{message: "review execution mode does not match its work order"}},
		{"assignment_pickup", func(f *startRunExitFixture) {
			f.run.Trace = json.RawMessage(`{"worker_assignment":{"run_id":"run","work_order_revision":1}}`)
		}, startRunExitWant{message: "assignment order revision changed"}},
		{"pickup_changed", func(f *startRunExitFixture) {
			f.saved("claim_pending", launchPrepared).record.WorkerPickup = &WorkerPickup{}
		}, startRunExitWant{message: "journaled assignment pickup changed", state: "claim_pending", launch: launchPrepared}},
		{"checkout_unconfirmed_writer", func(f *startRunExitFixture) {
			f.s.runs["other"] = &owned{record: Record{RunID: "other", State: "ownership_lost", LaunchState: launchAttempted}}
		}, startRunExitWant{message: "checkout already has an unconfirmed writer: " + ErrProcessesUnconfirmed.Error()}},
		{"order_duration", func(f *startRunExitFixture) { zero := int64(0); f.api.order.MaxDurationSeconds = &zero }, startRunExitWant{message: "work order duration invalid", checkout: true}},
		{"recovery_brief", func(f *startRunExitFixture) { f.run.RecoveryBrief = strings.Repeat("x", 20001) }, startRunExitWant{message: "recovery handover exceeds bound", checkout: true}},
		{"capacity_handoff", func(f *startRunExitFixture) { f.run.CapacityHandoff = true }, startRunExitWant{message: "capacity handoff needs the previous local attempt", checkout: true}},
		{"prompt_bound", func(f *startRunExitFixture) { f.api.node.Body = strings.Repeat("x", 256<<10) }, startRunExitWant{message: "work order prompt exceeds local bound", checkout: true}},
		{"missing_estimates", func(f *startRunExitFixture) { f.s.estimates = nil }, startRunExitWant{message: "allowance estimates required", checkout: true}},
		{"route_error", func(f *startRunExitFixture) { f.api.routeErr = fault }, startRunExitWant{err: fault, routes: 1, checkout: true}},
		{"route_wrong_daemon", func(f *startRunExitFixture) { f.api.route.DaemonID = "other" }, startRunExitWant{message: "account route is not bound to daemon", routes: 1, checkout: true}},
		{"route_empty_key", func(f *startRunExitFixture) { f.api.route.AccountKey = "" }, startRunExitWant{message: "account route is not bound to daemon", routes: 1, checkout: true}},
		{"route_no_reservations", func(f *startRunExitFixture) { f.api.route.Reservations = nil }, startRunExitWant{message: "account route is not bound to daemon", routes: 1, checkout: true}},
		{"route_wrong_account", func(f *startRunExitFixture) { f.api.route.AccountID = "other" }, startRunExitWant{message: "account route is not bound to daemon", routes: 1, checkout: true}},
		{"route_wrong_requested_account", func(f *startRunExitFixture) {
			f.run.AccountID = ""
			f.run.RequestedAccountID = "account"
			f.api.route.AccountID = "other"
		}, startRunExitWant{message: "account route is not bound to daemon", routes: 1, checkout: true}},
		{"route_no_local_enrollment", func(f *startRunExitFixture) { f.api.route.AccountKey = "other" }, startRunExitWant{message: "account route has no matching local enrollment", routes: 1, checkout: true}},
		{"route_empty_reservation", func(f *startRunExitFixture) { f.api.route.Reservations[0].ID = "" }, startRunExitWant{message: "empty account reservation", routes: 1, checkout: true}},
		{"route_account_became_unavailable", func(f *startRunExitFixture) { f.api.onRoute = func() { f.s.blockedAccounts["account"] = true } }, startRunExitWant{err: ErrDraining, routes: 1, checkout: true}},
		{"routed_execution_mode", func(f *startRunExitFixture) {
			f.verification()
			f.api.onRoute = func() { f.adapter.verification = false }
		}, startRunExitWant{err: ErrVerificationUnavailable, routes: 1}},
		{"new_claim_journal", func(f *startRunExitFixture) { f.api.onRoute = func() { f.s.journal = nil } }, startRunExitWant{message: invalidJournal, routes: 1, checkout: true}},
		{"retry_claim_journal", func(f *startRunExitFixture) {
			f.saved("claim_pending", launchPrepared).record.ClaimRoute = nil
			f.api.onRoute = func() { f.s.journal = nil }
		}, startRunExitWant{message: invalidJournal, routes: 1, state: "claim_pending", launch: launchPrepared, checkout: true}},
		{"claim_rejected_queued", func(f *startRunExitFixture) { f.api.claimErr = fault }, startRunExitWant{err: fault, routes: 1, claims: 1, state: "claim_pending", launch: launchPrepared, checkout: true}},
		{"claim_response_lost_committed", func(f *startRunExitFixture) { f.api.claimErr = fault; f.api.claimCommitted = true }, func() startRunExitWant { w := claimed(""); w.err = fault; return w }()},
		{"claim_response_and_lookup_lost", func(f *startRunExitFixture) {
			f.api.claimErr = fault
			f.api.claimCommitted = true
			f.api.getErr = fault
		}, startRunExitWant{message: fault.Error() + "\n" + errClaimUnconfirmed.Error(), routes: 1, claims: 1, state: "claim_pending", launch: launchPrepared, checkout: true}},
		{"review_prompt", func(f *startRunExitFixture) {
			no := false
			f.run.ReadOnlyReview, f.run.Purpose, f.run.RepositoryMutationAllowed = true, "managed", &no
			f.api.order.Kind = "review"
		}, func() startRunExitWant { w := claimed(errReviewContext.Error()); w.checkout = false; return w }()},
		{"scratch", func(f *startRunExitFixture) {
			f.verification()
			f.s.prepareScratch = func(string) (string, error) { return "", fault }
		}, func() startRunExitWant { w := claimed(""); w.err, w.checkout = fault, false; return w }()},
		{"project", func(f *startRunExitFixture) { f.api.projectErr = fault }, func() startRunExitWant { w := claimed(""); w.err = fault; return w }()},
		{"harness_registration", func(f *startRunExitFixture) { f.api.registerErr = fault }, func() startRunExitWant { w := registered(""); w.err, w.stops = fault, 0; return w }()},
		// Every successful registration is closed once on a pre-launch refusal.
		{"recovery_tier_invalid", func(f *startRunExitFixture) { f.run.RecoveryTier = "invalid" }, registered("invalid recovery service tier")},
		{"recovery_tier_unoffered", func(f *startRunExitFixture) { f.run.RecoveryTier = "fastest" }, registered("recovery service tier no longer offered")},
		{"tier_report", func(f *startRunExitFixture) { f.api.tierErr = fault }, func() startRunExitWant { w := registered(""); w.err = fault; return w }()},
		{"tools_start", func(f *startRunExitFixture) { f.tools("") }, func() startRunExitWant { w := registered("managed tools are not bound"); w.stops = 1; return w }()},
		{"managed_bound_tools_required", func(f *startRunExitFixture) { f.managed() }, func() startRunExitWant {
			w := registered("bound tools required for managed controls")
			w.stops = 1
			return w
		}()},
		{"managed_rules", func(f *startRunExitFixture) { f.managed(); f.tools("fixture-local-capability") }, func() startRunExitWant {
			w := registered("managed rules API unavailable")
			w.stops, w.tools = 1, "closed"
			return w
		}()},
		{"launch_intent_journal", func(f *startRunExitFixture) {
			f.tools("fixture-local-capability")
			f.api.onTier = func() { f.s.runs[f.run.ID].record.TenantID = "wrong-tenant" }
		}, func() startRunExitWant {
			w := registered(invalidJournal + "\n" + invalidJournal)
			w.reports, w.stops, w.tools, w.chat = 0, 1, "closed", "closed"
			return w
		}()},
		// Start errors close ephemeral resources but retain disputed checkout
		// ownership. No terminal report is safe without process-exit proof.
		{"start_unconfirmed", func(f *startRunExitFixture) { f.tools("fixture-local-capability"); f.adapter.startErr = fault }, startRunExitWant{err: fault, routes: 1, claims: 1, registrations: 1, stops: 1, starts: 1, state: "ownership_lost", launch: launchAttempted, tools: "closed", chat: "closed", checkout: true}},
		{"model_invalid_start", func(f *startRunExitFixture) { f.adapter.startErr = errModelInvalid }, startRunExitWant{err: errModelInvalid, routes: 1, claims: 1, registrations: 1, stops: 1, starts: 1, state: "ownership_lost", launch: launchAttempted, chat: "closed", checkout: true}},
	}
	for _, stage := range []int{1, 2, 3} {
		tests = append(tests, struct {
			name  string
			setup func(*startRunExitFixture)
			want  startRunExitWant
		}{
			name: []string{"harness_reference", "harness_lease_a", "harness_lease_b"}[stage-1],
			setup: func(f *startRunExitFixture) {
				calls := 0
				f.s.newHarnessID = func() (string, error) {
					calls++
					if calls == stage {
						return "", fault
					}
					return "fixture-reference", nil
				}
			},
			want: func() startRunExitWant { w := claimed(""); w.err = fault; return w }(),
		})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartRunExitFixture(t)
			tc.setup(f)
			// Server state starts with the same queued immutable approval terms.
			// Recovery cases explicitly supplied another status above.
			if f.api.server.Status == "queued" {
				f.api.server = f.run
			}
			err := f.s.StartRun(f.ctx, f.run)
			f.assert(err, tc.want)
		})
	}
}

// Risk: moving cleanup into one owner either loses settlement after request
// cancellation or creates a second terminal report after a lost response. The
// local harness must close once even while that exact report remains pending.
func TestStartRunCleanupRetainsSingleSettlementOnDeliveryFailure(t *testing.T) {
	f := newStartRunExitFixture(t)
	f.tools("fixture-local-capability")
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.ctx = ctx
	responseLost := errors.New("fixture settlement response lost")
	f.managed()
	f.api.reportErr = responseLost
	f.api.onTier = cancel
	f.api.onStop = func(ctx context.Context) {
		if ctx.Err() != nil {
			t.Fatal("cleanup inherited the cancelled request")
		}
	}
	f.api.onReport = f.api.onStop
	err := f.s.StartRun(f.ctx, f.run)
	// The tool API has no managed rules endpoint. Use its exact failure as
	// evidence that cancellation did not make this pass at an earlier step.
	if err == nil || !strings.Contains(err.Error(), "managed rules API unavailable") || !errors.Is(err, responseLost) {
		t.Fatalf("wrong cleanup outcome: %v", err)
	}
	f.assert(err, startRunExitWant{err: responseLost, routes: 1, claims: 1, reports: 1, registrations: 1, stops: 1,
		state: "failed", launch: launchPrepared, tools: "closed", checkout: true})
	entry := f.s.runs[f.run.ID]
	entry.mu.Lock()
	sequence, pending := entry.record.Sequence, append([]Telemetry(nil), entry.record.Pending...)
	entry.mu.Unlock()
	if sequence != 1 || len(pending) != 1 || !reflect.DeepEqual(pending[0], f.api.accepted[1]) {
		t.Fatal("cleanup did not retain its exact terminal report")
	}
	f.api.reportErr = nil
	if err := f.s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry.mu.Lock()
	sequence, pendingCount := entry.record.Sequence, len(entry.record.Pending)
	entry.mu.Unlock()
	if sequence != 1 || pendingCount != 0 || len(f.api.accepted) != 1 || len(f.api.reportGenerations) != 2 || len(f.api.harnessStops) != 1 || f.adapter.starts != 0 {
		t.Fatal("delivery retry repeated settlement, startup or harness cleanup")
	}
}

func (f *startRunExitFixture) assert(err error, want startRunExitWant) {
	f.t.Helper()
	if want.err != nil {
		if !errors.Is(err, want.err) {
			f.t.Fatalf("outcome: got %v, want %v", err, want.err)
		}
	} else if want.message != "" {
		if err == nil || err.Error() != want.message {
			f.t.Fatalf("outcome: got %v, want %q", err, want.message)
		}
	} else if err != nil {
		f.t.Fatalf("unexpected outcome: %v", err)
	}
	if f.api.routes != want.routes || len(f.api.claimIDs) != want.claims || f.adapter.starts != want.starts ||
		f.api.registrations != want.registrations || len(f.api.harnessStops) != want.stops || f.api.refusals != want.refusals || f.api.refusalReason != want.refusalReason {
		f.t.Fatalf("effects: routes=%d claims=%d starts=%d registrations=%d stops=%v refusals=%d/%s; want %+v",
			f.api.routes, len(f.api.claimIDs), f.adapter.starts, f.api.registrations, f.api.harnessStops, f.api.refusals, f.api.refusalReason, want)
	}
	if len(f.api.accepted) != want.reports || len(f.api.reportGenerations) != want.reports {
		f.t.Fatalf("settlement: accepted=%v attempts=%v; want exactly %d", f.api.accepted, f.api.reportGenerations, want.reports)
	}
	if want.reports == 1 {
		r, ok := f.api.accepted[1]
		proof := ""
		if entry := f.s.runs[f.run.ID]; entry != nil && (entry.record.WorkerPickup != nil || entry.record.RoutineWorkspace != nil) {
			proof = "not_attempted"
		}
		if !ok || r.Sequence != 1 || r.Kind != "finished" || r.Status != "failed" || r.ProcessState != proof ||
			r.ErrorCode != "child_exit_failed" || r.TurnCountDelta != 0 || r.InputTokensDelta != 0 || r.OutputTokensDelta != 0 {
			f.t.Fatalf("not exactly one honest no-launch settlement: %+v", r)
		}
	}
	entry := f.s.runs[f.run.ID]
	if want.state == "" {
		if entry != nil {
			f.t.Fatalf("unexpected run ownership: %+v", entry.record)
		}
	} else {
		if entry == nil {
			f.t.Fatal("missing run ownership")
		}
		entry.mu.Lock()
		r, tools, chat := entry.record, entry.tools, entry.chat
		entry.mu.Unlock()
		if r.State != want.state || r.LaunchState != want.launch {
			f.t.Fatalf("ownership: %s/%s, want %s/%s", r.State, r.LaunchState, want.state, want.launch)
		}
		toolsState := "absent"
		if tools != nil {
			assertManagedToolsClosed(f.t, tools)
			toolsState = "closed"
		}
		if want.tools == "" {
			want.tools = "absent"
		}
		if toolsState != want.tools {
			f.t.Fatalf("tools: %s, want %s", toolsState, want.tools)
		}
		chatState := "absent"
		if chat != nil {
			chat.mu.Lock()
			if chat.closed {
				chatState = "closed"
			} else {
				chatState = "open"
			}
			chat.mu.Unlock()
		}
		if want.chat == "" {
			want.chat = "absent"
		}
		if chatState != want.chat {
			f.t.Fatalf("chat: %s, want %s", chatState, want.chat)
		}
	}
	if (f.s.checkout != nil) != want.checkout {
		f.t.Fatalf("checkout retained=%v, want %v", f.s.checkout != nil, want.checkout)
	}
	if len(f.s.routineResources) != want.resources || len(f.s.routineCheckouts) != want.checkouts {
		f.t.Fatalf("routine ownership: resources=%d checkouts=%d, want %d/%d", len(f.s.routineResources), len(f.s.routineCheckouts), want.resources, want.checkouts)
	}
	f.power.check(f.t, want.starts, want.starts)
}

func newStartRunRoutineExitFixture(t *testing.T) (*startRunExitFixture, RoutineRepository) {
	t.Helper()
	s, routine, process, repository, _ := routineSupervisorFixture(t)
	api := &startRunExitAPI{
		claimFaultAPI: &claimFaultAPI{fakeAPI: routine.fakeAPI, server: routine.run, accepted: map[int64]Telemetry{}},
		node:          Node{ID: "order", Key: "TASK-1", Title: "Do work", Body: "Criteria"},
		order:         WorkOrder{NodeID: "order", Status: "ready"}, routine: routine,
		route: Route{AccountID: routine.grant.AccountID, AccountKey: "local", DaemonID: s.daemonID, Reservations: []Reservation{{ID: "reservation"}}},
	}
	adapter := &startRunExitAdapter{fakeAdapter: fakeAdapter{proc: process}, harness: Codex, verification: true}
	s.api, s.adapters[Codex] = api, adapter
	f := &startRunExitFixture{t: t, s: s, api: api, adapter: adapter, process: process, ctx: t.Context(), run: routine.run}
	f.power = &fakeRunPower{releaseDone: make(chan struct{}, 4)}
	s.power = f.power
	t.Cleanup(func() {
		for _, entry := range s.runs {
			_ = entry.tools.Close()
		}
	})
	return f, repository
}

func (f *startRunExitFixture) continuation(repository RoutineRepository) *owned {
	f.t.Helper()
	old, grant := f.run, f.api.routine.grant
	w, err := f.s.prepareRoutineWorkspace(f.ctx, old, grant, repository)
	if err != nil {
		f.t.Fatal(err)
	}
	record := Record{RoutineWorkspace: w, TenantID: f.s.tenantID, PrincipalID: f.s.principalID, RunID: old.ID, WorkOrderID: old.WorkOrderID,
		Generation: f.s.generation, Workspace: w.Path, LaunchBranch: w.Branch, AccountID: grant.AccountID, LaunchState: launchAttempted, State: "failed", ExitObserved: true}
	prior := &owned{record: record, process: &routineExitedFixture{fakeProcess: &fakeProcess{}, fixturesExited: true}}
	f.s.runs[old.ID] = prior
	if err := f.s.journal.Put(record); err != nil {
		f.t.Fatal(err)
	}
	grant.LaunchID, grant.AgentRunID = routineTestGrant("22").LaunchID, routineTestGrant("22").AgentRunID
	f.run.ID, f.run.RetryOfRunID, f.run.CapacityHandoff = grant.AgentRunID, old.ID, true
	f.grant(grant)
	return prior
}

func (f *startRunExitFixture) fixContinuation(repository RoutineRepository, findings bool) {
	f.t.Helper()
	g, err := routineGrant(f.run)
	if err != nil {
		f.t.Fatal(err)
	}
	g.Lineage = &agentsetup.RoutineLineage{Role: "fix", PreviousRunID: f.run.RetryOfRunID, ReviewOrderID: g.LaunchID,
		Repository: "example/routine", BaseSHA: strings.Repeat("a", 40), HeadSHA: repository.BaseCommit, PolicyDigest: strings.Repeat("a", 64)}
	f.grant(*g)
	if findings {
		var trace map[string]json.RawMessage
		if err := json.Unmarshal(f.run.Trace, &trace); err != nil {
			f.t.Fatal(err)
		}
		trace["routine_fix_findings"] = json.RawMessage(`[{"file":"fixture.go","message":"fixture finding"}]`)
		f.run.Trace, err = json.Marshal(trace)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	f.budget()
}

func TestStartRunFixFindingsExit(t *testing.T) {
	f, repository := newStartRunRoutineExitFixture(t)
	f.continuation(repository)
	f.fixContinuation(repository, false)
	f.api.server = f.run
	f.assert(f.s.StartRun(f.ctx, f.run), startRunExitWant{err: ErrRoutineUnavailable, resources: 1, checkouts: 1})
}

// Risk: a continuation can lose its stopped-process/checkout proof while
// registration is in flight. Close ephemeral resources and retain the fence.
func TestStartRunContinuationExits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*startRunExitFixture, *owned)
		message string
	}{
		{"prior_exit_proof_changed", func(_ *startRunExitFixture, prior *owned) { prior.record.ExitObserved = false }, ErrProcessesUnconfirmed.Error()},
		{"retained_checkout_identity_changed", func(f *startRunExitFixture, _ *owned) {
			f.s.runs[f.run.ID].record.RoutineWorkspace.ID = strings.Repeat("a", 64)
		}, ErrProcessesUnconfirmed.Error()},
		{"capacity_handoff_revalidation", func(_ *startRunExitFixture, prior *owned) { prior.record.State = "completed" }, "capacity handoff waits for the original workspace, branch and stopped process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, repository := newStartRunRoutineExitFixture(t)
			prior := f.continuation(repository)
			if tc.name == "capacity_handoff_revalidation" {
				f.fixContinuation(repository, true)
			}
			f.api.onTier = func() { tc.mutate(f, prior) }
			f.api.server = f.run
			err := f.s.StartRun(f.ctx, f.run)
			f.assert(err, startRunExitWant{message: tc.message, routes: 1, claims: 1, reports: 1, registrations: 1, stops: 1,
				state: "failed", launch: launchPrepared, resources: 1, checkouts: 1, chat: "closed"})
		})
	}
}

// Risk: the orchestration hand-off must retain live owners until the monitor
// observes exit, even when initial persistence/telemetry/heartbeat fails.
func TestStartRunLaunchHandoff(t *testing.T) {
	fault := errors.New("fixture post-launch boundary failed")
	for _, tc := range []struct {
		name                        string
		setup                       func(*startRunExitFixture)
		err                         error
		message                     string
		started, archived, protocol bool
	}{
		{"running", func(*startRunExitFixture) {}, nil, "", true, false, false},
		{"running_journal", func(f *startRunExitFixture) {
			f.adapter.onStart = func(func(AdapterEvent)) { f.s.runs[f.run.ID].record.TenantID = "wrong-tenant" }
		}, nil, "local journal record is invalid", false, false, false},
		{"started_report", func(f *startRunExitFixture) { f.api.reportErr = fault }, fault, "", true, false, false},
		{"initial_heartbeat", func(f *startRunExitFixture) { f.api.heartbeatErr = fault }, fault, "", true, false, false},
		{"harness_archived", func(f *startRunExitFixture) { f.api.heartbeatErr = ErrHarnessArchived }, nil, "", true, true, false},
		{"synchronous_protocol_failure", func(f *startRunExitFixture) {
			f.adapter.onStart = func(func(AdapterEvent)) { f.s.runs[f.run.ID].protocolFailed = true }
		}, ErrTelemetryProtocol, "", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartRunExitFixture(t)
			f.s.heartbeatInterval = time.Hour
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			f.ctx = ctx
			f.tools("fixture-local-capability")
			finish := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(finish) }) }
			t.Cleanup(release)
			f.process.onExit = func() { <-finish }
			tc.setup(f)
			err := f.s.StartRun(f.ctx, f.run)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("outcome: %v, want %v", err, tc.err)
				}
			} else if tc.message != "" {
				if err == nil || err.Error() != tc.message {
					t.Fatalf("outcome: %v, want %s", err, tc.message)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			entry := f.s.runs[f.run.ID]
			entry.mu.Lock()
			r, process, done, archived := entry.record, entry.process, entry.monitorDone, entry.harnessArchived
			entry.mu.Unlock()
			if process != f.process || done == nil || r.State != "running" || r.LaunchState != launchAttempted || archived != tc.archived || r.ExitObserved {
				t.Fatalf("monitor hand-off lost ownership: %+v archived=%v", r, archived)
			}
			if f.adapter.starts != 1 || f.api.registrations != 1 || len(f.api.claimIDs) != 1 || len(f.api.harnessStops) != 0 {
				t.Fatal("start repeated or monitor-owned harness already closed")
			}
			f.api.lock.Lock()
			started := f.api.accepted[1]
			count := len(f.api.accepted)
			f.api.reportErr = nil
			f.api.lock.Unlock()
			if tc.started && (count != 1 || started.Kind != "started" || started.Status != "running") || !tc.started && count != 0 {
				t.Fatalf("started telemetry: %+v count=%d", started, count)
			}
			if !reflect.DeepEqual(f.adapter.request.Run, f.run) || f.adapter.request.Workspace != f.s.workspace || f.adapter.request.AccountKey != "local" ||
				f.adapter.request.Profile.ID != "profile" || f.adapter.request.Tools == nil || !strings.HasPrefix(f.adapter.request.Prompt, "Do work\n\nCriteria\n\nAcceptance criteria") {
				t.Fatal("stage hand-off changed the adapter's bound request")
			}
			f.power.check(t, 1, 0)
			// Repair only the injected fixture failure, then let the same owned
			// process exit. A channel establishes the hand-off before cleanup.
			entry.mu.Lock()
			entry.record.TenantID = f.s.tenantID
			entry.mu.Unlock()
			_ = f.process.Stop(f.ctx)
			release()
			select {
			case <-done:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			f.api.lock.Lock()
			finals := []Telemetry{}
			for _, report := range f.api.accepted {
				if report.Kind == "finished" {
					finals = append(finals, report)
				}
			}
			f.api.lock.Unlock()
			status, code := "completed", ""
			if tc.protocol {
				status, code = "failed", "reporter_unavailable"
			}
			if len(finals) != 1 || finals[0].Status != status || finals[0].ErrorCode != code {
				t.Fatalf("exactly one final settlement: %+v", finals)
			}
			f.api.mu.Lock()
			stops := append([]string(nil), f.api.harnessStops...)
			f.api.mu.Unlock()
			wantStops := 1
			if tc.archived {
				wantStops = 0
			}
			if len(stops) != wantStops {
				t.Fatalf("monitor cleanup: stops=%v, want %d", stops, wantStops)
			}
			entry.chat.mu.Lock()
			closed := entry.chat.closed
			entry.chat.mu.Unlock()
			if !closed {
				t.Fatal("monitor left chat open")
			}
			assertManagedToolsClosed(t, entry.tools)
			f.power.check(t, 1, 1)
		})
	}
}

func (f *startRunExitFixture) grant(g agentsetup.RoutineGrant) {
	f.t.Helper()
	f.run.RoutineLaunchID = &g.LaunchID
	raw, err := json.Marshal(map[string]any{"routine_launch": g, "routine_run_id": g.RoutineRunID})
	if err != nil {
		f.t.Fatal(err)
	}
	f.run.Trace = raw
}

type startRunExitBudgetAPI struct {
	*startRunExitAPI
	RoutineBudgetBroker
}
type startRunExitBudgetAdapter struct {
	*startRunExitAdapter
	qualification RoutineBudgetQualification
}

func (a *startRunExitBudgetAdapter) RoutineBudgetQualification(Profile) (RoutineBudgetQualification, error) {
	return a.qualification, nil
}
func (a *startRunExitBudgetAdapter) StartBudgeted(ctx context.Context, request StartRequest, _ *RoutineCallBudget, observe func(AdapterEvent)) (Process, error) {
	return a.Start(ctx, request, observe)
}

func (f *startRunExitFixture) budget() *startRunExitBudgetAdapter {
	f.t.Helper()
	_, broker, request, adapter := routineBudgetFixture(f.t, "both", 100, 100)
	g := *request.Run.RoutineBudget
	g.TenantID, g.PrincipalID, g.Generation = f.s.tenantID, f.s.principalID, f.s.generation
	g.Grant.AgentRunID, g.Grant.AccountID = f.run.ID, f.run.requestedAccount()
	// One injected clock supplies every expiry comparison in this fixture.
	now := f.s.capacityNow()
	f.s.capacityNow = func() time.Time { return now }
	g.ExpiresAt = now.Add(time.Hour)
	f.run.RoutineBudget, f.api.profile.Model = &g, g.Price.Model
	f.s.api = &startRunExitBudgetAPI{startRunExitAPI: f.api, RoutineBudgetBroker: broker}
	a := &startRunExitBudgetAdapter{startRunExitAdapter: f.adapter, qualification: adapter.qualification}
	f.s.adapters[Codex] = a
	f.api.route.BillingMode = "api"
	return a
}

func TestStartRunBudgetExits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*startRunExitFixture)
		want  startRunExitWant
	}{
		{"broker_missing", func(f *startRunExitFixture) { f.budget(); f.s.api = f.api }, startRunExitWant{err: ErrBudgetEnforcementUnavailable}},
		{"qualification", func(f *startRunExitFixture) { a := f.budget(); a.qualification.Tokens = false }, startRunExitWant{err: ErrBudgetEnforcementUnavailable}},
		{"budget_binding", func(f *startRunExitFixture) { f.budget(); f.run.RoutineBudget.Generation = "other" }, startRunExitWant{err: ErrRoutineBudgetBinding}},
		{"route_budget_binding", func(f *startRunExitFixture) { f.budget(); f.api.route.BillingMode = "subscription" }, startRunExitWant{err: ErrRoutineBudgetBinding, routes: 1, checkout: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartRunExitFixture(t)
			tc.setup(f)
			f.api.server = f.run
			f.assert(f.s.StartRun(f.ctx, f.run), tc.want)
		})
	}
}

// Risk: refusal after allocation must retain the exact disputed routine lease,
// checkout and ledger group. In particular, an error is not proof of release.
func TestStartRunRoutineExits(t *testing.T) {
	fault := errors.New("fixture routine boundary failed")
	allocated := func() startRunExitWant { return startRunExitWant{resources: 1, checkouts: 1} }
	claimed := func() startRunExitWant {
		w := allocated()
		w.routes, w.claims, w.reports, w.registrations, w.stops = 1, 1, 1, 1, 1
		w.state, w.launch, w.chat, w.tools = "failed", launchPrepared, "closed", "closed"
		return w
	}
	tests := []struct {
		name  string
		setup func(*startRunExitFixture, RoutineRepository)
		want  startRunExitWant
	}{
		{"routine_route_pending", func(f *startRunExitFixture, _ RoutineRepository) { f.saved("route_pending", launchRoutePending) }, startRunExitWant{err: ErrProcessesUnconfirmed, state: "route_pending", launch: launchRoutePending}},
		{"routine_ownership_lost", func(f *startRunExitFixture, _ RoutineRepository) { f.saved("ownership_lost", launchAttempted) }, startRunExitWant{err: ErrProcessesUnconfirmed, state: "ownership_lost", launch: launchAttempted}},
		{"routine_requires_ledger", func(f *startRunExitFixture, _ RoutineRepository) {
			ledger := f.s.ledger
			f.s.ledger, f.s.ledgerRequired = nil, false
			f.t.Cleanup(func() { f.s.ledger = ledger })
		}, startRunExitWant{err: ErrRoutineUnavailable}},
		{"routine_repository_qualification", func(f *startRunExitFixture, _ RoutineRepository) { f.s.routineConfig = nil }, startRunExitWant{err: ErrRoutineUnavailable}},
		{"routine_lineage_budget_required", func(f *startRunExitFixture, r RoutineRepository) {
			g := f.api.routine.grant
			g.Lineage = &agentsetup.RoutineLineage{Role: "fix", PreviousRunID: routineTestGrant("22").AgentRunID, ReviewOrderID: g.LaunchID, Repository: "example/routine", BaseSHA: r.BaseCommit, HeadSHA: strings.Repeat("a", 40), PolicyDigest: strings.Repeat("a", 64)}
			f.grant(g)
		}, startRunExitWant{err: ErrBudgetEnforcementUnavailable}},
		{"routine_resource_acquisition", func(f *startRunExitFixture, _ RoutineRepository) {
			f.s.routineConfig.AcquireResources = func(context.Context, RoutineResourceOwner) (RoutineResourceLease, error) { return nil, fault }
		}, startRunExitWant{err: fault}},
		{"routine_environment", func(f *startRunExitFixture, _ RoutineRepository) {
			acquire := f.s.routineConfig.AcquireResources
			f.s.routineConfig.AcquireResources = func(ctx context.Context, owner RoutineResourceOwner) (RoutineResourceLease, error) {
				lease, err := acquire(ctx, owner)
				if err == nil {
					lease.(*routineLeaseFixture).environment = nil
				}
				return lease, err
			}
		}, func() startRunExitWant { w := allocated(); w.err = ErrRoutineUnavailable; return w }()},
		{"ledger_acquire", func(f *startRunExitFixture, _ RoutineRepository) {
			f.api.onNode = func() { f.s.ledgerBinding.Generation = "00000000-0000-4000-8000-000000000001" }
		}, func() startRunExitWant { w := allocated(); w.err = agentsetup.ErrLedgerGeneration; return w }()},
		{"ledger_route_intent_journal", func(f *startRunExitFixture, _ RoutineRepository) {
			f.s.ledgerBarrier = func(stage string) {
				if stage == "group" {
					f.s.journal = nil
				}
			}
		}, func() startRunExitWant { w := allocated(); w.message = "local journal record is invalid"; return w }()},
		{"routine_route", func(f *startRunExitFixture, _ RoutineRepository) { f.api.routeErr = fault }, func() startRunExitWant {
			w := allocated()
			w.err, w.routes, w.state, w.launch = fault, 1, "route_pending", launchRoutePending
			return w
		}()},
		{"ledger_route_binding", func(f *startRunExitFixture, _ RoutineRepository) { f.api.route.AccountID = "unoffered" }, func() startRunExitWant {
			w := allocated()
			w.err, w.routes, w.state, w.launch = ErrScope, 1, "route_pending", launchRoutePending
			return w
		}()},
		// These post-registration refusals settle once and close the harness,
		// tools and chat while retaining the disputed resource lease.
		{"routine_checkout_revalidation", func(f *startRunExitFixture, _ RoutineRepository) {
			f.tools("fixture-local-capability")
			f.api.onTier = func() {
				w := f.s.runs[f.run.ID].record.RoutineWorkspace
				if _, err := routineGit(f.ctx, w.Path, "checkout", "--detach"); err != nil {
					f.t.Fatal(err)
				}
			}
		}, func() startRunExitWant { w := claimed(); w.err = ErrProcessesUnconfirmed; return w }()},
		{"routine_second_claim", func(f *startRunExitFixture, _ RoutineRepository) {
			f.tools("fixture-local-capability")
			f.api.onTier = func() { f.api.claimErr = fault }
		}, func() startRunExitWant { w := claimed(); w.err, w.claims = fault, 2; return w }()},
		{"routine_second_environment", func(f *startRunExitFixture, _ RoutineRepository) {
			f.tools("fixture-local-capability")
			f.api.onClaim = func() {
				if len(f.api.claimIDs) == 2 {
					f.s.routineResources[f.run.ID].(*routineLeaseFixture).environment = nil
				}
			}
		}, func() startRunExitWant { w := claimed(); w.err, w.claims = ErrRoutineUnavailable, 2; return w }()},
		{"ledger_launching", func(f *startRunExitFixture, _ RoutineRepository) {
			f.tools("fixture-local-capability")
			f.api.onTier = func() { f.s.runs[f.run.ID].record.LedgerGeneration = "00000000-0000-4000-8000-000000000001" }
		}, func() startRunExitWant { w := claimed(); w.err, w.claims = agentsetup.ErrLedgerGeneration, 2; return w }()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, repository := newStartRunRoutineExitFixture(t)
			tc.setup(f, repository)
			f.api.server = f.run
			err := f.s.StartRun(f.ctx, f.run)
			f.assert(err, tc.want)
			for _, lease := range f.s.routineResources {
				if l, ok := lease.(*routineLeaseFixture); ok && (l.closed || len(l.finished) != 0) {
					t.Fatalf("refusal released disputed resource lease: %+v", l)
				}
			}
		})
	}
}
