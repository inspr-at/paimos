// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"testing"
	"time"
)

func TestAgentRecoveryDiagnosisDoesNotGuessExitOrAuth(t *testing.T) {
	now := time.Date(2026, 10, 5, 20, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Second)
	stale := now.Add(-3 * time.Minute)
	run := "run"
	base := Session{ID: "session", Management: "managed", RunID: &run, Capabilities: []string{recoveryCapability}, ProcessOwnership: &ownedprocess.Identity{}, HeartbeatAt: &stale, InboxSeenAt: &fresh}
	for _, tc := range []struct {
		name                  string
		paired, key, host     bool
		status, cause, action string
	}{
		{"unpaired", false, true, true, "running", "host_unpaired", ""},
		{"revoked paired credential", true, false, true, "running", "credential_rejected", ""},
		{"host silence", true, true, false, "running", "host_not_reporting", ""},
		{"session silence", true, true, true, "running", "heartbeat_overdue", "restart"},
		{"unconfirmed ownership", true, true, true, "ownership_lost", "ownership_unconfirmed", ""},
		{"terminal run requires daemon exit proof", true, true, true, "failed", "run_ended", "restart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := diagnoseAgent(base, now, tc.paired, tc.key, tc.host, tc.status)
			if d.Cause != tc.cause || d.Action != tc.action {
				t.Fatalf("diagnosis=%+v", d)
			}
		})
	}
	base.HeartbeatAt = &fresh
	base.InboxSeenAt = &stale
	if d := diagnoseAgent(base, now, true, true, true, "running"); d.Cause != "inbox_not_listening" || d.Action != "reconnect" {
		t.Fatalf("inbox diagnosis=%+v", d)
	}
	base.Management = "unmanaged"
	if d := diagnoseAgent(base, now, true, true, true, "running"); d.Action != "" || d.Cause != "hook_binding_unavailable" {
		t.Fatal("attached session without holder binding acquired authority")
	}
	base.Management = "managed"
	base.Capabilities = nil
	if d := diagnoseAgent(base, now, true, true, true, "running"); d.Action != "" {
		t.Fatal("legacy daemon acquired recovery authority")
	}
}

// Queued registrations have no process owner. Pairing and account advice must
// explain the wait without enabling restart/reconnect or guessing a sibling's
// sign-in state.
func TestQueuedAgentDiagnosisReportsDispatchBlockerWithoutRecoveryAuthority(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	run := "run"
	session := Session{ID: "session", Management: "managed", RunID: &run}
	for _, tc := range []struct {
		name, code, cause, detail string
		key, fresh                bool
	}{
		{"sign-in", "sign_in", "login_required", "Claude needs signing in on fixture-computer.", true, true},
		{"probe failure", "offline", "probe_failed", "Claude availability could not be confirmed on fixture-computer.", true, true},
		{"ready", "", "dispatch_pending", "Claude is waiting for dispatch on fixture-computer.", true, true},
		{"capacity", "capacity", "dispatch_wait", "Claude is waiting for an available account on fixture-computer.", true, true},
		{"stale", "sign_in", "host_not_reporting", "The paired host has not reported recently; its process state is unknown.", true, false},
		{"key revoked", "sign_in", "credential_rejected", "The paired daemon credential is revoked or expired. Credential delivery is required before recovery.", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wait *agentaccounts.CapacityWait
			if tc.code != "" {
				wait = &agentaccounts.CapacityWait{Code: tc.code}
			}
			got := diagnoseQueuedAgent(session, now, tc.key, tc.fresh, "claude", "fixture-computer", wait)
			if got.Cause != tc.cause || got.Detail != tc.detail || got.Action != "" {
				t.Fatalf("wrong queued diagnosis: %+v", got)
			}
		})
	}
}
