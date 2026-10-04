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

	"github.com/inspr-at/paimos/internal/fieldschema"
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
	result, _, err := rt.toolIssueCreate(context.Background(), nil, issueCreateArgs{Project: "AEON", Title: "MCP work", Type: "task"})
	if err != nil || result.IsError || !strings.Contains(strings.ReplaceAll(toolText(result), " ", ""), `"level_name":"Step"`) {
		t.Fatalf("MCP create: %v %+v", err, result)
	}
	result, _, err = rt.toolIssueList(context.Background(), nil, issueListArgs{Project: "AEON", Type: "epic"})
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
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := rt.toolIssueCreate(ctx, nil, issueCreateArgs{Project: "AEON", Title: "Cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create: %v", err)
	}
	for _, n := range []int{-1, 10001} {
		if err := rt.listIssues("", "", "", "", "", n, 0); err == nil || !strings.Contains(err.Error(), "between 0 and 10000") {
			t.Fatalf("limit %d: %v", n, err)
		}
		if err := rt.listIssues("", "", "", "", "", 1, n); err == nil || !strings.Contains(err.Error(), "between 0 and 10000") {
			t.Fatalf("offset %d: %v", n, err)
		}
	}
}
