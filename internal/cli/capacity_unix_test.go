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

func TestCapacityEnvUsesOnlyAuthenticatedLocalSocket(t *testing.T) {
	isolate(t)
	root, err := os.MkdirTemp("/tmp", "aeon383-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "agentd.sock")
	if err = os.WriteFile(socket+".token", []byte("12345678901234567890123456789012"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "private config's home")
	localRequests := 0
	local := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localRequests++
		if r.Header.Get("Authorization") != "Bearer 12345678901234567890123456789012" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/v1/account-environment" || r.URL.Query().Get("account_id") != "local" || r.URL.Query().Get("daemon_id") != "here" {
			w.WriteHeader(409)
			return
		}
		_ = json.NewEncoder(w).Encode(agentd.AccountEnvironment{AccountID: "local", DaemonID: "here", Harness: "codex", Variable: "CODEX_HOME", Home: home})
	})}
	go local.Serve(listener)
	defer local.Close()
	owner := "here"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), root) {
			t.Error("local path sent to remote server")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"harness": "codex", "parallel_runs": 1, "accounts": []map[string]any{{"account_id": "local", "account_label": "Main", "daemon_id": owner, "harness": "codex", "rank": 1, "available_slots": 1}}})
	}))
	defer remote.Close()
	t.Setenv("AEON_URL", remote.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "capacity", "next", "codex", "--env", "--socket", socket}
	code, out, errOut := runCLI(args, "")
	want, _ := capacityExport(agentd.AccountEnvironment{Harness: "codex", Variable: "CODEX_HOME", Home: home}, "sh")
	if code != 0 || out != want+"\n" || errOut != "" {
		t.Fatal("owning local export failed")
	}
	if localRequests != 1 {
		t.Fatal("local daemon was not consulted")
	}
	owner = "elsewhere"
	code, out, _ = runCLI(args, "")
	if code == 0 || out != "" {
		t.Fatal("remote owner printed local environment")
	}
}
