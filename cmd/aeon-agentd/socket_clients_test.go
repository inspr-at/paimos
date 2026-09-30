// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestPairedCommandsReachRecordedFallbackSocket(t *testing.T) {
	home, err := filepath.EvalSymlinks(pairingShortTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	root := filepath.Join(home, strings.Repeat("state", 25))
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
	if err := agentsetup.PrepareSocketDirectory(socket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	const token = "01234567890123456789012345678901" // local test fixture only
	if err := os.WriteFile(socket+".token", []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	generation := strings.Repeat("a", 32)
	raw, _ := json.Marshal(agentsetup.ControlReference{Socket: socket, DaemonID: "fixture", Generation: generation})
	if err := store.Write("control.json", raw, true); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/lifecycle":
			_ = json.NewEncoder(w).Encode(agentd.LifecycleStatus{DaemonID: "fixture", CapacityAccounts: []agentd.CapacityAccountStatus{}})
		case "/v1/attach":
			_ = json.NewEncoder(w).Encode(agentd.AttachLocalView{})
		case "/v1/runs/run/control":
			_ = json.NewEncoder(w).Encode(agentd.Receipt{RunID: "run", Generation: generation, CorrelationID: "request"})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	// Pair/setup/status use localPairing; attach uses the same OpenClient.
	local := localPairing{root: root}
	status, err := local.Status(context.Background(), "")
	if err != nil || status.DaemonID != "fixture" {
		t.Fatalf("status did not reach fallback: %v", err)
	}
	client, err := local.client()
	if err != nil || client.Socket != socket {
		t.Fatalf("local client did not discover fallback: %v", err)
	}
	if _, err := client.Attach(context.Background(), agentd.AttachLocalRequest{}); err != nil {
		t.Fatalf("attach did not reach fallback: %v", err)
	}
	var out bytes.Buffer
	if err := capacityCommand([]string{"--setup-root", root}, &out); err != nil {
		t.Fatalf("capacity did not reach fallback: %v", err)
	}
	if err := control([]string{"--setup-root", root, "--run-id", "run", "--generation", generation, "--correlation-id", "request"}, &out); err != nil {
		t.Fatalf("control did not reach fallback: %v", err)
	}
	if err := control([]string{"--setup-root", root, "--socket", socket}, &out); err == nil {
		t.Fatal("ambiguous control path accepted")
	}
}
