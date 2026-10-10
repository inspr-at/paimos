// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux || darwin

package doctrine

import (
	"os"

	"golang.org/x/sys/unix"
)

// mirrorReadOnly checks the actual open inode's mount, not writable mode bits.
// ST_RDONLY (Linux) and MNT_RDONLY (Darwin) both use bit 0 in statfs.Flags.
func mirrorReadOnly(f *os.File) bool {
	var stat unix.Statfs_t
	return unix.Fstatfs(int(f.Fd()), &stat) == nil && stat.Flags&1 != 0
}

func openMirrorNode(root *os.Root, path string, directory bool, checks ...func(*os.File) bool) (*os.File, error) {
	flags := os.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	f, err := root.OpenFile(path, flags, 0)
	if err != nil {
		return nil, gitFail("the host mirror path or commit marker is unavailable; symlinks are refused")
	}
	info, err := f.Stat()
	if err != nil || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		f.Close()
		return nil, gitFail("the host mirror contains an unsupported file")
	}
	check := mirrorReadOnly
	if len(checks) > 0 && checks[0] != nil {
		check = checks[0]
	}
	if !check(f) {
		f.Close()
		return nil, gitFail("the host mirror must be mounted read-only")
	}
	return f, nil
}

func openMirrorFile(root *os.Root, path string, checks ...func(*os.File) bool) (*os.File, error) {
	return openMirrorNode(root, path, false, checks...)
}
func openMirrorDirectory(root *os.Root, path string, checks ...func(*os.File) bool) (*os.File, error) {
	return openMirrorNode(root, path, true, checks...)
}
