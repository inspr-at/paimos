// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package nativeprofiles

import (
	"fmt"
	"os"
	"syscall"
)

type localFilesystem struct{}

func (localFilesystem) Lstat(path string) (Directory, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Directory{}, ErrDirectory
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Directory{}, ErrDirectory
	}
	return Directory{Mode: info.Mode(), Owned: stat.Uid == uint32(os.Geteuid()),
		Identity: fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)}, nil
}
