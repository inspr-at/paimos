//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

package main

import (
	"os/exec"

	"github.com/inspr-at/paimos/backend/ownedprocess"
)

func configureProcessGroup(cmd *exec.Cmd) bool { return ownedprocess.Configure(cmd) }

func verifyOwnedProcess(cmd *exec.Cmd, groupConfigured bool) error {
	return ownedprocess.Verify(cmd, groupConfigured)
}

func signalOwnedProcess(cmd *exec.Cmd, force bool) error { return ownedprocess.Signal(cmd, force) }
