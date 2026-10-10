//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import "os"

func openSubagentParent(string) (*os.File, error) { return nil, errHeartbeatState }

func openSubagentDir(*os.File, string, bool) (*os.File, error) {
	return nil, errHeartbeatState
}

func lockSubagentDir(*os.File) (heartbeatHold, error) {
	return heartbeatHold{}, errHeartbeatState
}
