// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/hostcapacity"
)

// Risk: probe failures or a display-sleep timer produce false ready, or the
// daemon changes OPS-owned settings instead of reporting content-free enums.
func TestUnattendedProbesReadOnlyAndUnknownOnFailure(t *testing.T) {
	wantCommands := []string{"/usr/bin/stat -f %u /dev/console", "/usr/bin/pmset -g", "/usr/bin/fdesetup status"}
	for _, tc := range []struct {
		name, platform, console, power, vault string
		want                                  hostcapacity.UnattendedSignals
	}{
		{"ready", "darwin", "501\n", "Currently in use:\n sleep 0\n displaysleep 10\n", "FileVault is On.\n", hostcapacity.UnattendedSignals{LoginSession: "ready", IdleSleep: "disabled", FileVault: "on"}},
		{"login window", "darwin", "0\n", "sleep 0", "FileVault is Off.", hostcapacity.UnattendedSignals{LoginSession: "required", IdleSleep: "disabled", FileVault: "off"}},
		{"other person", "darwin", "502", "sleep 10 (sleep prevented by powerd)", "FileVault is On.", hostcapacity.UnattendedSignals{LoginSession: "required", IdleSleep: "enabled", FileVault: "on"}},
		{"failures", "darwin", "", "", "", hostcapacity.UnattendedSignals{LoginSession: "unknown", IdleSleep: "unknown", FileVault: "unknown"}},
		{"display only", "darwin", "501", "displaysleep 0\n disksleep 0", "Encryption in progress", hostcapacity.UnattendedSignals{LoginSession: "ready", IdleSleep: "unknown", FileVault: "unknown"}},
		{"malformed", "darwin", "garbage", "sleep -1", "unexpected", hostcapacity.UnattendedSignals{LoginSession: "unknown", IdleSleep: "unknown", FileVault: "unknown"}},
		{"duplicate", "darwin", "501", "sleep 0\n sleep 5", "FileVault is Off.", hostcapacity.UnattendedSignals{LoginSession: "ready", IdleSleep: "unknown", FileVault: "off"}},
		{"unsupported", "linux", "501", "sleep 0", "FileVault is Off.", hostcapacity.UnattendedSignals{LoginSession: "unknown", IdleSleep: "unknown", FileVault: "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var commands []string
			var sharedDeadline time.Time
			got := probeUnattended(t.Context(), tc.platform, 501, func(ctx context.Context, path string, args ...string) string {
				deadline, ok := ctx.Deadline()
				if !ok || !sharedDeadline.IsZero() && deadline != sharedDeadline {
					t.Fatal("probes lack one shared deadline")
				}
				sharedDeadline = deadline
				commands = append(commands, path+" "+strings.Join(args, " "))
				switch path {
				case "/usr/bin/stat":
					return tc.console
				case "/usr/bin/pmset":
					return tc.power
				case "/usr/bin/fdesetup":
					return tc.vault
				}
				t.Fatal("unexpected command", path)
				return ""
			})
			if *got != tc.want || got.Validate() != nil {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if tc.platform == "darwin" && !reflect.DeepEqual(commands, wantCommands) || tc.platform != "darwin" && len(commands) != 0 {
				t.Fatal("unexpected commands", commands)
			}
		})
	}
}
