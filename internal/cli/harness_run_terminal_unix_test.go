// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Risk: an interactive worker loses terminal access or escapes owned stopping,
// leaving its registered generation invisible, stuck or falsely completed.
func TestHarnessRunInteractiveTTY(t *testing.T) {
	if os.Getenv("AEON_TEST_INTERACTIVE_RUN") == "1" {
		var args []string
		for i, arg := range os.Args {
			if arg == "--" {
				args = os.Args[i+1:]
				break
			}
		}
		if len(args) == 1 && args[0] == "interactive-child" {
			for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
				wantTerminal := f != os.Stdout || os.Getenv("AEON_TEST_INTERACTIVE_MODE") != "captured"
				if term.IsTerminal(int(f.Fd())) != wantTerminal {
					os.Exit(21)
				}
			}
			group, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
			if err != nil || group != syscall.Getpgrp() || group != os.Getpid() {
				os.Exit(22)
			}
			if _, err := term.MakeRaw(0); err != nil {
				os.Exit(23)
			}
			fmt.Fprintln(os.Stderr, "AEON1410_READY")
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil || line != "fixture input\n" {
				os.Exit(24)
			}
			fmt.Fprintln(os.Stdout, "AEON1410_STDOUT")
			fmt.Fprintln(os.Stderr, "AEON1410_STDERR")
			// Deliberately leave raw mode enabled to prove wrapper restoration.
			if os.Getenv("AEON_TEST_INTERACTIVE_MODE") == "failed" {
				os.Exit(7)
			}
			os.Exit(0)
		}
		beforeGroup, beforeErr := unix.IoctlGetInt(0, unix.TIOCGPGRP)
		code := Run(args, os.Stdin, os.Stdout, os.Stderr)
		group, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
		if !errors.Is(err, beforeErr) || group != beforeGroup {
			fmt.Fprintf(os.Stderr, "AEON1410_RESTORE_FAILED group=%d want=%d error=%v\n", group, beforeGroup, err)
			os.Exit(25)
		}
		os.Exit(code)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the controlling-terminal fixture")
	}
	for _, tc := range []struct {
		instance, role, mode, reason string
		parent                       bool
		code                         int
	}{
		{instance: "pma", role: "worker", reason: "process_exited"},
		{instance: "pma", role: "coordinator", parent: true, reason: "process_exited"},
		{instance: "ppm", role: "coordinator", reason: "process_exited"},
		{instance: "ppm", role: "worker", parent: true, reason: "process_exited"},
		{instance: "pma", role: "worker", mode: "captured", reason: "process_exited"},
		{instance: "pma", role: "worker", mode: "failed", reason: "process_failed", code: 7},
		{instance: "pma", role: "worker", mode: "missing", reason: "process_failed", code: 1},
		{instance: "pma", role: "worker", mode: "foreign", reason: "process_failed", code: 1},
		{instance: "pma", role: "worker", mode: "interrupted", reason: "stopped", code: 143},
	} {
		t.Run(fmt.Sprintf("%s/parent=%t/%s", tc.instance, tc.parent, tc.mode), func(t *testing.T) {
			var calls []hbCall
			srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
				if r.Method == http.MethodGet && r.URL.Path == "/api/nodes" && r.URL.Query().Get("q") == "AEON-1410" {
					_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": transcriptEntryID, "key": "AEON-1410", "is_leaf": true}}})
					return true
				}
				return false
			})
			defer srv.Close()
			_, _, _ = heartbeatRuntime(t, srv)
			t.Setenv("AEON_URL", "")
			t.Setenv("AEON_API_KEY", "")
			t.Setenv("AEON_TEST_INTERACTIVE_RUN", "1")
			t.Setenv("AEON_TEST_INTERACTIVE_MODE", tc.mode)
			// No installed model CLI or account probe may run in this fixture.
			t.Setenv("PATH", t.TempDir())
			dir := t.TempDir()
			t.Setenv("AEON_TEST_INTERACTIVE_STDOUT", filepath.Join(dir, "stdout"))
			cfg := filepath.Join(dir, "config.yaml")
			if err := saveConfig(cfg, fileConfig{Instances: map[string]fileInstance{tc.instance: {URL: srv.URL}}}); err != nil {
				t.Fatal(err)
			}
			if err := writeKey(keyPath(cfg, tc.instance), testKey); err != nil {
				t.Fatal(err)
			}
			args := []string{"-c", harnessRunPTYDriver, os.Args[0], "-test.run=^TestHarnessRunInteractiveTTY$", "--", "aeon", "--config", cfg, "--instance", tc.instance,
				"harness", "run", "--ticket", "AEON-1410", "--agent", "worker", "--role", tc.role, "--harness", "claude", "--label", "START", "--model", "fixture-model", "--effort", "xhigh", "--state-dir", filepath.Join(dir, "state")}
			if tc.parent {
				args = append(args, "--parent-session", transcriptEntryID)
			}
			if tc.mode == "missing" {
				args = append(args, "--", filepath.Join(dir, "missing-command"))
			} else {
				args = append(args, "--", os.Args[0], "-test.run=^TestHarnessRunInteractiveTTY$", "--", "interactive-child")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, python, args...).CombinedOutput()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("PTY wrapper: %v\n%s", err, output)
				}
				code = exit.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("PTY wrapper code=%d want=%d\n%s", code, tc.code, output)
			}
			launched := tc.mode != "missing" && tc.mode != "foreign" && tc.mode != "interrupted"
			if launched && (!strings.Contains(string(output), "AEON1410_STDOUT") || !strings.Contains(string(output), "AEON1410_STDERR")) || strings.Contains(string(output), "RESTORE_FAILED") {
				t.Fatalf("terminal streams/restoration changed: %s", output)
			}
			if tc.code != 0 && !strings.Contains(string(output), "harness run:") && !strings.Contains(string(output), "no such file") {
				t.Fatalf("silent PTY failure: %s", output)
			}
			srv.Close() // Join handlers before reading their recorded requests.
			regs, stops := hbWhere(calls, http.MethodPost, "/harness-sessions"), hbWhere(calls, http.MethodPost, "/stop")
			if len(regs) != 1 || len(stops) != 1 || stops[0].body["reason"] != tc.reason {
				reasons := make([]any, 0, len(stops))
				for _, stop := range stops {
					reasons = append(reasons, stop.body["reason"])
				}
				t.Fatalf("interactive registration/stop: registrations=%d stops=%d reasons=%v output=%s", len(regs), len(stops), reasons, output)
			}
			if regs[0].body["harness"] != "claude" || regs[0].body["display_label"] != "START" || regs[0].body["role"] != tc.role || regs[0].body["model"] != "fixture-model" || regs[0].body["ticket_node_id"] != transcriptEntryID {
				t.Fatal("interactive registration lost its metadata")
			}
			if got := regs[0].body["parent_harness_session_id"]; tc.parent && got != transcriptEntryID || !tc.parent && got != nil {
				t.Fatal("parent selection changed")
			}
			if _, err := os.Stat(filepath.Join(dir, "state", "state.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// pty.fork gives the wrapper a real controlling terminal, as in a shell or
// terminal multiplexer. Readiness establishes input ordering; deadlines only
// bound hangs. The wrapper retains its signal cleanup on any fixture failure.
const harnessRunPTYDriver = `
import errno, os, pty, select, signal, sys, termios, time
start_r, start_w = os.pipe()
if os.environ['AEON_TEST_INTERACTIVE_MODE'] == 'foreign':
    master, slave = pty.openpty()
    pid = os.fork()
    if pid == 0:
        os.setsid()  # stdin is a terminal, but is not ours to foreground.
        os.close(master)
        for fd in (0, 1, 2):
            os.dup2(slave, fd)
        os.close(slave)
    else:
        os.close(slave)
else:
    pid, master = pty.fork()
if pid == 0:
    os.close(start_w)
    if os.read(start_r, 1) != b'1':
        os._exit(26)
    os.close(start_r)
    if os.environ['AEON_TEST_INTERACTIVE_MODE'] == 'captured':
        output_fd = os.open(os.environ['AEON_TEST_INTERACTIVE_STDOUT'], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        os.dup2(output_fd, 1)
        os.close(output_fd)
    os.execv(sys.argv[1], sys.argv[1:])
os.close(start_r)
before = termios.tcgetattr(master)
os.write(start_w, b'1')
os.close(start_w)
output, sent, reaped, eof = bytearray(), False, False, False
try:
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if select.select([] if eof else [master], [], [], 0.1)[0]:
            try:
                chunk = os.read(master, 4096)
            except OSError as exc:
                if exc.errno != errno.EIO:
                    raise
                chunk = b''
            if not chunk:
                eof = True
            output.extend(chunk)
            if len(output) > 1024 * 1024:
                raise RuntimeError('PTY fixture output exceeded its bound')
            if not sent and b'AEON1410_READY' in output:
                if os.environ['AEON_TEST_INTERACTIVE_MODE'] == 'interrupted':
                    os.kill(pid, signal.SIGTERM)
                else:
                    os.write(master, b'fixture input\n')
                sent = True
        if not reaped:
            done, status = os.waitpid(pid, os.WNOHANG)
            reaped = bool(done)
        if reaped and eof:
            break
    sys.stdout.buffer.write(output)
    sys.stdout.buffer.flush()
    if not reaped:
        raise RuntimeError('PTY child did not complete')
    after = termios.tcgetattr(master)
    # Darwin sets PENDIN when switching back to canonical input. This is
    # kernel reprocessing bookkeeping, not a changed user terminal setting.
    before[3] &= ~termios.PENDIN
    after[3] &= ~termios.PENDIN
    if before != after:
        raise RuntimeError('terminal settings were not restored')
    if os.environ['AEON_TEST_INTERACTIVE_MODE'] == 'captured':
        with open(os.environ['AEON_TEST_INTERACTIVE_STDOUT'], 'rb') as captured:
            sys.stdout.buffer.write(captured.read(4096))
    sys.exit(os.waitstatus_to_exitcode(status))
finally:
    if not reaped:
        os.kill(pid, signal.SIGTERM)
        os.waitpid(pid, 0)
    os.close(master)
`
