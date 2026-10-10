// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/hooknote"
)

func TestPairedServeFromRootWorkingDirectory(t *testing.T) {
	if os.Getenv("AEON_ROOT_CWD_SERVE_HELPER") == "1" {
		cwd, err := os.Getwd()
		if err != nil || cwd != "/" {
			t.Fatal("serve helper did not start at the filesystem root", err)
		}
		der, err := base64.StdEncoding.DecodeString(os.Getenv("AEON_ROOT_CWD_CERT"))
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		pool.AddCert(cert)
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		http.DefaultTransport = transport
		defer transport.CloseIdleConnections()
		// Keep an accepted connection from this root-cwd process live so the
		// parent can check the incoming hook-peer boundary after serve is ready.
		peer, err := net.Dial("unix", os.Getenv("AEON_ROOT_CWD_PEER_SOCKET"))
		if err != nil {
			t.Fatal(err)
		}
		defer peer.Close()
		if err := run([]string{"serve", "--setup-root", os.Getenv("AEON_ROOT_CWD_SETUP"), "--capacity-interval", "1h"}, os.Stdout); err != nil {
			t.Fatal(err)
		}
		return
	}

	// R9/R10: login services must reach paired readiness from cwd "/", while
	// an incoming hook process at "/" must retain its existing rejection.
	root, err := os.MkdirTemp("", "a858-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	const tenant = "11111111-1111-4111-8111-111111111111"
	const computer = "22222222-2222-4222-8222-222222222222"
	const principal = "33333333-3333-4333-8333-333333333333"
	const request = "44444444-4444-4444-8444-444444444444"
	const account = "55555555-5555-4555-8555-555555555555"
	view := agentsetup.View{RequestID: request, TenantID: tenant, ComputerID: computer, PrincipalID: principal, DaemonID: "root-cwd-fixture", ComputerName: "fixture", Workspace: root, ComputerState: "connected", Revision: 1}
	ready := make(chan struct{})
	var readiness sync.Once
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent-pairing/reconcile", "/api/agent-pairing/self":
			_ = json.NewEncoder(w).Encode(view)
		case "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant": map[string]string{"id": tenant}, "principal": map[string]string{"id": principal, "tenant_id": tenant, "kind": "agent"}})
		case "/api/agent-pairing/attach":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "fixture attach disabled"})
		case "/api/runs/queued/notifications", "/api/agent-accounts/probes":
			// This previous-server fixture denies optional routes to agent keys.
			w.WriteHeader(http.StatusForbidden)
		case "/api/runs/queued":
			_ = json.NewEncoder(w).Encode([]any{})
			// The production loop polls only after the socket, control reference
			// and hook pin have all been published. No elapsed-time assertion.
			readiness.Do(func() { close(ready) })
		case "/api/agent-accounts", "/api/harness-recoveries/claim":
			_ = json.NewEncoder(w).Encode([]any{})
		case "/api/agent-accounts/" + account + "/probe":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected root-cwd serve route: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := agentsetup.RuntimeConfig{Schema: "aeon.agent-runtime.v1", Origin: server.URL, TenantID: tenant, PrincipalID: principal, ComputerID: computer, DaemonID: view.DaemonID, Workspace: root,
		Accounts: []agentsetup.RuntimeAccount{{Harness: "claude", Key: "fixture", AccountID: account, Home: root, Path: filepath.Join(root, "unavailable-claude")}}}
	// Only synthetic pairing state is used. The unavailable harness is blocked;
	// this test never launches a model or reads the operator's paired state.
	proof := strings.Repeat("ab", 32)
	snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]string{"request_id": request, "computer_name": "fixture", "workspace_path": root}, "response": map[string]string{"tenant_id": tenant}, "view": view, "device_secret": proof, "runtime_secret": proof, "lifecycle_secret": proof}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"pairing.json": snapshot, agentsetup.RuntimeName: c} {
		raw, err := json.Marshal(value)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		if err := store.Write(name, raw, true); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Write("runtime.key", []byte("aeon_fixture_"+proof), true); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	peerSocket := filepath.Join(root, "peer.sock")
	listener, err := net.Listen("unix", peerSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPairedServeFromRootWorkingDirectory$")
	cmd.Dir = "/"
	cmd.Env = []string{"HOME=" + root, "PATH=" + os.Getenv("PATH"), "GOMAXPROCS=2", "AEON_ROOT_CWD_SERVE_HELPER=1", "AEON_ROOT_CWD_SETUP=" + root,
		"AEON_ROOT_CWD_PEER_SOCKET=" + peerSocket, "AEON_ROOT_CWD_CERT=" + base64.StdEncoding.EncodeToString(server.Certificate().Raw)}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var serveErr error
	go func() { serveErr = cmd.Wait(); close(exited) }()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			cancel()
			<-exited
			t.Error("root-cwd serve did not shut down")
		}
		if serveErr != nil {
			t.Errorf("root-cwd serve failed: %v\n%s", serveErr, output.String())
		}
	}()
	select {
	case <-ready:
	case <-exited:
		t.Fatalf("serve exited before paired readiness: %v\n%s", serveErr, output.String())
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not reach paired readiness")
	}
	local, err := agentdwire.OpenClient(filepath.Join(root, "daemon"))
	if err != nil {
		t.Fatal(err)
	}
	op, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	status, err := local.Lifecycle(op, "")
	if err != nil || status.DaemonID != c.DaemonID {
		t.Fatal("paired local control did not reach readiness", err)
	}
	state, err := agentsetup.OpenStore(filepath.Join(root, "daemon"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	raw, err := state.Read("hook-peer.json", 4096)
	if err != nil {
		t.Fatal(err)
	}
	var pin hooknote.DaemonPin
	if json.Unmarshal(raw, &pin) != nil || !pin.Valid() || pin.PID != cmd.Process.Pid || pin.UID != os.Getuid() {
		t.Fatal("root-cwd serve did not publish its usable kernel identity")
	}
	daemon, err := net.DialTimeout("unix", local.Socket, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()
	observed, err := hooknote.SnapshotDaemon(daemon)
	if err != nil || observed.CWD != "/" || !hooknote.MatchesPin(observed, pin) || hooknote.RecheckDaemon(daemon, observed) != nil {
		t.Fatal("published pin did not match the connected root-cwd daemon", err)
	}
	// This same process also connected to our separate listener as an incoming
	// hook. That direction must retain the project-cwd rule.
	if _, err := hooknote.Observe(cmd.Process.Pid); !errors.Is(err, hooknote.ErrPeer) {
		t.Fatalf("incoming root-cwd process observation: got %v, want ErrPeer", err)
	}
	_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err := hooknote.Snapshot(peer); !errors.Is(err, hooknote.ErrPeer) {
		t.Fatalf("incoming root-cwd socket peer: got %v, want ErrPeer", err)
	}
}
