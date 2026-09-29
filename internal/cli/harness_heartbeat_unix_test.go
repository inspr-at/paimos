//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"golang.org/x/sys/unix"
)

func TestOwnerStampMatchesStartAndRejectsReuse(t *testing.T) {
	stamp, err := readOwnerStamp(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if stamp.Start == "" || stamp.PID != os.Getpid() || !ownerAlive(os.Getpid(), stamp.Start) {
		t.Fatalf("live stamp %#v", stamp)
	}
	if ownerAlive(os.Getpid(), stamp.Start+"9") || ownerAlive(os.Getpid(), "") {
		t.Fatal("a different start identity was treated as the same process")
	}
}

func TestOwnerStampRejectsZombie(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	zombie := false
	for time.Now().Before(deadline) {
		killErr := syscall.Kill(pid, 0)
		_, readErr := readOwnerStamp(pid)
		if killErr == nil && readErr != nil {
			zombie = true
			break
		}
		if killErr != nil && readErr != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = cmd.Wait()
	if !zombie {
		t.Fatal("an exited unreaped process was still treated as alive")
	}
}

func TestRunHeartbeatLiveOwnerKeepsBeating(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(2 * time.Second)
	var stamp ownerStamp
	var err error
	for {
		stamp, err = readOwnerStamp(cmd.Process.Pid)
		if err == nil && stamp.Start != "" && ownerAlive(cmd.Process.Pid, stamp.Start) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live owner stamp: %v %#v", err, stamp)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.OwnerPID = cmd.Process.Pid
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		wait: func(context.Context, int, time.Duration) error { return errOwnerExited },
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 1 {
		t.Fatalf("heartbeats %d stderr %s", len(hbWhere(calls, http.MethodPost, "/heartbeat")), stderr.String())
	}
	if len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatal("live owner was not stopped after the injected exit")
	}
	disk := loadHeartbeatDisk(t, opts.StateDir)
	if disk.OwnerStart == "" || !ownerAlive(cmd.Process.Pid, disk.OwnerStart) {
		t.Fatalf("stamp %q", disk.OwnerStart)
	}
	if strings.Contains(stderr.String(), "failed the start check") || strings.Contains(stderr.String(), "will not resume") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestRunHeartbeatLiveOwnerAgainstDatabase(t *testing.T) {
	isolate(t)
	opened := dbtest.Open(t)
	if err := db.EnsureTenant(t.Context(), opened.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	base := renameServer(t, opened, "aeon", true)
	agent := mintAgent(t, base, "heartbeat-owner")
	seedProject(t, base, agent.Token)
	t.Setenv("AEON_URL", base)
	t.Setenv("AEON_API_KEY", agent.Token)
	count := heartbeatSessionCount(t, opened, agent.TenantID)

	deadDir := t.TempDir()
	code, stdout, stderr := runCLI([]string{"aeon", "--config", filepath.Join(deadDir, "missing"), "harness", "run-heartbeat",
		"--owner-pid", "2147483646",
		"--state-dir", filepath.Join(deadDir, "state"),
		"--project", "AEON", "--agent", "heartbeat-owner", "--harness", "claude", "--host", "test-host",
		"--codex-index", filepath.Join(deadDir, "missing-index.jsonl"),
		"--claude-projects", filepath.Join(deadDir, "missing-projects"),
	}, "")
	if code != 1 || !strings.Contains(stderr, "heartbeat: owner 2147483646 failed the start check") || !strings.Contains(stderr, "aeon: owner process is not alive") {
		t.Fatalf("dead owner exit %d stdout %s stderr %s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(deadDir, "state", "state.json")); !os.IsNotExist(err) {
		t.Fatal("dead owner wrote heartbeat state")
	}
	if got := heartbeatSessionCount(t, opened, agent.TenantID); got != count {
		t.Fatalf("dead owner created %d sessions", got-count)
	}

	cmd := exec.Command("/bin/sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	liveDir := t.TempDir()
	stateDir := filepath.Join(liveDir, "state")
	done := make(chan struct{})
	var liveCode int
	var liveOut, liveErr string
	go func() {
		liveCode, liveOut, liveErr = runCLI([]string{"aeon", "--config", filepath.Join(liveDir, "missing"), "harness", "run-heartbeat",
			"--owner-pid", strconv.Itoa(cmd.Process.Pid),
			"--interval", "1",
			"--state-dir", stateDir,
			"--project", "AEON", "--agent", "heartbeat-owner", "--harness", "claude", "--host", "test-host",
			"--codex-index", filepath.Join(liveDir, "missing-index.jsonl"),
			"--claude-projects", filepath.Join(liveDir, "missing-projects"),
		}, "")
		close(done)
	}()
	var disk heartbeatDisk
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case <-done:
			t.Fatalf("heartbeat exited before a beat: code %d stdout %s stderr %s", liveCode, liveOut, liveErr)
		default:
		}
		raw, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
		if err == nil && json.Unmarshal(raw, &disk) == nil && disk.Sequence >= 1 && disk.SessionID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live owner did not record a heartbeat")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("heartbeat did not stop after the owner exited")
	}
	if liveCode != 0 {
		t.Fatalf("live owner exit %d stdout %s stderr %s", liveCode, liveOut, liveErr)
	}
	if strings.Contains(liveErr, "failed the start check") {
		t.Fatalf("stderr %s", liveErr)
	}
	phase, heartbeat, stopped := heartbeatSessionPhase(t, opened, agent.TenantID, disk.SessionID)
	if phase != "stopped" || !heartbeat || !stopped {
		t.Fatalf("session phase %q heartbeat %v stopped %v", phase, heartbeat, stopped)
	}
}

func heartbeatSessionCount(t *testing.T, opened *dbtest.DB, tenantID string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), opened.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func heartbeatSessionPhase(t *testing.T, opened *dbtest.DB, tenantID, id string) (string, bool, bool) {
	t.Helper()
	var phase string
	var heartbeat, stopped bool
	err := db.InTenant(dbtest.Seed(t.Context()), opened.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT phase, heartbeat_at IS NOT NULL, stopped_at IS NOT NULL FROM harness_sessions WHERE id=$1`, id).Scan(&phase, &heartbeat, &stopped)
	})
	if err != nil {
		t.Fatal(err)
	}
	return phase, heartbeat, stopped
}

func TestOpenNoFollowRejectsFIFO(t *testing.T) {
	path := t.TempDir() + "/pipe"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := openNoFollow(path)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("opened a fifo")
		}
	case <-time.After(time.Second):
		t.Fatal("opening a fifo blocked")
	}
}
