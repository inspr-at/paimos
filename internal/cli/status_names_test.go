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

// AEON-1401. The risk: the workspace's names leak into machine output, or
// people keep reading the canonical state after a rename.
func TestIssueTextUsesStatusNamesAndJSONStaysCanonical(t *testing.T) {
	isolate(t)
	ticket := map[string]any{"id": transcriptEntryID, "key": "MEM-1", "kind_id": "ticket-kind", "parent_id": transcriptProjectID, "title": "Rename statuses", "state": "in_progress", "fields": map[string]any{"priority": "high"}, "updated_at": "2026-10-19T00:00:00Z"}
	project := map[string]any{"id": transcriptProjectID, "key": "PRJ-1", "kind_id": "project-kind", "title": "AEON", "state": "active", "fields": map[string]any{"project_key": "AEON"}}
	names, reads := `{"revision":2,"names":{"in_progress":{"en":"Building","de":"In Umsetzung"}}}`, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/settings/status-names":
			reads++
			if names == "" {
				http.NotFound(w, r) // a server from before AEON-1401
				return
			}
			_, _ = w.Write([]byte(names))
		case r.URL.Path == "/api/kinds":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]string{{"id": "ticket-kind", "slug": "ticket"}, {"id": "project-kind", "slug": "project"}}})
		case r.URL.Path == "/api/node-keys/MEM-1":
			_ = json.NewEncoder(w).Encode(ticket)
		case r.URL.Path == "/api/projects/lookup":
			_ = json.NewEncoder(w).Encode(project)
		case r.URL.Path == "/api/nodes" && r.URL.Query().Get("kind_id") == "project-kind":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{project}})
		case r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{ticket}})
		case r.URL.Path == "/api/nodes/"+transcriptEntryID+"/activity":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "next_cursor": nil})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	run := func(args ...string) string {
		t.Helper()
		code, out, errOut := runCLI(append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}, args...), "")
		if code != 0 {
			t.Fatalf("%v: %d %s", args, code, errOut)
		}
		return out
	}

	if out := run("issue", "get", "MEM-1"); !strings.Contains(out, "  status:   Building (in_progress)\n") {
		t.Fatalf("human get: %q", out)
	}
	if out := run("issue", "list", "--project", "AEON"); !strings.Contains(out, "MEM-1         Building       high   Rename statuses") {
		t.Fatalf("human list: %q", out)
	}
	before := reads
	for _, args := range [][]string{{"--json", "issue", "get", "MEM-1"}, {"--json", "issue", "list", "--project", "AEON"}} {
		out := run(args...)
		if !strings.Contains(strings.ReplaceAll(out, " ", ""), `"status":"in_progress"`) || strings.Contains(out, "Building") {
			t.Fatalf("%v: machine output changed: %s", args, out)
		}
	}
	if reads != before {
		t.Fatal("--json must not read status names")
	}
	// An older server or an unreadable answer keeps the canonical state.
	names = ""
	if out := run("issue", "get", "MEM-1"); !strings.Contains(out, "  status:   in_progress\n") {
		t.Fatalf("fallback get: %q", out)
	}
}
