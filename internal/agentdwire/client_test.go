// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentdwire

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestOpenClientReportsFallbackHomeMismatch(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "aeon-home-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	serviceHome := filepath.Join(home, "service")
	if err := os.Mkdir(serviceHome, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, strings.Repeat("state", 25), "daemon")
	store, err := agentsetup.OpenStore(state, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Setenv("HOME", serviceHome)
	path, err := agentsetup.ResolveSocketPath(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := agentsetup.ControlReference{Socket: path, DaemonID: "fixture", Generation: strings.Repeat("a", 32)}
	raw, _ := json.Marshal(ref)
	if err := store.Write("control.json", raw, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	_, err = OpenClient(state)
	if err == nil {
		t.Fatal("mismatched fallback reference accepted")
	}
	for _, want := range []string{"local daemon reference unavailable", "resolved HOME \"" + home + "\"", "setup root \"~/" + strings.Repeat("state", 25) + "\"", "socket \"~/.aeon/run/", "recorded socket \"~/service/.aeon/run/", "service and shell use the same HOME and setup root"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing diagnostic %q: %v", want, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".aeon")); !os.IsNotExist(err) {
		t.Fatal("client created fallback state")
	}
}

// Maximum bodies must remain replayable until the foreground hook acknowledges.
func TestAttachedHookMaximumInboxDeliveryDoesNotBlockNext(t *testing.T) {
	for _, body := range []string{strings.Repeat("😀", 65536), strings.Repeat("\x01", 65536)} {
		t.Run(fmt.Sprintf("encoded-bytes-%d", len(body)), func(t *testing.T) {
			first := agentd.HarnessDelivery{ID: "11111111-1111-4111-8111-111111111111", MessageID: "22222222-2222-4222-8222-222222222222", SenderPrincipalID: "33333333-3333-4333-8333-333333333333", Cursor: 1, Body: body}
			next := first
			next.ID, next.MessageID, next.Cursor, next.Body = "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555", 2, "next delivery"
			queue := []agentd.HarnessDelivery{first, next}
			acks := 0
			c := attachedInboxTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				var in agentd.AttachedHookRequest
				if r.Method != "POST" || r.URL.Path != "/v1/attached-hook" || json.NewDecoder(r.Body).Decode(&in) != nil || in.SessionID != "bound-session" {
					t.Error("wrong attached inbox request")
					w.WriteHeader(400)
					return
				}
				switch in.Operation {
				case "pull":
					out := queue
					if len(out) > 1 {
						out = out[:1]
					}
					_ = json.NewEncoder(w).Encode(out)
				case "complete":
					if len(queue) == 0 || in.DeliveryID != queue[0].ID || in.Cursor != queue[0].Cursor {
						t.Error("acknowledged a different delivery")
						w.WriteHeader(409)
						return
					}
					acks++
					queue = queue[1:]
					_ = json.NewEncoder(w).Encode([]agentd.HarnessDelivery{})
				default:
					t.Error("unexpected operation")
					w.WriteHeader(400)
				}
			})
			for i := 0; i < 2; i++ {
				out, err := c.PullAttachedHook(t.Context(), "bound-session")
				if err != nil {
					t.Fatalf("maximum valid inbox delivery rejected: %v", err)
				}
				if len(out) != 1 || out[0] != first {
					t.Fatal("maximum inbox delivery changed")
				}
			}
			if acks != 0 {
				t.Fatal("pull acknowledged before foreground handoff")
			}
			if err := c.CompleteAttachedHook(t.Context(), "bound-session", first); err != nil {
				t.Fatal(err)
			}
			out, err := c.PullAttachedHook(t.Context(), "bound-session")
			if err != nil || len(out) != 1 || out[0] != next {
				t.Fatalf("subsequent inbox delivery blocked: %v", err)
			}
			if err := c.CompleteAttachedHook(t.Context(), "bound-session", next); err != nil {
				t.Fatal(err)
			}
			out, err = c.PullAttachedHook(t.Context(), "bound-session")
			if err != nil || len(out) != 0 || acks != 2 {
				t.Fatal("inbox queue did not settle exactly once per delivery")
			}
		})
	}
}

func TestAttachedHookInboxResponseBoundsAndStrictJSON(t *testing.T) {
	for _, response := range []string{
		`[{"body":"` + strings.Repeat("x", 512<<10) + `"}]`,
		`[]` + strings.Repeat(" ", 512<<10),
		`[{"unknown":"field"}]`,
		`[] []`,
		`[{"body":"truncated`,
	} {
		c := attachedInboxTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) })
		if _, err := c.PullAttachedHook(t.Context(), "bound-session"); err == nil || err.Error() != "invalid local lifecycle response" {
			t.Fatalf("invalid response accepted or failed for wrong reason: %v", err)
		}
	}
	// The larger bound belongs only to inbox pulls, never ordinary lifecycle reads.
	c := attachedInboxTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`+strings.Repeat(" ", 64<<10))
	})
	if _, err := c.Lifecycle(t.Context(), ""); err == nil || err.Error() != "invalid local lifecycle response" {
		t.Fatalf("lifecycle byte bound ignored: %v", err)
	}
}

func attachedInboxTestClient(t *testing.T, handler http.HandlerFunc) Client {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "aeon-inbox-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "daemon.sock")
	if err := os.WriteFile(socket+".token", []byte(strings.Repeat("f", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("f", 32) {
			w.WriteHeader(401)
			return
		}
		handler(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return Client{Socket: socket, TokenFile: socket + ".token"}
}
