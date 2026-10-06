// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/localjournal"
)

func TestPairedServeContinuesAfterAttachRegistrationRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		status        int
	}{
		{"old server", "invalid attach request", 400},
		{"protocol refusal", "update agentd to attach protocol 2", 409},
		{"proof refusal", "computer proof rejected", 403},
		{"consent version refusal", "upgrade paimos-agentd to local consent proof v2", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep the real generation socket below Darwin's path limit.
			root, err := os.MkdirTemp("/tmp", "aeon-skew-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			const tenantID = "11111111-1111-4111-8111-111111111111"
			const computer = "22222222-2222-4222-8222-222222222222"
			const principal = "33333333-3333-4333-8333-333333333333"
			const request = "44444444-4444-4444-8444-444444444444"
			const account = "55555555-5555-4555-8555-555555555555"
			view := agentsetup.View{RequestID: request, TenantID: tenantID, ComputerID: computer, PrincipalID: principal, DaemonID: "skew-fixture", ComputerName: "fixture", Workspace: root, ComputerState: "connected", Revision: 1}
			var registrations, negotiations atomic.Int32
			var updated atomic.Bool
			polled := make(chan struct{}, 4)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/agent-pairing/reconcile":
					_ = json.NewEncoder(w).Encode(view)
				case "/api/agent-pairing/self":
					// Host-capacity negotiation: the released server serves this
					// view without host_capacity, so starts stay on the legacy
					// path and /self/capacity must never be called (default case).
					if r.Method != http.MethodGet {
						t.Errorf("host-capacity negotiation used %s", r.Method)
					}
					negotiations.Add(1)
					_ = json.NewEncoder(w).Encode(view)
				case "/api/me":
					_ = json.NewEncoder(w).Encode(map[string]any{"tenant": map[string]string{"id": tenantID}, "principal": map[string]string{"id": principal, "tenant_id": tenantID, "kind": "agent"}})
				case "/api/agent-pairing/attach":
					registrations.Add(1)
					if updated.Load() {
						_ = json.NewEncoder(w).Encode(map[string]any{"state": "registered", "local_consent_proof_version": attachwatch.LocalConsentProofVersion})
						return
					}
					if tc.status == 400 {
						// Model the released server's strict decoder, rather than
						// returning a canned 400 for an unrelated reason.
						var legacy struct {
							Operation           string `json:"operation"`
							ComputerID          string `json:"computer_id"`
							DeviceProof         string `json:"device_proof"`
							PollKey             string `json:"poll_key"`
							LocalAuthCapability string `json:"local_auth_capability"`
						}
						d := json.NewDecoder(r.Body)
						d.DisallowUnknownFields()
						if err := d.Decode(&legacy); err == nil || !strings.Contains(err.Error(), `unknown field "attach_protocol"`) {
							t.Error("registration did not exercise the older server's unknown-field refusal")
						}
					}
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.message})
				case "/api/runs/queued":
					_ = json.NewEncoder(w).Encode([]any{})
					select {
					case polled <- struct{}{}:
					default:
					}
				case "/api/agent-accounts":
					if r.Method != http.MethodGet || r.URL.RawQuery != "include_checks=true" {
						t.Error("capacity check poll did not use the scoped contract")
					}
					_ = json.NewEncoder(w).Encode([]any{})
				case "/api/agent-accounts/" + account + "/probe":
					w.WriteHeader(204)
				case "/api/harness-recoveries/claim":
					// The runtime polls recovery commands with its own daemon
					// identity; nothing is pending for this fixture.
					var claim struct{ DaemonID, Generation string }
					if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&struct {
						DaemonID   *string `json:"daemon_id"`
						Generation *string `json:"generation"`
					}{&claim.DaemonID, &claim.Generation}) != nil || claim.DaemonID != view.DaemonID || claim.Generation == "" {
						t.Error("recovery claim poll did not carry the paired daemon identity")
					}
					_ = json.NewEncoder(w).Encode([]any{})
				default:
					t.Errorf("unexpected paired runtime route: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			// All production transports still use their real origin and TLS checks;
			// trust only this test server in this nonparallel test.
			priorTransport := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = priorTransport }()
			var logs bytes.Buffer
			priorLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(priorLogger)
			var fixture [32]byte
			if _, err := rand.Read(fixture[:]); err != nil {
				t.Fatal(err)
			}
			proof := hex.EncodeToString(fixture[:])
			c := agentsetup.RuntimeConfig{Schema: "aeon.agent-runtime.v1", Origin: server.URL, TenantID: tenantID, PrincipalID: principal, ComputerID: computer, DaemonID: view.DaemonID, Workspace: root,
				Accounts: []agentsetup.RuntimeAccount{{Harness: "claude", Key: "fixture", AccountID: account, Home: root, Path: filepath.Join(root, "unavailable-claude")}}}
			// An unavailable Claude is held without launching any model CLI. Work
			// polling and local control must still be live through attach refusal.
			snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]string{"request_id": request, "computer_name": "fixture", "workspace_path": root}, "response": map[string]string{"tenant_id": tenantID}, "view": view, "device_secret": proof, "runtime_secret": proof, "lifecycle_secret": proof}
			store, err := agentsetup.OpenStore(root, false)
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]any{"pairing.json": snapshot, agentsetup.RuntimeName: c} {
				raw, _ := json.Marshal(value)
				if err := store.Write(name, raw, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Write("runtime.key", []byte("aeon_fixture_"+proof), true); err != nil {
				t.Fatal(err)
			}
			store.Close()
			for _, afterUpdate := range []bool{false, true} {
				updated.Store(afterUpdate)
				negotiated := negotiations.Load()
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- servePairedContext(ctx, root, time.Hour) }()
				func() {
					defer func() {
						cancel()
						select {
						case err := <-done:
							if err != nil {
								t.Errorf("paired serve failed: %v", err)
							}
						case <-time.After(10 * time.Second):
							t.Error("paired serve did not shut down")
						}
					}()
					for range 2 {
						select {
						case <-polled:
						case err := <-done:
							done <- err
							t.Fatalf("paired serve exited before polling work: %v", err)
						case <-time.After(10 * time.Second):
							t.Fatal("paired serve stopped polling work")
						}
					}
					// Each poll negotiates host capacity before reading the queue.
					if negotiations.Load() == negotiated {
						t.Fatal("paired serve polled work without negotiating host capacity")
					}
					local, err := agentdwire.OpenClient(filepath.Join(root, "daemon"))
					if err != nil {
						t.Fatal(err)
					}
					status, err := local.Lifecycle(t.Context(), "")
					if err != nil || status.DaemonID != c.DaemonID {
						t.Fatal("local control unavailable after registration", err)
					}
					// The existing socket keeps the auth guard even while attach
					// is disabled; unauthenticated callers receive no cause.
					transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, "unix", local.Socket)
					}}
					defer transport.CloseIdleConnections()
					hc := &http.Client{Transport: transport, Timeout: 5 * time.Second}
					res, err := hc.Post("http://agentd/v1/attach", "application/json", strings.NewReader(`{}`))
					if err != nil {
						t.Fatal(err)
					}
					res.Body.Close()
					want := 403
					if res.StatusCode != want {
						t.Fatalf("attach availability: got %d, want %d", res.StatusCode, want)
					}
				}()
				if !afterUpdate && (!strings.Contains(logs.String(), "attach disabled") || !strings.Contains(logs.String(), tc.message)) {
					t.Fatal("attach refusal did not log the server's message")
				}
			}
			if registrations.Load() != 2 {
				t.Fatal("attach registration retried without a restart")
			}
		})
	}
}

func TestPairedAttachRegistersFreshMemoryOnlyKeyAtEveryStart(t *testing.T) {
	t.Setenv("AEON_URL", "https://unpaired.invalid")
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
	var fixture [32]byte
	if _, err = rand.Read(fixture[:]); err != nil {
		t.Fatal(err)
	}
	lifecycle := hex.EncodeToString(fixture[:])
	var registrations []string
	var mu sync.Mutex
	refuse := 0
	proofVersion := attachwatch.LocalConsentProofVersion
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var in attachwatch.DeviceRequest
		if r.Method != "POST" || r.URL.Path != "/api/agent-pairing/attach" || json.NewDecoder(r.Body).Decode(&in) != nil || in.Operation != "register" || in.AttachProtocol != attachwatch.Protocol || in.LocalConsentProofVersion != attachwatch.LocalConsentProofVersion || in.ComputerID != computer || in.DeviceProof != lifecycle || len(in.PollKey) != 64 || in.PollKey == lifecycle || in.Text != "" || !attachwatch.LocalAuthCapabilityReported(in.LocalAuthCapability) {
			t.Error("invalid daemon-start registration")
			w.WriteHeader(403)
			return
		}
		registrations = append(registrations, in.PollKey)
		if refuse != 0 {
			w.Header().Set("Location", "/must-not-follow")
			w.WriteHeader(refuse)
			return
		}
		ack := map[string]any{"state": "registered"}
		if proofVersion != 0 {
			ack["local_consent_proof_version"] = proofVersion
		}
		_ = json.NewEncoder(w).Encode(ack)
	}))
	defer server.Close()
	view := agentsetup.View{RequestID: request, TenantID: tenant, ComputerID: computer, PrincipalID: principal}
	snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]string{"request_id": request, "computer_name": "fixture", "workspace_path": root}, "view": view, "device_secret": lifecycle, "runtime_secret": lifecycle, "lifecycle_secret": lifecycle}
	raw, _ := json.Marshal(snapshot)
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Write("pairing.json", raw, true); err != nil {
		t.Fatal(err)
	}
	store.Close()
	c := agentsetup.RuntimeConfig{Origin: server.URL, TenantID: tenant, PrincipalID: principal, ComputerID: computer, Workspace: root}
	remote := agentd.NewRemote("https://unpaired.invalid", "fixture-runtime-bearer")
	remote.Client.HTTP = server.Client()
	for i := 0; i < 2; i++ {
		manager, err := pairedAttach(root, c, remote)
		if err != nil {
			t.Fatal("daemon-start registration failed")
		}
		manager.Close(t.Context())
	}
	mu.Lock()
	distinct := len(registrations) == 2 && registrations[0] != registrations[1]
	refuse = 403
	mu.Unlock()
	if !distinct {
		t.Fatal("daemon restart reused poll key")
	}
	for _, version := range []int{0, 1, 3} {
		mu.Lock()
		refuse, proofVersion = 0, version
		mu.Unlock()
		if manager, err := pairedAttach(root, c, remote); err == nil || manager != nil || !strings.Contains(err.Error(), "upgrade Aeon and paimos-agentd") {
			t.Fatal("missing or unsupported server proof version left attach enabled")
		}
	}
	mu.Lock()
	refuse, proofVersion = 403, attachwatch.LocalConsentProofVersion
	mu.Unlock()
	if manager, err := pairedAttach(root, c, remote); err == nil || manager != nil {
		t.Fatal("registration failure left attach enabled")
	}
	for _, code := range []int{301, 302, 303, 307, 308} {
		mu.Lock()
		refuse = code
		mu.Unlock()
		if manager, err := pairedAttach(root, c, remote); err == nil || manager != nil {
			t.Fatal("redirect retained attach authority")
		}
	}
	// Inspect only this generated fixture, never the operator's setup store.
	mu.Lock()
	defer mu.Unlock()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatal("watch registration created a store")
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range registrations {
			if bytes.Contains(data, []byte(key)) {
				t.Fatal("poll key written to disk")
			}
		}
		if entry.Name() == "pairing.json" && !bytes.Equal(data, raw) {
			t.Fatal("watch registration changed pairing store")
		}
	}
}

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
			cli := filepath.Join(root, "claude")
			if err = os.WriteFile(cli, []byte("#!/bin/sh\n"), 0700); err != nil {
				t.Fatal(err)
			}
			runtime := agentsetup.RuntimeConfig{Schema: "aeon.agent-runtime.v1", Origin: server.URL, TenantID: tenant, PrincipalID: principal, DaemonID: "fixture", ComputerID: computer, Workspace: root,
				NodePath: "/missing/node", ClaudeSDKPath: "/missing/sdk.mjs", Accounts: []agentsetup.RuntimeAccount{{Harness: "claude", AccountID: "account", Path: cli}}}
			accounts, adapters, err := pairedAdapters(runtime)
			if err != nil || len(adapters) != 0 || len(accounts) != 1 || !accounts[0].DependencyBlocked || accounts[0].PinReason != agentsetup.PinInvalid || accounts[0].PinFix != agentsetup.FixRepin {
				t.Fatal("missing dependencies allowed an execution adapter", err)
			}
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
