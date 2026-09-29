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

func TestIssueMoveAliasCLI(t *testing.T) {
	isolate(t)
	const issueID = "22222222-2222-4222-8222-222222222222"
	const projectID = "11111111-1111-4111-8111-111111111111"
	moved := false
	var patched bool
	const revision = "2026-09-29T12:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		issue := map[string]any{"id": issueID, "kind_id": "ticket-kind", "key": "SRC-1", "title": "Move me", "state": "active", "fields": map[string]any{}, "updated_at": revision}
		if moved {
			issue["key"] = "DST-1"
			issue["parent_id"] = projectID
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/kinds":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": "project-kind", "slug": "project"}, map[string]any{"id": "ticket-kind", "slug": "ticket"}}})
		case r.Method == "GET" && r.URL.Path == "/api/nodes" && r.URL.Query().Get("kind_id") == "project-kind":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": projectID, "kind_id": "project-kind", "key": "DST-1", "title": "Target", "fields": map[string]any{"project_key": "DST"}}}})
		case r.Method == "GET" && r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{issue}})
		case r.Method == "GET" && r.URL.Path == "/api/node-keys/SRC-1":
			_ = json.NewEncoder(w).Encode(issue)
		case r.Method == "GET" && r.URL.Path == "/api/nodes/"+issueID+"/activity":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		case r.Method == "POST" && r.URL.Path == "/api/nodes/"+issueID+"/project-move":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["project_id"] != projectID {
				t.Errorf("move body: %+v", body)
			}
			moved = true
			_ = json.NewEncoder(w).Encode(map[string]any{"issue_id": issueID, "old_key": "SRC-1", "new_key": "DST-1", "project_id": projectID, "detached": []string{}, "notes": []string{}})
		case r.Method == "PATCH" && r.URL.Path == "/api/nodes/"+issueID:
			if got := r.Header.Get("If-Unmodified-Since"); got != revision {
				t.Errorf("alias write revision: %q", got)
			}
			patched = true
			_ = json.NewEncoder(w).Encode(issue)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	base := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "issue"}
	code, out, errOut := runCLI(append(append([]string{}, base...), "move", "SRC-1", "--to", "DST"), "")
	if code != 0 || errOut != "" || !strings.Contains(out, `"new_key":"DST-1"`) {
		t.Fatalf("move: %d %q %q", code, out, errOut)
	}
	code, out, errOut = runCLI(append(append([]string{}, base...), "get", "SRC-1"), "")
	if code != 0 || errOut != "" || !strings.Contains(out, `"issue_key":"DST-1"`) {
		t.Fatalf("alias read: %d %q %q", code, out, errOut)
	}
	code, out, errOut = runCLI(append(append([]string{}, base...), "update", "SRC-1", "--title", "Changed"), "")
	if code != 0 || errOut != "" || !patched {
		t.Fatalf("alias write: %d %q %q", code, out, errOut)
	}
}

func TestBaselineReportBuiltCLI(t *testing.T) {
	isolate(t)
	const projectID = "11111111-1111-4111-8111-111111111111"
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/kinds":
			_, _ = w.Write([]byte(`{"items":[{"id":"project-kind","slug":"project"}]}`))
		case r.Method == "GET" && r.URL.Path == "/api/nodes":
			_, _ = w.Write([]byte(`{"items":[{"id":"` + projectID + `","key":"PRJ-1","kind_id":"project-kind","title":"Project","fields":{"project_key":"AEON"}}]}`))
		case r.Method == "POST" && r.URL.Path == "/api/projects/"+projectID+"/baseline-batches/batches/42/built-receipt":
			_ = json.NewDecoder(r.Body).Decode(&posted)
			_, _ = w.Write([]byte(`{"id":42,"project_id":7,"progress":{"next_action":"operator_external_stage_cli"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	digest := strings.Repeat("a", 64)
	args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "baseline-batch", "report-built", "--project", "AEON", "--batch-id", "42",
		"--idempotency-key", "receipt-123", "--expected-attempt-id", "1", "--expected-plan-revision", "2",
		"--commit", strings.Repeat("a", 40), "--oci-config-digest", digest, "--release-manifest-digest", digest,
		"--release-coordinate", "release:aeon", "--scheme", "legacy", "--channel", "stable", "--sequence", "1", "--version", "1.0", "--qa-digest", digest}
	code, out, errOut := runCLI(args, "")
	if code != 0 || errOut != "" || strings.TrimSpace(out) != `{"id":42,"progress":{"next_action":"operator_external_stage_cli"},"project_id":7}` {
		t.Fatalf("report-built: %d %q %q", code, out, errOut)
	}
	if posted["expected_attempt_id"] != float64(1) || posted["release_manifest_coordinate"] != "release:aeon" {
		t.Fatalf("posted: %+v", posted)
	}
	code, out, errOut = runCLI(append(append([]string{}, args...), "--dry-run"), "")
	if code != 0 || errOut != "" || !strings.Contains(out, `"path":"/api/projects/AEON/baseline-batches/batches/42/built-receipt"`) {
		t.Fatalf("dry run: %d %q %q", code, out, errOut)
	}
}
