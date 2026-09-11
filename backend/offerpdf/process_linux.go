//go:build linux

// SPDX-License-Identifier: AGPL-3.0-only
package offerpdf

import (
	"os/exec"
	"syscall"
)

func configureRendererProcess(cmd *exec.Cmd) {
	// Keep chromedp's normal parent-death cleanup while using a clean browser environment.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
