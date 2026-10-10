// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
)

// Doctrine inventory (read-only grep of paimos/doctrine, paimos/AGENTS.md and
// ~/.claude/skills, 2026-09-25): auth login/whoami; model resolve; onboard;
// issue create/get/list/update/comment/search/move; knowledge list/get/create/
// update; project list; session start; anchors scan/verify; skill render;
// sync check; harness register/list/status/orchestrator/bind/heartbeat/yield/
// drain/complete-delivery/interrupt/stop/complete-control/mark-stopped/run-heartbeat;
// run-agent watch; baseline-batch report-built; tell/listen/message (CP1);
// runtime setup (historical integration); manifest pull (removed in PAI-358).
// Messaging and intercom are deliberately exercised in their owning package.

const (
	transcriptProjectID = "11111111-1111-4111-8111-111111111111"
	transcriptEntryID   = "22222222-2222-4222-8222-222222222222"
	transcriptSessionID = "33333333-3333-4333-8333-333333333333"
)

type transcriptRequest struct {
	method string
	path   string
	body   map[string]any
}

func transcriptFixture(t *testing.T, kind, slug string, calls *[]transcriptRequest) *httptest.Server {
	t.Helper()
	ids := map[string]string{"project": "project-kind", "ticket": "ticket-kind", "memory": "memory-kind", "runbook": "runbook-kind", "guideline": "guideline-kind", "external_system": "external-kind", "related_project": "related-kind"}
	project := map[string]any{"id": transcriptProjectID, "key": "PRJ-1", "kind_id": ids["project"], "title": "AEON", "body": "Agent workspace", "state": "active", "fields": map[string]any{"project_key": "AEON"}}
	entry := map[string]any{"id": transcriptEntryID, "key": "MEM-1", "kind_id": ids[kind], "parent_id": transcriptProjectID, "title": "Note", "body": "remember", "state": "active", "fields": map[string]any{"slug": slug, "classic": map[string]any{"id": 7}}, "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z"}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		*calls = append(*calls, transcriptRequest{r.Method, r.URL.String(), body})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/kinds":
			items := []map[string]string{}
			for _, name := range []string{"project", "ticket", "memory", "runbook", "guideline", "external_system", "related_project"} {
				items = append(items, map[string]string{"id": ids[name], "slug": name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == "GET" && r.URL.Path == "/api/nodes" && r.URL.Query().Get("kind_id") == ids["project"]:
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{project}})
		case r.Method == "GET" && r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{entry}})
		case r.Method == "GET" && r.URL.Path == "/api/search":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"node": entry}}, "next_cursor": nil})
		case r.Method == "POST" && r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(entry)
		case r.Method == "PATCH" && r.URL.Path == "/api/nodes/"+transcriptEntryID:
			_ = json.NewEncoder(w).Encode(entry)
		case r.Method == "POST" && r.URL.Path == "/api/nodes/"+transcriptEntryID+"/comments":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "1", "type": "comment", "body_markdown": body["body_markdown"]})
		case r.Method == "GET" && r.URL.Path == "/api/nodes/"+transcriptEntryID+"/activity":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "next_cursor": nil})
		case r.Method == "GET" && r.URL.Path == "/api/projects/"+transcriptProjectID+"/harness-sessions":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": transcriptSessionID}})
		case r.Method == "GET" && r.URL.Path == "/api/projects/"+transcriptProjectID+"/harness-sessions/"+transcriptSessionID:
			_, _ = w.Write([]byte(`{"ok":true,"harness":"codex"}`))
		case r.Method == "GET" && r.URL.Path == "/api/me":
			_, _ = w.Write([]byte(`{"principal":{"id":"44444444-4444-4444-8444-444444444444","name":"worker"}}`))
		case r.Method == "GET" && r.URL.Path == "/api/models/resolve":
			_, _ = w.Write([]byte(`{"role":"review-gate","profile":{"id":"55555555-5555-4555-8555-555555555555","slug":"claude-review","version":"1","family":"anthropic","harness":"claude","model":"fable","effort":"xhigh"},"ladder":[{"profile_id":"55555555-5555-4555-8555-555555555555","selected":true,"skip_reasons":[]}],"command_template":"claude -p --model fable '{prompt}'","owner_required":false,"source":"aeon"}`))
		case r.Method == "GET" && r.URL.Path == "/api/models":
			_, _ = w.Write([]byte(`[{"id":"55555555-5555-4555-8555-555555555555","slug":"claude-review"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/projects/"+transcriptProjectID+"/harness-sessions"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
}

func TestKnowledgeCompatTranscripts(t *testing.T) {
	isolate(t)
	types := map[string]string{"memory": "memory", "runbook": "runbook", "guideline": "guideline", "external-system": "external_system", "related-project": "related_project"}
	for _, typ := range []string{"memory", "runbook", "guideline", "external-system", "related-project"} {
		for _, verb := range []string{"list", "get", "create", "update"} {
			t.Run(typ+"/"+verb, func(t *testing.T) {
				var calls []transcriptRequest
				srv := transcriptFixture(t, types[typ], "note", &calls)
				defer srv.Close()
				t.Setenv("PAIMOS_URL", srv.URL)
				t.Setenv("PAIMOS_API_KEY", testKey)
				args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "knowledge", verb}
				switch verb {
				case "list":
					args = append(args, "--project", "AEON", "--type", typ)
				case "get":
					args = append(args, typ, "note", "--project", "AEON")
				case "create":
					args = append(args, "--project", "AEON", "--type", typ, "--slug", "note", "--title", "Note", "--body", "remember")
				case "update":
					args = append(args, typ, "note", "--project", "AEON", "--title", "Note")
				}
				code, out, stderr := runCLI(args, "")
				if code != 0 || stderr != "" {
					t.Fatalf("argv %q: exit %d, stderr %q", args, code, stderr)
				}
				golden := fmt.Sprintf(`{"id":%q,"project_id":%q,"type":%q,"slug":"note","title":"Note","body":"remember","status":"active","metadata":{},"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","reference_count":0}`, transcriptEntryID, transcriptProjectID, typ)
				if verb == "list" {
					golden = "[" + golden + "]"
				}
				var gotJSON, wantJSON any
				if err := json.Unmarshal([]byte(out), &gotJSON); err != nil {
					t.Fatalf("output %q: %v", out, err)
				}
				if err := json.Unmarshal([]byte(golden), &wantJSON); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotJSON, wantJSON) {
					t.Fatalf("output %s, want %s", out, golden)
				}
				want := []string{"GET /api/kinds", "GET /api/nodes?kind_id=project-kind&limit=200"}
				switch verb {
				case "list", "get", "update":
					want = append(want, "GET /api/nodes?include_descendants=true&kind_id="+map[string]string{"memory": "memory-kind", "runbook": "runbook-kind", "guideline": "guideline-kind", "external_system": "external-kind", "related_project": "related-kind"}[types[typ]]+"&limit=200&parent_id="+transcriptProjectID)
				case "create":
					want = append(want, "POST /api/nodes")
				}
				if verb == "update" {
					want = append(want, "PATCH /api/nodes/"+transcriptEntryID)
				}
				var got []string
				for _, call := range calls {
					got = append(got, call.method+" "+call.path)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("requests %q, want %q", got, want)
				}
				if verb == "create" {
					if calls[2].body["parent_id"] != transcriptProjectID || calls[2].body["title"] != "Note" {
						t.Fatalf("create body %+v", calls[2].body)
					}
				}
			})
		}
	}
}

func TestProjectListCompatTranscript(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "memory", "note", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "project", "list"}
	code, out, stderr := runCLI(args, "")
	if code != 0 || stderr != "" {
		t.Fatalf("argv %q: exit %d, stderr %q", args, code, stderr)
	}
	var got, want any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`[{"id":"`+transcriptProjectID+`","key":"AEON","name":"AEON","description":"Agent workspace","status":"active"}]`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output %s, want %v", out, want)
	}
	paths := []string{}
	for _, call := range calls {
		paths = append(paths, call.method+" "+call.path)
	}
	if !reflect.DeepEqual(paths, []string{"GET /api/kinds", "GET /api/nodes?kind_id=project-kind&limit=200"}) {
		t.Fatalf("requests %v", paths)
	}
}

func TestCompatDefaultConfigPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ program, directory string }{{"paimos", ".paimos"}, {"aeon", ".aeon"}} {
		rt := &runtime{program: tc.program}
		path, err := rt.configFile()
		if err != nil || path != filepath.Join(home, tc.directory, "config.yaml") {
			t.Fatalf("%s config path %q: %v", tc.program, path, err)
		}
	}
}

func TestIssueCommentCompatTranscript(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "ticket", "note", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "issue", "comment", "MEM-1", "--body", "worker marker"}
	code, out, stderr := runCLI(args, "")
	if code != 0 || stderr != "" {
		t.Fatalf("argv %q: exit %d, stderr %q", args, code, stderr)
	}
	if strings.TrimSpace(out) != `{"body_markdown":"worker marker","id":"1","type":"comment"}` {
		t.Fatalf("output %s", out)
	}
	paths := []string{}
	for _, call := range calls {
		paths = append(paths, call.method+" "+call.path)
	}
	want := []string{"GET /api/nodes?limit=200&q=MEM-1", "GET /api/kinds", "POST /api/nodes/" + transcriptEntryID + "/comments"}
	if !reflect.DeepEqual(paths, want) || calls[2].body["body_markdown"] != "worker marker" {
		t.Fatalf("requests %+v, want %v", calls, want)
	}
}

func TestOnboardCompatTranscript(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "guideline", "guide", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	path := filepath.Join(t.TempDir(), "onboarding.md")
	args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "onboard", "--project", "AEON", "--agent", "worker", "--out", path}
	code, out, stderr := runCLI(args, "")
	if code != 0 || stderr != "" || !strings.Contains(out, "wrote "+path) {
		t.Fatalf("render %d %q %q", code, out, stderr)
	}
	if len(calls) != 3 || calls[0].path != "/api/kinds" || !strings.Contains(calls[2].path, "include_descendants=true") {
		t.Fatalf("requests %+v", calls)
	}
	code, out, stderr = runCLI(append(args, "--check"), "")
	if code != 0 || stderr != "" || !strings.Contains(out, "identical") {
		t.Fatalf("check %d %q %q", code, out, stderr)
	}
	code, out, stderr = runCLI(append(args, "--json"), "")
	if code != 0 || stderr != "" {
		t.Fatalf("JSON render %d %q %q", code, out, stderr)
	}
	var result struct {
		Path  string `json:"path"`
		Rev   string `json:"rev"`
		Bytes int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Path != path || result.Rev == "" || result.Bytes == 0 {
		t.Fatalf("JSON render %q: %+v, %v", out, result, err)
	}
}

func TestHarnessCompatTranscripts(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref")
	lease := filepath.Join(dir, "lease")
	if err := os.WriteFile(ref, []byte("local-reference-0000000000000001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lease, []byte("local-lease-00000000000000000000000001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	baseWorker := []string{"--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", lease}
	cases := []struct {
		name, method, path, output string
		args                       []string
	}{
		{"register", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions", `{"ok":true}`, []string{"--project", "AEON", "--agent", "worker", "--harness", "codex", "--host", "local", "--harness-session-file", ref, "--worker-lease-file", lease, "--ticket-id", "7", "--work-shape", "ship", "--model", "gpt-6-sol", "--effort", "xhigh", "--account-label", "Codex Pro", "--harness-version", "1.2.3", "--brief", "AEON-213", "--worktree", "/Code/aeon", "--branch", "tm1.session-metadata"}},
		{"list", "GET", "/api/projects/" + transcriptProjectID + "/harness-sessions", `[{"id":"` + transcriptSessionID + `"}]`, []string{"--project", "AEON"}},
		{"status", "GET", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID, `{"ok":true,"harness":"codex"}`, []string{"--project", "AEON", "--session", transcriptSessionID}},
		{"orchestrator", "GET", "/api/projects/" + transcriptProjectID + "/harness-sessions/orchestrator", `{"ok":true}`, []string{"--project", "AEON"}},
		{"bind", "PATCH", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/binding", `{"ok":true}`, []string{"--project", "AEON", "--session", transcriptSessionID, "--revision", "1", "--work-shape", "unknown"}},
		{"heartbeat", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/heartbeat", `{"ok":true}`, append(append([]string{}, baseWorker...), "--phase", "working", "--activity-kind", "turn_started", "--activity-sequence", "1", "--note", "Running PDF tests", "--model", "gpt-6-sol", "--effort", "xhigh", "--account-label", "Codex Pro", "--harness-version", "1.2.3", "--brief", "AEON-214", "--worktree", "/Code/aeon", "--branch", "tm1.session-metadata", "--commit", "abc1234:Store session setup")},
		{"yield", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/yield", `{"ok":true}`, baseWorker},
		{"drain", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/drain", `{"ok":true}`, baseWorker},
		{"complete-delivery", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/complete-delivery", `{"ok":true}`, append(append([]string{}, baseWorker...), "--delivery-id", transcriptEntryID, "--cursor", "1")},
		{"interrupt", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/controls/interrupt", `{"ok":true}`, []string{"--project", "AEON", "--session", transcriptSessionID}},
		{"stop", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/controls/stop", `{"ok":true}`, []string{"--project", "AEON", "--session", transcriptSessionID}},
		{"complete-control", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/controls/" + transcriptEntryID + "/complete", `{"ok":true}`, append(append([]string{}, baseWorker...), "--control-id", transcriptEntryID)},
		{"mark-stopped", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/stop", `{"ok":true}`, baseWorker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []transcriptRequest
			kind := "memory"
			if tc.name == "register" {
				kind = "ticket"
			}
			srv := transcriptFixture(t, kind, "note", &calls)
			defer srv.Close()
			t.Setenv("PAIMOS_URL", srv.URL)
			t.Setenv("PAIMOS_API_KEY", testKey)
			args := append([]string{"paimos", "--config", filepath.Join(dir, "missing"), "--json", "harness", tc.name}, tc.args...)
			code, out, stderr := runCLI(args, "")
			if code != 0 || stderr != "" {
				t.Fatalf("argv %q: exit %d, stderr %q", args, code, stderr)
			}
			var got, want any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.output), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("output %s, want %s", out, tc.output)
			}
			last := calls[len(calls)-1]
			if last.method != tc.method || last.path != tc.path {
				t.Fatalf("final request %s %s, want %s %s", last.method, last.path, tc.method, tc.path)
			}
			if tc.name == "register" && last.body["ticket_node_id"] != transcriptEntryID {
				t.Fatalf("classic --ticket-id did not resolve: %+v", last.body)
			}
			if tc.name == "register" || tc.name == "heartbeat" {
				if last.body["max_session_file_bytes"] != float64(rules.SessionFileLimit("codex")) {
					t.Fatalf("%s reported %v, want Codex's project_doc_max_bytes", tc.name, last.body["max_session_file_bytes"])
				}
				for key, want := range map[string]string{"model": "gpt-6-sol", "reasoning_effort": "xhigh", "account_label": "Codex Pro", "harness_version": "1.2.3", "worktree": "/Code/aeon", "branch": "tm1.session-metadata"} {
					if last.body[key] != want {
						t.Fatalf("%s: %s = %v, want %s", tc.name, key, last.body[key], want)
					}
				}
			}
			if tc.name == "heartbeat" {
				commits, ok := last.body["commits"].([]any)
				if !ok || len(commits) != 1 || commits[0].(map[string]any)["sha"] != "abc1234" {
					t.Fatalf("heartbeat commits missing: %+v", last.body["commits"])
				}
			}
			if tc.name == "heartbeat" && last.body["activity"] != "busy" {
				t.Fatalf("classic --activity-kind did not map: %+v", last.body)
			}
			if tc.name == "heartbeat" && last.body["activity_note"] != "Running PDF tests" {
				t.Fatal("heartbeat --note did not reach the request")
			}
			if strings.Contains(out+stderr, "local-lease") || strings.Contains(out+stderr, "local-reference") {
				t.Fatal("private harness input appeared in output")
			}
		})
	}
}

func TestHarnessRegistrationFileTranscript(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "ticket", "note", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	path := filepath.Join(t.TempDir(), "registration.json")
	const ref = "local-reference-0000000000000001"
	const lease = "local-lease-00000000000000000000000001"
	if err := os.WriteFile(path, []byte(`{"harness_session_ref":"`+ref+`","worker_lease":"`+lease+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "harness", "register", "--project", "AEON", "--agent", "worker", "--harness", "codex", "--host", "local", "--label", "AC4 hierarchy worker", "--registration-file", path}
	code, out, stderr := runCLI(args, "")
	if code != 0 || strings.TrimSpace(out) != `{"ok":true}` || stderr != "" {
		t.Fatalf("register exit %d out %q stderr %q", code, out, stderr)
	}
	last := calls[len(calls)-1]
	if last.method != "POST" || last.path != "/api/projects/"+transcriptProjectID+"/harness-sessions" || last.body["harness_session_ref"] != ref || last.body["worker_lease"] != lease || last.body["display_label"] != "AC4 hierarchy worker" {
		t.Fatalf("request path/body mismatch: %s %s", last.method, last.path)
	}
	if strings.Contains(out+stderr, ref) || strings.Contains(out+stderr, lease) {
		t.Fatal("registration secret appeared in output")
	}
	stdinArgs := append(append([]string{}, args[:len(args)-1]...), "-")
	code, out, stderr = runCLI(stdinArgs, `{"harness_session_ref":"`+ref+`","worker_lease":"`+lease+`"}`)
	if code != 0 || strings.TrimSpace(out) != `{"ok":true}` || stderr != "" {
		t.Fatalf("stdin registration exit %d out %q stderr %q", code, out, stderr)
	}
	if err := os.WriteFile(path, []byte(`{"worker_lease":"one","worker_lease":"two"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before := len(calls)
	code, out, stderr = runCLI(args, "")
	if code != 2 || len(calls) != before || out != "" || !strings.Contains(stderr, "duplicate") {
		t.Fatalf("malformed register exit %d out %q stderr %q requests %d", code, out, stderr, len(calls))
	}
}

func TestModelResolveCompatTranscript(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "memory", "note", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "model", "resolve", "review-gate", "--author-family", "xai"}
	code, out, stderr := runCLI(args, "")
	if code != 0 || stderr != "" {
		t.Fatalf("resolve exit %d, stderr %q", code, stderr)
	}
	wantPaths := []string{"GET /api/models/resolve?author_family=xai&role=review-gate", "GET /api/models"}
	var gotPaths []string
	for _, call := range calls {
		gotPaths = append(gotPaths, call.method+" "+call.path)
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("requests %q, want %q", gotPaths, wantPaths)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["command_template"] != "claude -p --model fable '{prompt}'" || got["source"] != "instance" || got["owner_required"] != false {
		t.Fatalf("output %s", out)
	}
	code, _, stderr = runCLI(append(args[:len(args)-1], "not-a-family"), "")
	if code != 2 || !strings.Contains(stderr, "unknown author family") || len(calls) != 2 {
		t.Fatalf("invalid family exit %d stderr %q requests %d", code, stderr, len(calls))
	}
}

func TestModelResolveAuthorFamilyAliases(t *testing.T) {
	for _, tc := range []struct{ input, family string }{
		{"claude", "anthropic"}, {"codex", "openai"}, {"grok", "xai"},
		{"anthropic", "anthropic"}, {"openai", "openai"}, {"xai", "xai"}, {"cursor", "cursor"},
		{" codex ", "openai"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			isolate(t)
			var paths []string
			sameFamily := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.String())
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/models/resolve":
					if r.URL.Query().Get("author_family") != tc.family {
						t.Errorf("request author_family = %q; want %q", r.URL.Query().Get("author_family"), tc.family)
					}
					family := "anthropic"
					if tc.family == family {
						family = "openai"
					}
					if sameFamily {
						family = tc.family
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"role": "review-gate", "author_family": tc.family,
						"profile": map[string]any{"id": "profile", "slug": "reviewer", "family": family},
						"ladder":  []any{}, "owner_required": false, "source": "aeon",
					})
				case "/api/models":
					_, _ = w.Write([]byte(`[]`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("PAIMOS_URL", srv.URL)
			t.Setenv("PAIMOS_API_KEY", testKey)
			args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "model", "resolve", "review-gate", "--author-family", tc.input}
			code, out, stderr := runCLI(args, "")
			var got map[string]any
			if code != 0 || stderr != "" || json.Unmarshal([]byte(out), &got) != nil || got["author_family"] != tc.family || len(paths) != 2 {
				t.Fatalf("resolve exit %d out %q stderr %q requests %v", code, out, stderr, paths)
			}
			// An alias must preserve the CLI's guard against a same-family reviewer.
			sameFamily = true
			code, out, stderr = runCLI(args, "")
			if code != 1 || out != "" || !strings.Contains(stderr, "selected author family") || len(paths) != 3 {
				t.Fatalf("same-family resolve exit %d out %q stderr %q requests %v", code, out, stderr, paths)
			}
		})
	}
}

func TestModelResolveRejectsAmbiguousAndUnknownAuthorFamilies(t *testing.T) {
	isolate(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	for _, input := range []string{"pi", "unknown"} {
		args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "model", "resolve", "review-gate", "--author-family", input}
		code, out, stderr := runCLI(args, "")
		if code != 2 || out != "" || requests != 0 {
			t.Fatalf("invalid family exit %d out %q stderr %q requests %d", code, out, stderr, requests)
		}
		for _, value := range []string{"openai", "anthropic", "xai", "cursor", "codex", "claude", "grok"} {
			if !strings.Contains(stderr, value) {
				t.Errorf("error %q omits accepted value %q", stderr, value)
			}
		}
		if input == "pi" && (!strings.Contains(stderr, "ambiguous") || !strings.Contains(stderr, "pass the model family")) {
			t.Errorf("pi should request an explicit family: %q", stderr)
		}
	}
}

func TestModelResolveOwnerRequiredTranscript(t *testing.T) {
	isolate(t)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/models/resolve" {
			_, _ = w.Write([]byte(`{"role":"review-gate","profile":null,"ladder":[{"profile_id":"55555555-5555-4555-8555-555555555555","selected":false,"skip_reasons":["author family"]}],"owner_required":true,"source":"aeon"}`))
		} else if r.URL.Path == "/api/models" {
			_, _ = w.Write([]byte(`[{"id":"55555555-5555-4555-8555-555555555555","slug":"codex-astra-xhigh"}]`))
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	base := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "model", "resolve", "review-gate"}
	code, out, stderr := runCLI(append(append([]string{}, base...), "--author-family", "openai"), "")
	if code != 1 || !strings.Contains(stderr, "owner approval required") {
		t.Fatalf("owner gate exit %d out %q stderr %q", code, out, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["owner_required"] != true {
		t.Fatalf("owner gate JSON %q: %v", out, err)
	}
	want := []string{"GET /api/models/resolve?author_family=openai&role=review-gate", "GET /api/models"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("requests %v, want %v", paths, want)
	}
	for _, argv := range [][]string{
		base,
		append(append([]string{}, base...), "--author-family", "other"),
		{"paimos", "--config", base[2], "model", "resolve", "unknown-role"},
	} {
		code, _, _ = runCLI(argv, "")
		if code != 2 || len(paths) != 2 {
			t.Fatalf("invalid argv %q: exit %d, requests %v", argv, code, paths)
		}
	}
}

func TestLocalToolCompatTranscripts(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("// @paimos AEON-1\npackage sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, "anchors.json")
	code, out, stderr := runCLI([]string{"paimos", "anchors", "scan", "--repo-root", root, "--output", index}, "")
	if code != 0 || stderr != "" || out != "wrote "+index+"\n" {
		t.Fatalf("anchors scan exit %d out %q stderr %q", code, out, stderr)
	}
	code, out, stderr = runCLI([]string{"paimos", "anchors", "verify", "--repo-root", root, "--index", index}, "")
	if code != 0 || stderr != "" || out != "anchor index is current\n" {
		t.Fatalf("anchors verify exit %d out %q stderr %q", code, out, stderr)
	}
	var calls []transcriptRequest
	srv := transcriptFixture(t, "guideline", "guide", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	missing := filepath.Join(t.TempDir(), "missing")
	code, out, stderr = runCLI([]string{"paimos", "--config", missing, "skill", "render", "ops", "--project", "AEON", "--workspace", root}, "")
	if code != 0 || stderr != "" || !strings.Contains(out, "wrote ") {
		t.Fatalf("skill render exit %d out %q stderr %q", code, out, stderr)
	}
	if len(calls) != 4 || calls[0].path != "/api/kinds" || calls[1].method != "GET" || calls[3].method != "GET" {
		t.Fatalf("skill requests %+v", calls)
	}
	code, out, stderr = runCLI([]string{"paimos", "--config", missing, "sync", "check", "--project", "AEON", "--workspace", root}, "")
	if code != 0 || stderr != "" || !strings.Contains(out, "identical") {
		t.Fatalf("sync check exit %d out %q stderr %q", code, out, stderr)
	}
}

// The migrated fixture exposes only work for issue kinds, as migration 1215 does.
func sessionBundleFixture(t *testing.T, slugs []string, list http.HandlerFunc) apiNode {
	t.Helper()
	project := apiNode{ID: transcriptProjectID, Key: "PRJ-1", KindID: "project-kind", Title: "AEON", Fields: json.RawMessage(`{"project_key":"AEON"}`)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			items := []apiKind{{ID: "project-kind", Slug: "project"}}
			for _, slug := range slugs {
				items = append(items, apiKind{ID: slug + "-kind", Slug: slug})
			}
			json.NewEncoder(w).Encode(kindPage{Items: items})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes" && r.URL.Query().Get("kind_id") == "project-kind":
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{project}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			list(w, r)
		default:
			t.Errorf("unexpected bundle request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	return project
}

func decodeSessionBundle(t *testing.T, raw []byte) (map[string]any, map[string][]map[string]any) {
	t.Helper()
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle["schema"] != "aeon.session.bundle.v1" || bundle["agent_name"] != "worker" || !validUUID(bundle["session_id"].(string)) {
		t.Fatalf("bundle identity: %s", raw)
	}
	rev := bundle["rev"]
	delete(bundle, "rev")
	content, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if rev != hex.EncodeToString(digest[:]) {
		t.Fatal("bundle revision does not cover the full snapshot")
	}
	groups := map[string][]map[string]any{}
	for _, name := range []string{"nodes", "work", "tickets", "tasks", "epics", "memory", "runbooks", "guidelines", "work_orders"} {
		array, ok := bundle[name].([]any)
		if !ok {
			t.Errorf("bundle %s must be an array, got %#v", name, bundle[name])
			continue
		}
		groups[name] = []map[string]any{}
		for _, item := range array {
			entry, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("%s entry: %#v", name, item)
			}
			groups[name] = append(groups[name], entry)
		}
	}
	return bundle, groups
}

func TestSessionFullWorkCollections(t *testing.T) {
	isolate(t)
	for _, vocabulary := range []string{"migrated", "legacy"} {
		for _, format := range []string{"json", "files", "env"} {
			t.Run(vocabulary+"/"+format, func(t *testing.T) {
				slugs := []string{"work", "memory", "runbook", "guideline", "work_order"}
				workSlugs := []string{"work", "work", "work"}
				if vocabulary == "legacy" {
					slugs = []string{"ticket", "task", "epic", "memory", "runbook", "guideline", "work_order"}
					workSlugs = []string{"ticket", "task", "epic"}
				}
				items := []apiNode{}
				parent := transcriptProjectID
				for i, slug := range append(workSlugs, "memory", "runbook", "guideline", "work_order") {
					id := fmt.Sprintf("50000000-0000-4000-8000-%012d", i+1)
					items = append(items, apiNode{ID: id, Key: fmt.Sprintf("AEON-%d", i+1), KindID: slug + "-kind", ParentID: &parent, Title: "Entry", Body: "Context", Fields: json.RawMessage(`{"status":"open"}`)})
				}
				var project apiNode
				project = sessionBundleFixture(t, slugs, func(w http.ResponseWriter, r *http.Request) {
					page := items
					if r.URL.Query().Get("within") == "" {
						page = append([]apiNode{project}, items...)
					}
					json.NewEncoder(w).Encode(nodePage{Items: page})
				})
				cfg := filepath.Join(t.TempDir(), "missing.yaml")
				args := []string{"aeon", "--config", cfg, "session", "start", "--project", "AEON", "--agent", "worker", "--bundle", "full", "--format", format}
				code, out, errOut := runCLI(args, "")
				if code != 0 || errOut != "" {
					t.Fatalf("full bundle: exit %d stdout %q stderr %q", code, out, errOut)
				}
				raw := []byte(out)
				if format != "json" {
					paths, err := filepath.Glob(filepath.Join(filepath.Dir(cfg), "bundles", project.ID, "*", "manifest.json"))
					if err != nil || len(paths) != 1 {
						t.Fatalf("manifest paths %v: %v", paths, err)
					}
					raw, err = os.ReadFile(paths[0])
					if err != nil {
						t.Fatal(err)
					}
					info, err := os.Stat(paths[0])
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("manifest is not private: %v", err)
					}
				}
				_, groups := decodeSessionBundle(t, raw)
				if len(groups["nodes"]) != len(items)+1 {
					t.Fatalf("nodes: got %d want %d", len(groups["nodes"]), len(items)+1)
				}
				wantWork := 3
				if vocabulary == "legacy" {
					wantWork = 0
				}
				if len(groups["work"]) != wantWork {
					t.Errorf("canonical work: got %d want %d", len(groups["work"]), wantWork)
				}
				for i, name := range []string{"tickets", "tasks", "epics"} {
					want := []map[string]any{}
					for j, node := range items[:3] {
						if vocabulary == "migrated" || i == j {
							want = append(want, map[string]any{"id": node.ID, "key": node.Key, "kind": workSlugs[j], "title": node.Title, "body": node.Body, "fields": map[string]any{"status": "open"}})
						}
					}
					if !reflect.DeepEqual(groups[name], want) {
						t.Errorf("%s: got %#v want %#v", name, groups[name], want)
					}
					if vocabulary == "migrated" && !reflect.DeepEqual(groups["work"], groups[name]) {
						t.Errorf("%s does not alias canonical work", name)
					}
				}
				for i, name := range []string{"memory", "runbooks", "guidelines", "work_orders"} {
					if len(groups[name]) != 1 || groups[name][0]["id"] != items[i+3].ID {
						t.Errorf("%s collection: %#v", name, groups[name])
					}
				}
			})
		}
	}
}

func TestSessionFullProjectScope(t *testing.T) {
	isolate(t)
	for _, scenario := range []string{"depth-32", "depth-33", "depth-1000", "withheld-ancestor", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			items := []apiNode{}
			depth := 0
			if strings.HasPrefix(scenario, "depth-") {
				depth, _ = strconv.Atoi(strings.TrimPrefix(scenario, "depth-"))
			}
			parent := transcriptProjectID
			for i := 1; i <= depth; i++ {
				id := fmt.Sprintf("60000000-0000-4000-8000-%012d", i)
				parentID := parent
				items = append(items, apiNode{ID: id, Key: fmt.Sprintf("AEON-%d", i), KindID: "work-kind", ParentID: &parentID})
				parent = id
			}
			if scenario == "withheld-ancestor" {
				hidden := "60000000-0000-4000-8000-000000000001"
				items = append(items, apiNode{ID: transcriptEntryID, Key: "AEON-2", KindID: "work-kind", ParentID: &hidden})
			}
			calls := 0
			var project apiNode
			project = sessionBundleFixture(t, []string{"work"}, func(w http.ResponseWriter, r *http.Request) {
				calls++
				q := r.URL.Query()
				if q.Get("within") != transcriptProjectID || q.Get("limit") != "200" {
					t.Errorf("bundle must request bounded project descendants: %s", r.URL)
				}
				pageItems := items
				if q.Get("within") == "" {
					// Simulate the tenant-wide endpoint too, so the old code sees
					// every ancestor but still silently omits depths >=32.
					other := apiNode{ID: "70000000-0000-4000-8000-000000000001", Key: "OTHER-1", KindID: "work-kind"}
					pageItems = append(append([]apiNode{project}, items...), other)
				}
				offset := 0
				if q.Get("cursor") != "" {
					var err error
					offset, err = strconv.Atoi(q.Get("cursor"))
					if err != nil || offset < 0 || offset >= len(pageItems) {
						t.Errorf("invalid page cursor: %s", r.URL)
						http.Error(w, "invalid cursor", http.StatusBadRequest)
						return
					}
				}
				end := min(offset+200, len(pageItems))
				page := nodePage{Items: pageItems[offset:end]}
				if end < len(pageItems) {
					cursor := strconv.Itoa(end)
					page.NextCursor = &cursor
				}
				json.NewEncoder(w).Encode(page)
			})
			var out bytes.Buffer
			rt := &runtime{program: "aeon", configPath: filepath.Join(t.TempDir(), "missing.yaml"), stdout: &out, stderr: &bytes.Buffer{}}
			if err := rt.harnessSessionFull("AEON", "worker", "json", transcriptSessionID); err != nil {
				t.Fatal(err)
			}
			_, groups := decodeSessionBundle(t, out.Bytes())
			wantIDs := map[string]bool{project.ID: true}
			for _, n := range items {
				wantIDs[n.ID] = true
			}
			gotIDs := map[string]bool{}
			for _, entry := range groups["nodes"] {
				id := entry["id"].(string)
				if gotIDs[id] {
					t.Errorf("duplicate node %s", id)
				}
				gotIDs[id] = true
			}
			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Errorf("bundle returned %d of %d project nodes (deepest %s present: %v)", len(gotIDs), len(wantIDs), parent, gotIDs[parent])
			}
			if len(groups["work"]) != len(items) {
				t.Errorf("scoped work: got %d want %d", len(groups["work"]), len(items))
			}
			if calls != max(1, (len(items)+199)/200) {
				t.Errorf("list requests: got %d want %d", calls, max(1, (len(items)+199)/200))
			}
		})
	}
}

func TestSessionFullScopeIncomplete(t *testing.T) {
	isolate(t)
	for _, scenario := range []string{"repeated-cursor", "page-budget", "page-error"} {
		for _, format := range []string{"json", "files", "env"} {
			t.Run(scenario+"/"+format, func(t *testing.T) {
				calls := 0
				project := sessionBundleFixture(t, []string{"work"}, func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Query().Get("within") != transcriptProjectID {
						t.Errorf("incomplete bundle query must remain project-scoped: %s", r.URL)
					}
					if scenario == "page-error" && calls == 2 {
						w.WriteHeader(http.StatusForbidden)
						w.Write([]byte(`{"error":"forbidden"}`))
						return
					}
					cursor := "next"
					if scenario == "page-budget" {
						cursor = strconv.Itoa(calls)
					}
					items := []apiNode{}
					parent := transcriptProjectID
					for i := 0; i < 200; i++ {
						id := fmt.Sprintf("80000000-0000-4000-8000-%012d", (calls-1)*200+i+1)
						items = append(items, apiNode{ID: id, KindID: "work-kind", ParentID: &parent})
					}
					json.NewEncoder(w).Encode(nodePage{Items: items, NextCursor: &cursor})
				})
				cfg := filepath.Join(t.TempDir(), "missing.yaml")
				args := []string{"aeon", "--config", cfg, "session", "start", "--project", "AEON", "--agent", "worker", "--bundle", "full", "--format", format}
				code, out, errOut := runCLI(args, "")
				want, wantCalls := "repeated cursor", 2
				if scenario == "page-budget" {
					want, wantCalls = "exceeds 10000 items", 50
				} else if scenario == "page-error" {
					want = "forbidden"
				}
				if code == 0 || out != "" || !strings.Contains(errOut, want) || calls != wantCalls {
					t.Fatalf("incomplete bundle: exit %d stdout %q stderr %q requests %d; want %q and %d requests", code, out, errOut, calls, want, wantCalls)
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(cfg), "bundles", project.ID)); !os.IsNotExist(err) {
					t.Fatalf("incomplete bundle wrote cache: %v", err)
				}
			})
		}
	}
}
