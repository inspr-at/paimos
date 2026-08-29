//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/backend/ownedprocess"
)

func TestOwnedProcessCancellationEscalatesAndReaps(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", `trap '' TERM; echo ready; while :; do sleep 1; done`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	configured := ownedprocess.Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := ownedprocess.Verify(cmd, configured); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("readiness=%q err=%v", line, err)
	}
	process := newOwnedProcess(cmd)
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		process.reapAfterDrain()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	effect, err := process.Stop(ctx, ControlRequest{CorrelationID: "cancelled-stop"})
	if err != nil {
		t.Fatal(err)
	}
	if effect.Primitive != "owned process-group kill" || effect.CorrelationID != "cancelled-stop" {
		t.Fatalf("effect=%+v", effect)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("forced termination unexpectedly reported a clean child exit")
	}
}

func TestOwnedProcessNeverSignalsAfterReapBegins(t *testing.T) {
	process := newOwnedProcess(exec.Command("/bin/true"))
	waiting := make(chan struct{})
	release := make(chan struct{})
	process.wait = func() error {
		close(waiting)
		<-release
		return nil
	}
	var signals atomic.Int32
	process.signal = func(*exec.Cmd, bool) error {
		signals.Add(1)
		return nil
	}
	go process.reapAfterDrain()
	<-waiting
	if sent, err := process.signalOwned(true); err != nil || sent {
		t.Fatalf("sent=%t err=%v", sent, err)
	}
	close(release)
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	if sent, err := process.signalOwned(true); err != nil || sent || signals.Load() != 0 {
		t.Fatalf("post-reap sent=%t signals=%d err=%v", sent, signals.Load(), err)
	}
}
