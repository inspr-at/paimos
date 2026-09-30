// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
)

func TestCapacityNextAdviceAndNoRemoteEnvironment(t *testing.T) {
	isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-accounts/capacity/next" || r.URL.Query().Get("harness") != "codex" {
			t.Error("wrong advice request")
		}
		if strings.Contains(r.URL.RawQuery, "home") || strings.Contains(r.URL.RawQuery, "socket") {
			t.Error("local path crossed the network")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"harness": "codex", "parallel_runs": 2, "accounts": []map[string]any{{"account_id": "foreign", "account_label": "Spare", "daemon_id": "elsewhere", "harness": "codex", "rank": 1, "available_slots": 1}}})
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, errOut := runCLI([]string{"aeon", "capacity", "next", "codex"}, "")
	if code != 0 || !strings.Contains(out, "Spare · elsewhere · 2 agents") || errOut != "" {
		t.Fatalf("advice code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, _ = runCLI([]string{"aeon", "capacity", "next", "codex", "--env", "--socket", "/unavailable/agentd.sock"}, "")
	if code == 0 || out != "" {
		t.Fatal("remote account printed an environment")
	}
	code, out, _ = runCLI([]string{"aeon", "capacity", "next", "codex", "--json"}, "")
	if code != 0 || !strings.Contains(out, `"account_label":"Spare"`) {
		t.Fatal("JSON advice failed")
	}
}
func TestCapacityExportQuotesAndAllowlist(t *testing.T) {
	local := agentd.AccountEnvironment{Harness: "codex", Variable: "CODEX_HOME", Home: "/private/account's $(no) `no`"}
	got, err := capacityExport(local, "zsh")
	if err != nil || got != `export CODEX_HOME='/private/account'"'"'s $(no) `+"`no`'" {
		t.Fatalf("unsafe shell quoting %q", got)
	}
	got, err = capacityExport(local, "fish")
	if err != nil || !strings.Contains(got, `account\'s`) {
		t.Fatal("fish quoting failed")
	}
	local.Variable = "PATH"
	if _, err = capacityExport(local, "sh"); err == nil {
		t.Fatal("untrusted variable accepted")
	}
	local.Variable = "CODEX_HOME"
	local.Home = "/private/bad\nexport PATH=bad"
	if _, err = capacityExport(local, "sh"); err == nil {
		t.Fatal("multiline path accepted")
	}
}
