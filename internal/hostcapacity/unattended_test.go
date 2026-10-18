// SPDX-License-Identifier: AGPL-3.0-only
package hostcapacity

import (
	"strings"
	"testing"
	"time"
)

// Risk: asleep/unreachable/old helpers are counted as available, or FileVault
// reboot constraints incorrectly prevent work on a currently logged-in Mac.
func TestUnattendedReadinessFreshnessAndRebootConstraints(t *testing.T) {
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	stale, future, edge := now.Add(-time.Minute-time.Nanosecond), now.Add(time.Nanosecond), now.Add(-time.Minute)
	ready := UnattendedSignals{LoginSession: "ready", IdleSleep: "disabled", FileVault: "on"}
	cases := []struct {
		name, platform, state string
		signals               *UnattendedSignals
		at                    *time.Time
		reason                string
	}{
		{"ready with FileVault", "darwin", "connected", &ready, &now, ""},
		{"freshness boundary", "darwin", "connected", &ready, &edge, ""},
		{"no report", "darwin", "connected", nil, nil, "unattended_unreported"},
		{"old helper", "darwin", "connected", nil, &now, "unattended_signals_unknown"},
		{"asleep or rebooted", "darwin", "connected", &ready, &stale, "unattended_host_unreachable"},
		{"future timestamp", "darwin", "connected", &ready, &future, "unattended_host_unreachable"},
		{"revoked", "darwin", "revoked", &ready, &now, "unattended_computer_disconnected"},
		{"draining", "darwin", "draining", &ready, &now, "unattended_computer_disconnected"},
		{"unsupported", "linux", "connected", &ready, &now, "unattended_platform_unsupported"},
	}
	for _, tc := range []struct{ field, value, reason string }{
		{"login", "required", "unattended_login_required"},
		{"login", "unknown", "unattended_login_unknown"},
		{"sleep", "enabled", ""},
		{"sleep", "unknown", "unattended_sleep_unknown"},
		{"login", "", "unattended_signals_unknown"},
		{"sleep", strings.Repeat("x", 4096), "unattended_signals_unknown"},
		{"filevault", "garbage", "unattended_signals_unknown"},
	} {
		s := ready
		switch tc.field {
		case "login":
			s.LoginSession = tc.value
		case "sleep":
			s.IdleSleep = tc.value
		case "filevault":
			s.FileVault = tc.value
		}
		cases = append(cases, struct {
			name, platform, state string
			signals               *UnattendedSignals
			at                    *time.Time
			reason                string
		}{tc.field + tc.value, "darwin", "connected", &s, &now, tc.reason})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := EvaluateUnattended(tc.platform, tc.state, tc.signals, tc.at, now)
			if v.Reason != tc.reason || (v.Status == "ready") != (tc.reason == "") || v.Message == "" {
				t.Fatalf("wrong availability: %+v; want %s", v, tc.reason)
			}
			if tc.platform == "darwin" && tc.signals != nil && tc.signals.FileVault == "on" && v.AfterRebootReason != "unattended_filevault_after_reboot" {
				t.Fatal("lost reboot constraint", v)
			}
			if tc.reason == "" && tc.signals.IdleSleep == "enabled" && !strings.Contains(v.Message, "idle sleep is enabled") {
				t.Fatal("enabled idle sleep lost its informational risk", v)
			}
			if tc.reason == "unattended_host_unreachable" && !strings.Contains(v.Message, "asleep, lid closed or waiting for login") {
				t.Fatal("unreachable Mac lost plain sleep/login wording", v)
			}
			if tc.reason == "unattended_login_required" && !strings.Contains(v.Message, "Waiting for login") {
				t.Fatal("missing plain waiting-for-login wording", v)
			}
			// Ordinary admission ignores unattended readiness, even when the
			// new report tells future routine dispatch to wait.
			if reason, _ := Evaluate(Default(), &Signals{Unattended: tc.signals}, tc.at, 0, now); reason != "" {
				t.Fatal("report changed ordinary admission", reason)
			}
		})
	}
	ready.FileVault = "off"
	if v := EvaluateUnattended("darwin", "connected", &ready, &now, now); v.Status != "ready" || v.AfterRebootReason != "unattended_login_after_reboot" {
		t.Fatal("FileVault off invented unattended reboot recovery", v)
	}
}
