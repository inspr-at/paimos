// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_checkpoint_unsupported

package sessionusage

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openCheckpointFile(abs string) (*os.File, error) {
	return openCheckpointFileAt(abs, unix.Openat)
}

// Physical paths only. Every name resolves relative to a pinned directory;
// neither ancestor nor leaf replacement can redirect an open through a symlink.
// NONBLOCK prevents a swapped FIFO from blocking before the caller's fstat.
func openCheckpointFileAt(abs string, openat func(int, string, int, uint32) (int, error)) (*os.File, error) {
	if !filepath.IsAbs(abs) || abs != filepath.Clean(abs) {
		return nil, &UsageError{Msg: "checkpoint path invalid"}
	}
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	dir, err := openat(unix.AT_FDCWD, "/", flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, &UsageError{Msg: "checkpoint unreadable"}
	}
	defer func() { _ = unix.Close(dir) }()
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for i, part := range parts {
		mode := flags
		if i < len(parts)-1 {
			mode |= unix.O_DIRECTORY
		}
		fd, err := openat(dir, part, mode, 0)
		if err != nil {
			return nil, &UsageError{Msg: "checkpoint unreadable"}
		}
		if i == len(parts)-1 {
			return os.NewFile(uintptr(fd), "checkpoint"), nil
		}
		_ = unix.Close(dir)
		dir = fd
	}
	return nil, &UsageError{Msg: "checkpoint unreadable"}
}
