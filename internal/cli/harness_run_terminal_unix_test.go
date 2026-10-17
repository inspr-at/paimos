// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
		if os.Args[len(os.Args)-1] == "interactive-child" {
			for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
				if !term.IsTerminal(int(f.Fd())) {
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
			fmt.Fprintln(os.Stdout, "AEON1410_READY")
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil || line != "fixture input\n" {
				os.Exit(24)
			}
			fmt.Fprintln(os.Stdout, "AEON1410_STDOUT")
			fmt.Fprintln(os.Stderr, "AEON1410_STDERR")
			// Deliberately leave raw mode enabled to prove wrapper restoration.
			os.Exit(0)
		}
		var args []string
		for i, arg := range os.Args {
			if arg == "--" {
				args = os.Args[i+1:]
				break
			}
		}
		before, beforeErr := term.GetState(0)
		code := Run(args, os.Stdin, os.Stdout, os.Stderr)
		group, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
		after, afterErr := term.GetState(0)
		if err != nil || group != syscall.Getpgrp() || beforeErr != nil || afterErr != nil || !reflect.DeepEqual(before, after) {
			fmt.Fprintln(os.Stderr, "AEON1410_RESTORE_FAILED")
			os.Exit(25)
		}
		os.Exit(code)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the controlling-terminal fixture")
	}
	for _, instance := range []string{"pma", "ppm"} {
		for _, parent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parent=%t", instance, parent), func(t *testing.T) {
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
				// No installed model CLI or account probe may run in this fixture.
				t.Setenv("PATH", t.TempDir())
				dir := t.TempDir()
				cfg := filepath.Join(dir, "config.yaml")
				if err := saveConfig(cfg, fileConfig{Instances: map[string]fileInstance{instance: {URL: srv.URL, APIKey: testKey}}}); err != nil {
					t.Fatal(err)
				}
				if err := writeKey(keyPath(cfg, instance), testKey); err != nil {
					t.Fatal(err)
				}
				args := []string{"-c", harnessRunPTYDriver, os.Args[0], "-test.run=^TestHarnessRunInteractiveTTY$", "--", "aeon", "--config", cfg, "--instance", instance,
					"harness", "run", "--ticket", "AEON-1410", "--agent", "worker", "--harness", "claude", "--label", "START", "--model", "fixture-model", "--effort", "xhigh", "--state-dir", filepath.Join(dir, "state")}
				if parent {
					args = append(args, "--parent-session", transcriptEntryID)
				}
				args = append(args, "--", os.Args[0], "-test.run=^TestHarnessRunInteractiveTTY$", "--", "interactive-child")
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				output, err := exec.CommandContext(ctx, python, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("PTY wrapper: %v\n%s", err, output)
				}
				if !strings.Contains(string(output), "AEON1410_STDOUT") || !strings.Contains(string(output), "AEON1410_STDERR") || strings.Contains(string(output), "RESTORE_FAILED") {
					t.Fatalf("terminal streams/restoration changed: %s", output)
				}
				srv.Close() // Join handlers before reading their recorded requests.
				regs, stops := hbWhere(calls, http.MethodPost, "/harness-sessions"), hbWhere(calls, http.MethodPost, "/stop")
				if len(regs) != 1 || len(stops) != 1 || stops[0].body["reason"] != "process_exited" {
					t.Fatal("interactive child did not register and stop exactly once")
				}
				if regs[0].body["harness"] != "claude" || regs[0].body["display_label"] != "START" || regs[0].body["model"] != "fixture-model" || regs[0].body["ticket_node_id"] != transcriptEntryID {
					t.Fatal("interactive registration lost its metadata")
				}
				if got := regs[0].body["parent_harness_session_id"]; parent && got != transcriptEntryID || !parent && got != nil {
					t.Fatal("parent selection changed")
				}
				if _, err := os.Stat(filepath.Join(dir, "state", "state.json")); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// pty.fork gives the wrapper a real controlling terminal, as in a shell or
// terminal multiplexer. Readiness establishes input ordering; deadlines only
// bound hangs. The wrapper retains its signal cleanup on any fixture failure.
const harnessRunPTYDriver = `
import errno, os, pty, select, signal, sys, time
pid, master = pty.fork()
if pid == 0:
    os.execv(sys.argv[1], sys.argv[1:])
output, sent, reaped = bytearray(), False, False
try:
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if select.select([master], [], [], 0.1)[0]:
            try:
                chunk = os.read(master, 4096)
            except OSError as exc:
                if exc.errno != errno.EIO:
                    raise
                break
            if not chunk:
                break
            output.extend(chunk)
            if not sent and b'AEON1410_READY' in output:
                os.write(master, b'fixture input\n')
                sent = True
        done, status = os.waitpid(pid, os.WNOHANG)
        if done:
            reaped = True
            break
    if not reaped:
        done, status = os.waitpid(pid, os.WNOHANG)
        reaped = bool(done)
    if not reaped:
        raise RuntimeError('PTY child did not complete')
    sys.stdout.buffer.write(output)
    sys.exit(os.waitstatus_to_exitcode(status))
finally:
    if not reaped:
        os.kill(pid, signal.SIGTERM)
        os.waitpid(pid, 0)
    os.close(master)
`
