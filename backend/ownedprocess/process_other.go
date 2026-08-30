//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package ownedprocess

import (
	"errors"
	"os"
	"os/exec"
)

func Configure(_ *exec.Cmd) bool { return false }

func Verify(_ *exec.Cmd, _ bool) error {
	return errors.New("owned process groups are unsupported")
}

// Signal may terminate only the exact live os.Process handle returned by
// Start. Group ownership remains unsupported, so no remote control lease is
// advertised and descendants are never guessed from a persisted PID.
func Signal(cmd *exec.Cmd, force bool) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("owned process is unavailable")
	}
	if force {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(os.Interrupt)
}
