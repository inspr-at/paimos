// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestTellReceiptOutput(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000004"
	const projectID = "00000000-0000-4000-8000-000000000002"
	for _, tc := range []struct {
		name, program, receipt, plain, state, reason, sentStatus string
		send, jsonOutput, action, receiptFailure                 bool
	}{
		{"queued status", "paimos", `{"message_id":"` + id + `","state":"queued"}`, "queued", "queued", "", "", false, false, false, false},
		{"confirmed status JSON", "aeon", `{"message_id":"` + id + `","state":"handed_off","handed_off_at":"2026-09-28T10:00:00Z"}`, "delivered", "handed_off", "", "", false, true, false, false},
		{"failed status", "paimos", `{"message_id":"` + id + `","state":"failed","failure_reason":"target_missing"}`, "failed/target_missing", "failed", "target_missing", "", false, false, false, false},
		{"queued send JSON classic", "paimos", `{"message_id":"` + id + `","state":"queued"}`, "queued", "queued", "", "accepted", true, true, false, false},
		{"queued send JSON native", "aeon", `{"message_id":"` + id + `","state":"queued"}`, "queued", "queued", "", "accepted", true, true, false, false},
		{"confirmed send classic", "paimos", `{"message_id":"` + id + `","state":"handed_off"}`, "delivered", "handed_off", "", "accepted", true, false, false, false},
		{"confirmed send native JSON", "aeon", `{"message_id":"` + id + `","state":"handed_off"}`, "delivered", "handed_off", "", "accepted", true, true, false, false},
		{"failed send classic JSON", "paimos", `{"message_id":"` + id + `","state":"failed","failure_reason":"target_missing"}`, "failed/target_missing", "failed", "target_missing", "accepted", true, true, false, false},
		{"failed send native JSON", "aeon", `{"message_id":"` + id + `","state":"failed","failure_reason":"target_missing"}`, "failed/target_missing", "failed", "target_missing", "accepted", true, true, false, false},
		{"held action classic", "paimos", `{"message_id":"` + id + `","state":"queued"}`, "held: action request - requires human approval", "queued", "", "held", true, false, true, false},
		{"held action native", "aeon", `{"message_id":"` + id + `","state":"queued"}`, "held: action request - requires human approval", "queued", "", "held", true, false, true, false},
		{"held action classic JSON", "paimos", `{"message_id":"` + id + `","state":"queued"}`, "", "queued", "", "held", true, true, true, false},
		{"held action native JSON", "aeon", `{"message_id":"` + id + `","state":"queued"}`, "", "queued", "", "held", true, true, true, false},
		{"stored receipt failure classic", "paimos", "", "receipt unavailable", "unavailable", "", "accepted", true, false, false, true},
		{"stored receipt failure classic JSON", "paimos", "", "", "unavailable", "", "accepted", true, true, false, true},
		{"stored receipt failure native", "aeon", "", "receipt unavailable", "unavailable", "", "accepted", true, false, false, true},
		{"stored receipt failure native JSON", "aeon", "", "", "unavailable", "", "accepted", true, true, false, true},
		{"held receipt failure", "paimos", "", "held: action request - requires human approval", "unavailable", "", "held", true, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				switch {
				case r.URL.Path == "/api/kinds":
					fmt.Fprint(w, `{"items":[{"id":"00000000-0000-4000-8000-000000000001","slug":"project"}]}`)
				case strings.HasPrefix(r.URL.Path, "/api/nodes"):
					fmt.Fprint(w, `{"items":[{"id":"`+projectID+`","key":"AEON-1","title":"AEON","fields":{"project_key":"AEON"}}]}`)
				case r.URL.Path == "/api/me":
					fmt.Fprint(w, `{"principal":{"id":"00000000-0000-4000-8000-000000000008","name":"sender"}}`)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
					fmt.Fprint(w, `{"id":"`+id+`","sender_principal_id":"00000000-0000-4000-8000-000000000008","recipient_principal_id":"00000000-0000-4000-8000-000000000003","from":"paimos:sender","to":"codex:receiver","body":"synthetic","thread_id":"`+id+`","hop":1,"status":"`+tc.sentStatus+`","is_action_request":`+fmt.Sprint(tc.action)+`,"created_at":"2026-09-28T10:00:00Z"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages/"+id+"/receipt":
					if tc.receiptFailure {
						http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
						return
					}
					fmt.Fprint(w, tc.receipt)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			if tc.program == "aeon" {
				t.Setenv("AEON_URL", srv.URL)
				t.Setenv("AEON_API_KEY", "synthetic-key")
			} else {
				t.Setenv("PAIMOS_URL", srv.URL)
				t.Setenv("PAIMOS_API_KEY", "synthetic-key")
			}
			args := []string{tc.program, "--config", filepath.Join(t.TempDir(), "missing")}
			if tc.jsonOutput {
				args = append(args, "--json")
			}
			if tc.send {
				args = append(args, "tell", "codex:receiver", "--project", "AEON", "-m", "synthetic")
				if tc.action {
					args = append(args, "--action-request")
				}
			} else {
				args = append(args, "tell", "status", id)
			}
			var out, errOut bytes.Buffer
			if code := runMessaging(args, strings.NewReader(""), &out, &errOut, nil); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, errOut.String())
			}
			if !tc.jsonOutput && (!strings.Contains(out.String(), "push: "+tc.plain) || tc.send && !strings.Contains(out.String(), "stored:")) {
				t.Fatalf("plain output %q", out.String())
			}
			if tc.jsonOutput {
				var got map[string]any
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if tc.send {
					if got["stored"] != true || got["receipt_state"] != tc.state || tc.reason != "" && got["failure_reason"] != tc.reason || tc.reason == "" && got["failure_reason"] != nil {
						t.Fatalf("send JSON %v", got)
					}
					if tc.program == "paimos" && (got["delivered"] != (tc.state == "handed_off" && !tc.action) || got["is_action_request"] != tc.action || got["message_id"] != id || got["held_reason"] != map[bool]any{true: "action request - requires human approval", false: nil}[tc.action]) {
						t.Fatalf("classic fields %v", got)
					}
					if tc.program == "aeon" && (got["status"] != tc.sentStatus || got["is_action_request"] != tc.action || got["id"] != id) {
						t.Fatalf("native fields %v", got)
					}
				} else if got["state"] != tc.state || got["message_id"] != id {
					t.Fatalf("status JSON %v", got)
				}
			}
			if requests[len(requests)-1] != "GET /api/inbox/messages/"+id+"/receipt" {
				t.Fatalf("receipt request missing: %q", requests)
			}
			if tc.send {
				posts := 0
				for _, request := range requests {
					if strings.HasPrefix(request, "POST ") {
						posts++
					}
				}
				if posts != 1 {
					t.Fatalf("send retried after storage: %q", requests)
				}
			}
		})
	}
}

func TestTellStatusSenderOnly(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000004"
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/inbox/messages/"+id+"/receipt" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		http.Error(w, `{"error":"not_found","message":"not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", "synthetic-key")
	var out, errOut bytes.Buffer
	code := runMessaging([]string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "tell", "status", id}, strings.NewReader(""), &out, &errOut, nil)
	if code == 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "not_found") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}
