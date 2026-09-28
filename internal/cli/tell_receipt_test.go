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
		name, receipt, plain, state, reason string
		send, jsonOutput                    bool
	}{
		{"queued status", `{"message_id":"` + id + `","state":"queued"}`, "queued", "queued", "", false, false},
		{"delivered status JSON", `{"message_id":"` + id + `","state":"handed_off","handed_off_at":"2026-09-28T10:00:00Z"}`, "delivered", "handed_off", "", false, true},
		{"failed status", `{"message_id":"` + id + `","state":"failed","failure_reason":"target_missing"}`, "failed/target_missing", "failed", "target_missing", false, false},
		{"queued send JSON", `{"message_id":"` + id + `","state":"queued"}`, "queued", "queued", "", true, true},
		{"queued send", `{"message_id":"` + id + `","state":"queued"}`, "queued", "queued", "", true, false},
		{"delivered send", `{"message_id":"` + id + `","state":"handed_off"}`, "delivered", "handed_off", "", true, false},
		{"failed send JSON", `{"message_id":"` + id + `","state":"failed","failure_reason":"target_missing"}`, "failed/target_missing", "failed", "target_missing", true, true},
		{"failed send", `{"message_id":"` + id + `","state":"failed","failure_reason":"target_missing"}`, "failed/target_missing", "failed", "target_missing", true, false},
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
					fmt.Fprint(w, `{"id":"`+id+`","sender_principal_id":"00000000-0000-4000-8000-000000000008","recipient_principal_id":"00000000-0000-4000-8000-000000000003","from":"paimos:sender","to":"codex:receiver","body":"synthetic","thread_id":"`+id+`","hop":1,"status":"accepted","created_at":"2026-09-28T10:00:00Z"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages/"+id+"/receipt":
					fmt.Fprint(w, tc.receipt)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("PAIMOS_URL", srv.URL)
			t.Setenv("PAIMOS_API_KEY", "synthetic-key")
			args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing")}
			if tc.jsonOutput {
				args = append(args, "--json")
			}
			if tc.send {
				args = append(args, "tell", "codex:receiver", "--project", "AEON", "-m", "synthetic")
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
					if got["stored"] != true || got["receipt_state"] != tc.plain || got["delivered"] != (tc.state == "handed_off") || got["failure_reason"] != nil && got["failure_reason"] != tc.reason {
						t.Fatalf("send JSON %v", got)
					}
				} else if got["state"] != tc.state || got["message_id"] != id {
					t.Fatalf("status JSON %v", got)
				}
			}
			if requests[len(requests)-1] != "GET /api/inbox/messages/"+id+"/receipt" {
				t.Fatalf("receipt request missing: %q", requests)
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
