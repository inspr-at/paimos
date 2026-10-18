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
		{"aeon", "issue", "update", "AEON-1", "--complexity", "XL"},
		{"aeon", "issue", "update", "AEON-1", "--role", ""},
	} {
		code, _, errOut := runCLI(args, "")
		if code != 2 || (!strings.Contains(errOut, "--role") && !strings.Contains(errOut, "--area") && !strings.Contains(errOut, "--complexity") && !strings.Contains(errOut, "nothing to update")) {
			t.Fatalf("%v: %d %s", args, code, errOut)
		}
	}
	code, out, errOut := runCLI([]string{"aeon", "issue", "update", "--help"}, "")
	if code != 0 || !strings.Contains(out, "--role") || !strings.Contains(out, "--area") || !strings.Contains(out, "--complexity") || errOut != "" {
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
	args := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "issue", "update", "AEON-1", "--estimate", "2", "--role", " build ", "--area", "backend", "--complexity", "M"}
	code, out, errOut = runCLI(args, "")
	if code != 0 || errOut != "" {
		t.Fatalf("update: %d %s %s", code, out, errOut)
	}
	if wrote["route_role"] != "build" || wrote["area"] != "backend" || wrote["priority"] != "high" || wrote["estimate_hours"] != float64(2) || wrote["complexity"] != "M" {
		t.Fatal(wrote)
	}
	for _, key := range []string{"route_role_source", "route_role_by", "route_role_at", "area_source", "area_by", "area_at", "complexity_source", "complexity_by", "complexity_at"} {
		if wrote[key] != nil {
			t.Fatalf("client sent %s: %#v", key, wrote)
		}
	}
	var view issueView
	if err := json.Unmarshal([]byte(out), &view); err != nil || view.RouteRole != "build" || view.Area != "backend" || view.Complexity != "M" {
		t.Fatalf("json %s %v", out, err)
	}
	for _, area := range []string{"security", "firmware"} {
		code, _, errOut = runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "issue", "update", "AEON-1", "--area", area}, "")
		if code != 0 || errOut != "" || wrote["area"] != area {
			t.Fatalf("dynamic area %s was not forwarded: %d %s %#v", area, code, errOut, wrote)
		}
	}
	code, _, errOut = runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "issue", "update", "AEON-2", "--role", "scout"}, "")
	if code != 2 || !strings.Contains(errOut, "tickets and tasks") || wrote["route_role"] != "build" {
		t.Fatalf("epic: %d %s %#v", code, errOut, wrote)
	}
}

func TestIssueRouteRepeatKeepsPersonProvenance(t *testing.T) {
	isolate(t)
	const (
		personID = "11111111-1111-4111-8111-111111111111"
		roleAt   = "2026-09-29T08:00:00Z"
	)
	revision := time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC)
	ticket := apiNode{
		ID: transcriptEntryID, Key: "AEON-1", KindID: "ticket-kind", Title: "Route", State: "open", UpdatedAt: revision,
		Fields: json.RawMessage(`{"priority":"high","route_role":"build","route_role_source":"person","route_role_by":"` + personID + `","route_role_at":"` + roleAt + `","area":"backend","area_source":"person","area_by":"` + personID + `","area_at":"` + roleAt + `"}`),
	}
	var wrote map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			_ = json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "ticket-kind", Slug: "ticket"}, {ID: "task-kind", Slug: "task"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(nodePage{Items: []apiNode{ticket}})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/nodes/"+ticket.ID:
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
	args := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "issue", "update", "AEON-1", "--role", "build"}
	code, out, errOut := runCLI(args, "")
	if code != 0 || errOut != "" {
		t.Fatalf("repeat: %d %s %s", code, out, errOut)
	}
	if wrote["route_role"] != "build" || wrote["route_role_source"] != "person" || wrote["route_role_by"] != personID || wrote["route_role_at"] != roleAt {
		t.Fatalf("repeat stripped person provenance: %#v", wrote)
	}
	if wrote["area_source"] != "person" || wrote["area_by"] != personID || wrote["area_at"] != roleAt {
		t.Fatalf("repeat touched area provenance: %#v", wrote)
	}
	var view issueView
	if err := json.Unmarshal([]byte(out), &view); err != nil || view.RouteRole != "build" || view.RouteRoleSource != "person" || view.RouteRoleBy != personID || view.RouteRoleAt != roleAt {
		t.Fatalf("repeat json %s %v", out, err)
	}

	code, out, errOut = runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "issue", "update", "AEON-1", "--role", "mechanical"}, "")
	if code != 0 || errOut != "" {
		t.Fatalf("change: %d %s %s", code, out, errOut)
	}
	if wrote["route_role"] != "mechanical" {
		t.Fatalf("change role: %#v", wrote)
	}
	for _, key := range []string{"route_role_source", "route_role_by", "route_role_at"} {
		if _, ok := wrote[key]; ok {
			t.Fatalf("change kept %s so the server cannot restamp: %#v", key, wrote)
		}
	}
	if wrote["area"] != "backend" || wrote["area_source"] != "person" || wrote["area_by"] != personID || wrote["area_at"] != roleAt {
		t.Fatalf("role change restamped area: %#v", wrote)
	}
}

func TestIssueSuggestionConfirmationDropsOnlyRequestedStamps(t *testing.T) {
	fields := map[string]any{"route_role": "build", "route_role_source": "suggested", "route_role_by": "agent", "route_role_at": "now", "route_role_confirmed": false, "area": "backend", "area_source": "suggested", "complexity": "S", "complexity_source": "suggested"}
	applyRouteFields(fields, "build", "", "")
	if fields["route_role_source"] != nil || fields["route_role_confirmed"] != nil || fields["area_source"] != "suggested" || fields["complexity_source"] != "suggested" {
		t.Fatal(fields)
	}
	rt := &runtime{}
	v := rt.viewIssue(apiNode{KindID: "ticket", Fields: json.RawMessage(`{"complexity":"S","complexity_source":"suggested","complexity_confirmed":false}`)}, kindTable{})
	if v.Complexity != "S" || v.ComplexityConfirmed == nil || *v.ComplexityConfirmed {
		t.Fatal(v)
	}
}
