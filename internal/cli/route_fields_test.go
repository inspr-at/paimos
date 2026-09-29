// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssueRouteFlags(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{
		{"aeon", "issue", "update", "AEON-1", "--role", "gruntwork"},
		{"aeon", "issue", "update", "AEON-1", "--area", "mobile"},
		{"aeon", "issue", "update", "AEON-1", "--role", ""},
	} {
		code, _, errOut := runCLI(args, "")
		if code != 2 || (!strings.Contains(errOut, "--role") && !strings.Contains(errOut, "--area") && !strings.Contains(errOut, "nothing to update")) {
			t.Fatalf("%v: %d %s", args, code, errOut)
		}
	}
	code, out, errOut := runCLI([]string{"aeon", "issue", "update", "--help"}, "")
	if code != 0 || !strings.Contains(out, "--role") || !strings.Contains(out, "--area") || errOut != "" {
		t.Fatalf("help: %d %s %s", code, out, errOut)
	}
	code, out, errOut = runCLI([]string{"aeon", "issue", "update", "AEON-1", "--role", " build-hard ", "--area", "full-stack", "--dry-run"}, "")
	if code != 0 || !strings.Contains(out, "dry-run: would update AEON-1") || errOut != "" {
		t.Fatalf("dry-run: %d %s %s", code, out, errOut)
	}

	revision := time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC)
	ticket := apiNode{ID: transcriptEntryID, Key: "AEON-1", KindID: "ticket-kind", Title: "Route", State: "open", UpdatedAt: revision, Fields: json.RawMessage(`{"priority":"high","route_role_by":"forged"}`)}
	epic := apiNode{ID: transcriptProjectID, Key: "AEON-2", KindID: "epic-kind", Title: "Epic", State: "open", UpdatedAt: revision, Fields: json.RawMessage(`{}`)}
	var wrote map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			_ = json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "ticket-kind", Slug: "ticket"}, {ID: "epic-kind", Slug: "epic"}, {ID: "task-kind", Slug: "task"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			q := r.URL.Query().Get("q")
			if q == epic.Key {
				_ = json.NewEncoder(w).Encode(nodePage{Items: []apiNode{epic}})
				return
			}
			_ = json.NewEncoder(w).Encode(nodePage{Items: []apiNode{ticket}})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/nodes/"+ticket.ID:
			if got := r.Header.Get("If-Unmodified-Since"); got != revision.Format(time.RFC3339Nano) {
				t.Errorf("revision %q", got)
			}
			var patch struct {
				Fields map[string]any `json:"fields"`
			}
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Error(err)
			}
			wrote = patch.Fields
			raw, _ := json.Marshal(patch.Fields)
			ticket.Fields = raw
			_ = json.NewEncoder(w).Encode(ticket)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "issue", "update", "AEON-1", "--role", " build ", "--area", "backend"}
	code, out, errOut = runCLI(args, "")
	if code != 0 || errOut != "" {
		t.Fatalf("update: %d %s %s", code, out, errOut)
	}
	if wrote["route_role"] != "build" || wrote["area"] != "backend" || wrote["priority"] != "high" {
		t.Fatal(wrote)
	}
	for _, key := range []string{"route_role_source", "route_role_by", "route_role_at", "area_source", "area_by", "area_at"} {
		if wrote[key] != nil {
			t.Fatalf("client sent %s: %#v", key, wrote)
		}
	}
	var view issueView
	if err := json.Unmarshal([]byte(out), &view); err != nil || view.RouteRole != "build" || view.Area != "backend" {
		t.Fatalf("json %s %v", out, err)
	}
	code, _, errOut = runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "issue", "update", "AEON-2", "--role", "scout"}, "")
	if code != 2 || !strings.Contains(errOut, "tickets and tasks") || wrote["route_role"] != "build" {
		t.Fatalf("epic: %d %s %#v", code, errOut, wrote)
	}
}
