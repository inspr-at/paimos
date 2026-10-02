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
	cmd := exec.Command(exe, "-test.run=^TestSIGTERMEndsIdleManagedCodex$", "-test.timeout=60s", "-test.count=1")
	cmd.Env = append(os.Environ(), "AEON_AGENTD_IDLE_CHILD=1", "AEON_AGENTD_IDLE_STATUS="+status)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	joined := make(chan struct{})
	go func() { waited <- cmd.Wait(); close(joined) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := stopIdleChild(ctx, cmd.Process.Signal, joined); err != nil {
			t.Errorf("join owned child fixture: %v", err)
		}
	})
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
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
			t.Fatal("managed Codex run did not go idle")
		}
		<-tick.C
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("idle agentd exit after SIGTERM: %v\n%s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not finish an idle managed Codex run within 5s")
	}
	<-joined
	raw, err := os.ReadFile(status)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != "completed;vendor-cleanup-confirmed" {
		t.Fatalf("settlement %q\n%s", got, output.String())
	}
}

// The parent owns only its child fixture. Vendor cleanup stays inside that
// child, where the adapter still owns the vendor's verified Lifetime.
func stopIdleChild(ctx context.Context, signal func(os.Signal) error, joined <-chan struct{}) error {
	select {
	case <-joined:
		return nil
	default:
	}
	if err := signal(syscall.SIGTERM); err != nil {
		select {
		case <-joined:
			return nil
		default:
			return err
		}
	}
	select {
	case <-joined:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestIdleChildDelayedCleanupNeverSignalsReleasedIdentity(t *testing.T) {
	joined := make(chan struct{})
	close(joined) // The child already reaped its vendor and the parent joined it.
	signals := 0
	signal := func(os.Signal) error { signals++; return nil }
	if err := stopIdleChild(t.Context(), signal, joined); err != nil || signals != 0 {
		t.Fatalf("delayed cleanup signalled a released identity: signals=%d err=%v", signals, err)
	}
}

func TestIdleChildCleanupStopsAndJoinsItsOwnedChild(t *testing.T) {
	joined := make(chan struct{})
	signalled := make(chan os.Signal, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { done <- stopIdleChild(ctx, func(s os.Signal) error { signalled <- s; return nil }, joined) }()
	select {
	case s := <-signalled:
		if s != syscall.SIGTERM {
			t.Fatalf("cleanup signal: %v", s)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-done:
		t.Fatalf("cleanup returned before joining: %v", err)
	default:
	}
	close(joined)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func runIdleManagedCodexChild(t *testing.T) {
	status := os.Getenv("AEON_AGENTD_IDLE_STATUS")
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
	t.Cleanup(func() {
		stop()
		s.mu.Lock()
		entry := s.runs["run"]
		s.mu.Unlock()
		if entry != nil {
			entry.mu.Lock()
			proc, exited := entry.process, entry.record.ExitObserved
			entry.mu.Unlock()
			if proc != nil && !exited {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := proc.Stop(ctx); err != nil {
					t.Errorf("clean up owned vendor lifetime: %v", err)
				}
			}
		}
	})
	if err := s.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var entry *owned
	for {
		s.mu.Lock()
		entry = s.runs["run"]
		s.mu.Unlock()
		if entry != nil {
			entry.mu.Lock()
			activity := entry.harness.Activity
			entry.mu.Unlock()
			if activity == "idle" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not observe idle")
		}
		<-tick.C
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
			<-tick.C
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
	if err := os.WriteFile(status, []byte("completed;vendor-cleanup-confirmed"), 0600); err != nil {
		t.Fatal(err)
	}
}
