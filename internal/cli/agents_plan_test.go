// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentsPlanHumanAndJSON(t *testing.T) {
	isolate(t)
	const payload = `{"total":5,"limits":{"codex":4,"cursor":"off","pi":0,"claude":"no_limit"},"principal_id":"11111111-1111-4111-8111-111111111111","running":{"codex":8,"cursor":2},"running_total":10,"source":"plan","updated_at":null}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/agents/plan" || r.URL.RawQuery != "" {
			t.Error("wrong plan request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, errOut := runCLI([]string{"aeon", "agents", "plan"}, "")
	if code != 0 || errOut != "" {
		t.Fatalf("plan code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"Planned total: 5 · running: 10", "Codex: At most 4 · 8 running", "Cursor: Off · 2 running", "Pi: At most 0 · 0 running", "Claude: No limit · 0 running", "Grok: No limit · 0 running"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	code, out, errOut = runCLI([]string{"aeon", "agents", "plan", "--json"}, "")
	var got, want any
	if err := json.Unmarshal([]byte(payload), &want); err != nil {
		t.Fatal(err)
	}
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("JSON plan code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	encoded, _ := json.Marshal(got)
	expected, _ := json.Marshal(want)
	if string(encoded) != string(expected) {
		t.Fatalf("JSON mismatch %s", encoded)
	}
}

func TestAgentsPlanDenialHasNoOutput(t *testing.T) {
	isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"permission denied"}`))
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, errOut := runCLI([]string{"aeon", "agents", "plan", "--json"}, "")
	if code != 1 || out != "" || !strings.Contains(errOut, "permission denied") {
		t.Fatalf("denial code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if code, _, _ := runCLI([]string{"aeon", "agents", "plan", "another-person"}, ""); code != 2 {
		t.Fatal("plan accepted a person selector")
	}
}
