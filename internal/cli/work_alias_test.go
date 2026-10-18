// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/fieldschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkTypeAliasesUseCanonicalIDAndExposeShape(t *testing.T) {
	isolate(t)
	schema, err := fieldschema.Compile(json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	leaf := true
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "pk", Slug: "project"}, {ID: "wk", Slug: "work"}}})
		case r.Method == "GET" && r.URL.Path == "/api/nodes":
			if r.URL.Query().Get("kind_id") == "pk" {
				json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptProjectID, KindID: "pk", Fields: json.RawMessage(`{"project_key":"AEON"}`)}}})
				return
			}
			if r.URL.Query().Get("kind_id") != "wk" {
				t.Errorf("list alias: %s", r.URL)
			}
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptEntryID, Key: "AEON-655", KindID: "wk", Title: "Work", IsLeaf: &leaf, Depth: 2, LevelName: "Step", Fields: json.RawMessage(`{}`)}}})
		case r.Method == "POST" && r.URL.Path == "/api/nodes":
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if string(body["kind_id"]) != `"wk"` {
				t.Errorf("alias write: %s", body["kind_id"])
			}
			value, err := fieldschema.Decode(body["fields"])
			if err == nil {
				err = schema.Validate(value)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			writes++
			json.NewEncoder(w).Encode(apiNode{ID: transcriptEntryID, Key: "AEON-655", KindID: "wk", Title: "Work", IsLeaf: &leaf, Depth: 2, LevelName: "Step", Fields: body["fields"]})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	for _, alias := range []string{"work", "epic", "ticket", "task", ""} {
		commands := [][]string{{"issue", "create", "--project", "AEON", "--title", "Work", "--type", alias}, {"issue", "list", "--project", "AEON", "--type", alias}}
		if alias == "" {
			commands = [][]string{{"issue", "create", "--project", "AEON", "--title", "Default work"}}
		}
		for _, args := range commands {
			argv := append([]string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json"}, args...)
			code, out, stderr := runCLI(argv, "")
			if code != 0 {
				t.Fatalf("%s %v: %d %s", alias, args, code, stderr)
			}
			for _, want := range []string{`"type":"work"`, `"is_leaf":true`, `"depth":2`, `"level_name":"Step"`} {
				if !strings.Contains(strings.ReplaceAll(out, " ", ""), want) {
					t.Fatalf("%s output missing %s: %s", alias, want, out)
				}
			}
		}
	}
	rt := &runtime{program: "paimos", configPath: filepath.Join(t.TempDir(), "missing"), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	session := workAliasMCPSession(t, rt)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "issue_create", Arguments: issueCreateArgs{Project: "AEON", Title: "MCP work", Type: "task"}})
	if err != nil || result.IsError || !strings.Contains(strings.ReplaceAll(toolText(result), " ", ""), `"level_name":"Step"`) {
		t.Fatalf("MCP create: %v %+v", err, result)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "issue_list", Arguments: issueListArgs{Project: "AEON", Type: "epic"}})
	if err != nil || result.IsError || !strings.Contains(strings.ReplaceAll(toolText(result), " ", ""), `"is_leaf":true`) {
		t.Fatalf("MCP list: %v %+v", err, result)
	}
	if writes != 6 {
		t.Fatalf("writes %d", writes)
	}
}
func TestWorkAliasNeverRemapsNonWorkKinds(t *testing.T) {
	table := kindTable{bySlug: map[string]apiKind{"work": {ID: "w", Slug: "work"}, "project": {ID: "p", Slug: "project"}, "question": {ID: "q", Slug: "question"}}}
	for _, slug := range []string{"project", "question"} {
		k, ok := table.issueKind(slug)
		if !ok || k.Slug != slug {
			t.Fatalf("remapped %s", slug)
		}
	}
	if _, ok := table.issueKind("retired-uuid"); ok {
		t.Fatal("guessed a retired id")
	}
}

func TestWorkMCPCancelAndListBounds(t *testing.T) {
	rt := &runtime{}
	session := workAliasMCPSession(t, rt)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "issue_create", Arguments: issueCreateArgs{Project: "AEON", Title: "Cancelled"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create: %v", err)
	}
	for _, n := range []int{-1, 10001} {
		if err := rt.listIssues("", "", "", "", "", n, 0); !isWorkBoundsUsage(err) {
			t.Fatalf("limit %d: %v", n, err)
		}
		if err := rt.listIssues("", "", "", "", "", 1, n); !isWorkBoundsUsage(err) {
			t.Fatalf("offset %d: %v", n, err)
		}
	}
}

// Risk: a retry through the shared client must retain CLI pagination and must
// never return an earlier page as a complete list when the next page fails.
func TestWorkListRetryRetainsPaginationAndExplicitFailure(t *testing.T) {
	for _, tc := range []struct {
		name               string
		enabled, exhausted bool
		calls              int
	}{
		{"default off", false, false, 1},
		{"continued page", true, false, 3},
		{"later page fails", true, true, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/nodes" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("kind_id") == "pk" {
					json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptProjectID, KindID: "pk", Fields: json.RawMessage(`{"project_key":"AEON"}`)}}})
					return
				}
				calls++
				if calls == 1 || (tc.exhausted && r.URL.Query().Get("cursor") == "next") {
					w.WriteHeader(503)
					json.NewEncoder(w).Encode(map[string]string{"error": "Work aggregates exceeded a resource limit; narrow the scope or retry."})
					return
				}
				key := "AEON-1"
				var next *string
				if r.URL.Query().Get("cursor") == "" {
					cursor := "next"
					next = &cursor
				} else {
					key = "AEON-2"
				}
				json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: key, Key: key, KindID: "wk", Fields: json.RawMessage(`{}`)}}, NextCursor: next})
			}))
			defer srv.Close()
			kinds := kindTable{byID: map[string]apiKind{"wk": {ID: "wk", Slug: "work"}}, bySlug: map[string]apiKind{"project": {ID: "pk", Slug: "project"}}}
			ctx := t.Context()
			if tc.enabled {
				ctx = client.WithWorkReadRetry(ctx)
			}
			rt := &runtime{requestContext: ctx, personClient: client.New(srv.URL, "fixture"), kinds: &kinds}
			result, err := rt.listIssuesResult("AEON", "", "", "", "", 50, 0)
			if calls != tc.calls {
				t.Fatalf("list attempts=%d, want %d", calls, tc.calls)
			}
			if !tc.enabled || tc.exhausted {
				if err == nil || !strings.Contains(err.Error(), "api 503: Work aggregates") || result.Total != 0 || len(result.Issues) != 0 {
					t.Fatalf("partial list claimed success: %v %+v", err, result)
				}
			} else if err != nil || result.Total != 2 || len(result.Issues) != 2 || result.Issues[0].IssueKey != "AEON-1" || result.Issues[1].IssueKey != "AEON-2" {
				t.Fatalf("retry changed pagination: %v %+v", err, result)
			}
		})
	}
}

// Exercise the registered MCP tools so aliases share the production adapter.
func workAliasMCPSession(t *testing.T, rt *runtime) *mcp.ClientSession {
	t.Helper()
	left, right := mcp.NewInMemoryTransports()
	server, err := rt.mcpServer().Connect(t.Context(), left, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "work-alias-test", Version: "dev"}, nil).Connect(t.Context(), right, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func isWorkBoundsUsage(err error) bool {
	var usage *exitError
	return errors.As(err, &usage) && usage.code == 2 && strings.Contains(usage.Error(), "offset") && strings.Contains(usage.Error(), "limit") && strings.Contains(usage.Error(), "10000")
}
