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

func TestRegisterRejectsEpicTicket(t *testing.T) {
	isolate(t)
	const (
		projectID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		epicID    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		ticketID  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		sessionID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	)
	project := apiNode{ID: projectID, Key: "AIT", KindID: "project-kind", Fields: json.RawMessage(`{"project_key":"AIT"}`)}
	epic := apiNode{ID: epicID, Key: "AIT-34", KindID: "epic-kind", Title: "Planner"}
	ticket := apiNode{ID: ticketID, Key: "AIT-35", KindID: "ticket-kind", Title: "Do the thing"}
	posts := 0
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{
				{ID: "project-kind", Slug: "project"},
				{ID: "epic-kind", Slug: "epic"},
				{ID: "ticket-kind", Slug: "ticket"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/me":
			w.Write([]byte(`{"principal":{"id":"p","name":"worker"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			q := r.URL.Query()
			switch {
			case q.Get("kind_id") == "project-kind":
				json.NewEncoder(w).Encode(nodePage{Items: []apiNode{project}})
			case q.Get("q") == "AIT-34":
				json.NewEncoder(w).Encode(nodePage{Items: []apiNode{epic}})
			case q.Get("q") == "AIT-35":
				json.NewEncoder(w).Encode(nodePage{Items: []apiNode{ticket}})
			default:
				json.NewEncoder(w).Encode(nodePage{})
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/"+projectID+"/harness-sessions":
			posts++
			json.NewDecoder(r.Body).Decode(&posted)
			json.NewEncoder(w).Encode(map[string]any{"id": sessionID})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	dir := t.TempDir()
	ref := filepath.Join(dir, "session.ref")
	lease := filepath.Join(dir, "worker.lease")
	if err := os.WriteFile(ref, []byte("vendor-session-ref-000000000001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lease, []byte("private-worker-lease-00000000000000000001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"aeon", "--config", filepath.Join(dir, "missing"), "harness", "register",
		"--project", "AIT", "--agent", "worker", "--harness", "codex", "--host", "local",
		"--harness-session-file", ref, "--worker-lease-file", lease}

	code, out, stderr := runCLI(append(base, "--ticket", "AIT-34", "--work-shape", "ship"), "")
	if code != 2 || !strings.Contains(stderr, "AIT-34 is an epic; bind the session to one of its tickets") || posts != 0 || out != "" {
		t.Fatalf("epic: exit %d stdout %q stderr %q posts %d", code, out, stderr, posts)
	}

	code, out, stderr = runCLI(append(base, "--ticket", "AIT-35"), "")
	if code != 2 || !strings.Contains(stderr, "--work-shape must be ship or scout") || posts != 0 {
		t.Fatalf("missing shape: exit %d stdout %q stderr %q posts %d", code, out, stderr, posts)
	}

	code, out, stderr = runCLI(append(base, "--ticket", "AIT-35", "--work-shape", "ship"), "")
	if code != 0 || posts != 1 || posted["ticket_node_id"] != ticketID || posted["work_shape"] != "ship" {
		t.Fatalf("ticket: exit %d stdout %q stderr %q posts %d body %v", code, out, stderr, posts, posted)
	}

	code, _, stderr = runCLI(base, "")
	if code != 0 || posts != 2 || stderr != "" {
		t.Fatalf("no ticket: exit %d stderr %q posts %d", code, stderr, posts)
	}
}
