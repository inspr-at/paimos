// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package agentd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativepty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type nativePinnedPTYProfile struct {
	nativeHostProfiles
	pin nativecontract.BinaryPin
}

func (p nativePinnedPTYProfile) Resolve(ctx context.Context, request nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	profile, err := p.nativeHostProfiles.Resolve(ctx, request)
	profile.Binary = p.pin
	return profile, err
}

type nativePTYCommandFunc func(context.Context, nativecontract.PTYLaunchSpec) (nativepty.CommandPlan, error)

func (f nativePTYCommandFunc) Prepare(ctx context.Context, spec nativecontract.PTYLaunchSpec) (nativepty.CommandPlan, error) {
	return f(ctx, spec)
}

// Risk: the host fixtures pass while the real pinned PTY cannot drain output,
// accept holder I/O or clean up its owned process. This executable is the only
// child, for both eligible provider identities; no vendor/model CLI is run.
func TestNativeHostPinnedPTYStartsReadsWritesAndExits(t *testing.T) {
	if os.Getenv("AEON_HOST_PTY_FIXTURE") == "1" {
		if _, err := term.MakeRaw(0); err != nil {
			os.Exit(61)
		}
		fmt.Println("ready observed-host-conversation")
		scan := bufio.NewScanner(os.Stdin)
		for scan.Scan() {
			switch scan.Text() {
			case "measure":
				size, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
				if err != nil {
					os.Exit(62)
				}
				fmt.Printf("size %d %d\n", size.Col, size.Row)
			case "exit":
				os.Exit(0)
			}
		}
		os.Exit(63)
	}
	for _, provider := range []string{Claude, Codex} {
		t.Run(provider, func(t *testing.T) {
			f, _, a := nativePTYHost(t)
			enableNativePTYRelay(t, f)
			f.s.accounts[0].Harness = provider
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
			_ = binary.Close()
			if err != nil {
				t.Fatal(err)
			}
			f.h.profiles = nativePinnedPTYProfile{nativeHostProfiles{f.root}, nativecontract.BinaryPin{Path: path, SHA256: hex.EncodeToString(hash.Sum(nil)), Version: "fixture", Protocol: "fixture-pty/1"}}
			f.h.config.Terminal = &nativepty.ExecLauncher{Builder: nativePTYCommandFunc(func(_ context.Context, spec nativecontract.PTYLaunchSpec) (nativepty.CommandPlan, error) {
				var startup strings.Builder
				return nativepty.CommandPlan{Args: []string{"-test.run=^TestNativeHostPinnedPTYStartsReadsWritesAndExits$"}, Environment: []string{"AEON_HOST_PTY_FIXTURE=1", "HOME=" + f.root, "TERM=xterm-256color"}, Provider: provider, ConversationRef: spec.ConversationRef, Require: func(context.Context, nativecontract.PTYLaunchSpec) error { return nil }, Ready: func(frame nativecontract.TerminalFrame) (string, error) {
					data, err := base64.StdEncoding.Strict().DecodeString(frame.DataBase64)
					if err != nil || startup.Len()+len(data) > 256 {
						return "", nativeRefusal(nativecontract.ReasonBufferFull)
					}
					startup.Write(data)
					if strings.Contains(startup.String(), "ready observed-host-conversation") {
						return "observed-host-conversation", nil
					}
					return "", nil
				}}, nil
			})}
			local := f.local(0)
			local.Intent.Harness = provider
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				f.h.mu.Lock()
				e := f.h.entries[local.Intent.RequestID]
				f.h.mu.Unlock()
				if e != nil && e.cancel != nil {
					e.cancel()
					if e.terminal != nil {
						_, _ = e.terminal.Wait(ctx)
					}
				}
			})
			out, err := f.h.Start(t.Context(), local)
			if err != nil || out.Session == nil || out.Session.State != "taken" {
				t.Fatal("pinned PTY start", out, err)
			}
			b := *out.Session.CurrentBinding
			a.binding = b
			f.h.mu.Lock()
			e := f.h.entries[local.Intent.RequestID]
			f.h.mu.Unlock()
			if e.ownership == nil || e.ownership.RootPID != e.transport.Identity().PID || f.launcher.opens != 0 {
				t.Fatal("PTY did not retain its exact owner")
			}
			sub := subscribeNativePTY(t, f, b)
			readUntil := func(want string) {
				t.Helper()
				var output strings.Builder
				for !strings.Contains(output.String(), want) {
					event := readNativePTYEvent(t, sub)
					if event.Kind == "snapshot" {
						continue
					}
					var frame nativecontract.TerminalFrame
					if event.Kind != "terminal" || json.Unmarshal(event.Payload, &frame) != nil {
						t.Fatal("terminal witness", event.Kind)
					}
					data, err := base64.StdEncoding.Strict().DecodeString(frame.DataBase64)
					if err != nil || output.Len()+len(data) > 65536 {
						t.Fatal("bounded output", err)
					}
					output.Write(data)
				}
			}
			readUntil("ready observed-host-conversation")
			ctx := nativePTYContext(a)
			size := nativePTYResize(a)
			size.Cols, size.Rows = 132, 40
			if _, err := f.h.Resize(ctx, size); err != nil {
				t.Fatal("holder resize", err)
			}
			in := nativePTYInput(a)
			in.DataBase64 = base64.StdEncoding.EncodeToString([]byte("measure\n"))
			if _, err := f.h.PTYInput(ctx, in); err != nil {
				t.Fatal("holder input", err)
			}
			readUntil("size 132 40")
			in.RequestID, in.Sequence, in.DataBase64 = uuid.NewString(), "2", base64.StdEncoding.EncodeToString([]byte("exit\n"))
			if _, err := f.h.PTYInput(ctx, in); err != nil {
				t.Fatal("holder exit input", err)
			}
			select {
			case <-e.released:
			case <-time.After(10 * time.Second):
				t.Fatal("owned PTY exit barrier")
			}
			if f.groups(t) != 0 {
				t.Fatal("exited PTY retained occupancy")
			}
		})
	}
}
