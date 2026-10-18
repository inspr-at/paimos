// SPDX-License-Identifier: AGPL-3.0-only
package agentdwire

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
)

// Risk: handover/leave can use an unauthenticated socket, wrong body or false
// success receipt. Both operations use the existing owner-only token boundary.
func TestSharedLedgerWireImportAndLeaveRequireOwnerReceipts(t *testing.T) {
	shortRoot, err := os.MkdirTemp("", "ldg-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
	root, err := filepath.EvalSymlinks(shortRoot)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "agentd.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef"
	tokenFile := socket + ".token"
	if err = os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	config := agentd.LedgerConfig{Path: filepath.Join(root, "ledger"), Root: root, Label: "cm.aeon.agentd.pma", Origin: "https://pma.example.invalid"}
	var imported, left atomic.Int32
	var confirmed atomic.Bool
	confirmed.Store(true)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.Method != "POST" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/ledger/import":
			var got agentd.LedgerConfig
			if json.NewDecoder(r.Body).Decode(&got) != nil || got != config {
				t.Error("handover changed local ownership")
			}
			imported.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]bool{"imported": confirmed.Load()})
		case "/v1/ledger/leave":
			left.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]bool{"left": confirmed.Load()})
		default:
			t.Error("unexpected local route")
			w.WriteHeader(404)
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	c := Client{Socket: socket, TokenFile: tokenFile}
	if err = c.ImportLedger(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if err = c.LeaveLedger(context.Background()); err != nil {
		t.Fatal(err)
	}
	confirmed.Store(false)
	if c.ImportLedger(context.Background(), config) == nil || c.LeaveLedger(context.Background()) == nil {
		t.Fatal("false receipt reported success")
	}
	if imported.Load() != 2 || left.Load() != 2 {
		t.Fatal("wrong local route inventory", imported.Load(), left.Load())
	}
}
