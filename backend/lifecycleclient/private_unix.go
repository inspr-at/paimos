//go:build (darwin || linux) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadPrivate checks the opened descriptor before reading configuration or the
// reporter API key. Diagnostics contain neither the path nor file contents.
func ReadPrivate(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || limit <= 0 {
		return nil, ErrOwnership
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrOwnership
	}
	f := os.NewFile(uintptr(fd), "runtime-private-input")
	defer f.Close()
	var st unix.Stat_t
	info, e := f.Stat()
	if e != nil || unix.Fstat(fd, &st) != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Size() > limit {
		return nil, ErrOwnership
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return nil, ErrOwnership
	}
	return raw, nil
}
func FileCredential(path string) func() (string, error) {
	return func() (string, error) {
		raw, err := ReadPrivate(path, 4096)
		if err != nil {
			return "", err
		}
		key := strings.TrimSpace(string(raw))
		if key == "" || strings.ContainsAny(key, "\x00\r\n") {
			return "", ErrOwnership
		}
		return key, nil
	}
}
