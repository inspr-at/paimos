// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package ownedprocess

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestObserverErrorWithLiveChildFailsClosedWithoutBlocking(t *testing.T) {
	// Closing stdin is the fixture's own exit path; no process lookup or
	// unverified group signal is used to clean up the failed observer.
	cmd := exec.Command("sh", "-c", "read finish")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	life := Track(cmd)
	done := make(chan struct{})
	go func() { _ = life.wait(func(int) error { return errors.New("injected waitid failure") }); close(done) }()
	t.Cleanup(func() { _ = in.Close(); <-done })
	unavailable := make(chan bool, 1)
	go func() {
		for life.Verify() == nil {
			time.Sleep(time.Millisecond)
		}
		unavailable <- life.Signal(false) != nil && life.Signal(true) != nil
	}()
	select {
	case rejected := <-unavailable:
		if !rejected {
			t.Fatal("observer failure still allowed process signals")
		}
	case <-time.After(time.Second):
		t.Fatal("ownership operations blocked behind Wait on a live child")
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("failed observer killed the live child")
	}
	select {
	case <-done:
		t.Fatal("child unexpectedly exited")
	default:
	}
}

func TestDelayedSignalRechecksAuthorizationUnderLifetimeLock(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "cancelled"}[cancelled], func(t *testing.T) {
			cmd := exec.Command("sh", "-c", "read finish")
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			Configure(cmd)
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lifetime := Track(cmd)
			done := make(chan struct{})
			go func() { _ = lifetime.Wait(); close(done) }()
			t.Cleanup(func() { _ = in.Close(); <-done })
			other := exec.Command("sh", "-c", "read finish")
			otherIn, err := other.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			Configure(other)
			if err = other.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = otherIn.Close(); _ = other.Wait() })
			expires := time.Now().Add(30 * time.Millisecond)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				expires = time.Now().Add(time.Minute)
			}
			lifetime.mu.Lock()
			started := make(chan struct{})
			result := make(chan error, 1)
			go func() { close(started); result <- lifetime.SignalBefore(ctx, true, expires) }()
			<-started
			if cancelled {
				cancel()
			} else {
				<-time.After(time.Until(expires))
			}
			// Archive is eligible now in the expiry case. Releasing the delayed
			// daemon operation must not allow its stale authorization to signal.
			lifetime.mu.Unlock()
			err = <-result
			if cancelled && !errors.Is(err, context.Canceled) || !cancelled && !errors.Is(err, ErrAuthorizationExpired) {
				t.Fatalf("delayed signal=%v", err)
			}
			if err = cmd.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatal("expired command killed target")
			}
			if err = other.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatal("expired command killed unrelated child")
			}
		})
	}
}

func TestWaitGroupObserverFailureDoesNotSignal(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "read finish")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	life := Track(cmd)
	if err := life.Verify(); err != nil {
		_ = in.Close()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	observed := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- life.waitOwned(func(int) error { close(observed); return errors.New("injected observation failure") }, true)
	}()
	t.Cleanup(func() { _ = in.Close(); <-done })
	<-observed
	// Wait until the failed observer has revoked signaling under the mutex.
	deadline := time.Now().Add(time.Second)
	for life.Verify() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if life.Signal(true) == nil {
		t.Fatal("failed observer retained signal authority")
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("failed group observation killed fixture")
	}
}
