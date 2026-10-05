// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/hooknote"
	"golang.org/x/sys/unix"
)

func TestPairedHookCanaryIsNeverOpenedOrForwarded(t *testing.T) {
	hooknote.EnableForTest(t.Cleanup)
	isolate(t)
	const canary = "CANARY-CREDENTIAL-394"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AEON_API_KEY", canary)
	configFIFO := filepath.Join(home, "config.yaml")
	keyFIFO := filepath.Join(home, "key")
	openedConfig := watchFIFO(t, configFIFO)
	openedKey := watchFIFO(t, keyFIFO)
	openedHome := watchFIFO(t, filepath.Join(home, ".aeon", "config.yaml"))
	t.Setenv("AEON_API_KEY_FILE", keyFIFO)
	dir, err := os.MkdirTemp("/tmp", "aeon-canary-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agentd.sock")
	openedToken := watchFIFO(t, socket+".token")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var got atomic.Value
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		got.Store(append([]byte(nil), buf[:n]...))
	}()
	pin := cliSelfPin(t)
	raw, _ := json.Marshal(pin)
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer api.Close()
	t.Setenv("AEON_URL", api.URL)
	var out, errOut bytes.Buffer
	code := RunMessaging([]string{"aeon", "--config", configFIFO, "hook", "claude", "--paired", "--socket", socket, "--daemon-peer", string(raw), "PostToolUse"}, strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"vendor-ref"}`), &out, &errOut)
	time.Sleep(30 * time.Millisecond)
	wire, _ := got.Load().([]byte)
	combined := out.String() + errOut.String() + string(wire)
	if code != 0 || hits.Load() != 0 || openedConfig.Load() || openedKey.Load() || openedHome.Load() || openedToken.Load() || strings.Contains(combined, canary) {
		t.Fatal("canary credential was opened or forwarded")
	}
}

func TestPairedHookDerivesEndpointFromStateRoot(t *testing.T) {
	hooknote.EnableForTest(t.Cleanup)
	isolate(t)
	decoy := t.TempDir()
	t.Setenv("HOME", decoy)
	t.Setenv("XDG_STATE_HOME", filepath.Join(decoy, "xdg"))
	openedDecoy := watchFIFO(t, filepath.Join(decoy, "paired-decoy"))
	root, err := os.MkdirTemp("/tmp", "aeon-paired-")
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
	if err = store.Write("control.json", ref, true); err != nil {
		t.Fatal(err)
	}
	pin, err := json.Marshal(cliSelfPin(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Write("hook-peer.json", pin, true); err != nil {
		t.Fatal(err)
	}
	openedToken := watchFIFO(t, socket+".token")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var got atomic.Value
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		got.Store(append([]byte(nil), buf[:n]...))
	}()
	var out, errOut bytes.Buffer
	code := RunMessaging([]string{"aeon", "--config", filepath.Join(decoy, "must-not-read"), "hook", "claude", "--paired", "--setup-root", root, "PostToolUse"}, strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"vendor-ref"}`), &out, &errOut)
	time.Sleep(30 * time.Millisecond)
	wire, _ := got.Load().([]byte)
	if code != 0 || openedDecoy.Load() || openedToken.Load() || !bytes.Contains(wire, []byte("POST /v1/inbox-hook")) {
		t.Fatalf("paired endpoint was not derived code=%d decoy=%v token=%v wire=%q err=%s", code, openedDecoy.Load(), openedToken.Load(), wire, errOut.String())
	}
}

func watchFIFO(t *testing.T, path string) *atomic.Bool {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	opened := &atomic.Bool{}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0)
			if err == nil {
				unix.Close(fd)
				opened.Store(true)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return opened
}

func cliSelfPin(t *testing.T) hooknote.DaemonPin {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "aeon-pin-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "p")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	peer, err := hooknote.Snapshot(server)
	if err != nil {
		t.Fatal(err)
	}
	return hooknote.PinFrom(peer)
}
