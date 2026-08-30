//go:build (darwin || dragonfly || freebsd || linux || netbsd || openbsd) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type InstanceLock struct{ file *os.File }

// AcquireInstanceLock is taken before the registry is opened, so a losing
// daemon cannot rewrite live rows to ownership_lost or steal the Unix socket.
func AcquireInstanceLock(root, instance string) (*InstanceLock, error) {
	dir, err := InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		return nil, errors.New("agentd lock directory is unsafe")
	}
	path := filepath.Join(dir, "agentd.lock")
	if info, statErr := os.Lstat(path); statErr == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600) {
		return nil, errors.New("agentd lock file is unsafe")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- fixed beneath private per-instance directory.
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !os.SameFile(info, pathInfo) {
		file.Close()
		return nil, errors.New("agentd lock file changed during open")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("paimos-agentd is already running for this PPM instance")
	}
	return &InstanceLock{file: file}, nil
}

func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return errors.Join(err, l.file.Close())
}
