// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux && !aeon_test_unsupported

package ownedprocess

import "golang.org/x/sys/unix"

const waitObservationSupported = true

func observeExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
	}
}

func emptyExitedGroup(int, error) bool { return false }
