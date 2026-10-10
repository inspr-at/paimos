//go:build darwin && !cgo

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"os"
	"os/exec"
	"strconv"
)

func (systemPower) PreventIdleSleep() (func(), error) {
	// The fixed system helper watches this daemon's PID, so even a daemon
	// crash ends the assertion. No-cgo builds cannot call IOPMLib directly.
	cmd := exec.Command("/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}, nil
}
