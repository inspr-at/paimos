// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!linux && !darwin) || aeon_test_provenance_unsupported

package harness

import "os"

// Never fall back to pathname-based opens on an unsupported platform.
func openInstructionFile(string) (*os.File, error) {
	return nil, errInstructionPath
}
