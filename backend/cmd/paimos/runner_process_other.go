//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package main

import (
	"errors"
	"os"
	"os/exec"
)

func configureProcessGroup(_ *exec.Cmd) bool { return false }

func verifyOwnedProcess(_ *exec.Cmd, _ bool) error {
	return errors.New("owned process groups are unsupported")
}

func signalOwnedProcess(cmd *exec.Cmd, force bool) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("owned process is unavailable")
	}
	if force {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(os.Interrupt)
}
