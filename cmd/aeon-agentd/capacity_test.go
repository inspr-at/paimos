// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
)

func TestCapacityCLI(t *testing.T) {
	// A short socket basename stays within the Unix socket path bound on macOS.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "s")
	if err = os.WriteFile(socket+".token", []byte(strings.Repeat("f", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	status := agentd.LifecycleStatus{CapacityAccounts: []agentd.CapacityAccountStatus{{AccountID: "account", Harness: agentd.Cursor, Fallback: "not available headless"}}}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("include_capacity") != "1" || r.Method != "GET" || r.URL.Path != "/v1/lifecycle" || r.URL.Query().Get("account_id") != "account" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("f", 32) {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	var out bytes.Buffer
	if err = run([]string{"capacity", "--socket", socket, "--account-id", "account"}, &out); err != nil {
		t.Fatal(err)
	}
	var got []agentd.CapacityAccountStatus
	if json.Unmarshal(out.Bytes(), &got) != nil || len(got) != 1 || got[0].Fallback != "not available headless" {
		t.Fatal("inventory missing")
	}
	// An older response cannot silently look like successful empty discovery.
	server.Close()
	listener, err = net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ready":true}`)) })}
	go func() { _ = legacy.Serve(listener) }()
	defer legacy.Close()
	out.Reset()
	if err = run([]string{"capacity", "--socket", socket}, &out); err == nil || out.Len() != 0 {
		t.Fatal("old daemon fabricated inventory")
	}
}
