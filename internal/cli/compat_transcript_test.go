// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Doctrine inventory (read-only grep of paimos/doctrine, paimos/AGENTS.md and
// ~/.claude/skills, 2026-09-25): auth login/whoami; model resolve; onboard;
// issue create/get/list/update/comment/search/move; knowledge list/get/create/
// update; project list; session start; anchors scan/verify; skill render;
// sync check; harness register/list/status/orchestrator/bind/heartbeat/yield/
// drain/complete-delivery/interrupt/stop/complete-control/mark-stopped;
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
	path := filepath.Join(t.TempDir(), "briefing.md")
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
		{"register", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions", `{"ok":true}`, []string{"--project", "AEON", "--agent", "worker", "--harness", "codex", "--host", "local", "--harness-session-file", ref, "--worker-lease-file", lease, "--ticket-id", "7", "--work-shape", "ship"}},
		{"list", "GET", "/api/projects/" + transcriptProjectID + "/harness-sessions", `[{"id":"` + transcriptSessionID + `"}]`, []string{"--project", "AEON"}},
		{"status", "GET", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID, `{"ok":true}`, []string{"--project", "AEON", "--session", transcriptSessionID}},
		{"orchestrator", "GET", "/api/projects/" + transcriptProjectID + "/harness-sessions/orchestrator", `{"ok":true}`, []string{"--project", "AEON"}},
		{"bind", "PATCH", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/binding", `{"ok":true}`, []string{"--project", "AEON", "--session", transcriptSessionID, "--revision", "1", "--work-shape", "unknown"}},
		{"heartbeat", "POST", "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID + "/heartbeat", `{"ok":true}`, append(append([]string{}, baseWorker...), "--phase", "working", "--activity-kind", "turn_started", "--activity-sequence", "1", "--note", "Running PDF tests")},
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
