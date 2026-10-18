// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

// Risk: cancelling servePairedContext while a retained process has not exited
// must keep draining. Supervisor.Close returns ErrProcessesUnconfirmed until
// that exit is recorded. Returning nil on the cancel is the regression: the
// turn is still busy, and a restarted daemon cannot re-adopt it.
func TestPairedServeKeepsDrainingBusyRun(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "aeon-drain-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	const tenant = "11111111-1111-4111-8111-111111111111"
	const computer = "22222222-2222-4222-8222-222222222222"
	const principal = "33333333-3333-4333-8333-333333333333"
	const request = "44444444-4444-4444-8444-444444444444"
	const account = "55555555-5555-4555-8555-555555555555"
	const demand = "66666666-6666-4666-8666-666666666666"
	view := agentsetup.View{RequestID: request, TenantID: tenant, ComputerID: computer, PrincipalID: principal, DaemonID: "drain-fixture", ComputerName: "fixture", Workspace: root, ComputerState: "connected", Revision: 1}
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
			w.WriteHeader(http.StatusForbidden)
		case "/api/models/harness-probes/claim":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "next_cursor": ""})
		case "/api/runs/queued":
			_ = json.NewEncoder(w).Encode([]any{})
			readiness.Do(func() { close(ready) })
		case "/api/agent-accounts", "/api/harness-recoveries/claim":
			_ = json.NewEncoder(w).Encode([]any{})
		case "/api/agent-accounts/" + account + "/probe":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected drain fixture route: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	priorTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = priorTransport })

	proof := strings.Repeat("ab", 32)
	c := agentsetup.RuntimeConfig{Schema: "aeon.agent-runtime.v1", Origin: server.URL, TenantID: tenant, PrincipalID: principal, ComputerID: computer, DaemonID: view.DaemonID, Workspace: root,
		Accounts: []agentsetup.RuntimeAccount{{Harness: "claude", Key: "fixture", AccountID: account, Home: root, Path: filepath.Join(root, "unavailable-claude")}}}
	snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]string{"request_id": request, "computer_name": "fixture", "workspace_path": root}, "response": map[string]string{"tenant_id": tenant}, "view": view, "device_secret": proof, "runtime_secret": proof, "lifecycle_secret": proof}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"pairing.json": snapshot, agentsetup.RuntimeName: c} {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			store.Close()
			t.Fatal(marshalErr)
		}
		if err = store.Write(name, raw, true); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	if err = store.Write("runtime.key", []byte("aeon_fixture_"+proof), true); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		errCh <- servePairedContext(ctx, root, time.Hour)
		close(exited)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(8 * time.Second):
			t.Error("paired serve did not leave the drain loop")
		}
		drainRetryObserver = nil
	})
	select {
	case <-ready:
	case err := <-errCh:
		t.Fatalf("paired serve exited before the socket was published: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("paired serve did not publish its socket")
	}

	state, err := agentsetup.OpenStore(filepath.Join(root, "daemon"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	raw, err := state.Read("control.json", 4096)
	if err != nil {
		t.Fatal(err)
	}
	var ref agentsetup.ControlReference
	if json.Unmarshal(raw, &ref) != nil || ref.Socket == "" {
		t.Fatal("paired serve did not publish its control socket")
	}
	// Close refuses while this receipt says the process has not exited.
	// Rewriting it later is the observed exit the drain loop is waiting for.
	if err = writeRetainedLead(state, demand, false); err != nil {
		t.Fatal(err)
	}
	retried := make(chan struct{})
	var once sync.Once
	drainRetryObserver = func() { once.Do(func() { close(retried) }) }
	cancel()
	select {
	case err := <-errCh:
		t.Fatalf("serve returned while the run was still busy: %v", err)
	case <-retried:
	case <-time.After(8 * time.Second):
		t.Fatal("drain did not retry and did not return")
	}
	select {
	case err := <-errCh:
		t.Fatalf("serve returned after one retry while the run was still busy: %v", err)
	default:
	}
	for _, path := range []string{ref.Socket, ref.Socket + ".token", filepath.Join(root, "daemon", "hook-peer.json")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("cleanup left %s during drain: %v", path, statErr)
		}
	}
	if err = writeRetainedLead(state, demand, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("drain failed after the retained process exited: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("drain did not finish after the retained process exited")
	}
}

func writeRetainedLead(state *agentsetup.Store, demand string, exited bool) error {
	body := map[string]any{
		"version":       1,
		"attempted":     true,
		"exit_observed": exited,
		"exit_reported": exited,
		"pickup":        map[string]any{"demand": map[string]string{"id": demand}, "daemon_id": "drain-fixture"},
	}
	if exited {
		body["ownership"] = map[string]any{
			"process_id": "retained-owner",
			"root_pid":   99,
			"group_id":   99,
			"started_at": "2026-01-01T00:00:00Z",
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return state.Write("lead-runtime-"+demand+".json", raw, false)
}
