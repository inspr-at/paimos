//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"os"
)

// processAlive cannot observe another process on this platform, so the helper
// stops instead of heartbeating an owner it cannot see.
func processAlive(int) bool {
	return false
}

func openNoFollow(string) (*os.File, error) {
	return nil, errors.New("name sources require a unix host")
}
