// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecurCLIContract(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	for _, tc := range []struct {
		args         []string
		method, path string
		body         map[string]any
	}{
		{[]string{"recur", "list"}, "GET", "/api/recurrences", nil},
		{[]string{"recur", "get", id}, "GET", "/api/recurrences/" + id, nil},
		{[]string{"recur", "create", "--body-file", "-"}, "POST", "/api/recurrences", map[string]any{"project_id": id}},
		{[]string{"recur", "update", id, "--body-file", "-"}, "PUT", "/api/recurrences/" + id, map[string]any{"project_id": id}},
		{[]string{"recur", "pause", id, "--revision", "7"}, "POST", "/api/recurrences/" + id + "/pause", map[string]any{"expected_revision": float64(7)}},
		{[]string{"recur", "resume", id, "--revision", "7"}, "POST", "/api/recurrences/" + id + "/resume", map[string]any{"expected_revision": float64(7)}},
		{[]string{"recur", "run-now", id, "--idempotency-key", "once"}, "POST", "/api/recurrences/" + id + "/run-now", map[string]any{"idempotency_key": "once"}},
		{[]string{"recur", "preview", id, "--count", "3"}, "GET", "/api/recurrences/" + id + "/preview?count=3", nil},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			isolate(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.RequestURI() != tc.path {
					t.Errorf("request %s %s", r.Method, r.URL.RequestURI())
					http.Error(w, "wrong request", 400)
					return
				}
				if tc.body != nil {
					var got map[string]any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					a, _ := json.Marshal(got)
					b, _ := json.Marshal(tc.body)
					if string(a) != string(b) {
						t.Errorf("body %s want %s", a, b)
					}
				}
				_, _ = w.Write([]byte(`{"id":"` + id + `","revision":7,"items":[],"times":[],"trigger_kind":"event"}`))
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			code, out, err := runCLI(append([]string{"aeon", "--json"}, tc.args...), `{"project_id":"`+id+`"}`)
			if code != 0 || calls != 1 {
				t.Fatalf("code %d calls %d out %s err %s", code, calls, out, err)
			}
			assertNoSecret(t, out+err)
		})
	}
}
func TestRecurCLIRevisionFetchAndLocalValidation(t *testing.T) {
	isolate(t)
	const id = "11111111-1111-1111-1111-111111111111"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if r.Method != "GET" || r.URL.Path != "/api/recurrences/"+id {
				t.Error("revision fetch")
			}
		} else {
			var in map[string]int
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&in) != nil || in["expected_revision"] != 9 {
				t.Error("revision write")
			}
		}
		_, _ = w.Write([]byte(`{"id":"` + id + `","revision":9}`))
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, _, err := runCLI([]string{"aeon", "recur", "pause", id}, "")
	if code != 0 || calls != 2 {
		t.Fatalf("pause %d %d %s", code, calls, err)
	}
	for _, args := range [][]string{{"recur", "get", "bad"}, {"recur", "run-now", id}, {"recur", "preview", id, "--count", "101"}, {"recur", "list", "--after", "bad"}, {"recur", "update", "bad"}, {"recur", "create"}} {
		before := calls
		code, _, _ := runCLI(append([]string{"aeon"}, args...), "")
		if code != 2 || calls != before {
			t.Fatalf("local validation %v code %d calls %d", args, code, calls)
		}
	}
}
