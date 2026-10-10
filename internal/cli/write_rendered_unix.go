//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func writeRenderedRelative(workspace, rel, body string) error {
	if !filepath.IsLocal(rel) || rel == "." {
		return fmt.Errorf("render %s: expected a file within the workspace", rel)
	}
	dir, err := openRenderedDir(workspace, filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer dir.Close()
	return writeRenderedAt(dir, filepath.Base(rel), body)
}

// Resolve the operator-chosen workspace root once, then open it and create/open
// each child relative to the held parent. A symlink planted before a descendant
// is opened is refused; replacing an opened component cannot redirect later
// operations to the symlink's target.
func openRenderedDir(workspace, rel string) (*os.File, error) {
	if !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("render directory %s escapes the workspace", rel)
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve render workspace %s: %w", workspace, err)
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open render workspace %s: %w", workspace, err)
	}
	for _, name := range strings.Split(filepath.Clean(rel), string(os.PathSeparator)) {
		if name == "." {
			continue
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			if err = unix.Mkdirat(fd, name, 0o750); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return nil, fmt.Errorf("mkdir render directory %s: %w", name, err)
			}
			// Another writer may have created the entry; always open it with
			// O_NOFOLLOW even after a successful mkdir.
			next, err = unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("open render directory %s without following symlinks: %w", name, err)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), filepath.Join(root, rel)), nil
}

func writeRenderedAt(dir *os.File, name, body string) error {
	dirfd := int(dir.Fd())
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return fmt.Errorf("render %s: refusing symlink destination", name)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("stat render destination %s: %w", name, err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("render temporary name: %w", err)
	}
	tmp := ".paimos-render-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(dirfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create render temporary file: %w", err)
	}
	defer unix.Unlinkat(dirfd, tmp, 0)
	f := os.NewFile(uintptr(fd), tmp)
	defer f.Close()
	if err := fillRenderedTemp(f, name, body); err != nil {
		return err
	}
	// Rename and cleanup use the same directory descriptor as creation, even
	// if an ancestor has since been replaced. Rename never follows a final link.
	if err := unix.Renameat(dirfd, tmp, dirfd, name); err != nil {
		return fmt.Errorf("rename %s: %w", name, err)
	}
	return nil
}
