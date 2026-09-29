// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalStageExplicitTarget(t *testing.T) {
	isolate(t)
	var posts []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/kinds":
			_, _ = w.Write([]byte(`{"items":[{"id":"project-kind","slug":"project"}]}`))
		case "/api/nodes":
			_, _ = w.Write([]byte(`{"items":[{"id":"` + rootProjectID + `","key":"AEON-1","kind_id":"project-kind","fields":{"project_key":"AEON"}}]}`))
		case "/api/nodes/" + rootIssueID:
			_, _ = w.Write([]byte(`{"id":"` + rootIssueID + `"}`))
		case "/api/stage-handoffs":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			posts = append(posts, body)
			_, _ = w.Write([]byte(`{"id":"` + rootHandoffID + `"}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	base := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "external-stage", "request", "--project", "AEON", "--release", rootIssueID, "--stage", "deploy", "--expected-journey-revision", "4", "--idempotency-key", "explicit-target"}
	for _, tc := range []struct {
		name, operation, input string
		flag, valid            bool
	}{
		{"missing", "deploy", "", false, true},
		{"null", "deploy", "null", true, true},
		{"incomplete", "deploy", `{"service":"aeon","change":"upgrade"}`, true, false},
		{"unknown", "deploy", `{"hosts":["edge-1"],"service":"aeon","change":"upgrade","guess":"unsafe"}`, true, false},
		{"trailing", "deploy", `{"environment":"prod","service":"aeon","change":"upgrade"} {}`, true, false},
		{"too large", "deploy", strings.Repeat(" ", 32769), true, false},
		{"verify override", "verify", `{"environment":"prod","service":"aeon","change":"upgrade"}`, true, false},
		{"explicit", "deploy", `{"hosts":["edge-2","edge-1"],"service":"aeon","image":"aeon:v2","change":"upgrade"}`, true, true},
		{"verify inherits", "verify", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, base...), "--operation", tc.operation)
			if tc.flag {
				args = append(args, "--target-file", "-")
			}
			before := len(posts)
			code, _, stderr := runCLI(args, tc.input)
			if (code == 0) != tc.valid {
				t.Fatalf("code %d: %s", code, stderr)
			}
			if !tc.valid {
				if len(posts) != before {
					t.Fatal("invalid target reached producer API")
				}
				return
			}
			if len(posts) != before+1 {
				t.Fatal("request missing")
			}
			body := posts[before]
			if tc.name == "explicit" {
				target := body["target"].(map[string]any)
				if target["hosts"].([]any)[0] != "edge-1" || target["image"] != "aeon:v2" || body["release_node_id"] != rootIssueID {
					t.Fatalf("target binding lost: %+v", body)
				}
			} else if _, ok := body["target"]; ok {
				t.Fatal("verify target was inferred by CLI")
			}
		})
	}
}
