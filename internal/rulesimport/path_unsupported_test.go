// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!linux && !darwin) || aeon_test_rulesimport_unsupported

package rulesimport

import (
	"errors"
	"testing"
)

func TestUnsupportedDoctrineReadFailsClosed(t *testing.T) {
	path := writeDoc(t, t.TempDir(), "AGENTS.md", "synthetic")
	_, _, _, err := readDoctrine(path)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal("unsupported platform did not fail closed")
	}
}
