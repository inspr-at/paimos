//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestOwnedProcessNeverSignalsOnceReapingBegins(t *testing.T) {
	const callers = 64
	enteredWait := make(chan struct{})
	releaseWait := make(chan struct{})
	var signalCalls atomic.Int64
	process := newOwnedProcess(&exec.Cmd{Process: &os.Process{Pid: 424242}})
	process.wait = func() error {
		close(enteredWait)
		<-releaseWait
		return errors.New("fixture reaped")
	}
	process.signal = func(*exec.Cmd, bool) error {
		signalCalls.Add(1)
		return nil
	}
	go process.reapAfterDrain()
	<-enteredWait

	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "post-eof-stop"})
		}()
	}
	close(releaseWait)
	wg.Wait()
	if got := signalCalls.Load(); got != 0 {
		t.Fatalf("sent %d process-group signals after ownership entered reap", got)
	}
}

func TestOwnedProcessReaderExitKillsBeforeReapAndStopNeverWedges(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", `trap '' TERM; exec 1>&-; while :; do sleep 1; done`)
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
	process := newOwnedProcess(cmd)
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		close(drained)
		process.finishAfterDrain()
	}()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("child did not close stdout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := process.Stop(ctx, ControlRequest{CorrelationID: "stdout-closed-stop"}); err != nil {
		t.Fatalf("stop wedged after stdout EOF: %v", err)
	}
	for range 8 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := process.Stop(ctx, ControlRequest{CorrelationID: "repeated-stop"}); err != nil {
			t.Fatalf("repeated stop: %v", err)
		}
	}
	if cmd.ProcessState == nil {
		t.Fatal("owned child was not reaped")
	}
}

func TestOwnedProcessCancelledStopIsBoundedOnceReapingBegins(t *testing.T) {
	enteredWait := make(chan struct{})
	releaseWait := make(chan struct{})
	process := newOwnedProcess(&exec.Cmd{Process: &os.Process{Pid: 424243}})
	process.wait = func() error {
		close(enteredWait)
		<-releaseWait
		return nil
	}
	go process.reapAfterDrain()
	<-enteredWait
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() {
		_, err := process.Stop(ctx, ControlRequest{CorrelationID: "cancelled-reap-stop"})
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v want context cancellation", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(releaseWait)
		t.Fatal("cancelled Stop blocked on reap")
	}
	close(releaseWait)
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
}
