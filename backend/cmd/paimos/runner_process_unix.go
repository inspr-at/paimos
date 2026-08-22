//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) bool {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return true
}

func verifyOwnedProcess(cmd *exec.Cmd, groupConfigured bool) error {
	if !groupConfigured || cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
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

func signalOwnedProcess(cmd *exec.Cmd, force bool) error {
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
