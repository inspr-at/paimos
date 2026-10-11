// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

type diagnosticProbeAdapter struct {
	*fakeAdapter
	statuses map[string]ProbeStatus
}

func (a *diagnosticProbeAdapter) ProbeStatus(_ context.Context, key string) ProbeStatus {
	return a.statuses[key]
}

func TestProbeDiagnosticReachesLifecycleJSONAndClearsOnRecovery(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.api = &readinessQueueAPI{fakeAPI: api}
	s.accounts[0].Harness = Claude
	adapter := &diagnosticProbeAdapter{fakeAdapter: &fakeAdapter{}, statuses: map[string]ProbeStatus{"local": {Failure: ProbeUnavailable, ReasonDetail: agentsetup.ProbeClaudeDefaultPrivate}}}
	s.adapters[Claude] = adapter
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	status := s.Lifecycle("")
	for _, got := range []agentsetup.HarnessDetail{status.HarnessDetails[Claude], status.AccountStatuses["account"]} {
		if got.State != "blocked" || got.Reason != "probe_failed" || got.ReasonDetail != agentsetup.ProbeClaudeDefaultPrivate || got.Fix.Command != `chmod 700 "$HOME/.claude"` {
			t.Fatalf("diagnostic lost: %+v", got)
		}
	}
	raw, err := json.Marshal(status)
	if err != nil || strings.Count(string(raw), `"reason_detail"`) != 3 {
		t.Fatalf("lifecycle JSON lost diagnostic: %s, %v", raw, err)
	}
	adapter.statuses["local"] = ProbeStatus{OK: true, ReasonDetail: agentsetup.ProbeClaudeDefaultPrivate}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(s.Lifecycle(""))
	if err != nil || strings.Contains(string(raw), "reason_detail") || strings.Contains(string(raw), "chmod") {
		t.Fatalf("recovered diagnostic persisted: %s, %v", raw, err)
	}
	if _, ok := s.probeReasonDetails["account"]; ok {
		t.Fatal("recovery retained local diagnostic")
	}
	adapter.statuses["local"] = ProbeStatus{Failure: ProbeUnavailable, ReasonDetail: "/private/profile: raw diagnostic"}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(s.Lifecycle(""))
	if err != nil || strings.Contains(string(raw), "reason_detail") || strings.Contains(string(raw), "/private/profile") {
		t.Fatalf("raw diagnostic escaped: %s, %v", raw, err)
	}
	s.probeReasonDetails["account"] = agentsetup.ProbeOutputInvalid
	s.beginAccountProbe("account", time.Now())
	if _, ok := s.probeReasonDetails["account"]; ok {
		t.Fatal("new probe inherited an old cause")
	}
}

func TestReadySiblingRetainsOnlyBlockedAccountProbeDetail(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.api = &readinessQueueAPI{fakeAPI: api}
	s.accounts = []EnrolledAccount{{ID: "blocked", Key: "bad", Harness: Claude}, {ID: "healthy", Key: "good", Harness: Claude}}
	s.adapters[Claude] = &diagnosticProbeAdapter{fakeAdapter: &fakeAdapter{}, statuses: map[string]ProbeStatus{
		"bad": {Failure: ProbeUnavailable, ReasonDetail: agentsetup.ProbeOutputInvalid}, "good": {OK: true},
	}}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.Lifecycle("").HarnessDetails[Claude]
	if got.State != "ready" || got.ReasonDetail != "" || got.Fix.Command != "" || len(got.Attention) != 1 || got.Attention[0].AccountID != "blocked" || got.Attention[0].ReasonDetail != agentsetup.ProbeOutputInvalid {
		t.Fatalf("sibling diagnosis lost: %+v", got)
	}
}

func TestBlockedClaudeAccountsKeepDistinctDiagnostics(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.api = &readinessQueueAPI{fakeAPI: api}
	s.accounts = []EnrolledAccount{{ID: "permissions", Key: "private", Harness: Claude}, {ID: "signed-out", Key: "login", Harness: Claude}}
	s.adapters[Claude] = &diagnosticProbeAdapter{fakeAdapter: &fakeAdapter{}, statuses: map[string]ProbeStatus{
		"private": {Failure: ProbeUnavailable, ReasonDetail: agentsetup.ProbeClaudeDefaultPrivate},
		"login":   {Failure: ProbeAuthFailed, ReasonDetail: agentsetup.ProbeSignedOut},
	}}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	status := s.Lifecycle("")
	raw, err := json.Marshal(status.HarnessDetails[Claude])
	if err != nil {
		t.Fatal(err)
	}
	var decoded agentsetup.HarnessDetail
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := agentsetup.ResolveAttention(Claude, decoded.State, []string{"permissions", "signed-out"}, decoded.Attention, decoded.AttentionCount, decoded.AttentionTruncated)
	if got.Count != 2 || got.Truncated || len(got.Accounts) != 2 {
		t.Fatalf("lost account diagnostics: %+v", got)
	}
	if got.Accounts[0].AccountID != "permissions" || got.Accounts[0].ReasonDetail != agentsetup.ProbeClaudeDefaultPrivate || got.Accounts[1].AccountID != "signed-out" || got.Accounts[1].Reason != "login_required" || got.Accounts[1].ReasonDetail != agentsetup.ProbeSignedOut {
		t.Fatalf("misattributed diagnoses: %+v", got)
	}
	if status.AccountStatuses["signed-out"].ReasonDetail != agentsetup.ProbeSignedOut {
		t.Fatal("sign-out lost its daemon-view diagnostic")
	}
}

type isolationProbeAdapter struct {
	*fakeAdapter
	statuses    map[string]ProbeStatus
	probeErr    error
	timeoutPath string
	starts      []string
}

func (a *isolationProbeAdapter) ProbeHarness(ctx context.Context, key string) (ProbeStatus, error) {
	if key == "bad" && a.timeoutPath != "" {
		_, _, err := probeRunWith(ctx, a.timeoutPath, nil, nil, func(op context.Context, _ *exec.Cmd) error { <-op.Done(); return op.Err() })
		return probeUnavailable, err
	}
	if key == "bad" && a.probeErr != nil {
		return probeUnavailable, a.probeErr
	}
	return a.statuses[key], nil
}

func (a *isolationProbeAdapter) Start(ctx context.Context, r StartRequest, emit func(AdapterEvent)) (Process, error) {
	a.starts = append(a.starts, r.AccountKey)
	return a.fakeAdapter.Start(ctx, r, emit)
}

type isolationProbeAPI struct {
	*fakeAPI
	runs      []Run
	profiles  []Profile
	reportErr error
	observed  map[string]bool
}

func (a *isolationProbeAPI) Queued(context.Context) ([]Run, error)       { return a.runs, nil }
func (a *isolationProbeAPI) Profiles(context.Context) ([]Profile, error) { return a.profiles, nil }
func (a *isolationProbeAPI) Probe(_ context.Context, id, _, _ string, available bool) error {
	a.observed[id] = available
	if id == "blocked" {
		return a.reportErr
	}
	return nil
}
func (a *isolationProbeAPI) Route(ctx context.Context, id, daemon string, accounts []string, units map[string]int64) (Route, error) {
	route, err := a.fakeAPI.Route(ctx, id, daemon, accounts, units)
	route.AccountID = accounts[0]
	if route.AccountID == "blocked" {
		route.AccountKey = "bad"
	}
	return route, err
}

// Risk: a failed/login-required account at the front of the queue must neither
// acquire a writer/resource hold nor prevent a sibling's fenced claim/launch.
func TestProbeFailureMatrixKeepsSiblingDispatchAndRecovers(t *testing.T) {
	for _, failedHarness := range []string{Claude, Codex, Cursor} {
		for _, readyHarness := range []string{Claude, Codex, Cursor} {
			for _, failure := range []string{"probe_failed", "login_required", "harness_failed", "vendor_timeout", "report_refused", "all_failed"} {
				t.Run(failedHarness+"/"+readyHarness+"/"+failure, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						s, base, process := testSupervisor(t)
						base.run.RequestedAccountID = "account"
						base.profile.Harness = readyHarness
						blockedRun := base.run
						blockedRun.ID, blockedRun.RequestedAccountID, blockedRun.ModelProfileID = "blocked-run", "blocked", "blocked-profile"
						api := &isolationProbeAPI{fakeAPI: base, runs: []Run{blockedRun, base.run}, profiles: []Profile{{ID: "blocked-profile", Harness: failedHarness, Model: "model"}, base.profile}, observed: map[string]bool{}}
						s.api = api
						s.accounts = []EnrolledAccount{{ID: "blocked", Key: "bad", Harness: failedHarness}, {ID: "account", Key: "local", Harness: readyHarness}}
						adapter := &isolationProbeAdapter{fakeAdapter: &fakeAdapter{proc: process}, statuses: map[string]ProbeStatus{"bad": probeUnavailable, "local": probeOK}}
						s.adapters = map[string]Adapter{failedHarness: adapter, readyHarness: adapter}
						wantReason := failure
						switch failure {
						case "login_required":
							adapter.statuses["bad"] = probeAuthFailed
						case "harness_failed":
							adapter.probeErr = harnesslaunch.ErrStart
						case "vendor_timeout":
							adapter.timeoutPath = fakeVendorPath(t, "codex")
							wantReason = "harness_failed"
						case "report_refused":
							api.reportErr = errors.New("stale account binding rejected")
							adapter.statuses["bad"] = probeOK
							wantReason = "probe_failed"
						case "all_failed":
							adapter.statuses["local"] = probeAuthFailed
							wantReason = "probe_failed"
						}
						var diagnostics []string
						s.pollDiagnostic = func(reason string) { diagnostics = append(diagnostics, reason) }
						if err := s.PollScheduledOnce(t.Context()); (err != nil) != (failure == "report_refused") {
							t.Fatalf("per-account outcome became global poll failure: %v", err)
						}
						status := s.Lifecycle("")
						if status.AccountStatuses["blocked"].Reason != wantReason || s.accountAvailable("blocked") || api.observed["blocked"] != adapter.statuses["bad"].OK {
							t.Fatalf("failed account lost its reason/fence: %+v, observations=%v", status, api.observed)
						}
						if failure == "all_failed" {
							if status.Ready || status.AccountStatuses["account"].Reason != "login_required" || base.claims != 0 || len(adapter.starts) != 0 || len(s.runs) != 0 || s.checkout != nil || !slices.Contains(diagnostics, "probe_failed") {
								t.Fatalf("all failed accounts acquired dispatch authority: %+v", status)
							}
							// Model an approved re-link: fresh observation and server ack
							// clear only this account, then normal dispatch retries it.
							adapter.statuses["bad"] = probeOK
							api.runs = []Run{blockedRun}
							s.wakeQueue()
							if err := s.PollScheduledOnce(t.Context()); err != nil || !s.accountAvailable("blocked") || s.accountAvailable("account") || base.claims != 1 || !slices.Equal(adapter.starts, []string{"bad"}) {
								t.Fatalf("account recovery did not retry through dispatch: %v", err)
							}
						} else if !status.Ready || status.AccountStatuses["account"].State != "ready" || base.claims != 1 || !slices.Equal(adapter.starts, []string{"local"}) || !slices.Equal(base.routeAccounts, []string{"account"}) || len(diagnostics) != 0 {
							t.Fatalf("failed %s blocked ready %s: %+v, starts=%v diagnostics=%v", failedHarness, readyHarness, status, adapter.starts, diagnostics)
						}
					})
				})
			}
		}
	}
}

// Inject a command timeout instead of waiting for a real vendor process. A
// caller cancellation still propagates; the private deadline stays local.
func TestVendorProbeDeadlineCannotCancelSiblingPolling(t *testing.T) {
	path := fakeVendorPath(t, "codex")
	for _, cancelled := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if cancelled {
				cancel()
			}
			_, _, err := probeRunWith(ctx, path, nil, nil, func(op context.Context, _ *exec.Cmd) error { <-op.Done(); return op.Err() })
			cancel()
			if !errors.Is(err, harnesslaunch.ErrStart) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) != cancelled {
				t.Fatalf("vendor deadline escaped its account or caller cancellation was lost: %v", err)
			}
		})
	}
}
