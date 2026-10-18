// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

// TestCleanStopReleasesOwnedRuntime is the signal and stop-command proof.
// The helper blocks in a read that ignores the signal, the same shape as a
// drain that has not returned, and the parent checks the files while that
// process is still alive.
func TestCleanStopReleasesOwnedRuntime(t *testing.T) {
	if mode := os.Getenv("AEON_CLEAN_STOP_MODE"); mode != "" {
		runCleanStopHelper(t, mode)
		return
	}
	for _, mode := range []string{"term", "int", "stop", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			dir := socketTestDir(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestCleanStopReleasesOwnedRuntime$", "-test.timeout=20s", "-test.count=1")
			cmd.Dir = dir
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + dir,
				"GOMAXPROCS=2",
				"AEON_CLEAN_STOP_MODE=" + mode,
				"AEON_CLEAN_STOP_DIR=" + dir,
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var output []byte
			cmd.Stderr = &captureWriter{buf: &output}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil && cmd.Process != nil {
					_ = cmd.Process.Kill()
					_, _ = cmd.Process.Wait()
				}
			})
			waitChild := func() {
				t.Helper()
				_ = stdin.Close()
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("helper %s: %v\n%s", mode, err, output)
					}
				case <-time.After(5 * time.Second):
					_ = cmd.Process.Kill()
					t.Fatalf("helper %s did not exit\n%s", mode, output)
				}
			}
			type readyLine struct {
				line string
				err  error
			}
			readyCh := make(chan readyLine, 1)
			go func() {
				line, err := bufio.NewReader(stdout).ReadString('\n')
				readyCh <- readyLine{line, err}
			}()
			var got readyLine
			select {
			case got = <-readyCh:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				waitChild()
				t.Fatalf("helper %s did not become ready\n%s", mode, output)
			}
			if got.err != nil || got.line != "ready\n" {
				waitChild()
				t.Fatalf("helper %s did not become ready: %v %q\n%s", mode, got.err, got.line, output)
			}
			socket := filepath.Join(dir, "agentd.sock")
			peer := filepath.Join(dir, "hook-peer.json")
			if mode != "stop" {
				sig := syscall.SIGTERM
				if mode == "int" {
					sig = syscall.SIGINT
				}
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				if mode == "term" {
					if err := cmd.Process.Signal(sig); err != nil {
						t.Fatal(err)
					}
				}
				waitAbsent(t, socket)
				waitAbsent(t, socket+".token")
				if mode == "foreign" {
					raw, err := os.ReadFile(peer)
					if err != nil || string(raw) != fmt.Sprintf(`{"pid":%d}`, os.Getpid()) {
						t.Fatalf("foreign pin changed: %v %q", err, raw)
					}
				} else {
					waitAbsent(t, peer)
				}
				if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
					t.Fatalf("cleanup ran only after the process exited: %v", err)
				}
				if _, err := io.WriteString(stdin, "\n"); err != nil {
					t.Fatal(err)
				}
			}
			waitChild()
			waitAbsent(t, socket)
			waitAbsent(t, socket+".token")
			if mode == "foreign" {
				raw, err := os.ReadFile(peer)
				if err != nil || string(raw) != fmt.Sprintf(`{"pid":%d}`, os.Getpid()) {
					t.Fatalf("foreign pin removed: %v %q", err, raw)
				}
				return
			}
			waitAbsent(t, peer)
		})
	}
}

type captureWriter struct{ buf *[]byte }

func (w captureWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

func waitAbsent(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stop left %s: %v", filepath.Base(path), err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runCleanStopHelper(t *testing.T, mode string) {
	dir := os.Getenv("AEON_CLEAN_STOP_DIR")
	store, err := agentsetup.OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	local, err := ServePairedLocal(&Supervisor{state: store}, filepath.Join(dir, "agentd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	pid := os.Getpid()
	if mode == "foreign" {
		pid = os.Getppid()
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("foreign pid is not live: %v", err)
		}
	}
	if err := store.Write("hook-peer.json", []byte(fmt.Sprintf(`{"pid":%d}`, pid)), false); err != nil {
		t.Fatal(err)
	}
	if mode == "stop" {
		// The in-process stop, called twice. The third Close is the deferred
		// listener close a later return would also run.
		stop := ArmCleanStop(context.Background(), local)
		stop()
		stop()
		if err := local.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stdout.WriteString("ready\n"); err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, stopNotify := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopNotify()
	ArmCleanStop(ctx, local)
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	// This read stands in for a drain that does not observe the signal.
	// Cleanup has to finish before it returns.
	_, _ = bufio.NewReader(os.Stdin).ReadBytes('\n')
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
}
