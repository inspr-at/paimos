// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package agentd

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func managedOpenAt(parent *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errManagedFile
	}
	return os.NewFile(uintptr(fd), name), nil
}
func managedRoot(root string) (*os.File, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errManagedFile
	}
	// Begin at /, so symlinked ancestors cannot redirect even the workspace root.
	dir, err := os.Open("/")
	if err != nil {
		return nil, errManagedFile
	}
	for _, part := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := managedOpenAt(dir, part, unix.O_RDONLY|unix.O_DIRECTORY)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}
func openManagedDirectory(root, path string) (*os.File, error) {
	return managedDirectory(root, path, false)
}

func managedDirectory(root, path string, create bool) (*os.File, error) {
	rel, err := managedRelative(root, path)
	if err != nil {
		return nil, err
	}
	dir, err := managedRoot(root)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return dir, nil
	}
	for _, part := range strings.Split(rel, "/") {
		next, err := managedOpenAt(dir, part, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil && create {
			// mkdirat and the subsequent no-follow open share the same parent fd.
			// Existing links or a raced replacement fail closed, never get resolved.
			if mkdirErr := unix.Mkdirat(int(dir.Fd()), part, 0700); mkdirErr == nil {
				next, err = managedOpenAt(dir, part, unix.O_RDONLY|unix.O_DIRECTORY)
			}
		}
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}
func openManagedFile(root, path string, write, create bool) (*os.File, error) {
	rel, err := managedRelative(root, path)
	if err != nil || rel == "." {
		return nil, errManagedFile
	}
	dir, err := managedDirectory(root, filepath.Dir(rel), create)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	flags := unix.O_RDONLY
	if write {
		flags = unix.O_RDWR
	}
	if create {
		flags |= unix.O_CREAT
	}
	// O_TRUNC is intentionally absent until fstat has verified nlink and type.
	f, err := managedOpenAt(dir, filepath.Base(rel), flags)
	if err != nil {
		return nil, err
	}
	if err = checkManagedRegular(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func checkManagedRegular(f *os.File) error {
	var stat unix.Stat_t
	if unix.Fstat(int(f.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return errManagedFile
	}
	return nil
}
func openManagedChild(parent *os.File, name string) (*os.File, bool, error) {
	f, err := managedOpenAt(parent, name, unix.O_RDONLY)
	if err != nil {
		return nil, false, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, false, errManagedFile
	}
	return f, info.IsDir(), nil
}
