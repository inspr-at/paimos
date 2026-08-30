//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package ownedprocess contains the process-group ownership checks shared by
// run-agent and paimos-agentd. A PID is never sufficient proof of ownership.
package ownedprocess

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

func Configure(cmd *exec.Cmd) bool {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return true
}

func Verify(cmd *exec.Cmd, configured bool) error {
	if !configured || cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return errors.New("owned process group is unavailable")
	}
	groupID, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return fmt.Errorf("read spawned process group: %w", err)
	}
	if groupID != cmd.Process.Pid {
		return fmt.Errorf("spawned process group %d does not match pid %d", groupID, cmd.Process.Pid)
	}
	return nil
}

func Signal(cmd *exec.Cmd, force bool) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return errors.New("owned process is unavailable")
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("signal owned process group: %w", err)
	}
	return nil
}
