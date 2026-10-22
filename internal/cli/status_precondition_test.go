// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Keep the shipped LEAD helpers on the PR's ESSENTIAL lane as well as the
// coordinator's in-memory integration run. All tracker/builder effects are stubs.
func TestLeadConditionalStatusGuards(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "python3", "../../scripts/lead/test_conditional_status.py").CombinedOutput()
	if err != nil {
		t.Fatalf("LEAD cancellation scenarios: %v\n%s", err, out)
	}
}

// Risk: the CLI drops the precondition, retries unconditionally, or reports a
// cancelled start as successful. The stub cancels after the lookup snapshot.
func TestIssueConditionalStatusCarriesPreconditionAndConflict(t *testing.T) {
	isolate(t)
	patches := 0
	conditional, mismatch := true, true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/node-keys/MEM-1":
			_, _ = w.Write([]byte(`{"id":"` + transcriptEntryID + `","key":"MEM-1","kind_id":"work-kind","title":"Start me","state":"open","fields":{},"updated_at":"2026-10-21T00:00:00Z"}`))
		case r.Method == "GET" && r.URL.Path == "/api/nodes" && r.URL.Query().Get("q") == "MEM-1":
			_, _ = w.Write([]byte(`{"items":[{"id":"` + transcriptEntryID + `","key":"MEM-1","kind_id":"work-kind","title":"Start me","state":"open","fields":{},"updated_at":"2026-10-21T00:00:00Z"}]}`))
		case r.Method == "GET" && r.URL.Path == "/api/kinds":
			_, _ = w.Write([]byte(`{"items":[{"id":"work-kind","slug":"work"}]}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/nodes/"+transcriptEntryID:
			patches++
			var patch map[string]any
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Error(err)
			}
			if patch["state"] != "in_progress" || r.Header.Get("If-Unmodified-Since") != "2026-10-21T00:00:00Z" {
				t.Errorf("lost transition or revision: %v %v", patch, r.Header)
			}
			if conditional && !reflect.DeepEqual(patch["expected_states"], []any{"open", "qa"}) {
				t.Errorf("lost status precondition: %v", patch)
			}
			if !conditional && patch["expected_states"] != nil {
				t.Errorf("changed unconditional payload: %v", patch)
			}
			if mismatch {
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"error":"status precondition failed: current state is cancelled","code":"status_mismatch","current_state":"cancelled"}`))
			} else {
				_, _ = w.Write([]byte(`{"id":"` + transcriptEntryID + `","key":"MEM-1","kind_id":"work-kind","state":"in_progress","fields":{}}`))
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	run := func(flags ...string) (int, string, string) {
		return runCLI(append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "issue", "update", "MEM-1"}, flags...), "")
	}
	for _, flags := range [][]string{
		{"--if-status", "open"},
		{"--status", "in_progress", "--if-status", ""},
		{"--status", "in_progress", "--if-status", "open,,qa"},
		{"--status", "in_progress", "--if-status", strings.Repeat("open,", 32) + "qa"},
	} {
		code, _, errOut := run(flags...)
		if code != 2 || !strings.Contains(errOut, "--if-status") || patches != 0 {
			t.Fatalf("invalid precondition wrote: %d %s; patches=%d", code, errOut, patches)
		}
	}
	code, out, errOut := run("--status", "in_progress", "--if-status", "open, qa")
	var failure map[string]string
	if err := json.Unmarshal([]byte(errOut), &failure); err != nil {
		t.Fatal(err, errOut)
	}
	if code != 1 || out != "" || failure["code"] != "status_mismatch" || failure["current_state"] != "cancelled" || patches != 1 {
		t.Fatalf("lost conflict or retried: %d %s %s; patches=%d", code, out, errOut, patches)
	}
	mismatch = false
	code, out, errOut = run("--status", "in_progress", "--if-status", "open", "--if-status", "qa")
	if code != 0 || !strings.Contains(out, `"status":"in_progress"`) || errOut != "" || patches != 2 {
		t.Fatalf("matching transition failed: %d %s %s; patches=%d", code, out, errOut, patches)
	}
	conditional = false
	code, _, errOut = run("--status", "in_progress")
	if code != 0 || errOut != "" || patches != 3 {
		t.Fatalf("unconditional behavior changed: %d %s; patches=%d", code, errOut, patches)
	}
}
