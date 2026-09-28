// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_rulesimport_unsupported

package rulesimport

import (
	"errors"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func openNoFollow(path string) (*os.File, error) {
	return openDoctrineAt(path, unix.Openat, unix.Readlinkat)
}

// Based on internal/harness/provenance_files_unix.go. Each lookup resolves one
// component against a pinned parent. Hooks are syscall seams for race tests.
func openDoctrineAt(path string, openat func(int, string, int, uint32) (int, error), readlinkat func(int, string, []byte) (int, error)) (*os.File, error) {
	clean, err := validateDoctrinePath(path)
	if err != nil {
		return nil, err
	}
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	dir, err := openat(unix.AT_FDCWD, "/", flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, ErrProhibitedPath
	}
	defer func() { _ = unix.Close(dir) }()
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if runtime.GOOS == "darwin" && (parts[0] == "var" || parts[0] == "tmp") {
		// Substitute only the exact root system aliases; never follow the alias.
		// /private and every later directory still pass O_DIRECTORY|O_NOFOLLOW.
		var target [32]byte
		n, e := readlinkat(dir, parts[0], target[:])
		if e == nil {
			if string(target[:n]) != "private/"+parts[0] && string(target[:n]) != "/private/"+parts[0] {
				return nil, ErrSymlink
			}
			parts = append([]string{"private"}, parts...)
		}
	}
	for i, part := range parts {
		mode := flags
		if i < len(parts)-1 {
			mode |= unix.O_DIRECTORY
		}
		fd, e := openat(dir, part, mode, 0)
		if e != nil {
			if errors.Is(e, unix.ELOOP) || errors.Is(e, unix.ENOTDIR) {
				return nil, ErrSymlink
			}
			return nil, ErrProhibitedPath
		}
		if i == len(parts)-1 {
			return os.NewFile(uintptr(fd), "doctrine"), nil
		}
		_ = unix.Close(dir)
		dir = fd
	}
	return nil, ErrProhibitedPath
}
