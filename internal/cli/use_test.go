// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUseRecordsNoPathAndPrintsNothingWithoutLocalAgentd(t *testing.T) {
	isolate(t)
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.URL.Path != "/api/agent-accounts/use" || r.URL.Query().Get("harness") != "codex" || r.URL.Query().Get("label") != "Spare" {
			t.Errorf("unexpected use request %s", r.URL.RequestURI())
		}
		blob := r.URL.RequestURI() + r.Header.Get("Authorization")
		assertCLINoPath(t, blob)
		_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []map[string]any{{
			"account_id": "11111111-1111-1111-1111-111111111111", "daemon_id": "daemon-a", "harness": "codex",
			"label": "Spare", "host_label": "studio", "quota_fingerprint": strings.Repeat("ab", 32),
		}}})
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, _ := runCLI([]string{"aeon", "use", "codex", "/tmp/not-a-label"}, "")
	if code == 0 || len(seen) != 0 || out != "" {
		t.Fatalf("path label reached the server code=%d requests=%d out=%q", code, len(seen), out)
	}
	code, out, errOut := runCLI([]string{"aeon", "use", "codex", "Spare", "--socket", "/tmp/aeon-389-missing.sock"}, "")
	if code == 0 || out != "" || !strings.Contains(errOut, "no environment was printed") {
		t.Fatalf("missing agentd printed an environment code=%d out=%q err=%q", code, out, errOut)
	}
	if len(seen) != 1 {
		t.Fatalf("requests %v", seen)
	}
	assertCLINoPath(t, strings.Join(seen, "\n"))
}

func assertCLINoPath(t *testing.T, raw string) {
	t.Helper()
	lower := strings.ToLower(raw)
	for _, bad := range []string{"/users", "codex_home", "claude_config", "socket", `\`, "c:/"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("network payload contains %q", bad)
		}
	}
}
