//go:build !darwin && !linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"errors"
	"os"
)

func owned(os.FileInfo) bool        { return false }
func privateDir(string, bool) error { return errors.New("runtime platform unsupported") }
func safeFile(string, bool) (os.FileInfo, error) {
	return nil, errors.New("runtime platform unsupported")
}
func readPrivate(string, int64) ([]byte, error) {
	return nil, errors.New("runtime platform unsupported")
}
func writePrivate(string, []byte) error { return errors.New("runtime platform unsupported") }
func syncDir(string) error              { return errors.New("runtime platform unsupported") }

type stateLock struct{}

func lockState(string, string) (*stateLock, error) {
	return nil, errors.New("runtime platform unsupported")
}
func (*stateLock) Close()           {}
func lockHeld(string) (bool, error) { return false, errors.New("runtime platform unsupported") }

func trustedDefinitionOwner(os.FileInfo) bool { return false }

func openUnfollowedRegular(string, os.FileInfo) (*os.File, error) {
	return nil, errors.New("runtime platform unsupported")
}

func writableDirectory(string) bool { return false }
