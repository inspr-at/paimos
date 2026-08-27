//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

package harness

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureProcessGroup places the vendor CLI in its own process group so a
// timeout can take the whole tree down, including the native child that the
// npm `codex` wrapper spawns under its Node parent.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup force-kills the command's process group; an already gone
// group is not an error.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
