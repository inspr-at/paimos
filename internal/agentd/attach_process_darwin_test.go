// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"errors"
	"os/exec"
	"testing"
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
