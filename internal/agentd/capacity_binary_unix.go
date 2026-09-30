// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package agentd

import (
	"os"

	"golang.org/x/sys/unix"
)

func stampCapacityBinary(path string, f *os.File) (capacityBinaryStamp, bool) {
	var stat unix.Stat_t
	if unix.Fstat(int(f.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return capacityBinaryStamp{}, false
	}
	return capacityBinaryStamp{
		path: path, size: stat.Size, mtime: stat.Mtim.Nano(), ctime: stat.Ctim.Nano(),
		device: uint64(stat.Dev), inode: uint64(stat.Ino),
	}, true
}
