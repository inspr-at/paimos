// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
)

func TestUseExportComesFromLocalAgentd(t *testing.T) {
	isolate(t)
	root, err := os.MkdirTemp("/tmp", "aeon389-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "agentd.sock")
	if err = os.WriteFile(socket+".token", []byte("12345678901234567890123456789012"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "private-home")
	local := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer 12345678901234567890123456789012" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/account-environment" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(agentd.AccountEnvironment{AccountID: "11111111-1111-1111-1111-111111111111", DaemonID: "daemon-a", Harness: "codex", Variable: "CODEX_HOME", Home: home})
	})}
	go local.Serve(listener)
	defer local.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob := r.URL.RequestURI()
		if strings.Contains(blob, root) || strings.Contains(strings.ToLower(blob), "codex_home") || strings.Contains(blob, home) {
			t.Errorf("local path crossed the network: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []map[string]any{{
			"account_id": "11111111-1111-1111-1111-111111111111", "daemon_id": "daemon-a", "harness": "codex",
			"label": "Spare", "host_label": "studio", "quota_fingerprint": strings.Repeat("cd", 32),
		}}})
	}))
	defer remote.Close()
	t.Setenv("AEON_URL", remote.URL)
	t.Setenv("AEON_API_KEY", testKey)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		code, out, errOut := runCLI([]string{"aeon", "use", "codex", "Spare", "--shell", shell, "--socket", socket}, "")
		want, err := capacityExport(agentd.AccountEnvironment{Harness: "codex", Variable: "CODEX_HOME", Home: home}, shell)
		if err != nil || code != 0 || out != want+"\n" || errOut != "" {
			t.Fatalf("%s code=%d out=%q err=%q want=%q", shell, code, out, errOut, want)
		}
		if strings.Contains(out, "Spare") {
			t.Fatal("shell export echoed the server label instead of the local home")
		}
	}
}
