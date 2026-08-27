//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
)

// TestDeliverCodexSteerTimeoutKillsProxyProcessTree reproduces the 5.18.0
// failure shape: the proxy wrapper is a shell parent whose native child never
// answers. The steer budget must kill the whole group so no orphan keeps the
// stdio pipes open after the error is returned.
func TestDeliverCodexSteerTimeoutKillsProxyProcessTree(t *testing.T) {
	_, _ = installFakeCodexAppServer(t)
	pidFile := filepath.Join(t.TempDir(), "proxy.pid")
	t.Setenv("PAIMOS_TEST_PIDFILE", pidFile)
	t.Setenv("PAIMOS_TEST_SCENARIO", "hang")
	previous := harnessplugin.CodexSteerTimeout
	harnessplugin.CodexSteerTimeout = 2 * time.Second
	t.Cleanup(func() { harnessplugin.CodexSteerTimeout = previous })
	_, _, err := deliverCodexSteer(context.Background(), "hang payload", "thread-hang")
	if err == nil || !strings.Contains(err.Error(), "no response to websocket handshake") {
		t.Fatalf("error=%v want handshake timeout", err)
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("fake proxy never recorded its pid: %v", readErr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid <= 0 {
		t.Fatalf("bad pid %q", raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("native proxy child %d survived the steer timeout", pid)
}
