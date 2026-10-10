// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/hostcapacity"
)

// These fixed read-only probes share one deadline and hostProbe's output cap.
// No service, power, encryption or login configuration is read from disk or
// changed. Only enums leave the Mac, never raw probe output or user identifiers.
func sampleUnattended(ctx context.Context) *hostcapacity.UnattendedSignals {
	return probeUnattended(ctx, runtime.GOOS, os.Getuid(), hostProbe)
}

func probeUnattended(ctx context.Context, platform string, uid int, probe func(context.Context, string, ...string) string) *hostcapacity.UnattendedSignals {
	s := &hostcapacity.UnattendedSignals{LoginSession: "unknown", IdleSleep: "unknown", FileVault: "unknown"}
	if platform != "darwin" {
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	consoleUID, err := strconv.Atoi(strings.TrimSpace(probe(ctx, "/usr/bin/stat", "-f", "%u", "/dev/console")))
	if err == nil && consoleUID >= 0 {
		s.LoginSession = "required"
		if uid > 0 && consoleUID == uid {
			s.LoginSession = "ready"
		}
	}
	// Match only the system idle timer, never disksleep/displaysleep or an
	// incidental temporary assertion. Missing/duplicate settings stay unknown.
	settings := probe(ctx, "/usr/bin/pmset", "-g")
	seenSleep := false
	for _, line := range strings.Split(settings, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "sleep" {
			continue
		}
		if seenSleep {
			s.IdleSleep = "unknown"
			break
		}
		seenSleep = true
		minutes, err := strconv.ParseUint(fields[1], 10, 32)
		if err == nil {
			s.IdleSleep = "enabled"
			if minutes == 0 {
				s.IdleSleep = "disabled"
			}
		}
	}
	switch strings.TrimSpace(probe(ctx, "/usr/bin/fdesetup", "status")) {
	case "FileVault is On.":
		s.FileVault = "on"
	case "FileVault is Off.":
		s.FileVault = "off"
	}
	return s
}
