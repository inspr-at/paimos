// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!linux && !darwin) || aeon_test_checkpoint_unsupported

package sessionusage

import "testing"

func TestCheckpointUnsupported(t *testing.T) {
	if f, err := openCheckpointFile("/synthetic/checkpoint.json"); f != nil || err == nil {
		t.Fatal("unsupported traversal did not fail closed")
	}
}
