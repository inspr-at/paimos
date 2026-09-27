// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/localjournal"
)

func TestColdRevokedDaemonReconcilesWithoutRuntimeAuthentication(t *testing.T) {
	for _, prior := range []string{"empty", "unresolved", "observed_exit"} {
		t.Run(prior, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
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
			private := make([]string, 3)
			for i := range private {
				var b [32]byte
				if _, err = rand.Read(b[:]); err != nil {
					t.Fatal(err)
				}
				private[i] = hex.EncodeToString(b[:])
			}
			view := agentsetup.View{RequestID: request, TenantID: tenant, State: "revoked", ComputerID: computer, PrincipalID: principal, DaemonID: "fixture", ComputerName: "fixture", Workspace: root, Platform: "darwin", Arch: "arm64", ComputerState: "revoked", Revision: 2}
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/agent-pairing/reconcile" || r.Header.Get("Authorization") != "" {
					t.Error("cold revoke attempted execution-key authentication")
					w.WriteHeader(403)
					return
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["lifecycle_secret"] != private[2] {
					t.Error("wrong lifecycle proof")
					w.WriteHeader(403)
					return
				}
				requests++
				_ = json.NewEncoder(w).Encode(view)
			}))
			defer server.Close()
			store, err := agentsetup.OpenStore(root, false)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]any{"request_id": request, "computer_name": "fixture", "workspace_path": root, "platform": "darwin", "arch": "arm64"}, "response": map[string]string{"tenant_id": tenant, "request_id": request}, "view": view, "device_secret": private[0], "runtime_secret": private[1], "lifecycle_secret": private[2], "lifecycle_request_id": request, "phase": "connected"}
			raw, _ := json.Marshal(snapshot)
			if err = store.Write("pairing.json", raw, true); err != nil {
				t.Fatal(err)
			}
			runtime := agentsetup.RuntimeConfig{Schema: "aeon.agent-runtime.v1", Origin: server.URL, TenantID: tenant, PrincipalID: principal, DaemonID: "fixture", ComputerID: computer, Workspace: root}
			raw, _ = json.Marshal(runtime)
			if err = store.Write(agentsetup.RuntimeName, raw, true); err != nil {
				t.Fatal(err)
			}
			// Deliberately no runtime.key: revoked execution authority is unavailable.
			if prior != "empty" {
				state := filepath.Join(root, "daemon")
				s, err := agentsetup.OpenStore(state, true)
				if err != nil {
					t.Fatal(err)
				}
				s.Close()
				j, err := localjournal.Open(localjournal.Config[agentd.Record]{Directory: state, Prefix: "aeon-agentd-fixture", Version: 2, MaxBytes: 4 << 20, MaxRecords: 4096, Key: func(r agentd.Record) (string, error) { return r.RunID, nil }, Validate: func(agentd.Record) error { return nil }})
				if err != nil {
					t.Fatal(err)
				}
				if err = j.Put(agentd.Record{TenantID: tenant, PrincipalID: principal, RunID: "prior", AccountID: "account", Generation: "old", State: "running", ExitObserved: prior == "observed_exit"}); err != nil {
					t.Fatal(err)
				}
			}
			permitted, err := pairedPreflight(t.Context(), root, server.URL, agentsetup.HTTPClient{Origin: server.URL, HTTP: server.Client()})
			if err != nil || permitted || requests != 1 {
				t.Fatal("cold revoked startup did not fence before runtime auth")
			}
			status, err := (localPairing{root: root}).Status(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			expected := "drained"
			if prior == "unresolved" {
				expected = "unconfirmed"
			}
			if status.State != expected {
				t.Fatal("cold restart invented process exit")
			}
		})
	}
}
