// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package agentd

import "os"

// Exact-binary capture stays unavailable without a complete file identity.
func stampCapacityBinary(string, *os.File) (capacityBinaryStamp, bool) {
	return capacityBinaryStamp{}, false
}
