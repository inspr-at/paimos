// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestStatusListsEveryAccountIncludingReady(t *testing.T) {
	p := agentsetup.Progress{Stage: "verification_failed", Action: "Verification did not complete.",
		HarnessDetails:  map[string]agentsetup.HarnessDetail{"claude": {State: "login_required"}, "codex": {State: "ready"}, "cursor": {State: "ready"}},
		AccountStatuses: map[string]agentsetup.HarnessDetail{"a": {State: "login_required"}, "b": {State: "ready"}, "c": {State: "ready"}, "d": {State: "blocked", Reason: "probe_failed"}},
		Accounts:        []agentsetup.Enrollment{{AccountID: "c", Harness: "cursor", State: "connected"}, {AccountID: "a", Harness: "claude", State: "connected"}, {AccountID: "b", Harness: "codex", State: "connected"}, {AccountID: "d", Harness: "codex", State: "connected"}, {AccountID: "e", Harness: "claude", State: "revoked"}, {AccountID: "f", Harness: "cursor", State: "connected"}},
	}
	var out bytes.Buffer
	if err := printSetupProgress(&out, false, p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"harness claude login_required\n", "harness codex ready\n", "harness cursor ready\n", "account a harness claude login_required\n", "account b harness codex ready\n", "account c harness cursor ready\n", "account d harness codex blocked reason probe_failed\n", "account e harness claude revoked\n", "account f harness cursor unconfirmed\n"} {
		if strings.Count(out.String(), "\n"+want) != 1 {
			t.Errorf("missing or repeated status %q", want)
		}
	}
	if strings.Index(out.String(), "account a ") > strings.Index(out.String(), "account b ") {
		t.Fatal("account ordering is unstable")
	}
}

func TestStatusShowsReadyAccountVerificationExpiryAndPendingCleanup(t *testing.T) {
	p := agentsetup.Progress{Stage: "connected", Action: "Verification expired — run verification again.",
		AccountStatuses: map[string]agentsetup.HarnessDetail{"a": {State: "ready"}},
		Accounts:        []agentsetup.Enrollment{{AccountID: "a", Harness: "claude", State: "connected", VerificationState: "expired", VerificationRunID: "old-run", Cleanup: "pending"}},
	}
	var out bytes.Buffer
	if err := printSetupProgress(&out, false, p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"connected:", "account a harness claude ready", "verification expired — run verification again", "run old-run", "local cleanup pending"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if setupNeedsPoll("setup", p) {
		t.Fatal("expired request still needs polling")
	}
}
