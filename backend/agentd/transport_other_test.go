//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"testing"
)

func TestUnsupportedPlatformTransportAndLockFailClosed(t *testing.T) {
	if _, err := NewClient("/tmp/agentd.sock"); err == nil {
		t.Fatal("unsupported transport was enabled")
	}
	if err := Serve(context.Background(), "/tmp/agentd.sock", nil); err == nil {
		t.Fatal("unsupported server was enabled")
	}
	if _, err := AcquireInstanceLock(t.TempDir(), "ppm"); err == nil {
		t.Fatal("unsupported instance lock was enabled")
	}
}
