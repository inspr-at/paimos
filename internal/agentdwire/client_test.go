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
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
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
