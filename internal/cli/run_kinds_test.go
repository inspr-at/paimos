// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"strings"
	"testing"
)

func TestRunKindHeartbeatOptions(t *testing.T) {
	t.Setenv("AEON_MODEL", "coordinator-model")
	t.Setenv("AEON_EFFORT", "high")
	for _, tc := range []struct{ harness, generator, command string }{
		{"media", "higgsfield/kling3_0", ""}, {"terminal", "", "ffmpeg"},
	} {
		o := heartbeatOptions{Harness: tc.harness, Generator: tc.generator, CommandLabel: tc.command, OwnerPID: 1, StateDir: t.TempDir(), Project: "AEON", Agent: "worker", Parent: transcriptSessionID, Ticket: "AEON-501", Shape: "ship", Interval: 50}
		if err := o.prepare(); err != nil {
			t.Fatal(err)
		}
		if o.Model != "" || o.Effort != "" {
			t.Fatal("process inherited an AI model")
		}
		for _, tc := range []struct{ field, value, fragment string }{
			{"harness", "higgsfield", "--harness"}, {"phase", "fake", "--phase must be"}, {"activity", "fake", "--activity must be"}, {"role", "fake", "--role"}, {"management", "fake", "--management"},
		} {
			bad := o
			switch tc.field {
			case "harness":
				bad.Harness = tc.value
			case "phase":
				bad.Phase = tc.value
			case "activity":
				bad.Activity = tc.value
			case "role":
				bad.Role = tc.value
			case "management":
				bad.Management = tc.value
			}
			if err := bad.prepare(); err == nil || !strings.Contains(err.Error(), tc.fragment) {
				t.Fatalf("%s: %v", tc.field, err)
			}
		}
	}
}

func TestInvalidRunKindCLIReportsFlagAndValues(t *testing.T) {
	isolate(t)
	for _, command := range []string{"register", "run-heartbeat"} {
		args := []string{"aeon", "harness", command, "--project", "AEON", "--agent", "worker", "--harness", "higgsfield", "--host", "local"}
		if command == "run-heartbeat" {
			args = append(args, "--owner-pid", "1", "--state-dir", t.TempDir())
		}
		code, _, stderr := runCLI(args, "")
		if code != 2 || !strings.Contains(stderr, "--harness") || !strings.Contains(stderr, "codex, claude, pi, cursor, grok, gemini, opencode, media, terminal") {
			t.Fatalf("%s: code %d, %s", command, code, stderr)
		}
	}
}
