// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package runisolation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
)

// R3/R9/R11: concurrent allocations must not share external resources; lost
// ownership must be reported, never recycled or used as signaling authority.
func TestIsolationAllocationAndOrphanReport(t *testing.T) {
	root := filepath.Join(t.TempDir(), "registry")
	start := make(chan struct{})
	type result struct {
		lease *Lease
		err   error
	}
	results := make(chan result, 2)
	for _, run := range []string{"attempt-a", "attempt-b"} {
		go func() {
			<-start
			l, err := Acquire(t.Context(), root, Owner{"tenant", "account", "host", run})
			results <- result{l, err}
		}()
	}
	close(start)
	leases := []*Lease{}
	t.Cleanup(func() {
		for _, l := range leases {
			if !l.closed {
				_ = l.Finish(true)
			}
		}
	})
	ports := map[int]bool{}
	completed := []result{<-results, <-results}
	for _, r := range completed {
		if r.lease != nil {
			leases = append(leases, r.lease)
		}
	}
	for _, r := range completed {
		if r.err != nil {
			t.Fatal(r.err)
		}
		for _, port := range r.lease.Record.Ports {
			if ports[port] {
				t.Fatal("parallel workers shared a port")
			}
			ports[port] = true
			listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if listener != nil {
				listener.Close()
				t.Fatal("allocated socket was not reserved")
			}
			if !errors.Is(err, syscall.EADDRINUSE) {
				t.Fatalf("socket collision failed for wrong reason: %v", err)
			}
		}
	}
	a, b := leases[0], leases[1]
	if a.Record.Database == b.Record.Database || a.Record.TempDir == b.Record.TempDir {
		t.Fatal("parallel workers shared database/temp directory")
	}
	if err := os.WriteFile(filepath.Join(a.Record.TempDir, "fixture"), []byte("kept"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(t.Context(), root, a.Record.Owner); !errors.Is(err, ErrRetained) {
		t.Fatalf("replay must refuse retained attempt: %v", err)
	}
	reports, err := Check(t.Context(), root)
	if err != nil || len(reports) != 2 || reports[0].Orphan || reports[1].Orphan {
		t.Fatalf("live report: %+v %v", reports, err)
	}
	// Simulate the allocator owner disappearing without a completion receipt.
	// No fixture has been started, so this test never leaves a live process.
	a.closeHandles()
	a.closed = true
	reports, err = Check(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, report := range reports {
		if report.ID == a.Record.ID {
			found = report.Orphan && report.Owner == a.Record.Owner && report.State == "reserved"
		}
	}
	if !found {
		t.Fatal("orphan check lost exact owning run")
	}
	if _, err := Acquire(t.Context(), root, a.Record.Owner); !errors.Is(err, ErrRetained) {
		t.Fatalf("orphan replay: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(a.Record.TempDir, "fixture")); err != nil || string(data) != "kept" {
		t.Fatal("report destroyed retained fixture")
	}
	// Identity bounds and root symlinks fail before resource allocation.
	if _, err := Acquire(t.Context(), root, Owner{"tenant", "account", "host", "../../other"}); err == nil {
		t.Fatal("path-shaped run accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(t.Context(), link, b.Record.Owner); err == nil {
		t.Fatal("symlink registry accepted")
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := Check(t.Context(), missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report created absent registry: %v", err)
	}
	t.Run("cross-process", func(t *testing.T) {
		registry := filepath.Join(t.TempDir(), "registry")
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		type worker struct {
			cmd    *exec.Cmd
			input  io.WriteCloser
			output io.ReadCloser
			done   chan error
		}
		workers := []*worker{}
		t.Cleanup(func() {
			for _, w := range workers {
				w.input.Close()
				w.output.Close()
				<-w.done
			}
		})
		for _, attempt := range []string{"process-a", "process-b"} {
			request, _ := json.Marshal(Request{Root: registry, Owner: Owner{"tenant", "account", "host", attempt}})
			cmd := exec.Command(executable, "-test.run=^TestIsolationHelper$", "isolation-allocate", string(request))
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			w := &worker{cmd, input, output, make(chan error, 1)}
			workers = append(workers, w)
			go func() { w.done <- w.cmd.Wait() }()
		}
		// Both independent processes wait at the same pipe barrier before flock.
		for _, w := range workers {
			if _, err := w.input.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
		}
		records := []Record{}
		for _, w := range workers {
			var r Record
			if err := json.NewDecoder(w.output).Decode(&r); err != nil {
				t.Fatal(err)
			}
			records = append(records, r)
		}
		if records[0].Database == records[1].Database || records[0].TempDir == records[1].TempDir {
			t.Fatal("independent processes shared resources")
		}
		for _, a := range records[0].Ports {
			for _, b := range records[1].Ports {
				if a == b {
					t.Fatal("independent processes shared a port")
				}
			}
		}
		if err := workers[0].cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		if err := <-workers[0].done; err == nil {
			t.Fatal("allocation owner did not crash")
		}
		// Restore the consumed completion for the common cleanup join.
		workers[0].done <- nil
		report, err := Check(t.Context(), registry)
		if err != nil || len(report) != 2 {
			t.Fatalf("process orphan report: %+v %v", report, err)
		}
		found := false
		for _, row := range report {
			if row.Owner.RunID == "process-a" {
				found = row.Orphan
			}
			if row.Owner.RunID == "process-b" && row.Orphan {
				t.Fatal("live independent owner reported orphan")
			}
		}
		if !found {
			t.Fatal("real process crash lost owning allocation")
		}
	})
}

// R9/R11: normal completion, cancellation and a SIGKILL of the controller must
// clean the actual child group, while an unrelated live fixture survives.
// FIFO readiness and witness EOF establish the interleaving and actual exit.
func TestIsolationControllerCrashCleansOnlyOwnedFixtures(t *testing.T) {
	for _, mode := range []string{"finish", "cancel", "crash"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AEON_RUN_DATABASE", "inherited-database-must-be-replaced")
			t.Setenv("AEON_RUN_APP_PORT", "inherited-port-must-be-replaced")
			fixture := processtest.New(t)
			unrelated := processtest.New(t)
			otherCtx, stopOther := context.WithCancel(t.Context())
			other, err := ownedprocess.Start(otherCtx, exec.Command("sh", "-c", unrelated.Script))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { stopOther(); _ = other.Wait() })
			unrelated.Ready(t)
			root := filepath.Join(t.TempDir(), "registry")
			owner := Owner{"tenant", "account", "host", "attempt-" + mode}
			// The actual child must consume the run's resources instead of the
			// controller's inherited fixture settings before releasing readiness.
			script := fmt.Sprintf(`[ "$AEON_RUN_ID" = %q ] && [ "$TMPDIR" = "$AEON_RUN_TEMP_DIR" ] && [ -d "$TMPDIR" ] && [ "$AEON_RUN_APP_PORT" != "$AEON_RUN_POSTGRES_PORT" ] || exit 23
case "$AEON_RUN_DATABASE" in aeon_run_*) ;; *) exit 23 ;; esac
case "$AEON_RUN_APP_PORT" in ''|*[!0-9]*) exit 23 ;; esac
%s`, owner.RunID, fixture.Script)
			request := Request{Root: root, Owner: owner, Directory: t.TempDir(), Command: []string{"sh", "-c", script}}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			if mode == "crash" {
				b, _ := json.Marshal(request)
				worker := exec.Command(executable, "-test.run=^TestIsolationHelper$", "isolation-worker", string(b))
				worker.Stdout, worker.Stderr = io.Discard, io.Discard
				if err := worker.Start(); err != nil {
					t.Fatal(err)
				}
				go func() { done <- worker.Wait() }()
				fixture.Ready(t)
				// Only our unreaped child is killed to simulate a worker crash.
				if err := worker.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				go func() {
					done <- Control(ctx, executable, []string{"-test.run=^TestIsolationHelper$", "isolation-guard"}, request, io.Discard, io.Discard)
				}()
				fixture.Ready(t)
				if mode == "finish" {
					fixture.ExitRoot()
				} else {
					cancel()
				}
			}
			select {
			case err := <-done:
				if mode == "finish" && err != nil {
					t.Fatalf("normal completion: %v", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel failed for wrong reason: %v", err)
				}
				if mode == "crash" {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
						t.Fatalf("crash failed for wrong reason: %v", err)
					}
				}
			case <-time.After(10 * time.Second):
				t.Fatal("guardian cleanup hung")
			}
			fixture.AssertExited(t)
			// The worker's output pipe EOF also proves its guardian exited.
			reports, err := Check(t.Context(), root)
			if err != nil || len(reports) != 1 || reports[0].Owner != owner || reports[0].State != "stopped" || reports[0].Orphan {
				t.Fatalf("completion receipt: %+v %v", reports, err)
			}
			if err := otherCtx.Err(); err != nil {
				t.Fatal("unrelated fixture canceled")
			}
			// A positive liveness assertion rejects over-broad group cleanup.
			unrelated.ExitRoot()
			if err := other.Wait(); err != nil {
				t.Fatalf("unrelated fixture did not survive: %v", err)
			}
			unrelated.AssertExited(t)
		})
	}
}

func TestIsolationHelper(t *testing.T) {
	if len(os.Args) < 2 {
		return
	}
	args := os.Args
	if args[len(args)-1] == "isolation-guard" {
		if err := Guard(context.Background(), os.Stdin, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) >= 3 && args[len(args)-2] == "isolation-worker" {
		var request Request
		if err := json.Unmarshal([]byte(args[len(args)-1]), &request); err != nil {
			os.Exit(2)
		}
		executable, _ := os.Executable()
		if err := Control(context.Background(), executable, []string{"-test.run=^TestIsolationHelper$", "isolation-guard"}, request, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) >= 3 && args[len(args)-2] == "isolation-allocate" {
		var request Request
		if json.Unmarshal([]byte(args[len(args)-1]), &request) != nil {
			os.Exit(2)
		}
		if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
			os.Exit(2)
		}
		lease, err := Acquire(context.Background(), request.Root, request.Owner)
		if err != nil {
			os.Exit(1)
		}
		if json.NewEncoder(os.Stdout).Encode(lease.Record) != nil {
			os.Exit(2)
		}
		_, _ = io.ReadFull(os.Stdin, make([]byte, 1))
		if lease.Finish(true) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	// Helper modes are reachable only via explicit synthetic test arguments.
	if strings.HasPrefix(args[len(args)-1], "isolation-") {
		t.Fatal("unknown helper mode")
	}
}
