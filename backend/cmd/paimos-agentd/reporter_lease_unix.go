//go:build darwin || linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/managedharness"
	"golang.org/x/sys/unix"
)

type diskReporterLeaseStore struct {
	directory string
}

func (s *diskReporterLeaseStore) Delete(sessionID string) error {
	if uuid.Validate(sessionID) != nil || filepath.Base(sessionID) != sessionID {
		return errors.New("reporter lease session is invalid")
	}
	dirFD, err := unix.Open(s.directory, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("reporter lease directory is unavailable")
	}
	defer unix.Close(dirFD)
	if err := unix.Unlinkat(dirFD, sessionID, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return errors.New("delete private reporter lease: unavailable")
	}
	if err := unix.Fsync(dirFD); err != nil {
		return errors.New("delete private reporter lease: unavailable")
	}
	return nil
}

func newDiskReporterLeaseStore(root, instance string) (reporterLeaseStore, error) {
	stateDir, err := agentd.InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(stateDir, "reporter-leases")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("reporter lease directory has unsafe mode or type")
	}
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0) // #nosec G304 -- instance-scoped private state directory.
	if err != nil {
		return nil, errors.New("reporter lease directory is unavailable")
	}
	directoryFile := os.NewFile(uintptr(fd), "reporter-lease-directory")
	if directoryFile == nil {
		_ = unix.Close(fd)
		return nil, errors.New("reporter lease directory is unavailable")
	}
	defer directoryFile.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(unix.Geteuid()) || stat.Mode&0o777 != 0o700 {
		return nil, errors.New("reporter lease directory has unsafe ownership or mode")
	}
	if err := sweepReporterLeaseTemps(directoryFile, fd); err != nil {
		return nil, err
	}
	return &diskReporterLeaseStore{directory: directory}, nil
}

func sweepReporterLeaseTemps(directory *os.File, dirFD int) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return errors.New("inspect reporter lease residue: unavailable")
	}
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		parts := strings.Split(strings.TrimPrefix(name, "."), ".")
		if !strings.HasPrefix(name, ".") || len(parts) != 2 || uuid.Validate(parts[0]) != nil || uuid.Validate(parts[1]) != nil {
			continue
		}
		var stat unix.Stat_t
		if unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(unix.Geteuid()) || stat.Nlink != 1 || stat.Mode&0o777 != 0o600 {
			return errors.New("reporter lease residue has unsafe ownership, mode, or type")
		}
		if err := unix.Unlinkat(dirFD, name, 0); err != nil {
			return errors.New("remove reporter lease residue: unavailable")
		}
		removed = true
	}
	if removed && unix.Fsync(dirFD) != nil {
		return errors.New("remove reporter lease residue: unavailable")
	}
	return nil
}

func (s *diskReporterLeaseStore) GetOrCreate(sessionID string) (string, error) {
	if uuid.Validate(sessionID) != nil || filepath.Base(sessionID) != sessionID {
		return "", errors.New("reporter lease session is invalid")
	}
	dirFD, err := unix.Open(s.directory, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0) // #nosec G304 -- previously validated private directory.
	if err != nil {
		return "", errors.New("reporter lease directory is unavailable")
	}
	defer unix.Close(dirFD)
	if value, err := readReporterLeaseAt(dirFD, sessionID); err == nil {
		return value, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	value, err := generateReporterLease()
	if err != nil {
		return "", err
	}
	tempName := "." + sessionID + "." + uuid.NewString()
	fd, err := unix.Openat(dirFD, tempName, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return "", errors.New("create private reporter lease: unavailable")
	}
	file := os.NewFile(uintptr(fd), "reporter-lease")
	cleanup := func() { _ = unix.Unlinkat(dirFD, tempName, 0) }
	if file == nil {
		_ = unix.Close(fd)
		cleanup()
		return "", errors.New("create private reporter lease: unavailable")
	}
	if unix.Fchmod(fd, 0o600) != nil {
		file.Close()
		cleanup()
		return "", errors.New("protect private reporter lease: unavailable")
	}
	if _, err := io.WriteString(file, value+"\n"); err != nil || file.Sync() != nil || file.Close() != nil {
		_ = file.Close()
		cleanup()
		return "", errors.New("persist private reporter lease: unavailable")
	}
	if err := unix.Renameat(dirFD, tempName, dirFD, sessionID); err != nil {
		cleanup()
		return "", errors.New("commit private reporter lease: unavailable")
	}
	if err := unix.Fsync(dirFD); err != nil {
		return "", errors.New("commit private reporter lease: unavailable")
	}
	return value, nil
}

func readReporterLeaseAt(dirFD int, name string) (string, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return "", os.ErrNotExist
		}
		return "", errors.New("open private reporter lease: unavailable or unsafe")
	}
	file := os.NewFile(uintptr(fd), "reporter-lease")
	if file == nil {
		_ = unix.Close(fd)
		return "", errors.New("open private reporter lease: unavailable")
	}
	defer file.Close()
	info, infoErr := file.Stat()
	var stat unix.Stat_t
	statErr := unix.Fstat(fd, &stat)
	if infoErr != nil || statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Uid != uint32(unix.Geteuid()) || stat.Nlink != 1 || info.Size() > 64 {
		return "", errors.New("private reporter lease has unsafe ownership, mode, or type")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil || len(raw) > 64 {
		return "", errors.New("private reporter lease exceeds its bound")
	}
	value := strings.TrimSpace(string(raw))
	if !managedharness.ValidWorkerLease(value) || strings.ContainsAny(string(raw), "\r") {
		return "", errors.New("private reporter lease is invalid")
	}
	return value, nil
}
