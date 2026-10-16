// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Risk: CLI changes patch omission/null intent, loses the stable retry header,
// or sends an invalid ref batch before validating it.
func TestGoalUpdateCommandPreservesPatchAndReceipt(t *testing.T) {
	isolate(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "PATCH" || r.URL.Path != "/api/goals/GOAL-3" || r.Header.Get("Idempotency-Key") != "cli-goal-key" {
			t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 1 || body["progress_override"] != nil {
			t.Errorf("patch lost clear/omission: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"goal","key":"GOAL-3","changed":true,"read_through":3,"generation":4}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "goal", "update", "GOAL-3", "--file", "-", "--idempotency-key", "cli-goal-key"}
	code, out, stderr := runCLI(args, `{"progress_override":null}`)
	if code != 0 || stderr != "" || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("CLI %d %s %s", code, out, stderr)
	}
	code, _, stderr = runCLI(args, `{"items":{"upsert":[{"label":"missing ref"}]}}`)
	if code != 2 || !strings.Contains(stderr, ".ref") || calls != 1 {
		t.Fatalf("invalid CLI wrote: %d calls=%d %s", code, calls, stderr)
	}
	code, out, stderr = runCLI(append(args, "--dry-run"), `{"progress_override":null}`)
	if code != 0 || calls != 1 || !strings.Contains(out, "Idempotency-Key") {
		t.Fatalf("dry run %d %s %s", code, out, stderr)
	}
	assertNoSecret(t, out+stderr)
}
