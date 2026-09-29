// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"testing"
	"time"
)

func TestCodexShutdownCancelsIdleWait(t *testing.T) {
	f := newCodexLifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.proc.bindLifetime(ctx)
	f.proc.eventMu.Lock()
	f.proc.persistent = true
	f.proc.idleTimeout = time.Hour
	f.proc.eventMu.Unlock()
	if err := f.ack(t, lifecycleAck); err != nil {
		t.Fatal(err)
	}
	f.emit(t, lifecycleTerminal)
	result := make(chan error, 1)
	go func() {
		result <- f.proc.waitForTurn(func(context.Context) error {
			close(f.proc.waitDone)
			return f.output.Close()
		}, time.Second)
	}()
	select {
	case err := <-result:
		t.Fatal("idle wait ended before shutdown", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("clean idle shutdown did not settle", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown left the idle wait armed")
	}
}

func TestCodexShutdownLeavesBusyTurnUntilItCompletes(t *testing.T) {
	f := newCodexLifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.proc.bindLifetime(ctx)
	f.proc.eventMu.Lock()
	f.proc.persistent = true
	f.proc.idleTimeout = time.Hour
	f.proc.eventMu.Unlock()
	if err := f.ack(t, lifecycleAck); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- f.proc.waitForTurn(func(context.Context) error {
			close(f.proc.waitDone)
			return f.output.Close()
		}, time.Second)
	}()
	cancel()
	select {
	case err := <-result:
		t.Fatal("busy turn ended on shutdown", err)
	case <-time.After(80 * time.Millisecond):
	}
	f.emit(t, lifecycleTerminal)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("turn completed after shutdown did not settle", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed turn stayed in the idle window after shutdown")
	}
}
