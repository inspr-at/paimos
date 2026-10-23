// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/inbox"
)

// Subtest of the registered inbox CLI behaviour test: a status must retain its
// exact lane/goal identity and unknown ETA, and failures must remain failures.
func testLaneStatusCLI(t *testing.T) {
	const recipient = "00000000-0000-4000-8000-000000000003"
	const session = "00000000-0000-4000-8000-000000000005"
	const target = "00000000-0000-4000-8000-000000000006"
	const messageID = "00000000-0000-4000-8000-000000000007"
	const project = "00000000-0000-4000-8000-000000000008"
	for _, mode := range []string{"inbox", "project", "unknown ETA", "failed write", "failed query", "silent", "silent JSON", "latest", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			isolate(t)
			var posts, queries atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "invalid" {
					t.Errorf("invalid flags made a request: %s", r.URL.Path)
					http.Error(w, "unexpected", 500)
					return
				}
				switch {
				case r.URL.Path == "/api/projects/lookup":
					if r.URL.Query().Get("ref") != "AEON" {
						t.Error("project reference lost")
					}
					fmt.Fprintf(w, `{"id":%q}`, project)
				case r.URL.Path == "/api/node-keys/AEON-1629":
					fmt.Fprintf(w, `{"id":%q}`, target)
				case r.Method == http.MethodPost && (r.URL.Path == "/api/inbox/messages" || r.URL.Path == "/api/projects/"+project+"/messages"):
					posts.Add(1)
					var request struct {
						Kind, Body, Key, Session, Recipient, To string
						Status                                  *inbox.LaneStatus
					}
					var raw map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
						t.Error(err)
						return
					}
					for key, dst := range map[string]any{"kind": &request.Kind, "body": &request.Body, "idempotency_key": &request.Key, "sender_session_id": &request.Session, "recipient_principal_id": &request.Recipient, "to": &request.To, "lane_status": &request.Status} {
						if b, ok := raw[key]; ok {
							if err := json.Unmarshal(b, dst); err != nil {
								t.Error(err)
							}
						}
					}
					if request.Kind != "status" || request.Session != session || request.Key != "daily-stable" || request.Status == nil || request.Status.TargetNodeID != target || request.Status.Done != "implemented" || request.Status.Next != "verify" || request.Status.BlockerOwner != "LEAD" || request.Body != request.Status.Line() {
						t.Errorf("status identity/fields lost: %+v", request)
					}
					if mode == "project" && request.To != "codex:lead" || mode != "project" && request.Recipient != recipient {
						t.Error("status recipient changed")
					}
					if mode == "unknown ETA" && request.Status.ETA != nil || mode != "unknown ETA" && request.Status.ETA == nil {
						t.Error("ETA nullability lost")
					}
					if mode == "failed write" {
						http.Error(w, "write failed", 503)
						return
					}
					w.WriteHeader(201)
					sid := session
					_ = json.NewEncoder(w).Encode(inbox.Message{ID: messageID, Kind: request.Kind, LaneStatus: request.Status, SenderSessionID: &sid})
				case r.URL.Path == "/api/inbox/messages/"+messageID+"/receipt":
					fmt.Fprintf(w, `{"message_id":%q,"state":"queued"}`, messageID)
				case r.URL.Path == "/api/inbox/lanes":
					queries.Add(1)
					if mode == "failed query" {
						http.Error(w, "read failed", 503)
						return
					}
					q := r.URL.Query()
					if q.Get("target") != target || q.Get("limit") != "2" {
						t.Error("lost bounded target filter")
					}
					if mode != "latest" && q.Get("silent_minutes") != "45" || mode == "latest" && q.Has("silent_minutes") {
						t.Error("lost strict silence filter")
					}
					_ = json.NewEncoder(w).Encode(inbox.LanePage{AsOf: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC), Items: []inbox.Lane{{SessionID: session, TargetNodeID: target, Label: "JoeDesk", SilentSeconds: 2701}}, NextAfter: session + "/" + target})
				default:
					t.Errorf("unexpected status discovery: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", "synthetic-lane-key")
			config := filepath.Join(t.TempDir(), "missing-config")
			base := []string{"aeon", "--config", config}
			if mode == "invalid" {
				for _, flags := range [][]string{
					{"tell", recipient, "--inbox", "--kind", "status"},
					{"tell", recipient, "--inbox", "--kind", "chat"},
					{"tell", recipient, "--inbox", "--kind", "status", "--target", target, "--done", "x", "--next", "y", "--eta", "tomorrow"},
					{"tell", recipient, "--inbox", "--kind", "status", "--target", target, "--done", "x", "--next", "y", "--eta", "unknown"},
					{"tell", recipient, "--inbox", "--kind", "status", "-m", "free chat"},
					{"lanes", "silent", "--minutes", "0"},
					{"lanes", "--limit", "101"},
					{"lanes", "--after", "broken"},
				} {
					if code, _, _ := runCLIWithMessaging(append(append([]string{}, base...), flags...), ""); code != 2 {
						t.Errorf("invalid flags accepted: %v (code %d)", flags, code)
					}
				}
				return
			}
			if mode == "silent" || mode == "silent JSON" || mode == "latest" || mode == "failed query" {
				args := append(base, "lanes")
				if mode != "latest" {
					args = append(args, "silent", "--minutes", "45")
				}
				args = append(args, "--target", target, "--limit", "2")
				if mode == "silent JSON" {
					args = append(args, "--json")
				}
				code, out, stderr := runCLIWithMessaging(args, "")
				if mode == "failed query" {
					if code == 0 || out != "" {
						t.Fatal("failed lane query reported success")
					}
					return
				}
				if code != 0 || queries.Load() != 1 || !strings.Contains(out, "JoeDesk") || !strings.Contains(out, "next_after") {
					t.Fatalf("lane query code %d: %s %s", code, out, stderr)
				}
				return
			}
			args := append(base, "tell", recipient, "--inbox")
			if mode == "project" {
				args = append(base, "tell", "codex:lead", "--project", "AEON")
			}
			eta := "2026-10-23T18:00:00Z"
			if mode == "unknown ETA" {
				eta = "unknown"
			}
			args = append(args, "--kind", "status", "--target", "AEON-1629", "--sender-session", session, "--done", "implemented", "--blocker", "decision needed", "--blocker-owner", "LEAD", "--next", "verify", "--eta", eta, "--idempotency-key", "daily-stable", "--json")
			code, out, stderr := runCLIWithMessaging(args, "")
			if mode == "failed write" {
				if code == 0 || out != "" || posts.Load() != 1 {
					t.Fatal("failed status write reported success")
				}
				return
			}
			var result map[string]any
			if code != 0 || posts.Load() != 1 || json.Unmarshal([]byte(out), &result) != nil || result["stored"] != true || result["receipt_state"] != "queued" || result["kind"] != "status" || result["sender_session_id"] != session {
				t.Fatalf("status output code %d: %s %s", code, out, stderr)
			}
		})
	}
}
