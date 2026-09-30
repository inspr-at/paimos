// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

func TestIssueCreateBugMarksReleaseFixAndPreservesKind(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "pk", Slug: "project"}, {ID: "tk", Slug: "ticket"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptProjectID, KindID: "pk", Fields: json.RawMessage(`{"project_key":"AEON"}`)}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/nodes":
			var body struct {
				KindID string          `json:"kind_id"`
				Fields json.RawMessage `json:"fields"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.KindID != "tk" || !releasehistory.ParseTicketMeta("ticket", body.Fields).Bug {
				t.Error("repair not classified as a fix with ticket kind")
			}
			var fields struct {
				Tags []string `json:"tags"`
			}
			json.Unmarshal(body.Fields, &fields)
			if len(fields.Tags) != 2 || fields.Tags[0] != "process-learning" || fields.Tags[1] != "bug" {
				t.Errorf("tags: %v", fields.Tags)
			}
			json.NewEncoder(w).Encode(apiNode{ID: transcriptEntryID, Key: "AEON-405", KindID: "tk", Title: "Repair", Fields: body.Fields})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, _, stderr := runCLI([]string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "issue", "create", "--project", "AEON", "--title", "Repair", "--bug", "--tags", "process-learning", "--tags", "bug"}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
