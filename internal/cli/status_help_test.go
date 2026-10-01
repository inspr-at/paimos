// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusHelpUsesAPIForAgents(t *testing.T) {
	isolate(t)
	body := `{"definitions":[{"state":"accepted","label":"Accepted","meaning":"Confirmed","set_by":"Person, or the 45-day rule"}],"queued":{"label":"Queued","meaning":"Open plus a place in the work queue","is_status":false},"autopilot":{"enabled":true,"effective_enabled":true,"project_mode":"inherit","rules":{"accept":{"enabled":true,"days":45}}},"triage":{"mode":"apply","available":false},"limits_source":"workspace"}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/status/help" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	config := filepath.Join(t.TempDir(), "missing-config.yaml")
	for _, program := range []string{"aeon", "paimos"} {
		code, out, err := runCLI([]string{program, "--config", config, "status", "help", "--json"}, "")
		if code != 0 || err != "" {
			t.Fatalf("%s: %d %s", program, code, err)
		}
		var got, want any
		_ = json.Unmarshal([]byte(out), &got)
		_ = json.Unmarshal([]byte(body), &want)
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			t.Fatalf("help changed the API definitions: %s", out)
		}
	}
	code, out, err := runCLI([]string{"aeon", "--config", config, "status", "help"}, "")
	if code != 0 || err != "" || !strings.Contains(out, "45-day rule") || !strings.Contains(out, "Queued") || calls != 3 {
		t.Fatalf("text help: %d %s %s; calls %d", code, out, err, calls)
	}
}

func TestStatusHelpFailsWhenLiveAPIUnavailable(t *testing.T) {
	isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"permission denied"}`))
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, err := runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "status", "help", "--json"}, "")
	if code != 1 || out != "" || !strings.Contains(err, "permission denied") {
		t.Fatalf("API failure became defaults: %d %s %s", code, out, err)
	}
}
