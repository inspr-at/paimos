// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!linux && !darwin) || aeon_test_checkpoint_unsupported

package sessionusage

import "os"

func openCheckpointFile(string) (*os.File, error) {
	return nil, &UsageError{Msg: "safe checkpoint reading unsupported"}
}
