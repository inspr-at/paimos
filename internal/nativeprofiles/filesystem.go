// SPDX-License-Identifier: AGPL-3.0-only

package nativeprofiles

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Directory is metadata only. Identity is a local device/inode identity, not a
// vendor identifier. Owned means owned by the current OS user.
type Directory struct {
	Mode     fs.FileMode
	Owned    bool
	Identity string
}

// Filesystem intentionally has no Open, Read, Copy, Write or Symlink operation.
// Resolution cannot read credentials, copy login state or materialize links.
type Filesystem interface {
	Lstat(string) (Directory, error)
}

func inspect(files Filesystem, path string, private bool) (Directory, error) {
	if !validPath(path) {
		return Directory{}, ErrDirectory
	}
	// Check every component: Lstat of just the final directory follows parent
	// symlinks, allowing a physically different profile to escape its binding.
	current := string(filepath.Separator)
	var dir Directory
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		var err error
		dir, err = files.Lstat(current)
		if err != nil || !dir.Mode.IsDir() || dir.Mode&fs.ModeSymlink != 0 {
			return Directory{}, ErrDirectory
		}
	}
	if !dir.Owned || dir.Identity == "" || private && dir.Mode.Perm()&0077 != 0 {
		return Directory{}, ErrDirectory
	}
	return dir, nil
}
