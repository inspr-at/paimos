//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSupervisorCancellationKillsOwnedProcessTree(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	ctx, cancel := context.WithCancel(context.Background())
	command := "sleep 30 & child=$!; echo $child > " + strconv.Quote(pidFile) + "; wait"
	result := make(chan supervisorResult, 1)
	go func() {
		req := supervisorFixture(command)
		req.ExecutionTimeout = time.Minute
		req.SilenceTimeout = time.Minute
		result <- superviseAgentProcess(ctx, req)
	}()
	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		cancel()
		t.Fatal("descendant pid was not recorded")
	}
	cancel()
	if got := <-result; got.Outcome != outcomeCancellation {
		t.Fatalf("outcome=%s summary=%q", got.Outcome, got.Summary)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived cancellation", pid)
}
