// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package runtimeconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestStreamGateBoundsIndependentWorkAndReclaimsWaiters(t *testing.T) {
	var gate StreamGate
	var releases []func()
	for i := range MaxConcurrentStreams {
		release, err := gate.Acquire(context.Background(), fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		release, err := gate.Acquire(ctx, "overflow")
		if release != nil {
			release()
		}
		done <- err
	}()
	<-started
	select {
	case <-done:
		t.Fatal("external concurrency limit exceeded")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, release := range releases {
		release()
	}
	releases = nil
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.entries) != 0 || len(gate.slots) != 0 {
		t.Fatal("drained stream gates were retained")
	}
}

func TestStreamGateSameStreamWaitersLeaveCapacityForIndependentWork(t *testing.T) {
	var gate StreamGate
	release, err := gate.Acquire(context.Background(), "busy")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for range MaxConcurrentStreams * 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := gate.Acquire(ctx, "busy")
			if release != nil {
				release()
				t.Error("same stream overlapped")
			}
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		}()
	}
	independent, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	releaseOther, err := gate.Acquire(independent, "healthy")
	if err != nil {
		t.Fatal("same-stream waiters exhausted independent capacity", err)
	}
	releaseOther()
	cancel()
	wg.Wait()
}
