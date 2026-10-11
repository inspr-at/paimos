// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Risk: failed writes look successful, or a partial audit hides stranded work.
func TestPillWritesSurface422AndReportOpenInvalidPills(t *testing.T) {
	isolate(t)
	leaf, parent := true, false
	report, failPage := false, false
	writeCalls := 0
	field, pill := "pill_en", "Word"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "pk", Slug: "project"}, {ID: "wk", Slug: "work"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			q := r.URL.Query()
			if q.Get("kind_id") == "pk" {
				json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptProjectID, KindID: "pk", Fields: json.RawMessage(`{"project_key":"AEON"}`)}}})
				return
			}
			if !report {
				json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptEntryID, Key: "AEON-1", KindID: "wk", State: "open", Fields: json.RawMessage(`{}`), UpdatedAt: time.Unix(1, 0)}}})
				return
			}
			if q.Get("parent_id") != transcriptProjectID || q.Get("include_descendants") != "true" || q.Get("hide_closed") != "true" || q.Get("limit") != "200" {
				t.Errorf("report must use bounded project/open query: %s", r.URL)
			}
			if q.Get("cursor") == "next" {
				if failPage {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: "de", Key: "AEON-2", KindID: "wk", State: "in_progress", IsLeaf: &leaf, Fields: json.RawMessage(`{"pill_de":"Eins zwei drei vier fünf"}`)}}})
				return
			}
			next := "next"
			json.NewEncoder(w).Encode(nodePage{NextCursor: &next, Items: []apiNode{
				{ID: "en", Key: "AEON-1", KindID: "wk", State: "open", IsLeaf: &leaf, Fields: json.RawMessage(`{"pill_en":"Word","hide_from_release_notes":true}`)},
				{ID: "valid", Key: "AEON-3", KindID: "wk", State: "open", IsLeaf: &leaf, Fields: json.RawMessage(`{"pill_en":"One two"}`)},
				{ID: "empty", Key: "AEON-4", KindID: "wk", State: "open", IsLeaf: &leaf, Fields: json.RawMessage(`{}`)},
				{ID: "parent", Key: "AEON-5", KindID: "wk", State: "open", IsLeaf: &parent, Fields: json.RawMessage(`{"pill_en":"Word"}`)},
			}})
		case r.Method == http.MethodPost || r.Method == http.MethodPatch:
			if report {
				t.Error("read-only report attempted a write")
			}
			writeCalls++
			var body struct {
				Fields map[string]any `json:"fields"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Fields[field] != pill {
				t.Errorf("pill not passed to server: %v", body.Fields)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]string{"error": field + " must contain 2–4 words"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	run := func(args ...string) (int, string, string) {
		return runCLI(append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}, args...), "")
	}
	for _, language := range []string{"en", "de"} {
		field = "pill_" + language
		for _, value := range []string{"Word", "One two three four five"} {
			pill = value
			for _, jsonOut := range []bool{false, true} {
				for _, verb := range []string{"create", "update"} {
					args := []string{"issue", verb}
					if verb == "create" {
						args = append(args, "--project", "AEON", "--title", "Draft")
					} else {
						args = append(args, "AEON-1")
					}
					args = append(args, "--pill-"+language, value)
					if jsonOut {
						args = append([]string{"--json"}, args...)
					}
					before := writeCalls
					code, out, stderr := run(args...)
					if code == 0 || out != "" || !strings.Contains(stderr, "api 422: "+field+" must contain 2–4 words") || strings.Contains(stderr, "warning:") || writeCalls != before+1 {
						t.Fatalf("%s failed write reported success: %d %q %q", verb, code, out, stderr)
					}
				}
			}
		}
	}
	report = true
	code, out, stderr := run("--json", "issue", "pill-report", "--project", "AEON")
	var result struct {
		Items []struct {
			Key    string   `json:"issue_key"`
			Issues []string `json:"issues"`
		} `json:"items"`
		Scanned, Total int
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if code != 0 || result.Scanned != 5 || result.Total != 3 || len(result.Items) != 3 || result.Items[0].Key != "AEON-1" || result.Items[1].Key != "AEON-5" || result.Items[2].Key != "AEON-2" || fmt.Sprint(result.Items[2].Issues) != "[pill_de must contain 2–4 words]" {
		t.Fatalf("report: %d %s %s", code, out, stderr)
	}
	failPage = true
	code, out, stderr = run("issue", "pill-report", "--project", "AEON")
	if code == 0 || out != "" || !strings.Contains(stderr, "503") {
		t.Fatalf("partial report claimed completeness: %d %q %q", code, out, stderr)
	}
}
