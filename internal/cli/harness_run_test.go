// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"errors"
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
		name, script string
		code         int
	}{{"success", "printf 'job output'; sleep 0.1", 0}, {"failed", "exit 7", 7}} {
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
	if len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatal("failed launch not stopped")
	}
}

func TestHarnessRunSIGTERM(t *testing.T) {
	if os.Getenv("AEON_TEST_HARNESS_RUN") == "1" {
		dir := os.Getenv("AEON_TEST_RUN_DIR")
		code := Run([]string{"aeon", "harness", "run", "--project", "AEON", "--harness", "claude", "--label", "Review", "--role", "reviewer", "--interval", "1", "--state-dir", filepath.Join(dir, "state"), "--", "sh", "-c", `printf ready > "$1"; exec sleep 30`, "sh", filepath.Join(dir, "ready")}, os.Stdin, os.Stdout, os.Stderr)
		os.Exit(code)
	}
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	_, _, _ = heartbeatRuntime(t, srv)
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHarnessRunSIGTERM$")
	cmd.Env = append(os.Environ(), "AEON_TEST_HARNESS_RUN=1", "AEON_TEST_RUN_DIR="+dir)
	// Do not capture or log inherited credentials or request proofs.
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(20 * time.Millisecond)
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
	regs := hbWhere(calls, http.MethodPost, "/harness-sessions")
	if len(regs) != 1 || regs[0].body["role"] != "worker" || regs[0].body["display_label"] != "Review" {
		t.Fatal("review registration missing")
	}
	if len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatal("SIGTERM did not stop session")
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
