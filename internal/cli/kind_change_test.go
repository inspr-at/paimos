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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIssueUpdateRefusesKindChange(t *testing.T) {
	isolate(t)
	revision := time.Date(2026, 9, 29, 10, 0, 0, 123456000, time.UTC)
	ticket := apiNode{ID: "11111111-1111-4111-8111-111111111111", Key: "AEON-1", KindID: "ticket-kind", Title: "Stay", UpdatedAt: revision, State: "open"}
	patches := 0
	var lastPatch map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{
				{ID: "ticket-kind", Slug: "ticket"},
				{ID: "epic-kind", Slug: "epic"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{ticket}})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/nodes/"+ticket.ID:
			patches++
			json.NewDecoder(r.Body).Decode(&lastPatch)
			if title, ok := lastPatch["title"].(string); ok {
				ticket.Title = title
			}
			json.NewEncoder(w).Encode(ticket)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}

	code, out, stderr := runCLI(append(args, "issue", "update", "AEON-1", "--type", "epic", "--title", "Changed"), "")
	want := `kind is immutable here: ticket → epic; use "aeon issue convert AEON-1 --to epic"`
	if code == 0 || !strings.Contains(stderr, want) || patches != 0 || ticket.Title != "Stay" {
		t.Fatalf("different kind: exit %d stdout %q stderr %q patches %d title %q", code, out, stderr, patches, ticket.Title)
	}

	code, out, stderr = runCLI(append(args, "issue", "update", "AEON-1", "--type", "ticket"), "")
	if code != 0 || !strings.Contains(out, "kind is already ticket") || patches != 0 || stderr != "" {
		t.Fatalf("same kind: exit %d stdout %q stderr %q patches %d", code, out, stderr, patches)
	}

	code, out, stderr = runCLI(append(args, "issue", "update", "AEON-1", "--type", "ticket", "--title", "Renamed"), "")
	if code != 0 || !strings.Contains(out, "kind is already ticket") || !strings.Contains(out, "✓ updated AEON-1") || patches != 1 {
		t.Fatalf("same kind plus title: exit %d stdout %q stderr %q patches %d", code, out, stderr, patches)
	}
	if _, ok := lastPatch["title"]; !ok || lastPatch["kind"] != nil || lastPatch["type"] != nil || lastPatch["kind_id"] != nil {
		t.Fatalf("patch %v", lastPatch)
	}
	if ticket.Title != "Renamed" {
		t.Fatalf("title %q", ticket.Title)
	}

	code, _, stderr = runCLI(append(args, "issue", "update", "AEON-1", "--type", "epic", "--dry-run"), "")
	if code == 0 || !strings.Contains(stderr, want) || patches != 1 {
		t.Fatalf("dry-run kind change: exit %d stderr %q patches %d", code, stderr, patches)
	}
}

func TestMCPIssueUpdateRefusesKind(t *testing.T) {
	isolate(t)
	srv := meServer(t, testKey)
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("default_instance: dev\ninstances:\n  dev:\n    url: "+srv.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "dev"), []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := &runtime{
		program:    "aeon",
		stdin:      strings.NewReader(""),
		stdout:     &strings.Builder{},
		stderr:     &strings.Builder{},
		configPath: cfg,
	}
	server := rt.mcpServer()
	ctx := t.Context()
	left, right := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, left, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "dev"}, nil).Connect(ctx, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for _, args := range []map[string]any{{"ref": "AEON-1", "type": "epic"}, {"ref": "AEON-1", "kind": "ticket", "title": "x"}} {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "issue_update", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || !strings.Contains(toolText(res), "kind_change_not_allowed") {
			t.Fatalf("args %v text %q", args, toolText(res))
		}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "issue_update", Arguments: map[string]any{"ref": "AEON-1", "title": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(toolText(res), "issue_update arrives in R1") || strings.Contains(toolText(res), "kind_change_not_allowed") {
		t.Fatalf("plain update text %q", toolText(res))
	}
}
