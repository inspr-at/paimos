// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package agentd

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
)

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
