//go:build (darwin || dragonfly || freebsd || linux || netbsd || openbsd) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimeconsumer

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func acquireLock(directory string) (func(), error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, ErrAuthority
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !ownedFile(info) {
		return nil, ErrAuthority
	}
	path := filepath.Join(directory, "consumers.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, ErrAuthority
	}
	file := os.NewFile(uintptr(fd), path)
	info, err = file.Stat()
	named, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(info, named) || !ownedFile(info) {
		file.Close()
		return nil, ErrAuthority
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrConflict
		}
		return nil, ErrAuthority
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = file.Close() }, nil
}

func ownedFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Getuid())
}
