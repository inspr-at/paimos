// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/client"
)

func TestEmptyIssueListJSONIsArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/nodes" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("kind_id") == "pk" {
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptProjectID, KindID: "pk", Fields: json.RawMessage(`{"project_key":"AEON"}`)}}})
			return
		}
		if q.Get("parent_id") != transcriptProjectID || q.Get("include_descendants") != "true" {
			t.Errorf("descendant query: %s", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(nodePage{Items: []apiNode{}})
	}))
	defer srv.Close()

	kinds := kindTable{
		bySlug: map[string]apiKind{"project": {ID: "pk", Slug: "project"}},
		byID:   map[string]apiKind{"pk": {ID: "pk", Slug: "project"}},
	}
	var stdout, stderr bytes.Buffer
	rt := &runtime{
		jsonOut:      true,
		stdout:       &stdout,
		stderr:       &stderr,
		personClient: client.New(srv.URL, "fixture"),
		kinds:        &kinds,
	}
	if err := rt.listIssues("AEON", "", "", "", "", 50, 0); err != nil {
		t.Fatal(err)
	}
	out := strings.TrimSpace(stdout.String())
	if !strings.Contains(out, `"issues":[]`) || strings.Contains(out, `"issues":null`) || !strings.Contains(out, `"total":0`) {
		t.Fatalf("issue list: %s", out)
	}

	stdout.Reset()
	if err := rt.printIssueList(nil, 0); err != nil {
		t.Fatal(err)
	}
	out = strings.TrimSpace(stdout.String())
	if !strings.Contains(out, `"issues":[]`) || strings.Contains(out, `"issues":null`) {
		t.Fatalf("nil issue list: %s", out)
	}
}
