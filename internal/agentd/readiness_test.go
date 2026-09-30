// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestLifecycleBoundsProbeAndCapacityWaits(t *testing.T) {
	s, _, _ := testSupervisor(t)
	if !s.probePendingSince["account"].Equal(s.startedAt) {
		t.Fatal("initial account probe clock did not start with the daemon")
	}
	now := time.Now()
	s.startedAt = now
	s.probePendingSince["account"] = now
	if got := s.lifecycleAt("", now).AccountStatuses["account"]; got.Reason != "probe_pending" {
		t.Fatal(got)
	}
	if got := s.lifecycleAt("", now.Add(time.Minute)).AccountStatuses["account"]; got.State != "blocked" || got.Reason != "probe_timeout" {
		t.Fatal(got)
	}
	s.probedAccounts["account"] = true
	s.capacityCapturing = true
	s.capacityAccountID = "account"
	s.capacityStartedAt = now
	if got := s.lifecycleAt("", now); got.Ready || got.AccountStatuses["account"].Reason != "capacity_capture" {
		t.Fatal("capacity wait missing")
	}
	if got := s.lifecycleAt("", now.Add(10*time.Second)).AccountStatuses["account"]; got.State != "blocked" || got.Reason != "capacity_timeout" {
		t.Fatal(got)
	}
	s.capacityCapturing = false
}

type readinessQueueAPI struct {
	*fakeAPI
	queueErr error
	probed   []string
}

func (a *readinessQueueAPI) Queued(context.Context) ([]Run, error) {
	return nil, a.queueErr
}

func (a *readinessQueueAPI) Probe(_ context.Context, id, _, _ string, _ bool) error {
	a.probed = append(a.probed, id)
	return nil
}

func TestAccountProbeWaitStartsAtRefreshOrRepin(t *testing.T) {
	for _, action := range []string{"add_harness", "repin"} {
		for _, outcome := range []string{"probed", "timeout"} {
			t.Run(action+"/"+outcome, func(t *testing.T) {
				s, api, _ := testSupervisor(t)
				queue := &readinessQueueAPI{fakeAPI: api, queueErr: errors.New("queue unavailable")}
				s.api = queue
				account := EnrolledAccount{ID: "added", Key: "added-local", Harness: Codex}
				if action == "repin" {
					account.Harness = Claude
					blocked := account
					blocked.DependencyBlocked, blocked.PinReason = true, agentsetup.PinDrifted
					if err := s.RefreshAccounts([]EnrolledAccount{blocked}, nil); err != nil {
						t.Fatal(err)
					}
				}
				// The daemon and every existing account have waited over a minute.
				s.startedAt = time.Now().Add(-2 * time.Minute)
				for id := range s.probePendingSince {
					s.probePendingSince[id] = s.startedAt
				}
				before := time.Now()
				if action == "repin" {
					s.SetHarnessHoldWithReason(Claude, "repin_pending", "waiting for idle Claude")
					path := fakeVendorPath(t, "claude")
					node, sdk := claudeAdapterDependencies(t, path)
					if err := s.RestartClaude(t.Context(), NewClaudeAdapter(node, sdk, path, nil)); err != nil {
						t.Fatal(err)
					}
				}
				adapter := &fakeAdapter{}
				if err := s.RefreshAccounts([]EnrolledAccount{account}, nil); err != nil {
					t.Fatal(err)
				}
				if action == "repin" {
					s.SetHarnessHold(Claude, "")
				}
				// Use a health-only fixture after exercising the real repin swap.
				s.adapters[account.Harness] = adapter
				since := s.probePendingSince[account.ID]
				if since.Before(before) || since.After(time.Now()) {
					t.Fatal("probe wait did not start with the account")
				}
				if err := s.PollOnce(t.Context()); !errors.Is(err, queue.queueErr) || len(queue.probed) != 0 {
					t.Fatal("queue outage should leave the account pending", err)
				}
				if got := s.lifecycleAt("", time.Now()).AccountStatuses[account.ID]; got.State != "checking" || got.Reason != "probe_pending" {
					t.Fatal("long daemon uptime prematurely blocked the account", got)
				}
				if got := s.lifecycleAt("", since.Add(time.Minute-time.Nanosecond)).AccountStatuses[account.ID]; got.State != "checking" || got.Reason != "probe_pending" {
					t.Fatal("account timed out before its own deadline", got)
				}
				if err := s.RefreshAccounts([]EnrolledAccount{account}, nil); err != nil || !s.probePendingSince[account.ID].Equal(since) {
					t.Fatal("unchanged refresh extended the probe deadline", err)
				}
				if got := s.lifecycleAt("", time.Now()).AccountStatuses["account"]; got.Reason != "probe_timeout" || !s.probePendingSince["account"].Equal(s.startedAt) {
					t.Fatal("refresh changed another account's deadline", got)
				}
				if outcome == "probed" {
					queue.queueErr = nil
					if err := s.PollOnce(t.Context()); err != nil {
						t.Fatal(err)
					}
					if got := s.lifecycleAt("", since.Add(time.Minute)).AccountStatuses[account.ID]; got.State != "ready" || got.Reason != "" || len(queue.probed) != 2 || api.claims != 0 {
						t.Fatal("successful probe did not end the wait", got)
					}
				} else if got := s.lifecycleAt("", since.Add(time.Minute)).AccountStatuses[account.ID]; got.State != "blocked" || got.Reason != "probe_timeout" {
					t.Fatal("account did not time out at its own deadline", got)
				}
			})
		}
	}
}

type readinessProbeErrorAPI struct{ *readinessQueueAPI }

func (*readinessProbeErrorAPI) Probe(context.Context, string, string, string, bool) error {
	return errors.New("private probe diagnostic must not be logged")
}

func TestPollDiagnosticsContainOnlyBoundedCauses(t *testing.T) {
	for _, cause := range []string{"queue_unavailable", "dispatch_not_allowed", "probe_failed"} {
		t.Run(cause, func(t *testing.T) {
			s, api, _ := testSupervisor(t)
			queue := &readinessQueueAPI{fakeAPI: api}
			s.api = queue
			var reasons []string
			s.pollDiagnostic = func(reason string) { reasons = append(reasons, reason) }
			want := []string{cause}
			switch cause {
			case "queue_unavailable":
				queue.queueErr = errors.New("private queue diagnostic must not be logged")
				s.probePendingSince["account"] = time.Now().Add(-time.Minute)
				want = append(want, "probe_timeout")
			case "dispatch_not_allowed":
				if _, err := s.Drain(DrainRequest{DaemonID: s.DaemonID()}); err != nil {
					t.Fatal(err)
				}
			case "probe_failed":
				s.api = &readinessProbeErrorAPI{queue}
			}
			_ = s.PollOnce(t.Context())
			if !slices.Equal(reasons, want) {
				t.Fatal("diagnostics lost a cause or exposed an unbounded error", reasons)
			}
		})
	}
}

func TestHarnessHoldReleaseRestartsProbeClockOnlyOnce(t *testing.T) {
	s, _, _ := testSupervisor(t)
	s.startedAt = time.Now().Add(-2 * time.Minute)
	s.probePendingSince["account"] = s.startedAt
	s.SetHarnessHoldWithReason(Codex, "repin_pending", "waiting for idle harness")
	before := time.Now()
	s.SetHarnessHold(Codex, "")
	since := s.probePendingSince["account"]
	if since.Before(before) || s.probedAccounts["account"] || !s.blockedAccounts["account"] {
		t.Fatal("hold release reused the old deadline or cleared the dispatch block")
	}
	if got := s.lifecycleAt("", since).AccountStatuses["account"]; got.State != "checking" || got.Reason != "probe_pending" {
		t.Fatal("unblocked harness did not wait for its fresh probe", got)
	}
	if got := s.lifecycleAt("", since.Add(time.Minute)).AccountStatuses["account"]; got.State != "blocked" || got.Reason != "probe_timeout" {
		t.Fatal("unblocked harness did not get its own deadline", got)
	}
	s.SetHarnessHold(Codex, "")
	if !s.probePendingSince["account"].Equal(since) {
		t.Fatal("repeated hold release extended the probe deadline")
	}
	s.freezeOnError("account")
	if got := s.lifecycleAt("", since).AccountStatuses["account"]; got.State != "blocked" || got.Reason != "probe_failed" {
		t.Fatal("probe wait hid an ownership failure", got)
	}
}

type pendingProbeAdapter struct {
	fakeAdapter
	entered chan struct{}
	resume  chan struct{}
}

func (a *pendingProbeAdapter) Probe(context.Context, string) bool {
	close(a.entered)
	<-a.resume
	return false
}

func TestRepinCannotConsumeAnOlderInflightProbe(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.api = &readinessQueueAPI{fakeAPI: api}
	adapter := &pendingProbeAdapter{entered: make(chan struct{}), resume: make(chan struct{})}
	s.adapters[Codex] = adapter
	done := make(chan error, 1)
	go func() { done <- s.PollOnce(t.Context()) }()
	<-adapter.entered
	s.SetHarnessHoldWithReason(Codex, "repin_pending", "waiting for repin")
	s.SetHarnessHold(Codex, "")
	since := s.probePendingSince["account"]
	close(adapter.resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !s.probePendingSince["account"].Equal(since) || s.loginRequired["account"] || s.probedAccounts["account"] || s.accountAvailable("account") {
		t.Fatal("old probe changed the new pending clock or dispatch state")
	}
	if got := s.lifecycleAt("", since).AccountStatuses["account"]; got.State != "checking" || got.Reason != "probe_pending" {
		t.Fatal("old probe ended the new pending wait", got)
	}
	s.adapters[Codex] = &fakeAdapter{}
	if err := s.PollOnce(t.Context()); err != nil || !s.Lifecycle("").Ready {
		t.Fatal("fresh probe did not restore readiness", err)
	}
}

func TestInvalidVerificationCannotBlockProbeAndRefusalRetries(t *testing.T) {
	for _, tc := range []string{"binding_incomplete", "adapter_unsupported", "local_binding_missing"} {
		t.Run(tc, func(t *testing.T) {
			s, api, adapter := claimFixture(t)
			verificationClaim(api)
			api.run.AccountID = ""
			api.run.RequestedAccountID = "account"
			switch tc {
			case "binding_incomplete":
				api.run.VerificationPolicy = "managed"
			case "adapter_unsupported":
				s.adapters[Codex] = &fakeAdapter{}
			case "local_binding_missing":
				api.run.RequestedAccountID = "missing"
			}
			api.server = api.run
			api.reportErr = errors.New("offline")
			if err := s.PollOnce(t.Context()); err == nil {
				t.Fatal("lost refusal response hidden")
			}
			if !s.Lifecycle("").Ready || len(api.probes) != 1 || adapter.starts != 0 || api.routes != 0 {
				t.Fatal("refusal blocked health or launched")
			}
			api.reportErr = nil
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if api.server.Status != "failed" || s.Lifecycle("").VerificationReasons[api.run.ID] != tc {
				t.Fatal("refusal not retried with cause")
			}
		})
	}
}
