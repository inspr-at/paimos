// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAttachDarwinExitedProcessIsKernelConfirmed(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := observeAttachProcess(pid); !errors.Is(err, errAttachExited) {
		t.Fatalf("exited process not distinguished: %v", err)
	}
}

func TestAttachDarwinMissingIdentityRequiresESRCH(t *testing.T) {
	for _, kind := range []string{"PID mismatch", "sysctl EIO", "sysctl denied", "empty result"} {
		for _, probeErr := range []error{nil, syscall.EPERM, syscall.EIO, syscall.ESRCH} {
			t.Run(fmt.Sprintf("%s/kill=%v", kind, probeErr), func(t *testing.T) {
				first := &unix.KinfoProc{}
				first.Proc.P_pid = 123
				var readErr error
				switch kind {
				case "PID mismatch":
					first.Proc.P_pid = 456
					first.Proc.P_stat = 5 // A different zombie proves nothing.
				case "sysctl EIO":
					readErr = syscall.EIO
				case "sysctl denied":
					readErr = syscall.EPERM
				case "empty result":
					first = nil
				}
				probed := false
				err := checkAttachDarwinProcess(123, first, readErr, func() error { probed = true; return probeErr })
				if !probed || err == nil || errors.Is(err, errAttachExited) != errors.Is(probeErr, syscall.ESRCH) {
					t.Fatalf("unconfirmed process exit: %v", err)
				}
			})
		}
	}
}
