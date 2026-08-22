//go:build !darwin && !linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import "os"

// Other platforms fail closed until they have an equivalent descriptor-level
// no-follow implementation. They must not silently weaken result evidence.
func openAgentRunRegularFile(_, _ string) (*os.File, error) {
	return nil, errAgentRunWorktreeEvidence
}

func openAgentRunDirectory(_, _ string) (*os.File, error) {
	return nil, errAgentRunWorktreeEvidence
}

func agentRunNodeAbsent(_, _ string) (bool, error) {
	return false, errAgentRunWorktreeEvidence
}

func readAgentRunSymlink(_, _ string) (string, error) {
	return "", errAgentRunWorktreeEvidence
}

func agentRunPathMutableByRunner(_ string, _ os.FileInfo) bool {
	return true
}
