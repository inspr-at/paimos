//go:build (darwin || dragonfly || freebsd || linux || netbsd || openbsd) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import "testing"

func TestInstanceLockAllowsExactlyOneDaemonBeforeRegistryOpen(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireInstanceLock(root, "ppm-lock")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := AcquireInstanceLock(root, "ppm-lock"); err == nil {
		t.Fatal("second daemon acquired the same instance lock")
	}
	other, err := AcquireInstanceLock(root, "ppm-other")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
}
