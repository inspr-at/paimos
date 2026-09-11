//go:build !linux

// SPDX-License-Identifier: AGPL-3.0-only
package offerpdf

import "os/exec"

func configureRendererProcess(_ *exec.Cmd) {}
