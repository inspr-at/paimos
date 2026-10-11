// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentsTierShowSetAsk(t *testing.T) {
	isolate(t)
	const project = "11111111-1111-4111-8111-111111111111"
	const session = "22222222-2222-4222-8222-222222222222"
	base := "/api/projects/" + project + "/harness-sessions/" + session
	var posted []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid body")
			}
			posted = append(posted, body)
			if r.Header.Get("X-Aeon-Worker-Lease") != "" {
				t.Error("ask exposed a daemon lease")
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"state": "pending"})
			return
		}
		switch r.URL.Path {
		case "/api/kinds":
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"id": "project-kind", "slug": "project"}}})
		case "/api/projects/lookup":
			json.NewEncoder(w).Encode(map[string]string{"id": project})
		case "/api/nodes":
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"id": project, "key": "PROJECT", "kind_id": "project-kind", "fields": map[string]any{"project_key": "PROJECT"}}}})
		case base + "/tier":
			json.NewEncoder(w).Encode(map[string]any{"session_id": session, "active_tier": "default", "revision": 7, "reports": []any{}, "requests": []any{}})
		case base:
			json.NewEncoder(w).Encode(map[string]any{"id": session, "process_ownership": map[string]any{"daemon_id": "fixture", "generation": "generation", "process_id": "process", "root_pid": 123, "group_id": 123, "started_at": "2026-10-02T00:00:00Z"}})
		default:
			t.Error("unexpected path " + r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	for _, action := range []string{"show", "set", "ask"} {
		args := []string{"aeon", "agents", "tier", action, "--project", "PROJECT", "--session", session}
		if action != "show" {
			args = append(args, "--tier", "Fast")
		}
		if action == "ask" {
			args = append(args, "--reason", "QA waits")
		}
		code, out, stderr := runCLI(args, "")
		if code != 0 {
			t.Fatalf("%s: %d %s", action, code, stderr)
		}
		assertNoSecret(t, out+stderr)
		if action == "show" && !strings.Contains(out, "Default") {
			t.Fatal(out)
		}
	}
	if len(posted) != 2 || posted[0]["expected_revision"] != float64(7) || posted[0]["expected_ownership"] == nil || posted[1]["reason"] != "QA waits" || posted[1]["tier"] != "fast" {
		t.Fatal(posted)
	}
}
