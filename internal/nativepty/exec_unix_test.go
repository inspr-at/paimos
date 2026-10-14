// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package nativepty

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"golang.org/x/sys/unix"
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
	defer cancel()
	spec.Lifetime = lifetime
	launcher := &ExecLauncher{Builder: fixtureBuilder{plan: CommandPlan{Args: []string{"-test.run=^TestPTYUnixOwnershipResizeAndExit$"}, Environment: []string{"AEON_NATIVE_PTY_FIXTURE=1", "HOME=" + dir, "TERM=xterm-256color"}, Provider: "fixture", Require: func(context.Context, nativecontract.PTYLaunchSpec) error { return nil }}}}
	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	r, err := launcher.OpenPTY(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err = r.Resize(ctx, resize); err != nil {
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
