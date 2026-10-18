// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/cli"
	"github.com/inspr-at/paimos/internal/hooknote"
)

func TestPublishHookPeerWritesAUsablePin(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "aeon-pinpub-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "daemon")
	store, err := agentsetup.OpenStore(state, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = publishHookPeer(store); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Read("hook-peer.json", 4096)
	if err != nil {
		t.Fatal(err)
	}
	var pin hooknote.DaemonPin
	if json.Unmarshal(raw, &pin) != nil || !pin.Valid() || pin.PID != os.Getpid() {
		t.Fatalf("production pin is not usable: %s", raw)
	}
	if runtime.GOOS == "darwin" && pin.PIDVersion == 0 {
		t.Fatal("production pin omitted the darwin pid version")
	}
}

type publishedNote struct {
	note  hooknote.Note
	nonce string
}

func (n publishedNote) Offer(context.Context, hooknote.Binding) (hooknote.Note, string, error) {
	return n.note, n.nonce, nil
}

func (publishedNote) Settle(context.Context, string, string, hooknote.Epoch) error { return nil }

func TestDerivedPairedEndpointRejectsUnqualifiedDaemon(t *testing.T) {
	hooknote.EnableForTest(t.Cleanup)
	t.Setenv("AEON_SESSION_ID", "")
	t.Setenv("AEON_API_KEY", "")
	t.Setenv("AEON_API_KEY_FILE", "")
	root, err := os.MkdirTemp("/tmp", "aeon-paired-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "daemon")
	store, err := agentsetup.OpenStore(state, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	socket, err := agentsetup.ResolveSocketPath(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = agentsetup.PrepareSocketDirectory(socket); err != nil {
		t.Fatal(err)
	}
	ref, err := json.Marshal(agentsetup.ControlReference{Socket: "agentd.sock", DaemonID: "daemon", Generation: strings.Repeat("ab", 16)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Write("control.json", ref, false); err != nil {
		t.Fatal(err)
	}
	if err = publishHookPeer(store); err != nil {
		t.Fatal(err)
	}
	self, err := hooknote.Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := hooknote.Observe(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	reg := hooknote.NewRegistry()
	reg.Add(hooknote.Grant{Binding: hooknote.Binding{
		SessionID: "00000000-0000-4000-8000-000000000394", MessageGeneration: "00000000-0000-4000-8000-000000000395", DaemonGeneration: "daemon-1",
		Harness: parent, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino,
	}})
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := hooknote.NewServer()
	srv.Set(publishedNote{
		note:  hooknote.Note{ID: "00000000-0000-4000-8000-000000000394", Owner: "Markus", Body: "inspect the paired gate", Origin: hooknote.OriginOwner, Created: "2026-09-30T02:00:00Z", Deadline: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		nonce: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}, reg)
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 5 * time.Second, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		return hooknote.Annotate(ctx, c)
	}}
	go func() { _ = httpSrv.Serve(ln) }()
	t.Cleanup(func() { _ = httpSrv.Close() })
	var out, errOut bytes.Buffer
	code := cli.RunMessaging([]string{"aeon", "--config", filepath.Join(root, "must-not-read"), "hook", "claude", "--paired", "--setup-root", root, "PostToolUse"}, strings.NewReader(`{"hook_event_name":"PostToolUse"}`), &out, &errOut)
	if code != 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "incomplete") {
		t.Fatalf("derived paired endpoint code=%d out=%q err=%s", code, out.String(), errOut.String())
	}
}

func (publishedNote) Validate(context.Context, string, hooknote.Binding) error { return nil }
