// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestPairingFailureOverridesProbeTimeoutAndRecovers(t *testing.T) {
	s, _, _ := testSupervisor(t)
	now := s.startedAt.Add(2 * time.Minute)
	s.capacityNow = func() time.Time { return now }
	s.SetPairingFailure(agentsetup.PairingAccountUnapproved)
	status := s.lifecycleAt("", now)
	for _, got := range []agentsetup.HarnessDetail{status.AccountStatuses["account"], status.HarnessDetails[Codex]} {
		if status.Ready || got.State != "blocked" || got.Reason != agentsetup.PairingSyncFailed || got.ReasonDetail != agentsetup.PairingAccountUnapproved {
			t.Fatal("sync failure misreported as probe timeout", got)
		}
	}
	s.SetPairingFailure("")
	if got := s.lifecycleAt("", now).AccountStatuses["account"]; got.Reason != "probe_pending" || got.ReasonDetail != "" {
		t.Fatal("recovery retained failure or expired probe clock", got)
	}
	now = now.Add(time.Minute)
	s.SetPairingFailure("")
	if got := s.lifecycleAt("", now).AccountStatuses["account"]; got.Reason != "probe_timeout" {
		t.Fatal("healthy refresh repeatedly reset probe deadline", got)
	}
	s.SetHarnessHoldWithReason(Codex, "pin_missing", "existing hold")
	s.SetPairingFailure(agentsetup.PairingServerUnavailable)
	s.SetPairingFailure("")
	if got := s.Lifecycle("").AccountStatuses["account"]; got.Reason != "pin_missing" {
		t.Fatal("pairing recovery erased harness hold", got)
	}
	s.SetPairingFailure(agentsetup.PairingServerUnavailable)
	if _, err := s.Drain(DrainRequest{DaemonID: s.daemonID, AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Lifecycle("").AccountStatuses["account"]; got.State != "draining" || got.Reason != "" {
		t.Fatal("sync failure hid durable fence", got)
	}
}
