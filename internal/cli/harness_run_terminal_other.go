// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!darwin && !linux) || aeon_test_unsupported

package cli

import "os/exec"

func prepareHarnessTerminal(*exec.Cmd) (func() error, error) {
	// runHarnessCommand refuses platforms without process lifetime tracking
	// before opening a session or configuring a child.
	return func() error { return nil }, nil
}
