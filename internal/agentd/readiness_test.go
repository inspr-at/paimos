// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"errors"
	"testing"
	"time"
)

func TestLifecycleBoundsProbeAndCapacityWaits(t *testing.T) {
	s, _, _ := testSupervisor(t)
	now := time.Now()
	s.startedAt = now
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
