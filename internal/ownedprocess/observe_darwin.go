// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin && !aeon_test_unsupported

package ownedprocess

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const waitObservationSupported = true

func observeExit(pid int) error {
	// Darwin waitid(P_PID=1) takes siginfo_t; only the exit notification is
	// needed. This aligned buffer is larger than either Darwin ABI siginfo_t.
	var info [32]uint64
	for {
		_, _, err := syscall.Syscall6(unix.SYS_WAITID, 1, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), unix.WEXITED|unix.WNOWAIT, 0, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != 0 {
			return err
		}
		return nil
	}
}

// Darwin returns EPERM (not ESRCH) when a group contains only zombies. Only
// accept that result after inspecting this still-reserved, verified group.
// This queries process state, never process arguments or environment.
func emptyExitedGroup(pid int, signalErr error) bool {
	if !errors.Is(signalErr, syscall.EPERM) {
		return false
	}
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if err != nil {
		return false
	}
	const zombie = 5 // Darwin sys/proc.h: SZOMB
	for _, member := range members {
		if member.Proc.P_stat != zombie {
			return false
		}
	}
	return true
}
