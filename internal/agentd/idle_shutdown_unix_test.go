// SPDX-License-Identifier: AGPL-3.0-only
//go:build unix

package agentd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

func TestSIGTERMEndsIdleManagedCodex(t *testing.T) {
	if os.Getenv("AEON_AGENTD_IDLE_CHILD") == "1" {
		runIdleManagedCodexChild(t)
		return
	}
	if !ownedprocess.TrackingSupported() {
		t.Skip("process ownership unsupported")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	status := filepath.Join(root, "status")
	pidPath := filepath.Join(root, "vendor.pid")
	cmd := exec.Command(exe, "-test.run=^TestSIGTERMEndsIdleManagedCodex$", "-test.timeout=60s", "-test.count=1")
	cmd.Env = append(os.Environ(), "AEON_AGENTD_IDLE_CHILD=1", "AEON_AGENTD_IDLE_STATUS="+status, "AEON_AGENTD_IDLE_PID="+pidPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if raw, err := os.ReadFile(pidPath); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err == nil && pid > 1 {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-waited:
			t.Fatalf("child exited before idle: %v\n%s", err, output.String())
		default:
		}
		raw, err := os.ReadFile(status)
		if err == nil && strings.TrimSpace(string(raw)) == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed Codex run did not go idle: %s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	started := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("idle agentd exit after SIGTERM: %v\n%s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("SIGTERM did not finish an idle managed Codex run within 5s\n%s", output.String())
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
	raw, err := os.ReadFile(status)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != "completed" {
		t.Fatalf("settlement %q\n%s", got, output.String())
	}
}

func runIdleManagedCodexChild(t *testing.T) {
	status := os.Getenv("AEON_AGENTD_IDLE_STATUS")
	pidPath := os.Getenv("AEON_AGENTD_IDLE_PID")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	for _, dir := range []string{home, work, state} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	adapter := NewCodexAdapter(fakeVendorPath(t, "codex_idle"), map[string]string{"local": home})
	adapter.IdleTimeout = time.Hour
	adapter.SetExpectedEmails(map[string]string{"local": "agent@example.test"})
	api := &fakeAPI{run: Run{ID: "run", WorkOrderID: "order", AgentPrincipalID: "agent", ModelProfileID: "profile", Status: "queued"}, profile: Profile{ID: "profile", Harness: Codex, Model: "model", Effort: "high"}}
	s, err := NewSupervisor(ctx, Config{API: api, StateRoot: state, DaemonID: "daemon", Workspace: work, Adapters: []Adapter{adapter}, EstimatedUnits: map[string]int64{"requests": 1}, Accounts: []EnrolledAccount{{ID: "account", Key: "local", Harness: Codex}}, HeartbeatInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var entry *owned
	for {
		s.mu.Lock()
		entry = s.runs["run"]
		s.mu.Unlock()
		if entry != nil {
			entry.mu.Lock()
			activity := entry.harness.Activity
			pid := entry.record.PID
			entry.mu.Unlock()
			if activity == "idle" {
				if pid > 1 {
					_ = os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0600)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not observe idle")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.WriteFile(status, []byte("idle"), 0600); err != nil {
		t.Fatal(err)
	}
	stopping := false
	for {
		if stopping || ctx.Err() != nil {
			stopping = true
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := s.Close(closeCtx)
			cancel()
			if err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		select {
		case <-ctx.Done():
			stopping = true
		case <-time.After(time.Hour):
			t.Fatal("idle window expired before SIGTERM")
		}
	}
	api.mu.Lock()
	var finished Telemetry
	for _, report := range api.reports {
		if report.Kind == "finished" {
			finished = report
		}
	}
	stops := append([]string(nil), api.harnessStops...)
	api.mu.Unlock()
	entry.mu.Lock()
	exited := entry.record.ExitObserved
	entry.mu.Unlock()
	if !exited || finished.Status != "completed" || finished.ErrorCode != "" || len(stops) != 1 || stops[0] != "process_exited" {
		t.Fatalf("settlement status=%s code=%s exited=%t stops=%v", finished.Status, finished.ErrorCode, exited, stops)
	}
	if err := os.WriteFile(status, []byte("completed"), 0600); err != nil {
		t.Fatal(err)
	}
}
