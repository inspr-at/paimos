// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLeadSettingsCLIContractAndValidation(t *testing.T) {
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, tc := range []struct {
		args                       []string
		method, path, query, stdin string
	}{
		{[]string{"get", id}, "GET", "/api/projects/" + id + "/lead-settings", "", ""},
		{[]string{"get", "--workspace"}, "GET", "/api/settings/lead-policy", "", ""},
		{[]string{"set", id, "--revision", "0", "--from", "-"}, "PUT", "/api/projects/" + id + "/lead-settings", "", `{"allowed_host_ids":[]}`},
		{[]string{"reset", id, "--revision", "3"}, "DELETE", "/api/projects/" + id + "/lead-settings", "revision=3", ""},
		{[]string{"reset", "--workspace", "--revision", "1"}, "DELETE", "/api/settings/lead-policy", "revision=1", ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			isolate(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != tc.query {
					t.Errorf("wrong request: %s %s", r.Method, r.URL.String())
					http.Error(w, "wrong request", 400)
					return
				}
				if tc.method == "PUT" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body["revision"] != float64(0) || body["overrides"] == nil {
						t.Error("lost revision or override", body)
					}
				}
				_, _ = w.Write([]byte(`{"revision":1,"automatic_launch_enabled":false}`))
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			args := append([]string{"aeon", "--json", "project", "lead-settings"}, tc.args...)
			code, out, err := runCLI(args, tc.stdin)
			if code != 0 || calls != 1 || !strings.Contains(out, `"automatic_launch_enabled": false`) {
				t.Fatalf("code %d calls %d out %s err %s", code, calls, out, err)
			}
			assertNoSecret(t, out+err)
		})
	}
	isolate(t)
	for _, args := range [][]string{{"get"}, {"get", id, "--workspace"}, {"set", id, "--from", "-"}, {"reset", id, "--revision", "-1"}, {"set", id, "--revision", "0", "--from", "-"}} {
		code, _, err := runCLI(append([]string{"aeon", "project", "lead-settings"}, args...), "[]")
		if code != 2 {
			t.Fatalf("validation %v returned %d: %s", args, code, err)
		}
	}
}
