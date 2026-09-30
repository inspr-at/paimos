// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
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
	want := `kind_change_not_allowed: ticket → epic; use "aeon issue convert AEON-1 --to epic"`
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

func TestIssueConvert(t *testing.T) {
	isolate(t)
	code, out, stderr := runCLI([]string{"aeon", "issue", "convert", "-h"}, "")
	if code != 0 || !strings.Contains(out, "issue convert <ref> --to <kind>") || !strings.Contains(out, "person session") {
		t.Fatalf("help: exit %d stdout %q stderr %q", code, out, stderr)
	}
	code, _, stderr = runCLI([]string{"aeon", "issue", "convert", "AEON-1"}, "")
	if code != 2 || !strings.Contains(stderr, "--to is required") {
		t.Fatalf("missing --to: exit %d stderr %q", code, stderr)
	}

	revision := time.Date(2026, 9, 29, 10, 0, 0, 123456000, time.UTC)
	ticket := apiNode{ID: "11111111-1111-4111-8111-111111111111", Key: "AEON-1", KindID: "ticket-kind", Title: "Stay", UpdatedAt: revision, State: "open"}
	var posted map[string]any
	var convertAuth string
	posts := 0
	meKind := "agent"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/me":
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"principal":{"id":"p","tenant_id":"t","kind":%q,"name":"caller","roles":["admin"]},"tenant":{"id":"t","slug":"aeon","name":"Aeon"}}`, meKind)
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{
				{ID: "ticket-kind", Slug: "ticket"},
				{ID: "epic-kind", Slug: "epic"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{ticket}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/nodes/"+ticket.ID+"/convert":
			posts++
			convertAuth = r.Header.Get("Authorization") + " cookie:" + r.Header.Get("Cookie")
			json.NewDecoder(r.Body).Decode(&posted)
			if !strings.Contains(r.Header.Get("Cookie"), "aeon_session=") && meKind != "person" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"permission denied"}`))
				return
			}
			ticket.KindID = "epic-kind"
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
	code, out, stderr = runCLI(append(args, "issue", "convert", "AEON-1", "--to", "ticket"), "")
	if code != 0 || !strings.Contains(out, "kind is already ticket") || posts != 0 {
		t.Fatalf("same kind: exit %d stdout %q stderr %q posts %d", code, out, stderr, posts)
	}
	code, out, stderr = runCLI(append(args, "issue", "convert", "AEON-1", "--to", "epic"), "")
	link := srv.URL + "/p/AEON/AEON-1"
	want := "Converting a kind needs a person. Open " + link + ", then use ⋯ → Convert to…"
	if code == 0 || !strings.Contains(stderr, want) || posts != 0 || strings.Contains(stderr, testKey) {
		t.Fatalf("agent key: exit %d stdout %q stderr %q posts %d", code, out, stderr, posts)
	}

	meKind = "person"
	code, out, stderr = runCLI(append(args, "issue", "convert", "AEON-1", "--to", "epic"), "")
	if code != 0 || !strings.Contains(out, "✓ AEON-1: ticket → epic") || posts != 1 || posted["to_kind"] != "epic" {
		t.Fatalf("person caller: exit %d stdout %q stderr %q posts %d body %v auth %q", code, out, stderr, posts, posted, convertAuth)
	}

	ticket.KindID = "ticket-kind"
	posts = 0
	posted = nil
	meKind = "agent"
	session := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(session, []byte("person-session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runCLI(append(args, "issue", "convert", "AEON-1", "--to", "epic", "--session-file", session), "")
	if code != 0 || !strings.Contains(out, "✓ AEON-1: ticket → epic") || posts != 1 || posted["to_kind"] != "epic" || !strings.Contains(convertAuth, "aeon_session=person-session") || strings.Contains(out+stderr, "person-session") {
		t.Fatalf("person session: exit %d stdout %q stderr %q posts %d body %v auth %q", code, out, stderr, posts, posted, convertAuth)
	}
}

func TestKindChangeAcrossSurfaces(t *testing.T) {
	isolate(t)
	ticket := apiNode{ID: "11111111-1111-4111-8111-111111111111", Key: "AEON-1", KindID: "ticket-kind", Title: "Stay", State: "open"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/me":
			_, _ = w.Write([]byte(`{"principal":{"id":"p","tenant_id":"t","kind":"agent","name":"cursor-grok","roles":["admin"]},"tenant":{"id":"t","slug":"aeon","name":"Aeon"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "ticket-kind", Slug: "ticket"}, {ID: "epic-kind", Slug: "epic"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{ticket}})
		default:
			http.NotFound(w, r)
		}
	}))
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
	cli := []string{"aeon", "--config", cfg}
	code, out, stderr := runCLI(append(cli, "issue", "update", "AEON-1", "--type", "epic"), "")
	if code == 0 || !strings.Contains(stderr, "kind_change_not_allowed") || !strings.Contains(stderr, `aeon issue convert AEON-1 --to epic`) {
		t.Fatalf("cli different: exit %d stdout %q stderr %q", code, out, stderr)
	}
	code, out, stderr = runCLI(append(cli, "issue", "update", "AEON-1", "--type", "ticket"), "")
	if code != 0 || !strings.Contains(out, "kind is already ticket") || stderr != "" {
		t.Fatalf("cli same: exit %d stdout %q stderr %q", code, out, stderr)
	}

	rt := &runtime{program: "aeon", stdin: strings.NewReader(""), stdout: &strings.Builder{}, stderr: &strings.Builder{}, configPath: cfg}
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

	cases := []struct {
		name    string
		args    map[string]any
		refused bool
		noop    bool
		plain   bool
		want    string
	}{
		{name: "mcp different type", args: map[string]any{"ref": "AEON-1", "type": "epic"}, refused: true},
		{name: "mcp different kind", args: map[string]any{"ref": "AEON-1", "kind": "epic", "title": "x"}, refused: true},
		{name: "mcp same kind", args: map[string]any{"ref": "AEON-1", "type": "ticket"}, noop: true},
		{name: "mcp same kind plus title", args: map[string]any{"ref": "AEON-1", "kind": "ticket", "title": "x"}, plain: true},
		{name: "mcp no kind", args: map[string]any{"ref": "AEON-1", "title": "x"}, plain: true},
		{name: "mcp disagree", args: map[string]any{"ref": "AEON-1", "type": "epic", "kind": "ticket"}, want: "kind and type disagree"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "issue_update", Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			text := toolText(res)
			if tc.want != "" {
				if !res.IsError || !strings.Contains(text, tc.want) || strings.Contains(text, "kind_change_not_allowed") {
					t.Fatalf("text %q", text)
				}
				return
			}
			switch {
			case tc.refused:
				if !res.IsError || !strings.Contains(text, "kind_change_not_allowed") {
					t.Fatalf("text %q", text)
				}
			case tc.noop:
				if res.IsError || !strings.Contains(text, "kind is already ticket") || strings.Contains(text, "kind_change_not_allowed") {
					t.Fatalf("error %v text %q", res.IsError, text)
				}
			case tc.plain:
				if !res.IsError || !strings.Contains(text, "issue_update arrives in R1") || strings.Contains(text, "kind_change_not_allowed") {
					t.Fatalf("text %q", text)
				}
			}
		})
	}
}
