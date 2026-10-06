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

func TestLeadDecisionCLIReadAndRecord(t *testing.T) {
	isolate(t)
	const project = "11111111-1111-1111-1111-111111111111"
	const session = "22222222-2222-2222-2222-222222222222"
	const request = "33333333-3333-3333-3333-333333333333"
	const lease = "synthetic-worker-lease-00000000000000000000001"
	posts, gets := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/projects/"+project+"/lead-decisions" {
			gets++
			if r.URL.Query().Get("after") != "8" || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("session_id") != session {
				t.Error("lost pagination/filter")
			}
			_, _ = w.Write([]byte(`{"items":[{"event_id":9,"evidence":"coordinator_reported","authority_granted":false,"outcome":"wait","reason_codes":["account_room_unreadable"],"request":{"stage":"admission","policy_source":"project","policy_revision":3,"attempt":2}}],"next_after":9}`))
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/projects/"+project+"/harness-sessions/"+session+"/lead-decisions" {
			posts++
			if r.Header.Get("X-Aeon-Worker-Lease") != lease {
				t.Error("missing exact generation proof")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["request_id"] != request || body["policy_revision"] != float64(3) {
				t.Error("lost decision identity")
			}
			_, _ = w.Write([]byte(`{"decision":{"event_id":10,"authority_granted":false,"evidence":"coordinator_reported"},"replayed":false}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", 400)
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "project", "decisions", project, "--after", "8", "--limit", "1", "--session", session}
	code, out, errOut := runCLI(args, "")
	if code != 0 || !strings.Contains(out, "wait") || !strings.Contains(out, "account_room_unreadable") || !strings.Contains(out, "More decisions: --after 9") {
		t.Fatalf("code %d out %s error %s", code, out, errOut)
	}
	code, out, errOut = runCLI(append([]string{"aeon", "--json"}, args[1:]...), "")
	if code != 0 || !strings.Contains(out, `"next_after":9`) || !strings.Contains(out, `"authority_granted":false`) {
		t.Fatalf("JSON lost evidence: %d %s %s", code, out, errOut)
	}
	leaseFile := filepath.Join(t.TempDir(), "lease")
	if err := os.WriteFile(leaseFile, []byte(lease), 0600); err != nil {
		t.Fatal(err)
	}
	body := `{"request_id":"` + request + `","stage":"queue","outcome":"selected","reason_codes":["oldest_eligible"],"policy_source":"project","policy_revision":3,"attempt":1}`
	code, out, errOut = runCLI([]string{"aeon", "harness", "decision", "--project", project, "--session", session, "--body-file", "-", "--worker-lease-file", leaseFile}, body)
	if code != 0 || !strings.Contains(out, `"event_id":10`) || posts != 1 || gets != 2 {
		t.Fatalf("record %d %s %s counts %d %d", code, out, errOut, posts, gets)
	}
	assertNoSecret(t, out+errOut)
	if strings.Contains(out+errOut, lease) {
		t.Fatal("lease leaked")
	}
}

func TestLeadDecisionCLIRejectsUnboundedAndPrivateEvidence(t *testing.T) {
	isolate(t)
	const project = "11111111-1111-1111-1111-111111111111"
	const session = "22222222-2222-2222-2222-222222222222"
	for _, args := range [][]string{
		{"project", "decisions", project, "--limit", "201"},
		{"project", "decisions", project, "--after", "-1"},
		{"project", "decisions", project, "--session", "bad"},
		{"harness", "decision", "--project", project, "--session", session, "--body-file", "-", "--worker-lease-file", "-"},
	} {
		code, _, _ := runCLI(append([]string{"aeon"}, args...), "")
		if code != 2 {
			t.Fatalf("invalid args %v returned %d", args, code)
		}
	}
	for _, body := range []string{strings.Repeat("x", 8193), `{"prompt":"private text"}`, `{} {}`} {
		code, out, errOut := runCLI([]string{"aeon", "harness", "decision", "--project", project, "--session", session, "--body-file", "-", "--worker-lease-file", "unused"}, body)
		if code != 2 || strings.Contains(out+errOut, "private text") {
			t.Fatalf("invalid evidence returned %d %s %s", code, out, errOut)
		}
	}
}
