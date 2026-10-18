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
)

const rootProjectID = "11111111-1111-4111-8111-111111111111"
const rootIssueID = "22222222-2222-4222-8222-222222222222"
const rootHandoffID = "33333333-3333-4333-8333-333333333333"
const rootAttachmentID = "44444444-4444-4444-8444-444444444444"

func TestCLIAdditionalRootTranscripts(t *testing.T) {
	isolate(t)
	// Doctor reads the home directory for the session hook. Keep that off this
	// machine's real harness files.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	type transcript struct {
		name    string
		argv    []string
		input   string
		method  string
		path    string
		jsonKey string
	}
	plan := filepath.Join(t.TempDir(), "plan.yaml")
	if err := os.WriteFile(plan, []byte("project: AEON\ncreate: []\nupdate: []\nrelations: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(upload, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []transcript{
		{"relation add", []string{"relation", "add", "AEON-1", "blocks", "AEON-2"}, "", "POST", "/api/relations", "source_node_id"},
		{"project create", []string{"project", "create", "--name", "AEON", "--key", "AEON"}, "", "POST", "/api/nodes", "kind_id"},
		{"tag create", []string{"tag", "create", "--name", "Ready", "--color", "blue"}, "", "POST", "/api/nodes", "kind_id"},
		{"attach list", []string{"attach", "list", "--issue", "AEON-1"}, "", "GET", "/api/nodes/" + rootIssueID + "/attachments", ""},
		{"attach upload", []string{"attach", "AEON-1", upload}, "", "POST", "/api/nodes/" + rootIssueID + "/attachments", ""},
		{"apply dry-run", []string{"apply", "--from-file", plan, "--dry-run"}, "", "", "", ""},
		{"schema", []string{"schema", "--refresh"}, "", "GET", "/api/kinds", ""},
		{"doctor", []string{"doctor"}, "", "GET", "/api/me", ""},
		{"curl", []string{"curl", "/version", "-X", "GET"}, "", "GET", "/api/version", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []transcriptRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantAuth := "Bearer " + testKey
				if tc.name == "doctor" && (r.URL.Path == "/api/health" || r.URL.Path == "/api/version") {
					wantAuth = ""
				}
				if got := r.Header.Get("Authorization"); got != wantAuth {
					t.Errorf("auth header %q", got)
				}
				var body map[string]any
				if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
					_ = json.NewDecoder(r.Body).Decode(&body)
				}
				calls = append(calls, transcriptRequest{r.Method, r.URL.String(), body})
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/health":
					_, _ = w.Write([]byte(`{"status":"ok","db":"ok"}`))
				case r.URL.Path == "/api/version":
					_, _ = w.Write([]byte(`{"version":"260925100000.0.0","scheme":"inspr-calendar-v2","brand":{"wordmark":"PAIMOS AEON","product":"PAIMOS"}}`))
				case r.URL.Path == "/api/me":
					_, _ = w.Write([]byte(`{"principal":{"name":"worker"}}`))
				case r.URL.Path == "/api/kinds" && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"items":[{"id":"project-kind","slug":"project"},{"id":"ticket-kind","slug":"ticket"},{"id":"tag-kind","slug":"tag"}]}`))
				case r.URL.Path == "/api/nodes" && r.Method == "GET" && r.URL.Query().Get("kind_id") == "project-kind":
					_, _ = w.Write([]byte(`{"items":[{"id":"` + rootProjectID + `","key":"AEON-1","kind_id":"project-kind","title":"AEON","state":"active","fields":{"project_key":"AEON"}}]}`))
				case r.URL.Path == "/api/nodes" && r.Method == "GET" && r.URL.Query().Get("q") == "AEON-2":
					_, _ = w.Write([]byte(`{"items":[{"id":"` + rootProjectID + `","key":"AEON-2","kind_id":"ticket-kind","title":"Target"}]}`))
				case r.URL.Path == "/api/nodes" && r.Method == "GET":
					_, _ = w.Write([]byte(`{"items":[{"id":"` + rootIssueID + `","key":"AEON-1","kind_id":"ticket-kind","title":"Source"}]}`))
				case r.URL.Path == "/api/nodes" && r.Method == "POST":
					_, _ = w.Write([]byte(`{"id":"` + rootProjectID + `","key":"AEON-1","kind_id":"project-kind","title":"AEON","state":"active"}`))
				case r.URL.Path == "/api/relations":
					_, _ = w.Write([]byte(`{"id":"` + rootHandoffID + `","source_node_id":"` + rootIssueID + `","target_node_id":"` + rootProjectID + `","type":"blocks"}`))
				case r.URL.Path == "/api/nodes/"+rootIssueID+"/attachments" && r.Method == "POST":
					if err := r.ParseMultipartForm(1 << 20); err != nil || len(r.MultipartForm.File["file"]) != 1 {
						t.Errorf("invalid upload multipart: %v", err)
					}
					_, _ = w.Write([]byte(`[{"id":"` + rootAttachmentID + `","node_id":"` + rootIssueID + `","name":"note.txt","size":5}]`))
				case r.URL.Path == "/api/nodes/"+rootIssueID+"/attachments":
					_, _ = w.Write([]byte(`[{"id":"` + rootAttachmentID + `","node_id":"` + rootIssueID + `","name":"a.txt","size":2}]`))
				case r.URL.Path == "/api/stage-handoffs/"+rootHandoffID:
					_, _ = w.Write([]byte(`{"id":"` + rootHandoffID + `","state":"requested"}`))
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.String())
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("PAIMOS_URL", srv.URL)
			t.Setenv("PAIMOS_API_KEY", testKey)
			argv := append([]string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json"}, tc.argv...)
			code, out, errOut := runCLI(argv, tc.input)
			if code != 0 || errOut != "" {
				t.Fatalf("exit %d out %q err %q", code, out, errOut)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("invalid JSON %q", out)
			}
			if tc.method == "" {
				if len(calls) != 0 {
					t.Fatalf("dry-run sent requests: %+v", calls)
				}
				return
			}
			var found bool
			for _, c := range calls {
				if c.method == tc.method && c.path == tc.path {
					found = true
					if tc.jsonKey != "" && c.body[tc.jsonKey] == nil {
						t.Errorf("missing body key %s in %+v", tc.jsonKey, c.body)
					}
				}
			}
			if !found {
				t.Fatalf("missing %s %s in %+v", tc.method, tc.path, calls)
			}
		})
	}
}
