// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentdwire

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestAuthenticatedLocalControl(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "aeon-wire-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "agentd.sock")
	tokenFile := socket + ".token"
	if err := os.WriteFile(tokenFile, []byte("12345678901234567890123456789012"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer 12345678901234567890123456789012" {
			w.WriteHeader(401)
			return
		}
		var req agentd.ControlRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.RunID != "run" {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentd.Receipt{RunID: req.RunID, Generation: req.Generation, CorrelationID: req.CorrelationID, Operation: req.Operation, AppliedAt: time.Now()})
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	c := Client{Socket: socket, TokenFile: tokenFile}
	receipt, err := c.Control(context.Background(), agentd.ControlRequest{RunID: "run", Generation: "generation", CorrelationID: "correlation", Operation: "interrupt"})
	if err != nil || receipt.CorrelationID != "correlation" {
		t.Fatalf("local receipt: %#v %v", receipt, err)
	}
	if err := os.Chmod(tokenFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Control(context.Background(), agentd.ControlRequest{RunID: "run"}); err == nil {
		t.Fatal("world-readable token accepted")
	}
}

func TestOpenClientUsesSharedSocketResolver(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "aeon-ref-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, kind := range []string{"short", "fallback", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			state := filepath.Join(home, kind, "daemon")
			if kind == "fallback" {
				state = filepath.Join(home, strings.Repeat("state", 25), "daemon")
			}
			store, err := agentsetup.OpenStore(state, true)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			path, err := agentsetup.ResolveSocketPath(state, nil)
			if err != nil {
				t.Fatal(err)
			}
			ref := agentsetup.ControlReference{DaemonID: "fixture", Generation: strings.Repeat("a", 32)}
			if kind == "legacy" {
				ref.Socket = "agentd-" + ref.Generation + ".sock"
				path = filepath.Join(state, ref.Socket)
			} else {
				ref.Socket = path
			}
			if err := agentsetup.PrepareSocketDirectory(path); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(ref)
			if err := store.Write("control.json", raw, true); err != nil {
				t.Fatal(err)
			}
			client, err := OpenClient(state)
			if err != nil || client.Socket != path || client.TokenFile != path+".token" {
				t.Fatalf("client path differs from server: %+v %v", client, err)
			}
			// A substituted socket symlink is never followed, even within the
			// same owner's directory and with an otherwise valid reference.
			listener.Close()
			if err := os.Symlink(filepath.Join(home, "foreign.sock"), path); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenClient(state); err == nil {
				t.Fatal("socket symlink accepted")
			}
		})
	}
}
