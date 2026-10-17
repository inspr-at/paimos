// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Keep the owned child in its own process group, but give that group the
// caller's terminal before exec. Merely inheriting the descriptors leaves it
// in the background, where reads can stop it with SIGTTIN or fail with EIO.
func prepareHarnessTerminal(cmd *exec.Cmd) (func() error, error) {
	noop := func() error { return nil }
	input, ok := cmd.Stdin.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return noop, nil
	}
	fd := int(input.Fd())
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return nil, fmt.Errorf("harness run: read stdin terminal foreground group: %w; use a foreground terminal or redirect stdin for a non-interactive command", err)
	}
	if foreground != syscall.Getpgrp() {
		return nil, errors.New("harness run: stdin terminal belongs to another foreground process group; run in the foreground or redirect stdin for a non-interactive command")
	}
	state, err := term.GetState(fd)
	if err != nil {
		return nil, fmt.Errorf("harness run: save stdin terminal settings: %w", err)
	}
	// Notify suppresses job-control stops while the wrapper is in the
	// background, including while reclaiming the terminal. Unlike Ignore, its
	// caught disposition is reset on exec, retaining the child's job control.
	background := make(chan os.Signal, 1)
	signal.Notify(background, syscall.SIGTTOU)
	cmd.SysProcAttr.Foreground = true
	cmd.SysProcAttr.Ctty = fd // Foreground uses the descriptor in the parent.
	return func() error {
		defer signal.Stop(background)
		groupErr := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, foreground)
		stateErr := term.Restore(fd, state)
		return errors.Join(groupErr, stateErr)
	}, nil
}
