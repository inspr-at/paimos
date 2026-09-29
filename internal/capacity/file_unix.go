// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux || darwin

package capacity

import (
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

// Pin each directory and refuse symlinks and FIFOs, including replacement
// races. Callers validate the opened descriptor with fstat before reading.
func openReadingFile(abs string) (*os.File, error) {
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	dir, err := unix.Open("/", flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dir) }()
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for i, part := range parts {
		mode := flags
		if i < len(parts)-1 {
			mode |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(dir, part, mode, 0)
		if err != nil {
			return nil, err
		}
		if i == len(parts)-1 {
			return os.NewFile(uintptr(fd), "capacity-stream"), nil
		}
		_ = unix.Close(dir)
		dir = fd
	}
	return nil, os.ErrInvalid
}
