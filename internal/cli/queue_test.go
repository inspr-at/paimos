// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/workqueue"
)

func TestQueueCLIContract(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	const agent = "22222222-2222-2222-2222-222222222222"
	const profile = "33333333-3333-3333-3333-333333333333"
	for _, tc := range []struct {
		args         []string
		method, path string
		want         map[string]any
	}{
		{[]string{"queue", "list"}, "GET", "/api/queue", nil},
		{[]string{"queue", "add", id}, "POST", "/api/queue", map[string]any{"node_id": id}},
		{[]string{"queue", "add", id, "--agent", agent, "--profile", profile}, "POST", "/api/queue", map[string]any{"node_id": id, "agent_principal_id": agent, "model_profile_id": profile}},
		{[]string{"queue", "remove", id}, "DELETE", "/api/queue/" + id, nil},
		{[]string{"queue", "move", id, "3"}, "POST", "/api/queue/" + id + "/move", map[string]any{"position": float64(3)}},
		{[]string{"queue", "reset"}, "POST", "/api/queue/reset", map[string]any{}},
		{[]string{"queue", "next"}, "POST", "/api/queue/next", map[string]any{}},
		{[]string{"queue", "next", "--agent", agent, "--profile", profile}, "POST", "/api/queue/next", map[string]any{"agent_principal_id": agent, "model_profile_id": profile}},
		{[]string{"queue", "readiness", id}, "GET", "/api/queue/" + id + "/readiness", nil},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			isolate(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/nodes/"+id {
					_, _ = w.Write([]byte(`{"id":"` + id + `","key":"QUE-1"}`))
					return
				}
				if r.URL.Path != tc.path || r.Method != tc.method {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
					http.Error(w, "wrong request", 400)
					return
				}
				calls++
				if tc.want != nil {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					a, _ := json.Marshal(body)
					b, _ := json.Marshal(tc.want)
					if string(a) != string(b) {
						t.Errorf("body %s, want %s", a, b)
					}
				}
				_, _ = w.Write([]byte(`{"items":[],"entry":null,"removed":true,"queueable":true,"ready":false,"missing":["estimate"],"suggested_estimate_hours":2}`))
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			args := append([]string{"aeon", "--json"}, tc.args...)
			code, out, err := runCLI(args, "")
			if code != 0 || calls != 1 {
				t.Fatalf("code %d calls %d out %s err %s", code, calls, out, err)
			}
			assertNoSecret(t, out+err)
		})
	}
}
func TestQueueCLILocalValidationAndIssueProjection(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"queue", "move", "QUE-1", "0"}, {"queue", "add", "QUE-1", "--agent", "bad"}, {"queue", "next", "--agent", "11111111-1111-1111-1111-111111111111"}} {
		code, _, _ := runCLI(append([]string{"aeon"}, args...), "")
		if code != 2 {
			t.Fatalf("validation returned %d for %v", code, args)
		}
	}
	rt := &runtime{}
	queued := &workqueue.Queued{Position: 3, By: workqueue.Actor{Name: "Coordinator"}}
	view := rt.viewIssue(apiNode{ID: "one", Key: "QUE-1", Queued: queued, Fields: json.RawMessage(`{}`)}, kindTable{})
	raw, _ := json.Marshal(view)
	if !strings.Contains(string(raw), `"queued":{"position":3`) {
		t.Fatalf("issue JSON lost queued projection %s", raw)
	}
}
