//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package ownedprocess

import (
	"os/exec"
	"strings"
	"testing"
)

func TestUnsupportedPlatformWithholdsGroupOwnership(t *testing.T) {
	cmd := exec.Command("not-started")
	if Configure(cmd) {
		t.Fatal("unsupported platform configured process-group ownership")
	}
	if err := Verify(cmd, false); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("verify error=%v", err)
	}
}
