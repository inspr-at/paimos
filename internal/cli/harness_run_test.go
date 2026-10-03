// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHarnessRunStopsAndPreservesExit(t *testing.T) {
	for _, tc := range []struct {
		name, script, reason string
		code                 int
	}{{"success", "printf 'job output'; sleep 0.1", "process_exited", 0}, {"failed", "exit 7", "process_failed", 7}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			rt, out, _ := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(t.TempDir())
			o.OwnerPID = os.Getpid()
			err := rt.runHarnessCommand(context.Background(), o, []string{"sh", "-c", tc.script})
			if tc.code == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.code != 0 {
				var exit *exitError
				if !errors.As(err, &exit) || exit.code != tc.code {
					t.Fatalf("wrong exit: %v", err)
				}
			}
			if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 1 || len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
				t.Fatal("missing registration or stop")
			}
			// A clean exit and a failure are told apart on the session (AEON-437).
			if got := hbWhere(calls, http.MethodPost, "/stop")[0].body["reason"]; got != tc.reason {
				t.Fatalf("stop reason %v, want %s", got, tc.reason)
			}
			if tc.code == 0 && out.String() != "job output" {
				t.Fatal("child stdout changed")
			}
			// Reusing private state must never execute the job twice.
			if err := rt.runHarnessCommand(context.Background(), o, []string{"sh", "-c", "exit 0"}); err == nil {
				t.Fatal("reused state executed another job")
			}
		})
	}
}

func TestHarnessRunLaunchFailureStops(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(t.TempDir())
	o.OwnerPID = os.Getpid()
	if err := rt.runHarnessCommand(context.Background(), o, []string{filepath.Join(t.TempDir(), "missing-command")}); err == nil {
		t.Fatal("launch failure ignored")
	}
	if stops := hbWhere(calls, http.MethodPost, "/stop"); len(stops) != 1 || stops[0].body["reason"] != "process_failed" {
		t.Fatal("failed launch not stopped as failed")
	}
}

func TestHarnessRunSIGTERM(t *testing.T) {
	if os.Getenv("AEON_TEST_HARNESS_RUN") == "1" {
		dir := os.Getenv("AEON_TEST_RUN_DIR")
		code := Run([]string{"aeon", "harness", "run", "--project", "AEON", "--harness", "claude", "--label", "Review", "--role", "reviewer", "--interval", "1", "--state-dir", filepath.Join(dir, "state"), "--", "sh", "-c", "printf ready; exec sleep 30"}, os.Stdin, os.Stdout, os.Stderr)
		os.Exit(code)
	}
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	_, _, _ = heartbeatRuntime(t, srv)
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHarnessRunSIGTERM$")
	cmd.Env = append(os.Environ(), "AEON_TEST_HARNESS_RUN=1", "AEON_TEST_RUN_DIR="+dir)
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyR.Close()
	defer readyW.Close()
	cmd.Stdout = readyW
	// Do not capture or log inherited credentials or request proofs.
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyW.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ready := make(chan error, 1)
	go func() {
		var marker [5]byte
		_, err := io.ReadFull(readyR, marker[:])
		if err == nil && string(marker[:]) != "ready" {
			err = errors.New("unexpected readiness marker")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("command readiness: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("command did not start")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 143 {
			t.Fatalf("signal exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM failed to settle")
	}
	// Waiting for the child does not join this process's HTTP handlers. Close
	// waits for them to finish before assertions read their recorded calls.
	srv.Close()
	regs := hbWhere(calls, http.MethodPost, "/harness-sessions")
	if len(regs) != 1 || regs[0].body["role"] != "worker" || regs[0].body["display_label"] != "Review" {
		t.Fatal("review registration missing")
	}
	if stops := hbWhere(calls, http.MethodPost, "/stop"); len(stops) != 1 || stops[0].body["reason"] != "stopped" {
		t.Fatal("SIGTERM did not stop session plainly")
	}
}

func TestCoordinatorHeartbeatUsesNativeReference(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	for range 2 {
		o := heartbeatTestOptions(t.TempDir())
		o.Role = "coordinator"
		o.SourceSession = transcriptSessionID
		o.Succeeds = transcriptEntryID
		if err := rt.runHeartbeat(context.Background(), o, heartbeatDeps{
			alive: func(int) bool { return true },
			wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
		}); err != nil {
			t.Fatal(err)
		}
	}
	regs := hbWhere(calls, http.MethodPost, "/harness-sessions")
	if len(regs) != 2 || regs[0].body["harness_session_ref"] != regs[1].body["harness_session_ref"] || !strings.HasPrefix(regs[0].body["harness_session_ref"].(string), "claude:") || regs[1].body["succeeds_session_id"] != transcriptEntryID {
		t.Fatal("coordinator reference/explicit succession not preserved")
	}
}

// AEON-437: how the job ended is part of the stop. When the first /stop fails,
// the recovery on the next start must replay that reason, not a plain "stopped":
// a crash replayed as "stopped" at 100% would later read as a finished job.
func TestHarnessRunStopRecoveryReplaysHowTheJobEnded(t *testing.T) {
	for _, tc := range []struct{ name, script, reason string }{
		{"failed", "exit 7", "process_failed"},
		{"clean", "exit 0", "process_exited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []hbCall
			stops := 0
			srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
					stops++
					if stops == 1 {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte(`{"error":"unavailable"}`))
						return true
					}
				}
				return false
			})
			defer srv.Close()
			rt, _, _ := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(t.TempDir())
			o.OwnerPID = os.Getpid()
			_ = rt.runHarnessCommand(context.Background(), o, []string{"sh", "-c", tc.script})
			if _, err := os.Lstat(filepath.Join(o.StateDir, "stop.intent")); err != nil {
				t.Fatalf("failed stop left no stop intent: %v", err)
			}
			if err := rt.runHeartbeat(context.Background(), o, heartbeatDeps{alive: func(int) bool { return false }}); err != nil {
				t.Fatalf("recovery: %v", err)
			}
			got := hbWhere(calls, http.MethodPost, "/stop")
			if len(got) != 2 {
				t.Fatalf("stops %d, want the failed one and its replay", len(got))
			}
			for i, stop := range got {
				if stop.body["reason"] != tc.reason {
					t.Fatalf("stop %d reason %v, want %s", i, stop.body["reason"], tc.reason)
				}
			}
		})
	}
}

// A stop intent written before the reason was persisted (or with a reason the
// server does not accept) replays as the plain stop it always was.
func TestHeartbeatStopIntentReasonIsAllowlisted(t *testing.T) {
	for raw, want := range map[string]string{
		"id\n\n1\n500\nprocess_failed\n": "process_failed",
		"id\n\n1\n500\nprocess_exited\n": "process_exited",
		"id\n\n1\n500\n":                 "",
		"id\n\n1\n500\nforce_stopped\n":  "",
		"id\n\n1\n500\nrm -rf /\n":       "",
	} {
		if _, _, _, _, got := heartbeatStopIntent([]byte(raw)); got != want {
			t.Fatalf("%q read as %q, want %q", raw, got, want)
		}
	}
}
