// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsecurity"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

// Risk: a fail-closed preflight used to exit 0 with no line, so launchd looked
// idle and a revoked or cleaned computer was indistinguishable from a crash.
func TestDispatchDeniedServeNamesReasonAndExit(t *testing.T) {
	if agentsecurity.DefaultVault() != nil {
		t.Skip("ordinary-build fixture; native denial has its own Keychain-free tests")
	}
	const secret = "abababababababababababababababababababababababababababababababab"
	cases := []struct {
		name  string
		state string
		clean bool
		line  string
	}{
		{name: "DisconnectAll", state: "revoked", line: "agentd serve: dispatch not permitted: DisconnectAll"},
		{name: "ComputerCleaned", state: "connected", clean: true, line: "agentd serve: dispatch not permitted: ComputerCleaned"},
		{name: "computer state", state: "paused", line: "agentd serve: dispatch not permitted: computer state not connected (paused)"},
		{name: "unsafe state", state: "paused " + secret, line: "agentd serve: dispatch not permitted: computer state not connected (unavailable)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := denyServe(t, tc.state, tc.clean, secret)
			var denied *dispatchDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("permitted=false did not deny: %v", err)
			}
			for _, parent := range []struct {
				name string
				code int
			}{{"systemd", dispatchDeniedExitCode}, {"zsh", dispatchDeniedExitCode}, {"launchd", 0}, {"/sbin/launchd", 0}} {
				var stderr bytes.Buffer
				code, ok := writeDispatchDenial(&stderr, err, parent.name)
				if !ok || code != parent.code || stderr.String() != tc.line+"\n" || strings.Contains(stderr.String(), secret) || strings.Count(stderr.String(), "\n") != 1 {
					t.Fatalf("parent %s: code %d ok %v line %q", parent.name, code, ok, stderr.String())
				}
			}
		})
	}
	t.Run("permitted", func(t *testing.T) {
		err := denyServe(t, "connected", false, secret)
		var denied *dispatchDeniedError
		if err == nil || errors.As(err, &denied) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "dispatch not permitted") {
			t.Fatalf("permitted serve changed: %v", err)
		}
	})
	t.Run("units", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		exe := filepath.Join(root, "agentd")
		for _, platform := range []agentsetup.Platform{{OS: "darwin", Arch: "arm64"}, {OS: "linux", Arch: "amd64"}} {
			raw, err := (agentsetup.ServiceManager{Platform: platform, Home: root, Executable: exe}).Definition(root)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if strings.Contains(text, secret) {
				t.Fatal("unit contained a secret")
			}
			if platform.OS == "darwin" {
				if !strings.Contains(text, "<key>SuccessfulExit</key><false/>") || !strings.Contains(text, "exit 78 means dispatch is not permitted") || !strings.Contains(text, "exits 0") {
					t.Fatal("launchd keepalive contract missing")
				}
				continue
			}
			if !strings.Contains(text, "Restart=on-failure") || !strings.Contains(text, "SuccessExitStatus=78") || !strings.Contains(text, "exit 78 means dispatch is not permitted") {
				t.Fatal("systemd denial status missing")
			}
		}
	})
}

func denyServe(t *testing.T, state string, cleaned bool, secret string) error {
	t.Helper()
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
	view := agentsetup.View{RequestID: request, TenantID: tenant, State: "redeemed", ComputerID: computer, PrincipalID: principal, DaemonID: "fixture", ComputerName: "fixture", Workspace: root, Platform: "darwin", Arch: "arm64", ComputerState: state, Revision: 1}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-pairing/reconcile" || r.Header.Get("Authorization") != "" {
			t.Error("denial preflight used a credentialed or unexpected request")
			w.WriteHeader(403)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["lifecycle_secret"] != secret {
			t.Error("denial preflight sent the wrong lifecycle proof")
			w.WriteHeader(403)
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
	snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]any{"request_id": request, "computer_name": "fixture", "workspace_path": root, "platform": "darwin", "arch": "arm64"}, "response": map[string]string{"tenant_id": tenant, "request_id": request}, "view": view, "device_secret": secret, "runtime_secret": secret, "lifecycle_secret": secret, "lifecycle_request_id": request, "phase": "connected", "computer_cleaned": cleaned}
	raw, _ := json.Marshal(snapshot)
	if err = store.Write("pairing.json", raw, true); err != nil {
		t.Fatal(err)
	}
	runtime := agentsetup.RuntimeConfig{Schema: "aeon.agent-runtime.v1", Origin: server.URL, TenantID: tenant, PrincipalID: principal, DaemonID: "fixture", ComputerID: computer, Workspace: root}
	raw, _ = json.Marshal(runtime)
	if err = store.Write(agentsetup.RuntimeName, raw, true); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(logger)
	err = serve([]string{"--setup-root", root})
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "dispatch not permitted") {
		t.Fatalf("denial was logged twice or leaked: %s", logs.String())
	}
	if cleaned || state != "connected" {
		if logs.Len() != 0 {
			t.Fatalf("denial also logged a failure: %s", logs.String())
		}
	}
	return err
}
