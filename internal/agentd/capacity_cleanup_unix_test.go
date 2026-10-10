// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package agentd

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
)

// Risk: a finalizer can revoke signaling before publishing its cleanup result.
// Cancellation must join that outcome, preserving uncertainty when it never
// arrives rather than immediately misclassifying the capture as a launch error.
func TestCapacityCancellationWaitsForCleanupOutcome(t *testing.T) {
	for _, outcome := range []string{"confirmed", "unconfirmed", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			p, r, w := heldWire(t)
			if outcome == "unconfirmed" {
				// The lifetime already rejects signals, but its final waiter has
				// not published. No real process or unverified PID is signaled.
				p.waitDone = make(chan struct{})
			} else if outcome == "failed" {
				p.waitErr = ownedprocess.ErrCleanupUnconfirmed
			}
			go p.read(r)
			a := NewCodexAdapter("", map[string]string{"local": privateCapacityHome(t)})
			a.SetExpectedEmails(map[string]string{"local": "fixture@example.test"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan CapacityCapture, 1)
			go func() {
				done <- a.captureCapacityResult(ctx, "local", func(string) (*wireProcess, error) {
					// Cancel after the launch seam: cancellation before launch
					// would bypass the cleanup path this test must exercise.
					cancel()
					return p, nil
				})
			}()
			want := "timeout"
			if outcome == "failed" {
				want = "launch_failed"
			}
			select {
			case got := <-done:
				if got.Result != want || got.CleanupUnconfirmed != (outcome != "confirmed") || len(got.Readings) != 0 {
					t.Fatalf("cleanup outcome lost: %+v; want result %s, unconfirmed %t", got, want, outcome != "confirmed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("capture cleanup never joined")
			}
			assertReaderReleased(t, p, r, w)
		})
	}
}

func TestCapacityCancellationKillsOwnedProcessAndDescendant(t *testing.T) {
	fixture := processtest.New(t)
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	a := NewCodexAdapter(shell, map[string]string{"local": privateCapacityHome(t)})
	a.SetExpectedEmails(map[string]string{"local": "fixture@example.test"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan CapacityCapture, 1)
	go func() {
		done <- a.captureCapacityResult(ctx, "local", func(home string) (*wireProcess, error) {
			return launchWire(shell, []string{"-c", fixture.Script}, home, nil, "jsonrpc", func(AdapterEvent) {})
		})
	}()
	// This barrier witnesses a live descendant, not merely a forked root. The
	// child holds an independent descriptor, so closing stdout cannot fake exit.
	fixture.Ready(t)
	cancel()
	select {
	case got := <-done:
		if got.Result != "timeout" || got.CleanupUnconfirmed || len(got.Readings) != 0 {
			t.Fatal("cancellation/cleanup evidence lost", got.Result, got.CleanupUnconfirmed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("capture cleanup never joined")
	}
	fixture.AssertExited(t)
}
