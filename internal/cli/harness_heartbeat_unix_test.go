//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
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

func TestOpenNoFollowReadsRegularFileAndRejectsSymlinkParent(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(real, "note.json")
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openNoFollow(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(got) != "ok" {
		t.Fatalf("read %q %v", got, err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	f, err = openNoFollow(filepath.Join(dir, "link", "note.json"))
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("opened through a symlinked parent")
	}
	alias := filepath.Join(real, "alias.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	f, err = openNoFollow(alias)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("opened a symlinked file")
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

func TestHeartbeatPairedReconnectBindsOnlyItsLiveIndexedSession(t *testing.T) {
	for _, code := range []int{200, 409} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			rt, _, stderr := heartbeatRuntime(t, srv)
			dir, err := os.MkdirTemp("/tmp", "aeon-hb-hook-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			dir, err = filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "hook.sock")
			if err := os.WriteFile(socket+".token", []byte(strings.Repeat("fixture-", 4)), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			requests := make(chan map[string]any, 1)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/attached-hook" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("fixture-", 4) {
					t.Error("unauthenticated or wrong local route")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad local binding")
				}
				requests <- body
				w.WriteHeader(code)
				_, _ = w.Write([]byte("{}"))
			})}
			go func() { _ = server.Serve(listener) }()
			defer server.Close()
			o := heartbeatTestOptions(dir)
			o.OwnerPID = os.Getpid()
			o.SourceSession = "11111111-1111-4111-8111-111111111111"
			o.ReconnectSocket = socket
			if err := rt.runHeartbeat(t.Context(), o, heartbeatDeps{wait: func(context.Context, int, time.Duration) error { return errOwnerExited }}); err != nil {
				t.Fatal(err)
			}
			select {
			case body := <-requests:
				if body["origin"] != srv.URL || body["session_id"] != transcriptSessionID || body["project_id"] != transcriptProjectID || body["owner_pid"] != float64(os.Getpid()) || body["activity_sequence"] != float64(1) || body["worker_lease"] == "" {
					t.Fatal("binding lost exact session, origin, owner or lease")
				}
			default:
				t.Fatalf("heartbeat never bound its indexed hook: %s", stderr.String())
			}
			if strings.Contains(stderr.String(), "binding was refused") != (code == 409) {
				t.Fatal("partial binding result was not reported honestly")
			}
			registrations := hbWhere(calls, "POST", "/harness-sessions")
			if len(registrations) != 1 {
				t.Fatal("missing registration")
			}
			caps := registrations[0].body["advertised_capabilities"].([]any)
			if len(caps) != 2 || caps[1] != "inbox" {
				t.Fatal("attached hook inbox not advertised")
			}
		})
	}
}

func TestPairedInboxHookAcknowledgesOnlyForegroundHandoff(t *testing.T) {
	for _, scenario := range []string{"delivered", "short-output", "closed-output"} {
		t.Run(scenario, func(t *testing.T) {
			setupHookTest(t)
			home, err := os.MkdirTemp("/tmp", "aeon-hook-home-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(home)
			home, err = filepath.EvalSymlinks(home)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("AEON_SESSION_ID", hookSessionID)
			root, err := agentsetup.DefaultStateRoot(goruntime.GOOS, home, "")
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(root, "daemon")
			store, err := agentsetup.OpenStore(state, true)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			socket, err := agentsetup.ResolveSocketPath(state, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := agentsetup.PrepareSocketDirectory(socket); err != nil {
				t.Fatal(err)
			}
			ref, _ := json.Marshal(agentsetup.ControlReference{Socket: socket, DaemonID: "fixture", Generation: strings.Repeat("a", 32)})
			if err := store.Write("control.json", ref, true); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close(); _ = os.Remove(socket); _ = os.Remove(socket + ".token") }()
			if err := os.Chmod(socket, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(socket+".token", []byte(strings.Repeat("fixture-", 4)), 0600); err != nil {
				t.Fatal(err)
			}
			var out observedHookWriter
			var stdout io.Writer = &out
			if scenario == "short-output" {
				stdout = shortHookWriter{}
			} else if scenario == "closed-output" {
				stdout = failedHookWriter{}
			}
			var complete bool
			delivery := agentd.HarnessDelivery{ID: "00000000-0000-4000-8000-000000000094", MessageID: hookMessageID, SenderPrincipalID: "00000000-0000-4000-8000-000000000093", Cursor: 7, Body: "Untrusted attached fixture message"}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request agentd.AttachedHookRequest
				if r.URL.Path != "/v1/attached-hook" || json.NewDecoder(r.Body).Decode(&request) != nil || request.SessionID != hookSessionID || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("fixture-", 4) {
					t.Error("hook lost owner/session authentication")
					w.WriteHeader(403)
					return
				}
				switch request.Operation {
				case "pull":
					_ = json.NewEncoder(w).Encode([]agentd.HarnessDelivery{delivery})
				case "complete":
					if !out.emitted.Load() || request.DeliveryID != delivery.ID || request.Cursor != delivery.Cursor {
						t.Error("ack before or outside exact stdout handoff")
					}
					complete = true
					_ = json.NewEncoder(w).Encode([]agentd.HarnessDelivery{})
				default:
					t.Error("hook changed its binding")
					w.WriteHeader(400)
				}
			})}
			go func() { _ = server.Serve(listener) }()
			defer server.Close()
			rt := &runtime{stdin: strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"fixture-vendor"}`), stdout: stdout, stderr: io.Discard}
			err = rt.runInboxHook(t.Context(), "PostToolUse")
			if scenario == "delivered" {
				if err != nil || !complete || !strings.Contains(out.String(), delivery.Body) {
					t.Fatal("paired hook did not deliver and confirm stdout")
				}
			} else if err == nil || complete {
				t.Fatal("failed foreground output was acknowledged")
			}
		})
	}
}
