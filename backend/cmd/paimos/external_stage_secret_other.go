//go:build !darwin && !linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"errors"
	"os"
	"path/filepath"
)

// The portable fallback rejects symlinks before open and verifies that the
// resulting descriptor is the same regular owner-only file. Platforms without
// portable uid/nlink descriptor fields cannot enforce those two Unix checks;
// supported macOS/Linux releases use the O_NOFOLLOW implementation instead.
func openExternalStageSecretFile(path string) (*os.File, error) {
	before, err := os.Lstat(path) // #nosec G304 -- explicit protected credential source chosen by the operator.
	if err != nil || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("open handoff credential file: unavailable or unsafe")
	}
	file, err := os.Open(path) // #nosec G304 -- checked explicit credential source; descriptor is verified below.
	if err != nil {
		return nil, errors.New("open handoff credential file: unavailable or unsafe")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("handoff credential file must be a regular owner-only file")
	}
	return file, nil
}

func syncExternalStageOutputDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path)) // #nosec G304 -- parent of the explicit credential output path.
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
