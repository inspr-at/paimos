//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentdwire

import (
	"context"
	"testing"
)

func TestUnsupportedPlatformManagedDeliveryFailsClosed(t *testing.T) {
	if _, err := Steer(context.Background(), "/tmp/agentd.sock", "019d1234-1234-7123-8123-123456789abc", ControlRequest{CorrelationID: "delivery"}); err == nil {
		t.Fatal("unsupported platform enabled managed delivery")
	}
}
