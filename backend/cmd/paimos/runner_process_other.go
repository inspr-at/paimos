//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

package main

import (
	"os/exec"

	"github.com/inspr-at/paimos/backend/ownedprocess"
)

func configureProcessGroup(cmd *exec.Cmd) bool { return ownedprocess.Configure(cmd) }

func verifyOwnedProcess(cmd *exec.Cmd, configured bool) error {
	return ownedprocess.Verify(cmd, configured)
}

func signalOwnedProcess(cmd *exec.Cmd, force bool) error { return ownedprocess.Signal(cmd, force) }
