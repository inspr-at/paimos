// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const releaseProjectTest = "11111111-1111-4111-8111-111111111111"
const releaseIDTest = "22222222-2222-4222-8222-222222222222"
const releaseItemTest = "33333333-3333-4333-8333-333333333333"

func releaseConfig(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if e := os.WriteFile(cfg, []byte("default_instance: dev\ninstances:\n  dev:\n    url: "+url+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(dir, "keys"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "keys", "dev"), []byte(testKey+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return cfg
}
func TestReleaseCLIUsesBoundPathsAndCapturedRevisions(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
		query, body  map[string]any
	}{
		{[]string{"list", "--limit", "2"}, "GET", "/api/projects/" + releaseProjectTest + "/releases", map[string]any{"limit": "2"}, nil},
		{[]string{"items", releaseIDTest, "--through", releaseIDTest}, "GET", "/api/projects/" + releaseProjectTest + "/releases/" + releaseIDTest + "/items", map[string]any{"through": releaseIDTest}, nil},
		{[]string{"backlog", "--completed-unplaced"}, "GET", "/api/projects/" + releaseProjectTest + "/backlog", map[string]any{"completed_unplaced": "1"}, nil},
		{[]string{"settings", "--defaults", "--expected-revision", "9", "--build-settings", `{"max_agents":2}`}, "PATCH", "/api/projects/" + releaseProjectTest + "/delivery", nil, map[string]any{"expected_revision": float64(9)}},
		{[]string{"cut", releaseIDTest, "--expected-revision", "7", "--version-scheme", "legacy", "--version", "1.2.3-rc.1"}, "POST", "/api/projects/" + releaseProjectTest + "/releases/" + releaseIDTest + "/cut", nil, map[string]any{"expected_revision": float64(7), "version": "1.2.3-rc.1", "version_scheme": "legacy"}},
		{[]string{"place", releaseItemTest, "--release", releaseIDTest, "--expected-revision", "0", "--expected-release-revision", "4"}, "PUT", "/api/nodes/" + releaseItemTest + "/ships-in", nil, map[string]any{"expected_project_id": releaseProjectTest, "expected_revision": float64(0), "expected_release_revision": float64(4), "release_id": releaseIDTest}},
	} {
		t.Run(tc.args[0]+tc.method, func(t *testing.T) {
			isolate(t)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				for k, v := range tc.query {
					if r.URL.Query().Get(k) != v {
						t.Errorf("query %s=%s", k, r.URL.Query().Get(k))
					}
				}
				if tc.body != nil {
					var in map[string]any
					if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
						t.Error(e)
					}
					for k, v := range tc.body {
						if in[k] != v {
							t.Errorf("body %s=%v want %v", k, in[k], v)
						}
					}
					if _, ok := in["expedite"]; ok {
						t.Error("omitted flag was sent")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"items":[]}`))
			}))
			defer srv.Close()
			cfg := releaseConfig(t, srv.URL)
			args := append([]string{"aeon", "release"}, tc.args...)
			args = append(args, "--project", releaseProjectTest, "--config", cfg)
			code, out, err := runCLI(args, "")
			if code != 0 || calls != 1 {
				t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, out, err)
			}
		})
	}
}
func TestReleaseMCPRegistersSixToolsAndTransportParity(t *testing.T) {
	isolate(t)
	requests := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	rt := &runtime{program: "aeon", stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, configPath: releaseConfig(t, srv.URL)}
	server := rt.mcpServer()
	ctx := t.Context()
	left, right := mcp.NewInMemoryTransports()
	ss, e := server.Connect(ctx, left, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	session, e := mcp.NewClient(&mcp.Implementation{Name: "release-test", Version: "dev"}, nil).Connect(ctx, right, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	listed, e := session.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, tc := range []struct {
		name, path string
		args       map[string]any
	}{
		{"release_list", "GET /api/projects/" + releaseProjectTest + "/releases", map[string]any{"project_id": releaseProjectTest}},
		{"release_get", "GET /api/projects/" + releaseProjectTest + "/releases/" + releaseIDTest, map[string]any{"project_id": releaseProjectTest, "release_id": releaseIDTest}},
		{"release_items", "GET /api/projects/" + releaseProjectTest + "/releases/" + releaseIDTest + "/items", map[string]any{"project_id": releaseProjectTest, "release_id": releaseIDTest}},
		{"backlog_list", "GET /api/projects/" + releaseProjectTest + "/backlog", map[string]any{"project_id": releaseProjectTest}},
		{"ships_in_place", "PUT /api/nodes/" + releaseItemTest + "/ships-in", map[string]any{"item_id": releaseItemTest, "expected_project_id": releaseProjectTest, "release_id": nil, "expected_revision": 0}},
		{"release_build_status", "GET /api/projects/" + releaseProjectTest + "/releases/" + releaseIDTest, map[string]any{"project_id": releaseProjectTest, "release_id": releaseIDTest}},
	} {
		if !names[tc.name] {
			t.Fatalf("missing %s", tc.name)
		}
		res, e := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if e != nil || res.IsError {
			t.Fatalf("%s: %v %+v", tc.name, e, res)
		}
		if got := <-requests; got != tc.path {
			t.Fatalf("%s routed to %s", tc.name, got)
		}
	}
	_, _, e = rt.toolShipsInPlace(context.Background(), nil, shipsInPlaceArgs{ItemID: releaseItemTest, ProjectID: releaseProjectTest, ReleaseID: &[]string{releaseIDTest}[0]})
	if e == nil {
		t.Fatal("missing destination revision passed MCP validation")
	}
}
