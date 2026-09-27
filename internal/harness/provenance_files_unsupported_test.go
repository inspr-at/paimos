// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!linux && !darwin) || aeon_test_provenance_unsupported

package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstructionUnsupportedPlatformFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("synthetic fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := CollectInstructionFiles([]string{path})
	if err != errInstructionPath || items != nil {
		t.Fatal("unsupported platform must not hash even a valid file")
	}
}
