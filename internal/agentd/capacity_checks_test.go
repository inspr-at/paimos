// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/openrouter"
)

type checkClock struct {
	sync.Mutex
	at time.Time
}

func (c *checkClock) Now() time.Time      { c.Lock(); defer c.Unlock(); return c.at }
func (c *checkClock) Add(d time.Duration) { c.Lock(); c.at = c.at.Add(d); c.Unlock() }

type checkFixtureAPI struct {
	API
	mu           sync.Mutex
	account      CapacityCheckAccount
	events       []string
	reports      []CapacityCheckReport
	statuses     []ProbeStatus
	lost         bool
	committed    bool
	heartbeatErr error
}

func (a *checkFixtureAPI) PollCapacityChecks(context.Context) ([]CapacityCheckAccount, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, "poll")
	if a.account.ID == "" {
		return nil, nil
	}
	raw, _ := json.Marshal(a.account)
	var copy CapacityCheckAccount
	_ = json.Unmarshal(raw, &copy)
	return []CapacityCheckAccount{copy}, nil
}
func (a *checkFixtureAPI) Probe(_ context.Context, _, _, generation string, available bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, "heartbeat")
	if !available {
		return errors.New("heartbeat unexpectedly unhealthy")
	}
	if a.heartbeatErr != nil {
		return a.heartbeatErr
	}
	if c := a.account.PendingCheck; c != nil && c.DaemonGeneration != nil && *c.DaemonGeneration != generation {
		a.account.PendingCheck = nil
	}
	return nil
}
func (a *checkFixtureAPI) ReportCapacityCheck(_ context.Context, account, daemon, generation string, status ProbeStatus, report CapacityCheckReport) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if account != a.account.ID || daemon != a.account.DaemonID || report.BindingRevision != a.account.LinkRevision {
		return ErrScope
	}
	a.events = append(a.events, "report")
	a.reports = append(a.reports, report)
	a.statuses = append(a.statuses, status)
	if a.committed && report.CheckID != "" {
		a.account.PendingCheck = nil
	}
	if a.lost {
		return errors.New("synthetic lost response")
	}
	if report.CheckID != "" {
		a.account.PendingCheck = nil
	}
	return nil
}
func (a *checkFixtureAPI) request(id, generation string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.account.PendingCheck = &CapacityCheckRequest{ID: id, AccountID: a.account.ID, BindingRevision: a.account.LinkRevision, DaemonGeneration: &generation, State: "pending", ExpiresAt: now.Add(5 * time.Minute)}
}

type checkFixtureAdapter struct {
	*fakeAdapter
	mu         sync.Mutex
	clock      *checkClock
	result     string
	calls      int
	resetAfter time.Duration
	entered    chan struct{}
	release    chan struct{}
	cleanup    bool
}

func (a *checkFixtureAdapter) CaptureCapacityResult(ctx context.Context, key string) CapacityCapture {
	a.mu.Lock()
	a.calls++
	result := a.result
	a.mu.Unlock()
	if a.entered != nil {
		close(a.entered)
		select {
		case <-a.release:
		case <-ctx.Done():
			return captureError(ctx)
		}
	}
	c := CapacityCapture{Result: result, CleanupUnconfirmed: a.cleanup}
	if result == "success" {
		now := a.clock.Now()
		c.Readings = []capacity.Reading{{ReadAt: now, ResetsAt: now.Add(a.resetAfter), WindowKind: "5h", WindowMinutes: 300, UsedPercent: 17, Source: "agentd"}}
	}
	return c
}
func checkFixture(t *testing.T) (*Supervisor, *checkFixtureAPI, *checkFixtureAdapter, *checkClock) {
	t.Helper()
	s, base, _ := testSupervisor(t)
	clock := &checkClock{at: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	api := &checkFixtureAPI{API: base, account: CapacityCheckAccount{ID: "account", AccountKey: "local", Harness: Codex, DaemonID: "daemon", OngoingUseApproved: true, State: "available", Resources: []CapacityResource{{ID: "resource", Kind: "subscription_quota"}}}}
	adapter := &checkFixtureAdapter{fakeAdapter: s.adapters[Codex].(*fakeAdapter), clock: clock, result: "success", resetAfter: time.Hour}
	s.api = api
	s.adapters[Codex] = adapter
	s.capacityNow = clock.Now
	s.probedAccounts["account"] = true
	return s, api, adapter, clock
}
func tickChecks(t *testing.T, s *Supervisor, clock *checkClock) {
	t.Helper()
	s.captureCapacityChecks(t.Context(), clock.Now())
}

func TestCapacityCheckHeartbeatPrecedesRestartCapture(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	api.request("old-check", "old-generation", clock.Now())
	tickChecks(t, s, clock)
	if a.calls != 1 || len(api.reports) != 1 || api.reports[0].CheckID != "" {
		t.Fatal("restart rebound an old check or lost automatic capture")
	}
	if !reflect.DeepEqual(api.events, []string{"poll", "heartbeat", "poll", "poll", "report"}) {
		t.Fatal("generation was not committed before check polling", api.events)
	}
	if api.reports[0].Result != "success" || api.reports[0].Facts[0].WindowKey != "5h:" || *api.reports[0].Facts[0].UsedPercent != 17 {
		t.Fatal("bounded result lost its measured fact")
	}
}
func TestCapacityCheckFailedHeartbeatNeverCaptures(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	api.heartbeatErr = errors.New("synthetic heartbeat lost")
	tickChecks(t, s, clock)
	if a.calls != 0 || len(api.reports) != 0 || s.capacityCheckHeartbeat["account"] {
		t.Fatal("capture without accepted plain heartbeat")
	}
}
func TestCapacityCheckDurableFailureBackoff(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	a.result = "protocol"
	delays := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, delay := range delays {
		tickChecks(t, s, clock)
		if a.calls != i+1 || s.capacityChecks["account"].Failures != i+1 || !s.capacityChecks["account"].NextAttempt.Equal(clock.Now().Add(delay)) {
			t.Fatal("wrong retry step", i, s.capacityChecks["account"])
		}
		clock.Add(delay - time.Second)
		s.capacityCheckRefresh["account"] = true
		tickChecks(t, s, clock)
		if a.calls != i+1 {
			t.Fatal("automatic trigger bypassed retry", i)
		}
		clock.Add(time.Second)
	}
	root, workspace, accounts := s.state.Path(), s.workspace, s.accounts
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, Workspace: workspace, DaemonID: "daemon", Accounts: accounts, Adapters: []Adapter{a}, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(t.Context())
	restarted.probedAccounts["account"] = true
	tickChecks(t, restarted, clock)
	if a.calls != 6 || restarted.capacityChecks["account"].Failures != 6 {
		t.Fatal("restart lost persisted retry")
	}
	// A second restart before the next deadline must keep the failure count.
	if err := restarted.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, Workspace: workspace, DaemonID: "daemon", Accounts: accounts, Adapters: []Adapter{a}, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close(t.Context())
	again.probedAccounts["account"] = true
	tickChecks(t, again, clock)
	if a.calls != 6 {
		t.Fatal("restart erased backoff deadline")
	}
}
func TestCapacityCheckNowBypassesBackoffOnlyOnce(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	a.result = "timeout"
	tickChecks(t, s, clock)
	clock.Add(time.Second)
	api.request("manual", s.generation, clock.Now())
	api.lost = true
	tickChecks(t, s, clock)
	if a.calls != 2 || len(api.reports) != 2 || api.reports[1].CheckID != "manual" {
		t.Fatal("check now was not serviced")
	}
	for i := 0; i < 3; i++ {
		tickChecks(t, s, clock)
	}
	if a.calls != 2 || len(api.reports) != 5 {
		t.Fatal("lost responses recaptured rather than replaying completion")
	}
	for _, report := range api.reports[1:] {
		if !reflect.DeepEqual(report, api.reports[1]) {
			t.Fatal("retry changed completion")
		}
	}
	api.lost = false
	tickChecks(t, s, clock)
	if s.capacityChecks["account"].Pending != nil || a.calls != 2 {
		t.Fatal("acknowledged completion was not cleared")
	}
}
func TestCapacityCheckLostCommittedResponseDoesNotRecapture(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	s.capacityCheckHeartbeat["account"] = true
	api.request("manual", s.generation, clock.Now())
	api.lost = true
	api.committed = true
	tickChecks(t, s, clock)
	tickChecks(t, s, clock)
	if a.calls != 1 || len(api.reports) != 1 {
		t.Fatal("already committed check was repeated")
	}
}
func TestCapacityCheckResetFactExpiryAndReconnect(t *testing.T) {
	s, _, a, clock := checkFixture(t)
	a.resetAfter = 2 * time.Minute
	tickChecks(t, s, clock)
	clock.Add(time.Minute)
	tickChecks(t, s, clock)
	if a.calls != 1 {
		t.Fatal("five minute cadence ignored")
	}
	clock.Add(time.Minute)
	tickChecks(t, s, clock)
	if a.calls != 2 {
		t.Fatal("reset did not trigger early refresh")
	}
	// A recent run fact expires before the next five-minute idle deadline.
	a.resetAfter = time.Hour
	s.rememberCapacityCapture("account", []capacity.Reading{{ReadAt: clock.Now().Add(-9 * time.Minute), ResetsAt: clock.Now().Add(time.Hour), Source: "harness", WindowKind: "5h", WindowMinutes: 300}})
	s.capacityChecks["account"] = capacityCheckState{Revision: 0, LastResult: "success", NextAttempt: clock.Now().Add(5 * time.Minute), RefreshAt: clock.Now().Add(time.Minute)}
	clock.Add(time.Minute)
	tickChecks(t, s, clock)
	if a.calls != 3 {
		t.Fatal("fact expiry did not trigger early refresh")
	}
	s.capacityProbeConnection("account", false)
	s.capacityProbeConnection("account", true)
	tickChecks(t, s, clock)
	if a.calls != 4 {
		t.Fatal("reconnect did not request refresh")
	}
}
func TestCapacityCheckUnsupportedAutomaticDoesNotRepeat(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	a.result = "unsupported"
	tickChecks(t, s, clock)
	for i := 0; i < 3; i++ {
		clock.Add(time.Hour)
		s.capacityCheckRefresh["account"] = true
		tickChecks(t, s, clock)
	}
	if a.calls != 1 {
		t.Fatal("unsupported automatic check repeated")
	}
	api.request("manual", s.generation, clock.Now())
	tickChecks(t, s, clock)
	if a.calls != 2 || api.reports[1].Result != "unsupported" {
		t.Fatal("manual unsupported result missing")
	}
}
func TestCapacityCheckSerialFenceAndRevocationDuringCapture(t *testing.T) {
	for _, change := range []string{"revoked", "revision", "expired", "generation", "local_drain"} {
		t.Run(change, func(t *testing.T) {
			s, api, a, clock := checkFixture(t)
			s.capacityCheckHeartbeat["account"] = true
			api.request("manual", s.generation, clock.Now())
			a.entered = make(chan struct{})
			a.release = make(chan struct{})
			done := make(chan struct{})
			go func() { defer close(done); tickChecks(t, s, clock) }()
			<-a.entered
			tickChecks(t, s, clock)
			if a.calls != 1 {
				t.Fatal("concurrent local capture")
			}
			if err := s.StartRun(t.Context(), api.API.(*fakeAPI).run); !errors.Is(err, ErrDraining) {
				t.Fatal("capture did not fence launch", err)
			}
			api.mu.Lock()
			switch change {
			case "revoked":
				api.account.OngoingUseApproved = false
			case "revision":
				api.account.LinkRevision++
			case "expired":
				clock.Add(5 * time.Minute)
			case "generation":
				old := "different-generation"
				api.account.PendingCheck.DaemonGeneration = &old
			case "local_drain":
				if _, err := s.Drain(DrainRequest{DaemonID: s.daemonID, AccountID: "account"}); err != nil {
					t.Fatal(err)
				}
			}
			api.mu.Unlock()
			close(a.release)
			<-done
			if len(api.reports) != 0 {
				t.Fatal("stale/revoked capture was published", change)
			}
			if s.capacityCapturing {
				t.Fatal("terminated capture retained busy slot")
			}
		})
	}
}
func TestCapacityCheckIdentityMismatchIsHardFailure(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	a.result = "identity_mismatch"
	tickChecks(t, s, clock)
	if len(api.reports) != 1 || api.reports[0].Result != "identity_mismatch" || api.statuses[0].OK || !s.blockedAccounts["account"] {
		t.Fatal("identity mismatch became unknown usage")
	}
}
func TestCapacityCheckUnconfirmedCleanupRetainsFence(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	a.cleanup = true
	a.result = "launch_failed"
	tickChecks(t, s, clock)
	if !s.capacityCapturing || !s.capacityChecks["account"].CleanupUnconfirmed {
		t.Fatal("unconfirmed cleanup released fence")
	}
	clock.Add(time.Hour)
	tickChecks(t, s, clock)
	if a.calls != 1 || len(api.reports) != 1 {
		t.Fatal("second launch with unconfirmed first capture")
	}
	if err := s.Close(t.Context()); !errors.Is(err, ErrDraining) {
		t.Fatal("close claimed capture cleanup", err)
	}
	// The fake owns no process; release only the fixture's state for cleanup.
	s.mu.Lock()
	s.capacityCapturing = false
	s.capacityChecks["account"] = capacityCheckState{}
	s.mu.Unlock()
}
func TestCapacityCheckFactBoundsAndNullCap(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	zero := float64(0)
	a := CapacityCheckAccount{Resources: []CapacityResource{{ID: "key", Kind: "key_cap"}, {ID: "balance", Kind: "shared_balance"}}}
	c := CapacityCapture{Credits: &openrouter.Credits{ObservedAt: now, Remaining: &zero}}
	facts := capacityCheckFacts(a, c, now)
	if len(facts) != 1 || facts[0].Remaining != nil || facts[0].CreditState != "unknown" || facts[0].StopKind != "none" {
		t.Fatal("null cap became total credit", facts)
	}
	limit := float64(10)
	c.Credits.Limit = &limit
	facts = capacityCheckFacts(a, c, now)
	if len(facts) != 1 || facts[0].CreditState != "exhausted" || facts[0].DenialReason != "key_cap_exhausted" {
		t.Fatal("zero cap did not create hard fact")
	}
}
func TestRemoteCapacityCheckContractAndBoundedErrors(t *testing.T) {
	var reports []map[string]json.RawMessage
	mode := "poll"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-daemon-key" {
			t.Error("scoped daemon auth absent")
		}
		if r.Method == "GET" {
			if r.URL.Path != "/api/agent-accounts" || r.URL.Query().Get("include_checks") != "true" {
				t.Error("unscoped polling")
			}
			if mode == "oversize" {
				fmt.Fprint(w, strings.Repeat(" ", (1<<20)+1))
				return
			}
			fmt.Fprint(w, `[{"id":"account","link_revision":8,"pending_check":{"id":"check","binding_revision":8,"state":"pending"},"readiness_resources":[]}]`)
			return
		}
		var v map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&v)
		reports = append(reports, v)
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":"private vendor sentinel"}`)
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "synthetic-daemon-key")
	accounts, err := remote.PollCapacityChecks(t.Context())
	if err != nil || len(accounts) != 1 || accounts[0].LinkRevision != 8 || accounts[0].PendingCheck.ID != "check" {
		t.Fatal("contract projection", err)
	}
	report := CapacityCheckReport{CheckID: "check", BindingRevision: 8, Result: "identity_mismatch"}
	err = remote.ReportCapacityCheck(t.Context(), "account", "daemon", "generation", ProbeStatus{Failure: ProbeIdentityMismatch}, report)
	var status *client.StatusError
	if !errors.As(err, &status) || status.Status != 409 || strings.Contains(err.Error(), "sentinel") || len(reports) != 1 {
		t.Fatal("fenced report was retried or leaked diagnostics", err)
	}
	var got CapacityCheckReport
	_ = json.Unmarshal(reports[0]["readiness"], &got)
	if !reflect.DeepEqual(got, report) || string(reports[0]["daemon_generation"]) != `"generation"` || string(reports[0]["available"]) != "false" || string(reports[0]["failure"]) != `"unavailable"` {
		t.Fatal("check fencing/cause lost")
	}
	mode = "oversize"
	if _, err := remote.PollCapacityChecks(t.Context()); err == nil {
		t.Fatal("unbounded poll response")
	}
}
func TestCapacityCheckStateRejectsForeignOwner(t *testing.T) {
	s, _, _, _ := checkFixture(t)
	raw, _ := json.Marshal(capacityChecksState{TenantID: "different-tenant", PrincipalID: s.principalID, Accounts: map[string]capacityCheckState{}})
	if err := s.state.Write(s.capacityChecksName(), raw, false); err != nil {
		t.Fatal(err)
	}
	if err := s.loadCapacityChecks(); err == nil {
		t.Fatal("retry state crossed tenant")
	}
}
func TestCapacityCheckRestartDoesNotReplayOldManualResult(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	s.capacityCheckHeartbeat["account"] = true
	api.request("manual", s.generation, clock.Now())
	api.lost = true
	tickChecks(t, s, clock)
	root, workspace, accounts := s.state.Path(), s.workspace, s.accounts
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, Workspace: workspace, DaemonID: "daemon", Accounts: accounts, Adapters: []Adapter{a}, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(t.Context())
	restarted.probedAccounts["account"] = true
	tickChecks(t, restarted, clock)
	if a.calls != 1 || len(api.reports) != 1 {
		t.Fatal("restart rebound completion or recaptured before cadence")
	}
	raw, err := os.ReadFile(filepath.Join(root, restarted.capacityChecksName()))
	if err != nil || strings.Contains(string(raw), "private vendor") || strings.Contains(string(raw), "api_key") {
		t.Fatal("retry store leaked diagnostic")
	}
}

func TestCapacityCheckLoopStartsWithoutWaitingForTicker(t *testing.T) {
	s, _, a, _ := checkFixture(t)
	a.entered = make(chan struct{})
	a.release = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.RunCapacityCaptures(ctx) }()
	select {
	case <-a.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("startup check never reached capture")
	}
	close(a.release)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle loop did not join cancellation")
	}
	if a.calls != 1 {
		t.Fatal("startup capture repeated")
	}
}

func TestCapacityCheckInterruptedAttemptKeepsBackoffAfterRestart(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	s.capacityChecks["account"] = capacityCheckState{Revision: 0, LastResult: "timeout", Failures: 2, NextAttempt: clock.Now().Add(2 * time.Minute)}
	s.mu.Lock()
	err := s.saveCapacityChecksLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	root, workspace, accounts := s.state.Path(), s.workspace, s.accounts
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, Workspace: workspace, DaemonID: "daemon", Accounts: accounts, Adapters: []Adapter{a}, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(t.Context())
	restarted.probedAccounts["account"] = true
	restarted.capacityProbeConnection("account", true)
	tickChecks(t, restarted, clock)
	if a.calls != 0 || restarted.capacityChecks["account"].Failures != 2 {
		t.Fatal("reconnect erased interrupted attempt")
	}
}

func TestCapacityCheckAuthenticationCauseSurvivesHealthProbe(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	a.result = "identity_mismatch"
	api.lost = true
	tickChecks(t, s, clock)
	_ = s.PollOnce(t.Context())
	if !s.blockedAccounts["account"] || api.API.(*fakeAPI).claims != 0 {
		t.Fatal("plain health probe cleared identity mismatch")
	}
	a.result = "unsupported"
	clock.Add(time.Minute)
	tickChecks(t, s, clock)
	if s.capacityChecks["account"].LastResult != "identity_mismatch" || api.reports[len(api.reports)-1].Result != "identity_mismatch" {
		t.Fatal("unsupported capture cleared identity stop")
	}
}

func TestCapacityCheckRestartKeepsIdentityFenceBeforeHealthPoll(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	a.result = "identity_mismatch"
	tickChecks(t, s, clock)
	root, workspace, accounts := s.state.Path(), s.workspace, s.accounts
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, Workspace: workspace, DaemonID: "daemon", Accounts: accounts, Adapters: []Adapter{a}, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(t.Context())
	if !restarted.blockedAccounts["account"] {
		t.Fatal("restart briefly opened identity fence")
	}
}

func TestFix2CapacityCheckKeepsIdentityAndSignOutDistinct(t *testing.T) {
	for _, result := range []string{"identity_mismatch", "authentication_failed"} {
		t.Run(result, func(t *testing.T) {
			s, api, a, clock := checkFixture(t)
			a.result = result
			tickChecks(t, s, clock)
			want := ProbeIdentityMismatch
			if result == "authentication_failed" {
				want = ProbeAuthFailed
			}
			if len(api.reports) != 1 || api.reports[0].Result != result || api.statuses[0].Failure != want || s.probeFailureReasons["account"] != want || !s.blockedAccounts["account"] {
				t.Fatalf("wrong identity repair: result=%s reports=%+v statuses=%+v local=%s blocked=%t", result, api.reports, api.statuses, s.probeFailureReasons["account"], s.blockedAccounts["account"])
			}
		})
	}
}

func TestFix2CapacityCheckNewIdentityEvidenceReplacesOldCause(t *testing.T) {
	s, api, a, clock := checkFixture(t)
	a.result = "authentication_failed"
	tickChecks(t, s, clock)
	clock.Add(time.Minute)
	a.result = "identity_mismatch"
	tickChecks(t, s, clock)
	if a.calls != 2 || len(api.reports) != 2 || api.reports[1].Result != "identity_mismatch" || s.capacityChecks["account"].LastResult != "identity_mismatch" || !s.blockedAccounts["account"] {
		t.Fatalf("fresh mismatched login retained an old sign-out: %+v", api.reports)
	}
}

func TestFix2RemoteCapacityCheckIdentityWireCategories(t *testing.T) {
	for _, result := range []string{"identity_mismatch", "authentication_failed", "timeout", "protocol", "launch_failed"} {
		t.Run(result, func(t *testing.T) {
			var body struct {
				Available bool                `json:"available"`
				Failure   string              `json:"failure"`
				Readiness CapacityCheckReport `json:"readiness"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			status := ProbeStatus{OK: true}
			wantFailure := ""
			if result == "identity_mismatch" {
				status = ProbeStatus{Failure: ProbeIdentityMismatch}
				wantFailure = ProbeUnavailable
			}
			if result == "authentication_failed" {
				status = ProbeStatus{Failure: ProbeAuthFailed}
				wantFailure = ProbeAuthFailed
			}
			report := CapacityCheckReport{BindingRevision: 0, Result: result}
			if err := NewRemote(server.URL, "synthetic-daemon-key").ReportCapacityCheck(t.Context(), "account", "daemon", "generation", status, report); err != nil {
				t.Fatal(err)
			}
			if body.Available != status.OK || body.Failure != wantFailure || body.Readiness.Result != result {
				t.Fatalf("identity and measurement categories collapsed: %+v", body)
			}
		})
	}
}

func TestFix2CapacityCheckZeroDeclaredCapIsHardFact(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	zero := 0.0
	facts := capacityCheckFacts(CapacityCheckAccount{Resources: []CapacityResource{{ID: "key", Kind: "key_cap"}}}, CapacityCapture{Credits: &openrouter.Credits{ObservedAt: now, Limit: &zero}}, now)
	if len(facts) != 1 || facts[0].Remaining == nil || *facts[0].Remaining != 0 || facts[0].CreditState != "exhausted" || facts[0].StopKind != "unnamed" || facts[0].DenialReason != "key_cap_exhausted" {
		t.Fatalf("declared zero cap became unknown availability: %+v", facts)
	}
}
