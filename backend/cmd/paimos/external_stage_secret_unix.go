//go:build darwin || linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// openExternalStageSecretFile uses a descriptor-level no-follow open so a
// path cannot be swapped to a symlink between inspection and use. The raw
// credential file is deliberately stricter than ordinary CLI input: it must
// be regular, owned by the effective user, single-linked, and owner-only.
func openExternalStageSecretFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0) // #nosec G304 -- explicit protected credential source chosen by the operator.
	if err != nil {
		return nil, errors.New("open handoff credential file: unavailable or unsafe")
	}
	file := os.NewFile(uintptr(fd), "handoff-credential")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open handoff credential file: unavailable or unsafe")
	}
	info, infoErr := file.Stat()
	var stat unix.Stat_t
	statErr := unix.Fstat(fd, &stat)
	if infoErr != nil || statErr != nil {
		_ = file.Close()
		return nil, errors.New("inspect handoff credential file: unavailable or unsafe")
	}
	effectiveUID, ok := nonNegativeIntToUint64(unix.Geteuid())
	if !ok {
		_ = file.Close()
		return nil, errors.New("inspect handoff credential file: unavailable or unsafe")
	}
	if err := validateExternalStageSecretMetadata(info.Mode(), uint64(stat.Uid), effectiveUID, uint64(stat.Nlink)); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func nonNegativeIntToUint64(value int) (uint64, bool) {
	if value < 0 {
		return 0, false
	}
	return uint64(value), true
}

func syncExternalStageOutputDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path)) // #nosec G304 -- parent of the explicit credential output path.
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
