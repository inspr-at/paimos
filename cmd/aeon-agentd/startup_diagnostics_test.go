// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsecurity"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestServeStartupLogsExactMissingFile(t *testing.T) {
	if agentsecurity.DefaultVault() != nil {
		t.Skip("ordinary-build fixture; native denial has its own Keychain-free tests")
	}
	for _, missing := range []string{agentsetup.RuntimeName, "pairing.json", "runtime.key"} {
		t.Run(missing, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			view := agentsetup.View{
				TenantID: "11111111-1111-4111-8111-111111111111", ComputerID: "22222222-2222-4222-8222-222222222222",
				PrincipalID: "33333333-3333-4333-8333-333333333333", RequestID: "44444444-4444-4444-8444-444444444444",
				DaemonID: "startup-fixture", ComputerState: "connected", Workspace: root, Revision: 1,
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/api/agent-pairing/reconcile" {
					t.Error("unexpected startup network request")
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(view)
			}))
			defer server.Close()
			transport := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = transport }()
			store, err := agentsetup.OpenStore(root, false)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			config := agentsetup.RuntimeConfig{Schema: "aeon.agent-runtime.v1", Origin: server.URL, TenantID: view.TenantID,
				PrincipalID: view.PrincipalID, ComputerID: view.ComputerID, DaemonID: view.DaemonID, Workspace: root}
			if missing != agentsetup.RuntimeName {
				raw, _ := json.Marshal(config)
				if err := store.Write(agentsetup.RuntimeName, raw, true); err != nil {
					t.Fatal(err)
				}
			}
			if missing == "runtime.key" {
				snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL,
					"request":  agentsetup.DeviceRequest{RequestID: view.RequestID, Details: agentsetup.Details{Workspace: root}},
					"response": agentsetup.DeviceResponse{TenantID: view.TenantID}, "view": view,
					"device_secret": strings.Repeat("a", 64), "runtime_secret": strings.Repeat("b", 64), "lifecycle_secret": strings.Repeat("c", 64)}
				raw, _ := json.Marshal(snapshot)
				if err := store.Write("pairing.json", raw, true); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(logger)
			err = run([]string{"serve", "--setup-root", root}, io.Discard)
			path := filepath.Join(root, missing)
			if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), path) {
				t.Fatalf("wrong startup failure for %s: %v", missing, err)
			}
			var entry struct{ Level, Msg, Error string }
			if json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry) != nil || entry.Level != "ERROR" || entry.Msg != "agentd serve failed" || !strings.Contains(entry.Error, path) {
				t.Fatal("startup did not emit one structured error naming the failing path")
			}
			for _, private := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)} {
				if strings.Contains(logs.String(), private) || strings.Contains(err.Error(), private) {
					t.Fatal("startup diagnostic exposed fixture capability")
				}
			}
			wantRequests := int32(0)
			if missing == "runtime.key" {
				wantRequests = 1
			}
			if requests.Load() != wantRequests {
				t.Fatal("failure occurred at the wrong startup stage")
			}
		})
	}
}
