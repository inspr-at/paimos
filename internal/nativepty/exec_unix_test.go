// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package nativepty

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type fixtureBuilder struct{ plan CommandPlan }

func (b fixtureBuilder) Prepare(context.Context, nativecontract.PTYLaunchSpec) (CommandPlan, error) {
	return b.plan, nil
}

// A compiled test child, never vendor software, witnesses the real controlling
// PTY, process group and TIOCSWINSZ. Pipes/terminal messages establish barriers.
func TestPTYUnixOwnershipResizeAndExit(t *testing.T) {
	if os.Getenv("AEON_NATIVE_PTY_FIXTURE") == "1" {
		if unix.Getpgrp() != os.Getpid() {
			os.Exit(71)
		}
		size, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
		if err != nil {
			os.Exit(72)
		}
		fmt.Printf("ready %d %d\n", size.Col, size.Row)
		scan := bufio.NewScanner(os.Stdin)
		for scan.Scan() {
			switch scan.Text() {
			case "measure":
				size, err = unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
				if err != nil {
					os.Exit(73)
				}
				fmt.Printf("size %d %d\n", size.Col, size.Row)
			case "exit":
				os.Exit(0)
			}
		}
		os.Exit(74)
	}
	r, spec, ctx := openPTYFixture(t)
	readUntil := func(want string) {
		t.Helper()
		text := ""
		for !strings.Contains(text, want) {
			select {
			case frame, ok := <-r.Frames():
				if !ok {
					t.Fatal("PTY output ended before witness")
				}
				if frame.TerminalEpoch != spec.Binding.RuntimeEpoch {
					t.Fatal("wrong terminal epoch")
				}
				data, err := base64.StdEncoding.DecodeString(frame.DataBase64)
				if err != nil {
					t.Fatal(err)
				}
				text += string(data)
				if len(text) > 65536 {
					t.Fatal("fixture output exceeded bound")
				}
			case <-ctx.Done():
				t.Fatal("PTY witness never arrived")
			}
		}
	}
	readUntil("ready 120 36")
	resize := nativecontract.Resize{RequestID: uuid.NewString(), ExpectedBinding: spec.Binding, ControllerEpoch: uuid.NewString(), Cols: 132, Rows: 40, ExpiresAt: time.Now().Add(30 * time.Second)}
	if _, err := r.Resize(ctx, resize); err != nil {
		t.Fatal(err)
	}
	write := func(text string) {
		t.Helper()
		if _, err := r.Write(ctx, nativecontract.PTYInput{RequestID: uuid.NewString(), ExpectedBinding: spec.Binding, ControllerEpoch: resize.ControllerEpoch, Sequence: "1", DataBase64: base64.StdEncoding.EncodeToString([]byte(text)), ExpiresAt: resize.ExpiresAt}); err != nil {
			t.Fatal(err)
		}
	}
	write("measure\n")
	readUntil("size 132 40")
	write("exit\n")
	exit, err := r.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !exit.RootExited || !exit.GroupAbsent || exit.ProcessIdentityDigest != r.Identity().ProcessIdentityDigest {
		t.Fatal("owned root/group exit not witnessed")
	}
	if again, err := r.Wait(ctx); err != nil || again != exit {
		t.Fatal("wait reaped/signaled a second time")
	}
	if _, err = r.Resize(ctx, resize); err == nil {
		t.Fatal("reaped terminal accepted resize")
	}
}

func openPTYFixture(t *testing.T, environment ...string) (nativecontract.PTYRuntime, nativecontract.PTYLaunchSpec, context.Context) {
	t.Helper()
	_, f, _, _, _ := takeoverFixture(t)
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, binary)
	binary.Close()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := nativecontract.PTYLaunchSpec{LaunchSpec: cloneSpec(f.spec), Cols: 120, Rows: 36}
	spec.Mode = "terminal"
	spec.Cwd = dir
	spec.Profile.AccountHome = dir
	spec.Profile.WorkspaceRoot = dir
	spec.Profile.Binary = nativecontract.BinaryPin{Path: path, SHA256: hex.EncodeToString(hash.Sum(nil)), Version: "test-fixture", Protocol: "test-pty/1"}
	lifetime, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	spec.Lifetime = lifetime
	launcher := &ExecLauncher{Builder: fixtureBuilder{plan: CommandPlan{Args: []string{"-test.run=^" + t.Name() + "$"}, Environment: append([]string{"AEON_NATIVE_PTY_FIXTURE=1", "HOME=" + dir, "TERM=xterm-256color"}, environment...), Provider: "fixture", Require: func(context.Context, nativecontract.PTYLaunchSpec) error { return nil }}}}
	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(done)
	r, err := launcher.OpenPTY(ctx, spec)
	if r != nil {
		t.Cleanup(func() {
			cancel()
			cleanup, finish := context.WithTimeout(context.Background(), 10*time.Second)
			defer finish()
			if _, err := r.Wait(cleanup); err != nil {
				t.Errorf("fixture cleanup: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, spec, ctx
}

// Risk: a full input buffer must not prevent draining the child's output.
// The child emits more than both PTY and frame buffers before reading stdin.
func TestPTYUnixPasteDrainsOutputWhileInputIsBlocked(t *testing.T) {
	const outputBytes = 128 << 10
	paste := bytes.Repeat([]byte{'x'}, nativecontract.PTYInputDecodedBytes)
	if os.Getenv("AEON_NATIVE_PTY_FIXTURE") == "1" {
		if _, err := term.MakeRaw(0); err != nil {
			os.Exit(81)
		}
		control, err := net.DialTimeout("tcp", os.Getenv("AEON_PTY_FIXTURE_ADDRESS"), 10*time.Second)
		if err != nil {
			os.Exit(82)
		}
		defer control.Close()
		fmt.Fprintln(control, "ready")
		var prefix int
		if _, err := fmt.Fscan(control, &prefix); err != nil || prefix <= 0 || prefix > outputBytes {
			os.Exit(83)
		}
		if _, err := os.Stdout.Write(bytes.Repeat([]byte{'o'}, outputBytes)); err != nil {
			os.Exit(84)
		}
		input := make([]byte, prefix+len(paste))
		if _, err := io.ReadFull(os.Stdin, input); err != nil || !bytes.Equal(input[:prefix], bytes.Repeat([]byte{'p'}, prefix)) || !bytes.Equal(input[prefix:], paste) {
			os.Exit(85)
		}
		fmt.Fprintln(os.Stdout, "\nreceived 8192")
		var command string
		if _, err := fmt.Fscan(control, &command); err != nil || command != "exit" {
			os.Exit(86)
		}
		os.Exit(0)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	r, spec, ctx := openPTYFixture(t, "AEON_PTY_FIXTURE_ADDRESS="+listener.Addr().String())
	deadline, _ := ctx.Deadline()
	if err := listener.(*net.TCPListener).SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	control, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := control.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	var ready string
	if _, err := fmt.Fscan(control, &ready); err != nil || ready != "ready" {
		t.Fatalf("raw-mode barrier: %q, %v", ready, err)
	}
	// The child waits on the control connection, so none of this prefix can be
	// consumed before the paste starts. Raw mode prevents truncation and echo.
	prefix := 0
	fill := bytes.Repeat([]byte{'p'}, len(paste))
	for {
		if err := ctx.Err(); err != nil {
			t.Fatal("fixture input-fill barrier timed out")
		}
		n, err := unix.Write(r.(*execPTY).masterFD, fill)
		prefix += max(n, 0)
		if prefix > outputBytes {
			t.Fatal("fixture failed to fill bounded PTY input")
		}
		if err == unix.EAGAIN && prefix > 0 {
			break
		}
		if err != nil && err != unix.EINTR {
			t.Fatalf("fill PTY input: %v", err)
		}
	}
	barrier := &ptyWriteBarrier{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(barrier.release) })
	defer release()
	in := nativecontract.PTYInput{RequestID: uuid.NewString(), ExpectedBinding: spec.Binding, ControllerEpoch: uuid.NewString(), Sequence: "1", DataBase64: base64.StdEncoding.EncodeToString(paste), ExpiresAt: deadline}
	type writeResult struct {
		evidence nativecontract.NativeEvidence
		err      error
	}
	written := make(chan writeResult, 1)
	go func() {
		evidence, err := r.Write(barrier, in)
		written <- writeResult{evidence, err}
	}()
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal("paste never reached authorization barrier")
	}
	if _, err := fmt.Fprintln(control, prefix); err != nil {
		t.Fatal(err)
	}
	release()
	output := make([]byte, 0, outputBytes+64)
	acknowledged := false
	for !acknowledged || !bytes.Contains(output, []byte("\nreceived 8192\n")) {
		select {
		case result := <-written:
			if result.err != nil || result.evidence.Stage != "write_acknowledged" || result.evidence.RequestID != in.RequestID {
				t.Fatalf("8 KiB paste was not acknowledged: %+v, %v", result.evidence, result.err)
			}
			acknowledged = true
		case frame, ok := <-r.Frames():
			if !ok {
				t.Fatal("PTY ended before confirming the complete paste")
			}
			data, err := base64.StdEncoding.Strict().DecodeString(frame.DataBase64)
			if err != nil || frame.TerminalEpoch != spec.Binding.RuntimeEpoch {
				t.Fatalf("invalid fixture frame: %v", err)
			}
			output = append(output, data...)
			if len(output) > outputBytes+64 {
				t.Fatal("fixture output exceeded bound")
			}
		case <-ctx.Done():
			t.Fatal("PTY output and 8 KiB paste deadlocked")
		}
	}
	if !bytes.Equal(output, append(bytes.Repeat([]byte{'o'}, outputBytes), []byte("\nreceived 8192\n")...)) {
		t.Fatal("PTY lost or changed fixture output")
	}
	// Retain the child until every asserted byte has been observed; owned exit
	// is a separate barrier and cannot discard the fixture's final witness.
	if _, err := fmt.Fprintln(control, "exit"); err != nil {
		t.Fatal(err)
	}
	if exit, err := r.Wait(ctx); err != nil || !exit.RootExited || !exit.GroupAbsent {
		t.Fatalf("owned fixture exit not confirmed: %+v, %v", exit, err)
	}
}

// Pause the first write check until the parent releases the child's output.
// This establishes overlap without sleeps or timing-based success assertions.
type ptyWriteBarrier struct {
	context.Context
	entered, release chan struct{}
	once             sync.Once
}

func (c *ptyWriteBarrier) Err() error {
	c.once.Do(func() {
		close(c.entered)
		select {
		case <-c.release:
		case <-c.Context.Done():
		}
	})
	return c.Context.Err()
}
