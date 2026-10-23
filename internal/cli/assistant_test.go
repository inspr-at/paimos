// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/assistants"
)

// Runs under the existing ESSENTIAL TestMessagingAssistantCLISecretOutput.
// Risk: assistant commands send agent authority for person writes, drop policy
// revisions, alter exact budget values, or hide pagination/API failures.
func testAssistantIdentityPolicyCLI(t *testing.T) {
	isolate(t)
	const id = "11111111-1111-4111-8111-111111111111"
	const owner = "22222222-2222-4222-8222-222222222222"
	cookie := strings.Repeat("e", 64)
	policy := assistants.DefaultPolicy()
	policy.WeeklyBudget = assistants.Budget{Amount: "12.500001", Unit: "EUR"}
	var seen []string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		c, err := r.Cookie("aeon_session")
		if err != nil || c.Value != cookie || r.Header.Get("Authorization") != "" {
			t.Error("person commands did not use cookie-only authentication")
		}
		if r.Header.Get("Origin") == "" {
			t.Error("person request omitted origin")
		}
		if fail {
			http.Error(w, "revision changed", 409)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/assistants" {
			var in struct {
				PrincipalID string            `json:"principal_id"`
				DisplayName string            `json:"display_name"`
				Vendor      string            `json:"vendor"`
				Owner       string            `json:"owner_person_id"`
				Policy      assistants.Policy `json:"policy"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.PrincipalID != id || in.DisplayName != "Amy" || in.Vendor != "grok" || in.Owner != owner || in.Policy.WeeklyBudget.Amount != "12.500001" {
				t.Error("create/link lost identity or exact budget")
			}
		}
		if r.Method == "PUT" {
			var in struct {
				Revision int64             `json:"expected_revision"`
				Policy   assistants.Policy `json:"policy"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Revision != 7 || in.Policy.WeeklyBudget.Amount != "12.500001" {
				t.Error("policy update lost revision or budget")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/assistants" && r.Method == "GET" {
			_, _ = w.Write([]byte(`{"assistants":[{"display_name":"Amy"}],"next_cursor":"` + id + `"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"principal_id": id, "display_name": "Amy", "revision": 8, "policy": policy})
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	sessionFile := filepath.Join(t.TempDir(), "synthetic-session")
	if err := os.WriteFile(sessionFile, []byte(cookie), 0600); err != nil {
		t.Fatal(err)
	}
	policyJSON, _ := json.Marshal(policy)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"list", "--limit", "1"}, "GET /api/assistants?limit=1"},
		{[]string{"list", "--after", id}, "GET /api/assistants?after=" + id},
		{[]string{"show", id}, "GET /api/assistants/" + id},
		{[]string{"create", "--name", "Amy", "--vendor", "grok", "--owner", owner, "--principal", id, "--policy-file", "-"}, "POST /api/assistants"},
		{[]string{"policy", "show", id}, "GET /api/assistants/" + id + "/policy"},
		{[]string{"policy", "set", id, "--expected-revision", "7", "--policy-file", "-"}, "PUT /api/assistants/" + id + "/policy"},
		{[]string{"update", id, "--expected-revision", "7", "--name", "Amy"}, "PATCH /api/assistants/" + id},
		{[]string{"disable", id}, "POST /api/assistants/" + id + "/disable"},
	} {
		args := append([]string{"aeon", "assistant"}, tc.args...)
		args = append(args, "--session-cookie-file", sessionFile, "--json")
		code, out, errOut := runCLI(args, string(policyJSON))
		if code != 0 || !strings.Contains(out, "Amy") || strings.Contains(out+errOut, cookie) || seen[len(seen)-1] != tc.want {
			t.Fatalf("assistant CLI %s failed (code %d)", tc.args[0], code)
		}
		if tc.args[0] == "list" && !strings.Contains(out, "next_cursor") {
			t.Fatal("CLI hid pagination")
		}
	}
	before := len(seen)
	for _, args := range [][]string{{"show", "bad"}, {"list", "--limit", "101"}, {"policy", "set", id, "--policy-file", "-"}, {"create", "--name", "Amy"}} {
		full := append([]string{"aeon", "assistant"}, args...)
		full = append(full, "--session-cookie-file", sessionFile)
		if code, _, _ := runCLI(full, string(policyJSON)); code != 2 || len(seen) != before {
			t.Fatal("invalid CLI input reached API")
		}
	}
	fail = true
	if code, out, _ := runCLI([]string{"aeon", "assistant", "policy", "set", id, "--expected-revision", "7", "--policy-file", "-", "--session-cookie-file", sessionFile}, string(policyJSON)); code == 0 || strings.Contains(out, "Amy") {
		t.Fatal("failed policy write reported success")
	}
}
