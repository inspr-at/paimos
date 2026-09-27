// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_provenance_unsupported

package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func openInstructionFile(abs string) (*os.File, error) {
	return openInstructionFileAt(abs, unix.Openat, unix.Readlinkat)
}

// Each open resolves exactly one component against a pinned parent descriptor.
// O_NOFOLLOW applies to EVERY component, including the leaf. Directory or leaf
// name swaps therefore cannot redirect the read through a symlink. Custom
// directory symlinks are deliberately unsupported: callers supply physical paths.
func openInstructionFileAt(abs string, openat func(int, string, int, uint32) (int, error), readlinkat func(int, string, []byte) (int, error)) (*os.File, error) {
	if !filepath.IsAbs(abs) || abs != filepath.Clean(abs) || refusedInstructionPath(abs) {
		return nil, errInstructionPath
	}
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	dir, err := openat(unix.AT_FDCWD, "/", flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, errInstructionPath
	}
	defer func() { _ = unix.Close(dir) }()
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	if runtime.GOOS == "darwin" && (parts[0] == "var" || parts[0] == "tmp") {
		// Read only the root alias itself. An exact known target substitutes its
		// spelling into our no-follow walk; the alias is never followed by open.
		// Changing it after this check cannot redirect any later syscall. Even
		// /private and its children must be real directories. No general-purpose
		// symlink resolution, target opening, or private-store traversal occurs.
		var target [32]byte
		n, linkErr := readlinkat(dir, parts[0], target[:])
		if linkErr == nil {
			if string(target[:n]) != "private/"+parts[0] && string(target[:n]) != "/private/"+parts[0] {
				return nil, errInstructionPath
			}
			parts = append([]string{"private"}, parts...)
		}
	}
	for i, part := range parts {
		mode := flags
		if i < len(parts)-1 {
			mode |= unix.O_DIRECTORY
		}
		fd, err := openat(dir, part, mode, 0)
		if err != nil {
			return nil, errInstructionPath
		}
		if i == len(parts)-1 {
			// A constant name also keeps os.File's errors path-free. The caller
			// owns this fd and must fstat its type/size before reading content.
			return os.NewFile(uintptr(fd), "instruction"), nil
		}
		_ = unix.Close(dir)
		dir = fd
	}
	return nil, errInstructionPath
}
