// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

import (
	"context"
	"errors"
	"testing"
)

func TestLaunchAdmissionQueueCancelDoesNotLeakSlot(t *testing.T) {
	var queue launchAdmissionQueue
	release, err := queue.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queue.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err=%v", err)
	}
	release()
	second, err := queue.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second()
}
