// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotCommandsRejectConcurrentFields(t *testing.T) {
	for _, command := range []string{"knowledge", "tag", "apply"} {
		t.Run(command, func(t *testing.T) {
			isolate(t)
			revision := time.Date(2026, 10, 2, 10, 0, 0, 123456000, time.UTC)
			project := apiNode{ID: transcriptProjectID, Key: "PRJ-1", KindID: "project-kind", Title: "AEON", UpdatedAt: revision, Fields: json.RawMessage(`{"project_key":"AEON","tags":[],"setting":"old"}`)}
			entry := apiNode{ID: transcriptEntryID, Key: "MEM-1", KindID: "memory-kind", UpdatedAt: revision, Fields: json.RawMessage(`{"slug":"note","metadata":{"value":"old"}}`)}
			target := &entry
			args := []string{"knowledge", "update", "memory", "note", "--project", "AEON", "--metadata", `{"value":"cli"}`}
			stdin := ""
			if command == "tag" {
				target = &project
				args = []string{"tag", "create", "--name", "New", "--project", "AEON"}
			}
			if command == "apply" {
				args = []string{"apply", "--from-file", "-"}
				stdin = "update:\n  - ref: " + transcriptEntryID + "\n    fields:\n      priority: high\n"
			}
			newer := json.RawMessage(`{"slug":"note","project_key":"AEON","metadata":{"value":"newer"},"setting":"newer","tags":[]}`)
			writes, created := 0, false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				writeNode := func(n *apiNode) {
					_ = json.NewEncoder(w).Encode(n)
					if n == target {
						n.Fields = newer
						n.UpdatedAt = revision.Add(time.Microsecond)
					}
				}
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/kinds":
					_ = json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "project-kind", Slug: "project"}, {ID: "memory-kind", Slug: "memory"}, {ID: "tag-kind", Slug: "tag"}}})
				case r.Method == "GET" && r.URL.Path == "/api/nodes/"+entry.ID:
					writeNode(&entry)
				case r.Method == "GET" && r.URL.Path == "/api/nodes":
					if r.URL.Query().Get("kind_id") == "tag-kind" {
						_ = json.NewEncoder(w).Encode(nodePage{Items: []apiNode{}})
						return
					}
					n := &entry
					if r.URL.Query().Get("kind_id") == "project-kind" {
						n = &project
					}
					_ = json.NewEncoder(w).Encode(nodePage{Items: []apiNode{*n}})
					if n == target {
						n.Fields = newer
						n.UpdatedAt = revision.Add(time.Microsecond)
					}
				case r.Method == "POST" && r.URL.Path == "/api/nodes":
					created = true
					_ = json.NewEncoder(w).Encode(apiNode{ID: tagTranscriptID, KindID: "tag-kind", Title: "New"})
				case r.Method == "PATCH" && r.URL.Path == "/api/nodes/"+target.ID:
					writes++
					if header := r.Header.Get("If-Unmodified-Since"); header != "" && header != target.UpdatedAt.Format(time.RFC3339Nano) {
						w.WriteHeader(412)
						_, _ = w.Write([]byte(`{"error":"node has changed"}`))
						return
					}
					var patch struct {
						Fields json.RawMessage `json:"fields"`
					}
					_ = json.NewDecoder(r.Body).Decode(&patch)
					target.Fields = patch.Fields
					_ = json.NewEncoder(w).Encode(target)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", testKey)
			code, out, stderr := runCLI(append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}, args...), stdin)
			if code != 1 || out != "" || !strings.Contains(stderr, "api 412:") {
				t.Fatalf("conflict: exit=%d out=%q err=%q", code, out, stderr)
			}
			if writes != 1 || string(target.Fields) != string(newer) {
				t.Fatalf("newer fields lost: writes=%d fields=%s", writes, target.Fields)
			}
			if command == "tag" && (!created || !strings.Contains(stderr, "tag remains created") || !strings.Contains(stderr, tagTranscriptID)) {
				t.Fatalf("partial creation not reported: %q", stderr)
			}
		})
	}
}
