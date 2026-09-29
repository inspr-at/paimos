// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package agentd

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

type terminalLifetimeHooks struct {
	signal func(bool) error
	wait   func() error
}

func (h terminalLifetimeHooks) Signal(force bool) error { return h.signal(force) }
func (h terminalLifetimeHooks) Wait() error             { return h.wait() }

type terminalReaderFunc func([]byte) (int, error)

func (f terminalReaderFunc) Read(p []byte) (int, error) { return f(p) }

type terminalResult struct {
	out string
	err error
}

// Each fixture is a newly spawned shell using only builtins. The stdin gate
// gives the test an exact exit schedule; no persisted PID or process lookup is
// used. Cleanup can only signal this child's unreaped Lifetime.
type terminalChild struct {
	cmd      *exec.Cmd
	lifetime *ownedprocess.Lifetime
	input    io.WriteCloser
	output   *os.File
	waitOnce sync.Once
	waitErr  error
}

func newTerminalChild(t *testing.T, script string) *terminalChild {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, writer, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = writer, writer
	if !ownedprocess.Configure(cmd) {
		t.Fatal("fixture needs owned process groups")
	}
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		_ = output.Close()
		_ = input.Close()
		t.Fatal(err)
	}
	_ = writer.Close()
	child := &terminalChild{cmd: cmd, lifetime: ownedprocess.Track(cmd), input: input, output: output}
	t.Cleanup(func() {
		_ = input.Close()
		_ = child.lifetime.Signal(true)
		_ = child.Wait()
		_ = output.Close()
	})
	if err := child.lifetime.Verify(); err != nil {
		t.Fatal(err)
	}
	return child
}

func (c *terminalChild) Wait() error {
	c.waitOnce.Do(func() { c.waitErr = c.lifetime.Wait() })
	return c.waitErr
}

func awaitTerminalEvent(t *testing.T, event <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func awaitTerminalResult(t *testing.T, done <-chan terminalResult) terminalResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("terminal did not finish")
		return terminalResult{}
	}
}

func TestTerminalLifetimeDelayedCancellationAfterReap(t *testing.T) {
	readErr := errors.New("injected terminal read failure")
	for _, tc := range []struct {
		name       string
		script     string
		output     string
		readErr    error
		wantOutput string
		wantErr    string
	}{
		{name: "success", script: "read finish; exit 0", output: "ok", wantOutput: "ok"},
		{name: "command_error", script: "read finish; exit 7", output: "failed", wantOutput: "failed", wantErr: "terminal command failed:"},
		{name: "output_overflow", script: "read finish", output: strings.Repeat("x", (64<<10)+1), wantErr: "terminal output exceeds 64 KiB"},
		{name: "read_error", script: "read finish", readErr: readErr, wantErr: readErr.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := newTerminalChild(t, tc.script)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			signalEntered := make(chan struct{})
			resumeSignal := make(chan struct{})
			signalDone := make(chan struct{})
			reaped := make(chan struct{})
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(resumeSignal) }) }
			t.Cleanup(resume)
			var signalCalls atomic.Int32
			var rejected atomic.Bool
			var forced atomic.Bool
			var waits atomic.Int32
			hooks := terminalLifetimeHooks{
				signal: func(force bool) error {
					if signalCalls.Add(1) == 1 {
						// The cancelled context wins the select, but its worker
						// pauses before entering the real lifetime's signal lock.
						forced.Store(force)
						close(signalEntered)
						<-resumeSignal
						err := child.lifetime.Signal(force)
						rejected.Store(err != nil)
						close(signalDone)
						return err
					}
					return child.lifetime.Signal(force)
				},
				wait: func() error {
					waits.Add(1)
					err := child.Wait()
					close(reaped)
					return err
				},
			}
			var readOnce sync.Once
			output := strings.NewReader(tc.output)
			reader := terminalReaderFunc(func(p []byte) (int, error) {
				readOnce.Do(func() {
					<-signalEntered
					if tc.wantErr == "" || tc.wantOutput != "" {
						_ = child.input.Close()
					}
				})
				if tc.readErr != nil {
					return 0, tc.readErr
				}
				return output.Read(p)
			})
			done := make(chan terminalResult, 1)
			go func() {
				out, err := collectTerminal(ctx, reader, hooks)
				done <- terminalResult{out, err}
			}()
			awaitTerminalEvent(t, reaped, "child reap while cancellation is paused")
			if child.cmd.ProcessState == nil {
				t.Fatal("test did not release the child identity")
			}
			// The invocation must join even a cancellation already selected
			// before finished closes. This pause is only a bounded assertion
			// of non-return; the interleaving itself is channel-controlled.
			select {
			case <-done:
				t.Fatal("terminal returned while cancellation worker was paused")
			case <-time.After(20 * time.Millisecond):
			}
			resume()
			result := awaitTerminalResult(t, done)
			select {
			case <-signalDone:
			default:
				t.Fatal("terminal did not join cancellation worker")
			}
			if !rejected.Load() || !forced.Load() {
				t.Fatal("delayed force cancellation was not rejected after identity release")
			}
			if waits.Load() != 1 {
				t.Fatalf("reaper calls=%d; want 1", waits.Load())
			}
			if result.out != tc.wantOutput || tc.wantErr == "" && result.err != nil || tc.wantErr != "" && (result.err == nil || !strings.Contains(result.err.Error(), tc.wantErr)) {
				t.Fatalf("result=%q, %v; want %q, %q", result.out, result.err, tc.wantOutput, tc.wantErr)
			}
			if tc.readErr != nil && !errors.Is(result.err, tc.readErr) {
				t.Fatal("read error identity was lost")
			}
		})
	}
}

func TestTerminalLifetimeCompletion(t *testing.T) {
	readErr := errors.New("injected read error")
	for _, tc := range []struct {
		name       string
		script     string
		readErr    error
		wantOutput string
		wantErr    string
		wantSignal bool
	}{
		{name: "success", script: "printf ok; exit 0", wantOutput: "ok"},
		{name: "command_error", script: "printf failed; exit 7", wantOutput: "failed", wantErr: "terminal command failed:"},
		{name: "output_overflow", script: "while :; do printf 0123456789abcdef; done", wantErr: "terminal output exceeds 64 KiB", wantSignal: true},
		{name: "read_error", script: "read finish", readErr: readErr, wantErr: readErr.Error(), wantSignal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := newTerminalChild(t, tc.script)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var signals atomic.Int32
			var waits atomic.Int32
			hooks := terminalLifetimeHooks{
				signal: func(force bool) error { signals.Add(1); return child.lifetime.Signal(force) },
				wait:   func() error { waits.Add(1); return child.Wait() },
			}
			var output io.Reader = child.output
			if tc.readErr != nil {
				output = terminalReaderFunc(func([]byte) (int, error) { return 0, tc.readErr })
			}
			done := make(chan terminalResult, 1)
			go func() {
				out, err := collectTerminal(ctx, output, hooks)
				done <- terminalResult{out, err}
			}()
			result := awaitTerminalResult(t, done)
			// This models runTerminal's deferred cancellation after return.
			// The worker has already joined, so it cannot race the reap.
			cancel()
			if result.out != tc.wantOutput || tc.wantErr == "" && result.err != nil || tc.wantErr != "" && (result.err == nil || !strings.Contains(result.err.Error(), tc.wantErr)) {
				t.Fatalf("result=%q, %v; want %q, %q", result.out, result.err, tc.wantOutput, tc.wantErr)
			}
			wantSignals := int32(0)
			if tc.wantSignal {
				wantSignals = 1
			}
			if signals.Load() != wantSignals || waits.Load() != 1 || child.cmd.ProcessState == nil {
				t.Fatalf("signals=%d waits=%d reaped=%t", signals.Load(), waits.Load(), child.cmd.ProcessState != nil)
			}
		})
	}
}

func TestTerminalLifetimeCancellationDuringWait(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[timeout], func(t *testing.T) {
			// The child closes both output descriptors but stays alive on stdin.
			// Stopping the cancellation worker at EOF would deadlock Wait here.
			child := newTerminalChild(t, "printf ready; exec 1>&- 2>&-; read finish")
			ctx, cancel := context.WithCancel(t.Context())
			if timeout {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
			}
			defer cancel()
			waiting := make(chan struct{})
			var signalled atomic.Bool
			hooks := terminalLifetimeHooks{
				signal: func(force bool) error {
					err := child.lifetime.Signal(force)
					signalled.Store(force && err == nil)
					return err
				},
				wait: func() error { return child.Wait() },
			}
			done := make(chan terminalResult, 1)
			go func() {
				reader := terminalReaderFunc(func(p []byte) (int, error) {
					n, err := child.output.Read(p)
					if err == io.EOF {
						close(waiting)
					}
					return n, err
				})
				out, err := collectTerminal(ctx, reader, hooks)
				done <- terminalResult{out, err}
			}()
			awaitTerminalEvent(t, waiting, "Wait after EOF")
			if !timeout {
				cancel()
			}
			result := awaitTerminalResult(t, done)
			if !signalled.Load() || child.cmd.ProcessState == nil || result.out != "ready" || result.err == nil || !strings.Contains(result.err.Error(), "terminal command failed:") {
				t.Fatalf("result=%q, %v; signalled=%t reaped=%t", result.out, result.err, signalled.Load(), child.cmd.ProcessState != nil)
			}
			if timeout && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("expected deadline, got %v", ctx.Err())
			}
		})
	}
}

func TestTerminalCompletionKillsBackgroundGroupMembers(t *testing.T) {
	for _, inherit := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed_output", true: "inherited_output"}[inherit], func(t *testing.T) {
			// The only descendants are our fixture shell and sleep. The shell exits
			// immediately; without group cleanup its child creates a sentinel later.
			root := t.TempDir()
			sentinel := filepath.Join(root, "escaped-child")
			redirect := " >/dev/null 2>&1"
			if inherit {
				redirect = ""
			}
			child := newTerminalChild(t, "(sleep 1; printf escaped > '"+sentinel+"')"+redirect+" &\nprintf ready\nexit 0")
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			out, err := collectTerminal(ctx, child.output, terminalGroupLifetime{child.lifetime})
			// collectTerminal owns this reap; prevent fixture cleanup from reaping again.
			child.waitOnce.Do(func() {})
			if err != nil || out != "ready" {
				t.Fatalf("terminal group completion: %q %v", out, err)
			}
			time.Sleep(1200 * time.Millisecond)
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatal("background child outlived terminal completion")
			}
		})
	}
}
