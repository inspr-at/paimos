//go:build darwin || linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openAgentRunParent walks each directory through no-follow descriptors. This
// keeps an untracked path inside the captured repository even if a directory is
// concurrently exchanged for a symlink.
func openAgentRunParent(repoRoot, rel string) (int, string, error) {
	rootFD, err := unix.Open(repoRoot, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0) // #nosec G304 -- bounded local repository selected by the operator.
	if err != nil {
		return -1, "", errAgentRunWorktreeEvidence
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		_ = unix.Close(rootFD)
		return -1, "", errAgentRunWorktreeEvidence
	}
	parentFD := rootFD
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			_ = unix.Close(parentFD)
			return -1, "", errAgentRunWorktreeEvidence
		}
		nextFD, openErr := unix.Openat(parentFD, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		_ = unix.Close(parentFD)
		if openErr != nil {
			return -1, "", errAgentRunWorktreeEvidence
		}
		parentFD = nextFD
	}
	return parentFD, parts[len(parts)-1], nil
}

func openAgentRunRegularFile(repoRoot, rel string) (*os.File, error) {
	parentFD, name, err := openAgentRunParent(repoRoot, rel)
	if err != nil {
		return nil, errAgentRunWorktreeEvidence
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errAgentRunWorktreeEvidence
	}
	file := os.NewFile(uintptr(fd), "implementation-evidence")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errAgentRunWorktreeEvidence
	}
	return file, nil
}

func openAgentRunDirectory(repoRoot, rel string) (*os.File, error) {
	parentFD, name, err := openAgentRunParent(repoRoot, rel)
	if err != nil {
		return nil, errAgentRunWorktreeEvidence
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, errAgentRunWorktreeEvidence
	}
	directory := os.NewFile(uintptr(fd), "implementation-evidence-directory")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, errAgentRunWorktreeEvidence
	}
	return directory, nil
}

func agentRunNodeAbsent(repoRoot, rel string) (bool, error) {
	rootFD, err := unix.Open(repoRoot, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0) // #nosec G304 -- bounded local repository selected by the operator.
	if err != nil {
		return false, errAgentRunWorktreeEvidence
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		_ = unix.Close(rootFD)
		return false, errAgentRunWorktreeEvidence
	}
	parentFD := rootFD
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			_ = unix.Close(parentFD)
			return false, errAgentRunWorktreeEvidence
		}
		nextFD, openErr := unix.Openat(parentFD, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		_ = unix.Close(parentFD)
		if openErr == unix.ENOENT || openErr == unix.ENOTDIR {
			return true, nil
		}
		if openErr != nil {
			return false, errAgentRunWorktreeEvidence
		}
		parentFD = nextFD
	}
	defer unix.Close(parentFD)
	var stat unix.Stat_t
	err = unix.Fstatat(parentFD, parts[len(parts)-1], &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return false, nil
	}
	if err == unix.ENOENT || err == unix.ENOTDIR {
		return true, nil
	}
	return false, errAgentRunWorktreeEvidence
}

func readAgentRunSymlink(repoRoot, rel string) (string, error) {
	parentFD, name, err := openAgentRunParent(repoRoot, rel)
	if err != nil {
		return "", errAgentRunWorktreeEvidence
	}
	defer unix.Close(parentFD)
	buffer := make([]byte, maxAgentRunWorktreePath+1)
	n, err := unix.Readlinkat(parentFD, name, buffer)
	if err != nil || n <= 0 || n > maxAgentRunWorktreePath {
		return "", errAgentRunWorktreeEvidence
	}
	return string(buffer[:n]), nil
}

// A same-UID owner can restore write permission with chmod even when the mode
// currently looks read-only. Access also covers writable group/world bits and
// ACL grants for paths owned by someone else.
func agentRunPathMutableByRunner(path string, info os.FileInfo) bool {
	var stat unix.Stat_t
	effectiveUID := unix.Geteuid()
	if effectiveUID < 0 || uint64(effectiveUID) > math.MaxUint32 {
		return true
	}
	runnerUID := uint32(effectiveUID)
	if unix.Lstat(path, &stat) != nil || info.Mode()&os.ModeSymlink != 0 || stat.Uid == runnerUID {
		return true
	}
	return unix.Access(path, unix.W_OK) == nil
}
