// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestStatusNamesActiveClaudeLoginAndApproval(t *testing.T) {
	p := agentsetup.Progress{Stage: "connected", HarnessDetails: map[string]agentsetup.HarnessDetail{"claude": {State: "ready"}},
		Accounts:      []agentsetup.Enrollment{{AccountID: "a", Harness: "claude", Label: "a@example.test", State: "connected"}, {AccountID: "b", Harness: "claude", Label: "b@example.test", State: "connected"}},
		AccountLogins: map[string]agentsetup.AccountLogin{"a": {ActiveIdentity: "a@example.test", Approved: true}, "b": {ActiveIdentity: "a@example.test"}}}
	var out bytes.Buffer
	if err := printSetupProgress(&out, false, p); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "active login a@example.test: approved on this computer") != 1 || !strings.Contains(out.String(), "account b harness claude unconfirmed login b@example.test") {
		t.Fatalf("active or enrolled login missing: %s", out.String())
	}
	for id := range p.AccountLogins {
		p.AccountLogins[id] = agentsetup.AccountLogin{ActiveIdentity: "c@example.test"}
	}
	out.Reset()
	if err := printSetupProgress(&out, false, p); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "active login c@example.test: not approved on this computer; run aeon-agentd add-harness --harness claude") != 1 {
		t.Fatalf("unapproved active login hidden: %s", out.String())
	}
}

// Risk: approval or output deduplication crosses home boundaries after a login
// changes. Equal active identities in different homes must remain separate.
func TestStatusKeepsHarnessHomesAndApprovalsSeparate(t *testing.T) {
	p := agentsetup.Progress{Stage: "connected", HarnessDetails: map[string]agentsetup.HarnessDetail{"claude": {State: "ready"}},
		Accounts: []agentsetup.Enrollment{{AccountID: "a", Harness: "claude", Label: "first@example.test", State: "connected"}, {AccountID: "b", Harness: "claude", Label: "second@example.test", State: "connected"}},
		AccountLogins: map[string]agentsetup.AccountLogin{
			"a": {Home: "/profiles/claude-first", ActiveIdentity: "first@example.test", Approved: true},
			"b": {Home: "/profiles/claude-second", ActiveIdentity: "second@example.test", Approved: true}},
		AccountStatuses: map[string]agentsetup.HarnessDetail{"a": {State: "ready"}, "b": {State: "ready"}}}
	var out bytes.Buffer
	if err := printSetupProgress(&out, false, p); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{"/profiles/claude-first", "/profiles/claude-second"} {
		if strings.Count(out.String(), fmt.Sprintf("home %q", home)) != 2 {
			t.Fatalf("home missing from login/account status: %s", out.String())
		}
	}
	t.Log("status with two fake Claude homes:\n" + out.String())
	p.AccountLogins["b"] = agentsetup.AccountLogin{Home: "/profiles/claude-second", ActiveIdentity: "first@example.test"}
	out.Reset()
	if err := printSetupProgress(&out, false, p); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "active login first@example.test") != 2 || !strings.Contains(out.String(), `home "/profiles/claude-second" active login first@example.test: not approved`) || !strings.Contains(out.String(), `--home '/profiles/claude-second'`) {
		t.Fatalf("approval crossed homes: %s", out.String())
	}
	out.Reset()
	if err := printSetupProgress(&out, true, p); err != nil {
		t.Fatal(err)
	}
	var got agentsetup.Progress
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.AccountLogins["a"].Home != "/profiles/claude-first" || got.AccountLogins["b"].Approved {
		t.Fatalf("local JSON lost home fence: %v", err)
	}
}
