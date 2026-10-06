// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
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
