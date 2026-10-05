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
			var result map[string]any
			if code != 0 || calls != 1 || json.Unmarshal([]byte(out), &result) != nil || result["automatic_launch_enabled"] != false {
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

func TestLeadSettingsCLIUsesExplicitPersonSession(t *testing.T) {
	isolate(t)
	cookie := strings.Repeat("a", 64) // Synthetic fixture; never a live session.
	file := filepath.Join(t.TempDir(), "session-cookie")
	if err := os.WriteFile(file, []byte(cookie), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "PUT" || r.URL.Path != "/api/settings/lead-policy" {
			t.Error("wrong request")
		}
		ck, err := r.Cookie("aeon_session")
		if err != nil || ck.Value != cookie || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") == "" {
			t.Error("person authentication was not isolated")
		}
		_, _ = w.Write([]byte(`{"revision":1,"automatic_launch_enabled":false}`))
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "--json", "project", "lead-settings", "set", "--workspace", "--revision", "0", "--from", "-", "--session-cookie-file", file}
	code, out, err := runCLI(args, `{}`)
	if code != 0 || calls != 1 {
		t.Fatalf("code %d calls %d", code, calls)
	}
	if strings.Contains(out+err, cookie) {
		t.Fatal("person session leaked")
	}
	assertNoSecret(t, out+err)
}
