// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"errors"
	"golang.org/x/sys/unix"
)

// confirmedLedgerExit never adopts or signals a process. Two read-only kernel
// probes must prove both the recorded root and its verified process group are
// absent. EPERM, PID/group reuse, unknown group provenance and an unpublished
// PID all remain occupied. Escaped descendants are outside ownedprocess scope.
func confirmedLedgerExit(record Record) bool {
	if record.PID < 1 || record.ProcessGroupID != record.PID || record.ProcessStartedAt.IsZero() {
		return false
	}
	return errors.Is(unix.Kill(record.PID, 0), unix.ESRCH) && errors.Is(unix.Kill(-record.ProcessGroupID, 0), unix.ESRCH)
}
