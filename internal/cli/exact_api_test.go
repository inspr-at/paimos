// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func exactRuntime(t *testing.T, handler http.HandlerFunc) *runtime {
	t.Helper()
	isolate(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	return &runtime{program: "aeon", configPath: filepath.Join(t.TempDir(), "missing"), stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
}

func TestExactAPIFieldWrites(t *testing.T) {
	for _, operation := range []string{"priority", "metadata", "apply"} {
		t.Run(operation, func(t *testing.T) {
			var written map[string]any
			rt := exactRuntime(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				kind := "ticket-kind"
				if operation == "metadata" {
					kind = "memory-kind"
				}
				node := fmt.Sprintf(`{"id":%q,"key":"AEON-1","kind_id":%q,"updated_at":"2026-10-02T12:00:00Z","fields":{"slug":"note","priority":"low","large":9007199254740993,"nested":[{"n":9007199254740995}],"estimate_hours":2}}`, transcriptEntryID, kind)
				switch {
				case r.Method == "PATCH":
					d := json.NewDecoder(r.Body)
					d.UseNumber()
					if err := d.Decode(&written); err != nil {
						t.Error(err)
					}
					fmt.Fprint(w, node)
				case r.URL.Path == "/api/kinds":
					fmt.Fprint(w, `{"items":[{"id":"project-kind","slug":"project"},{"id":"ticket-kind","slug":"ticket"},{"id":"memory-kind","slug":"memory"}]}`)
				case r.URL.Query().Get("kind_id") == "project-kind":
					fmt.Fprintf(w, `{"items":[{"id":%q,"key":"PRJ-1","kind_id":"project-kind","fields":{"project_key":"AEON"}}]}`, transcriptProjectID)
				default:
					fmt.Fprintf(w, `{"items":[%s]}`, node)
				}
			})
			var err error
			switch operation {
			case "priority":
				code, _, stderr := runCLI([]string{"aeon", "--config", rt.configPath, "issue", "update", "AEON-1", "--priority", "high"}, "")
				if code != 0 {
					t.Fatal(stderr)
				}
			case "metadata":
				err = rt.updateKnowledge("memory", "note", "AEON", "", "", "", "", `{"id":9007199254740997}`)
			case "apply":
				rt.stdin = strings.NewReader("update:\n  - ref: AEON-1\n    fields:\n      priority: high\n")
				_, _, stderr := runCLI([]string{"aeon", "--config", rt.configPath, "apply", "--from-file", "-"}, "update:\n  - ref: AEON-1\n    fields:\n      priority: high\n")
				if stderr != "" {
					t.Fatal(stderr)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			fields, ok := written["fields"].(map[string]any)
			if !ok || fields["large"] != json.Number("9007199254740993") || fields["nested"].([]any)[0].(map[string]any)["n"] != json.Number("9007199254740995") {
				t.Fatalf("integer precision lost: %v", fields)
			}
			if operation == "metadata" && fields["metadata"].(map[string]any)["id"] != json.Number("9007199254740997") {
				t.Fatal("metadata rounded")
			}
		})
	}
}

func TestExactAPINodeWalkRejectsIncompleteAndRepeatedCursors(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t.Run(fmt.Sprint(repeat), func(t *testing.T) {
			calls := 0
			rt := exactRuntime(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				next := fmt.Sprint(calls)
				if repeat {
					next = "same"
				}
				fmt.Fprintf(w, `{"items":[],"next_cursor":%q}`, next)
			})
			if _, err := rt.walkNodes(nil, nil); err == nil {
				t.Fatal("incomplete walk reported success")
			}
			if repeat && calls > 2 {
				t.Fatalf("repeated cursor not detected: %d requests", calls)
			}
		})
	}
}

func TestExactAPIOnboardChecksRenderedContentAndOptions(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "memory", "note", &calls)
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	path := filepath.Join(t.TempDir(), "onboard.txt")
	args := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "onboard", "--project", "AEON", "--out", path}
	for _, change := range []string{"body", "header", "timestamp", "--include-low", "--reading-list-size", "--format"} {
		t.Run(change, func(t *testing.T) {
			if code, _, stderr := runCLI(args, ""); code != 0 {
				t.Fatal(stderr)
			}
			check := append(append([]string{}, args...), "--check")
			switch change {
			case "body", "header", "timestamp":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if change == "body" {
					raw = append(raw, []byte("tampered\n")...)
				} else if change == "timestamp" {
					line, rest, _ := strings.Cut(string(raw), "\n")
					at := strings.LastIndex(line, " at ")
					raw = []byte(line[:at] + " at -->\n" + rest)
				} else {
					raw = bytes.Replace(raw, []byte(" at "), []byte(" [agent=forged] at "), 1)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "--include-low":
				check = append(check, change)
			case "--reading-list-size":
				check = append(check, change, "1")
			case "--format":
				check = append(check, change, "html")
			}
			if code, out, _ := runCLI(check, ""); code == 0 || strings.Contains(out, "identical") {
				t.Fatalf("changed bundle reported identical: %s", change)
			}
		})
	}
}

func TestExactAPIScopedSearchIncludesNestedLaterPages(t *testing.T) {
	parent := "33333333-3333-4333-8333-333333333333"
	rt := exactRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/kinds":
			fmt.Fprint(w, `{"items":[{"id":"project-kind","slug":"project"},{"id":"ticket-kind","slug":"ticket"},{"id":"task-kind","slug":"task"}]}`)
		case "/api/nodes":
			fmt.Fprintf(w, `{"items":[{"id":%q,"key":"PRJ-1","kind_id":"project-kind","fields":{"project_key":"AEON"}}]}`, transcriptProjectID)
		case "/api/nodes/" + parent:
			fmt.Fprintf(w, `{"id":%q,"parent_id":%q}`, parent, transcriptProjectID)
		case "/api/search":
			if r.URL.Query().Get("cursor") == "" {
				fmt.Fprint(w, `{"items":[{"node":{"id":"other","key":"OTHER-1","kind_id":"ticket-kind"}}],"next_cursor":"next"}`)
			} else {
				fmt.Fprintf(w, `{"items":[{"node":{"id":"target","key":"AEON-2","kind_id":"task-kind","parent_id":%q}}]}`, parent)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	rt.jsonOut = true
	if err := rt.searchIssues("target", "AEON", "", 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rt.stdout.(*bytes.Buffer).String(), "AEON-2") {
		t.Fatal("nested match after unrelated first page was lost")
	}
}

func TestExactAPIScopedSearchHasMoreRequiresScopedMatch(t *testing.T) {
	for _, extraMatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("extra-match-%t", extraMatch), func(t *testing.T) {
			rt := exactRuntime(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/kinds":
					fmt.Fprint(w, `{"items":[{"id":"project-kind","slug":"project"},{"id":"ticket-kind","slug":"ticket"}]}`)
				case "/api/nodes":
					fmt.Fprintf(w, `{"items":[{"id":%q,"key":"PRJ-1","kind_id":"project-kind","fields":{"project_key":"AEON"}}]}`, transcriptProjectID)
				case "/api/search":
					if r.URL.Query().Get("cursor") == "" {
						fmt.Fprintf(w, `{"items":[{"node":{"id":"target","key":"AEON-1","kind_id":"ticket-kind","parent_id":%q}}],"next_cursor":"next"}`, transcriptProjectID)
					} else if extraMatch {
						fmt.Fprintf(w, `{"items":[{"node":{"id":"second","key":"AEON-2","kind_id":"ticket-kind","parent_id":%q}}]}`, transcriptProjectID)
					} else {
						fmt.Fprint(w, `{"items":[{"node":{"id":"other","key":"OTHER-1","kind_id":"ticket-kind"}}]}`)
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			result, err := rt.searchIssuesResult("target", "AEON", "", 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Issues) != 1 || result.Issues[0].IssueKey != "AEON-1" || result.HasMore != extraMatch {
				t.Fatalf("incorrect scoped continuation: %+v", result)
			}
		})
	}
}

func TestExactAPICommentRetainsCanonicalKey(t *testing.T) {
	rt := exactRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/kinds":
			fmt.Fprint(w, `{"items":[{"id":"ticket-kind","slug":"ticket"}]}`)
		case "/api/nodes":
			fmt.Fprint(w, `{"items":[]}`)
		case "/api/node-keys/OLD-1":
			fmt.Fprint(w, `{"id":"target","key":"AEON-1","kind_id":"ticket-kind"}`)
		case "/api/nodes/target/comments":
			fmt.Fprint(w, `{"id":"comment"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	if err := rt.commentIssue("OLD-1", "Comment"); err != nil {
		t.Fatal(err)
	}
	if output := rt.stdout.(*bytes.Buffer).String(); !strings.Contains(output, "commented on AEON-1") {
		t.Fatalf("comment lost canonical key: %s", output)
	}
}

func TestExactAPIMCPToolsReachHTTP(t *testing.T) {
	var calls []transcriptRequest
	isolate(t)
	srv := transcriptFixture(t, "ticket", "note", &calls)
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	rt := &runtime{program: "aeon", configPath: filepath.Join(t.TempDir(), "missing"), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	left, right := mcp.NewInMemoryTransports()
	ss, err := rt.mcpServer().Connect(t.Context(), left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "dev"}, nil).Connect(t.Context(), right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tc := range []struct {
		name         string
		args         map[string]any
		method, path string
	}{
		{"issue_list", map[string]any{"project": "AEON"}, "GET", "/api/nodes"},
		{"issue_get", map[string]any{"ref": "MEM-1"}, "GET", "/api/nodes/" + transcriptEntryID + "/activity"},
		{"issue_create", map[string]any{"project": "AEON", "title": "Repair", "bug": true}, "POST", "/api/nodes"},
		{"issue_update", map[string]any{"ref": "MEM-1", "title": "Updated"}, "PATCH", "/api/nodes/" + transcriptEntryID},
		{"issue_comment", map[string]any{"ref": "MEM-1", "body": "Comment"}, "POST", "/api/nodes/" + transcriptEntryID + "/comments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls = nil
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatal(toolText(res))
			}
			found := false
			for _, c := range calls {
				found = found || (c.method == tc.method && strings.Split(c.path, "?")[0] == tc.path)
			}
			if !found {
				t.Fatalf("tool never reached %s %s", tc.method, tc.path)
			}
		})
	}
}

func TestExactAPIMCPKnowledgeToolsReachHTTP(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "memory", "note", &calls)
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	rt := &runtime{program: "aeon", configPath: filepath.Join(t.TempDir(), "missing"), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	left, right := mcp.NewInMemoryTransports()
	ss, err := rt.mcpServer().Connect(t.Context(), left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "dev"}, nil).Connect(t.Context(), right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tc := range []struct {
		name         string
		args         map[string]any
		method, path string
	}{
		{"knowledge_list", map[string]any{"project": "AEON", "type": "memory"}, "GET", "/api/nodes"},
		{"knowledge_get", map[string]any{"project": "AEON", "type": "memory", "slug": "note"}, "GET", "/api/nodes"},
		{"knowledge_create", map[string]any{"project": "AEON", "type": "memory", "slug": "note", "title": "Note"}, "POST", "/api/nodes"},
		{"knowledge_update", map[string]any{"project": "AEON", "type": "memory", "slug": "note", "title": "Changed"}, "PATCH", "/api/nodes/" + transcriptEntryID},
		{"search", map[string]any{"query": "note"}, "GET", "/api/search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls = nil
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatal(toolText(res))
			}
			found := false
			for _, c := range calls {
				found = found || (c.method == tc.method && strings.Split(c.path, "?")[0] == tc.path)
			}
			if !found {
				t.Fatal("tool did not reach HTTP")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls = nil
	if _, _, err := rt.toolIssueUpdate(ctx, nil, issueUpdateArgs{Ref: "MEM-1", Title: "Late"}); err == nil || len(calls) != 0 {
		t.Fatal("cancelled mutation reached HTTP")
	}
}

func TestExactAPISearchRejectsCyclingCursor(t *testing.T) {
	calls := 0
	rt := exactRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/kinds" {
			fmt.Fprint(w, `{"items":[{"id":"ticket-kind","slug":"ticket"}]}`)
			return
		}
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"items":[],"next_cursor":"same"}`)
		} else {
			fmt.Fprint(w, `{"items":[{"node":{"id":"duplicate","kind_id":"ticket-kind"}}],"next_cursor":"same"}`)
		}
	})
	if _, err := rt.searchIssuesResult("text", "", "", 1); err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatal("cycling search reported a successful result")
	}
}
