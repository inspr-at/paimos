// SPDX-License-Identifier: AGPL-3.0-only
//go:build aeon_test_unsupported || (!linux && !darwin)

package agentd

import (
	"strings"
	"testing"
)

func TestUnsupportedLifetimeRejectsLaunchBeforeExec(t *testing.T) {
	_, err := launchWire("/path/that/must/not/be/executed", nil, t.TempDir(), nil, "test", nil)
	if err == nil || !strings.Contains(err.Error(), "safe child lifetime observation unsupported") {
		t.Fatalf("unsupported launch did not fail before executable lookup: %v", err)
	}
}
