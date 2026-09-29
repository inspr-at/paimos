// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package agentd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

type recoveryFixtureProcess struct{ *wireProcess }

func (*recoveryFixtureProcess) Control(context.Context, string, string) error { return ErrUnsupported }

func recoveryShell(t *testing.T) *wireProcess {
	t.Helper()
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	proc, err := launchWire(shell, []string{"-c", `trap '' TERM; sleep 300 & echo "$!" > "$1"; wait`, "test", ready}, dir, nil, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proc.Stop(ctx)
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return proc
}

func TestGracefulStopDoesNotEscalateAndForceStaysInOwnedGroup(t *testing.T) {
	proc := recoveryShell(t)
	shell, _ := filepath.EvalSymlinks("/bin/sh")
	other := exec.Command(shell, "-c", "sleep 300")
	ownedprocess.Configure(other)
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownedprocess.Signal(other, true); _ = other.Wait() })
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err := proc.GracefulStop(ctx); !errors.Is(err, ErrGracefulTimeout) {
		t.Fatalf("stop=%v; expected explicit timeout", err)
	}
	identity, err := proc.Ownership()
	if err != nil {
		t.Fatalf("normal stop killed ignoring child: %v", err)
	}
	wrong := identity
	wrong.ProcessID = "00000000000000000000000000000000"
	if err := proc.ForceStop(ctx, wrong, time.Now().Add(time.Minute)); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("wrong process identity=%v", err)
	}
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("unrelated process harmed before force")
	}
	if err := proc.ForceStop(ctx, identity, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("force stop=%v", err)
	}
	if _, err := proc.Ownership(); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("reaped process still owned: %v", err)
	}
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("force stop harmed unrelated shell")
	}
	// A stale object whose numeric PID is reused cannot pass the reaping fence.
	old := proc.cmd.Process
	proc.cmd.Process = other.Process
	if err := proc.lifetime.Signal(true); err == nil {
		t.Fatal("reaped identity accepted reused PID")
	}
	proc.cmd.Process = old
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("PID reuse signalled unrelated process")
	}
	childRaw, err := os.ReadFile(filepath.Join(proc.cmd.Dir, "ready"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(childRaw)))
	if err != nil {
		t.Fatal(err)
	}
	// The child receives the same group kill. A short-lived zombie is already
	// dead and may await its system reaper, so inspect state rather than kill(0).
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(child)).Output()
		state := strings.TrimSpace(string(out))
		if state == "" || strings.HasPrefix(state, "Z") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned child survived force: %s", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestForceControlIdentityExpiryAndReplay(t *testing.T) {
	s, _, old := testSupervisor(t)
	defer s.Close(context.Background())
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	proc := recoveryShell(t)
	entry := s.runs["run"]
	entry.mu.Lock()
	entry.process = &recoveryFixtureProcess{proc}
	entry.mu.Unlock()
	// The fixture monitor owns the original fake Process; close it after the
	// force assertions so it cannot race the replacement fixture's state.
	defer old.Stop(context.Background())
	identity, err := proc.Ownership()
	if err != nil {
		t.Fatal(err)
	}
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", Generation: s.generation, CorrelationID: "force-1", Operation: "force_stop", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry}
	if _, err := s.Control(t.Context(), req); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local transport bypassed human recovery authorization: %v", err)
	}
	identity.Generation = "old-daemon"
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrGeneration) {
		t.Fatalf("daemon restart fence=%v", err)
	}
	identity.Generation = s.generation
	correct := identity.ProcessID
	identity.ProcessID = "different-process"
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("process generation fence=%v", err)
	}
	identity.ProcessID = correct
	expiry = time.Now().Add(-time.Second)
	req.deadline = expiry
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expired confirmation=%v", err)
	}
	// The database can be far behind this daemon. Only its remaining budget,
	// translated by the transport, reaches the real process signal lock.
	expiry = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	req.deadline = time.Now().Add(time.Minute)
	first, err := s.control(t.Context(), req, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.control(t.Context(), req, true)
	if err != nil || first != second {
		t.Fatalf("force receipt replay=%v %v", second, err)
	}
	req.CorrelationID = "force-2"
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("post-exit force=%v", err)
	}
}

type archiveRemoteFixture struct {
	*fakeAPI
	remote *Remote
}

func (a *archiveRemoteFixture) YieldHarness(ctx context.Context, h HarnessSession) ([]HarnessControl, error) {
	return a.remote.YieldHarness(ctx, h)
}

func TestArchivedWorkerResponseDetachesWithoutStoppingChild(t *testing.T) {
	s, a, p := testSupervisor(t)
	defer s.Close(context.Background())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(410)
		_, _ = w.Write([]byte(`{"error":"harness generation archived"}`))
	}))
	defer server.Close()
	s.api = &archiveRemoteFixture{fakeAPI: a, remote: NewRemote(server.URL, "")}
	s.heartbeatInterval = 10 * time.Millisecond
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry := s.runs["run"]
	deadline := time.Now().Add(time.Second)
	for {
		entry.mu.Lock()
		detached := entry.harnessArchived
		entry.mu.Unlock()
		if detached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("archive response did not detach harness")
		}
		time.Sleep(time.Millisecond)
	}
	if requests.Load() != 1 {
		t.Fatalf("archive worker requests=%d", requests.Load())
	}
	select {
	case <-p.stopped:
		t.Fatal("archive caused a process signal")
	default:
	}
	if _, err := s.Control(t.Context(), ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", Generation: s.generation, CorrelationID: "late-stop", Operation: "stop"}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("archived controls remained live: %v", err)
	}
	// Continue independent run heartbeats, without retrying revoked harness APIs.
	a.mu.Lock()
	reports := len(a.reports)
	a.mu.Unlock()
	for {
		a.mu.Lock()
		count := len(a.reports)
		a.mu.Unlock()
		if count > reports {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("archive unexpectedly stopped run reporting")
		}
		time.Sleep(time.Millisecond)
	}
	if requests.Load() != 1 {
		t.Fatal("archived harness continued servicing")
	}
	select {
	case <-p.stopped:
		t.Fatal("archive indirectly stopped child")
	default:
	}
}

type failedControlReportFixture struct {
	*fakeAPI
	failures atomic.Int32
}

func (a *failedControlReportFixture) CompleteHarnessControl(ctx context.Context, h HarnessSession, id, outcome, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.failures.Add(1) == 1 {
		return context.DeadlineExceeded
	}
	return a.fakeAPI.CompleteHarnessControl(ctx, h, id, outcome, reason)
}

func TestExpiredForceReportFailureNeverEscalatesThroughHeartbeat(t *testing.T) {
	s, a, p := testSupervisor(t)
	defer s.Close(context.Background())
	failing := &failedControlReportFixture{fakeAPI: a}
	s.api = failing
	s.heartbeatInterval = 10 * time.Millisecond
	expires := time.Now().Add(-time.Second)
	a.harnessControls = []HarnessControl{{ID: "expired-force", Kind: "force_stop", ExpiresAt: &expires, ExpectedOwnership: &ownedprocess.Identity{}}}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		completed := len(a.harnessCompletions) > 0
		a.mu.Unlock()
		if completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control report was not retried")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-p.stopped:
		t.Fatal("failed expired-control report escalated to Stop")
	default:
	}
	// A completion using an already-cancelled context is classified separately
	// from loss of process ownership, so it also cannot invoke heartbeat cleanup.
	a.mu.Lock()
	a.harnessControls = []HarnessControl{{ID: "cancelled-force", Kind: "force_stop", ExpiresAt: &expires, deadline: expires}}
	a.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.serviceHarness(ctx, s.runs["run"]); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatalf("cancelled report=%v", err)
	}
	select {
	case <-p.stopped:
		t.Fatal("cancelled control report stopped child")
	default:
	}
}
