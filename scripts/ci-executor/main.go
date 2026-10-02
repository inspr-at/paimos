// SPDX-License-Identifier: AGPL-3.0-only

// ci-executor is provisioned as /aeon-init, never run from a PR checkout.
package main

import (
	"github.com/inspr-at/paimos/internal/ciexecutor"
	"os"
)

func main() {
	// Failure emits no candidate stdout or credential-bearing diagnostics. The
	// host times out a guest without a complete typed terminal result.
	if err := ciexecutor.Init(); err != nil {
		os.Exit(1)
	}
}
