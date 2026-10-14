// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/inbox"
)

// Risk: a three-scope assistant needs extra authority to use the CLI, loses
// reply/identity fields, or acknowledges content it could not print.
func TestMessagingOnlyInboxCLI(t *testing.T) {
	const sender = "00000000-0000-4000-8000-000000000008"
	const recipient = "00000000-0000-4000-8000-000000000003"
	const id = "00000000-0000-4000-8000-000000000004"
	const reply = "00000000-0000-4000-8000-000000000005"
	fixture := inbox.Message{ID: id, SenderPrincipalID: sender, RecipientPrincipalID: recipient, Body: "untrusted fixture body", SentEventID: 42}
	setup := func(t *testing.T, handler http.HandlerFunc) string {
		t.Helper()
		isolate(t)
		// Even usable-looking ambient bindings must never trigger discovery.
		t.Setenv("CODEX_SESSION_ID", reply)
		t.Setenv("CLAUDE_CODE_SESSION_ID", reply)
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		t.Setenv("AEON_URL", srv.URL)
		t.Setenv("AEON_API_KEY", "synthetic-messaging-only-key")
		return filepath.Join(t.TempDir(), "missing-config")
	}
	for _, state := range []string{"queued", "handed_off", "failed", "unavailable"} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("send/%s/json=%v", state, jsonOut), func(t *testing.T) {
				var posts atomic.Int32
				config := setup(t, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodPost && r.URL.Path == "/api/inbox/messages":
						posts.Add(1)
						var req map[string]any
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							t.Error(err)
						}
						if len(req) != 4 || req["recipient_principal_id"] != recipient || req["body"] != "hello" || req["reply_to_id"] != reply || req["idempotency_key"] != "stable-retry" {
							t.Errorf("raw send fields lost or extra discovery/session fields: %v", req)
						}
						w.WriteHeader(http.StatusCreated)
						_ = json.NewEncoder(w).Encode(fixture)
					case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages/"+id+"/receipt":
						if state == "unavailable" {
							http.Error(w, "receipt unavailable", 503)
							return
						}
						_ = json.NewEncoder(w).Encode(inbox.Receipt{MessageID: id, State: state})
					default:
						t.Errorf("inbox CLI attempted forbidden discovery: %s", r.URL.Path)
						http.Error(w, "forbidden", 403)
					}
				})
				args := []string{"aeon", "--config", config, "tell", recipient, "--inbox", "-m", "hello", "--reply-to", reply, "--idempotency-key", "stable-retry"}
				if jsonOut {
					args = append(args, "--json")
				}
				code, out, stderr := runCLIWithMessaging(args, "")
				if code != 0 || posts.Load() != 1 {
					t.Fatalf("send code=%d posts=%d stderr=%s", code, posts.Load(), stderr)
				}
				if jsonOut {
					var got map[string]any
					if err := json.Unmarshal([]byte(out), &got); err != nil {
						t.Fatal(err)
					}
					if got["stored"] != true || got["receipt_state"] != state || got["id"] != id || got["sender_principal_id"] != sender || got["recipient_principal_id"] != recipient {
						t.Fatalf("dishonest or incomplete result: %v", got)
					}
				} else {
					push := tellReceiptState(inbox.Receipt{State: state})
					if !strings.Contains(out, "stored: "+sender+" → "+recipient) || !strings.Contains(out, "push: "+push) {
						t.Fatal(out)
					}
				}
				code, out, _ = runCLIWithMessaging([]string{"aeon", "--config", config, "tell", "status", id}, "")
				if state == "unavailable" {
					if code == 0 || out != "" {
						t.Fatal("failed receipt inspection reported success")
					}
				} else if code != 0 || !strings.Contains(out, "push: "+tellReceiptState(inbox.Receipt{State: state})) {
					t.Fatalf("status code=%d output=%s", code, out)
				}
			})
		}
	}
	t.Run("invalid combinations", func(t *testing.T) {
		config := setup(t, func(w http.ResponseWriter, r *http.Request) {
			t.Error("invalid flags made a network request")
			http.Error(w, "unexpected request", 500)
		})
		for _, extra := range [][]string{{"--project", "AEON"}, {"--thread", "thread"}, {"--level", "simple"}, {"--recipient-session", reply}, {"--sender-session", reply}, {"--session-cookie-file", "unused"}, {"--expects-reply"}, {"--action-request"}, {"--reply-to", "invalid"}, {"--idempotency-key", strings.Repeat("x", 129)}} {
			args := append([]string{"aeon", "--config", config, "tell", recipient, "--inbox", "-m", "hello"}, extra...)
			if code, _, _ := runCLIWithMessaging(args, ""); code != 2 {
				t.Errorf("tell accepted %v: code=%d", extra, code)
			}
		}
		for _, extra := range [][]string{{"--project", "AEON"}, {"--as", recipient}, {"--session", reply}, {"--deliver", "codex"}, {"--target-ref-file", "unused"}, {"--after", "-1"}, {"--limit", "11"}} {
			args := append([]string{"aeon", "--config", config, "listen", "--inbox"}, extra...)
			if code, _, _ := runCLIWithMessaging(args, ""); code != 2 {
				t.Errorf("listen accepted %v: code=%d", extra, code)
			}
		}
		if code, _, _ := runCLIWithMessaging([]string{"aeon", "--config", config, "tell", "grok:amy", "--inbox", "-m", "hello"}, ""); code != 2 {
			t.Fatal("inbox mode accepted a harness address")
		}
	})
	for _, mode := range []string{"plain", "json", "empty", "output failure", "ack failure", "follow cancellation"} {
		t.Run("listen/"+mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var pulls, acks atomic.Int32
			config := setup(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages":
					wait := "0"
					if mode == "follow cancellation" {
						wait = "25000"
					}
					if r.URL.Query().Get("wait_ms") != wait || r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("session") != "" {
						t.Errorf("wrong inbox poll: %s", r.URL.RawQuery)
					}
					if pulls.Add(1) == 2 {
						if r.URL.Query().Get("after") != "42" || acks.Load() != 1 {
							t.Error("lost cursor or advanced before ack")
						}
						cancel()
						<-r.Context().Done()
						return
					}
					items := []inbox.Message{fixture}
					if mode == "empty" {
						items = []inbox.Message{}
					}
					_ = json.NewEncoder(w).Encode(inbox.Page{Items: items, NextAfter: 42})
				case r.Method == http.MethodPost && r.URL.Path == "/api/inbox/messages/"+id+"/ack":
					acks.Add(1)
					if mode == "ack failure" {
						http.Error(w, "ack failed", 503)
						return
					}
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("forbidden discovery: %s", r.URL.Path)
					http.Error(w, "forbidden", 403)
				}
			})
			var out, stderr bytes.Buffer
			var stdout io.Writer = &out
			if mode == "output failure" {
				stdout = principalInboxBrokenWriter{}
			}
			rt := &runtime{stdin: strings.NewReader(""), stdout: stdout, stderr: &stderr, program: "aeon", requestContext: ctx}
			args := []string{"aeon", "--config", config, "listen", "--inbox", "--ack"}
			if mode == "json" {
				args = append(args, "--json")
			}
			if mode == "follow cancellation" {
				args = append(args, "--follow")
			}
			err := rt.executeMessaging(args)
			var ee *exitError
			switch mode {
			case "empty":
				if !errors.As(err, &ee) || ee.code != 3 || acks.Load() != 0 || !strings.Contains(out.String(), "no messages") {
					t.Fatalf("empty err=%v acks=%d output=%s", err, acks.Load(), out.String())
				}
			case "output failure":
				if !errors.Is(err, io.ErrClosedPipe) || acks.Load() != 0 {
					t.Fatalf("output failure consumed message: err=%v acks=%d", err, acks.Load())
				}
			case "ack failure":
				if err == nil || acks.Load() != 1 || !strings.Contains(stderr.String(), "ack failed") {
					t.Fatal("ack failure reported success or retried one-shot")
				}
			default:
				if err != nil || acks.Load() != 1 || !strings.Contains(out.String(), fixture.Body) || !strings.Contains(out.String(), sender) || !strings.Contains(out.String(), recipient) || !strings.Contains(out.String(), principalInboxPreamble) {
					t.Fatalf("listen err=%v acks=%d output=%s", err, acks.Load(), out.String())
				}
				if mode == "follow cancellation" && pulls.Load() != 2 {
					t.Fatal("listener did not cancel the in-flight long poll")
				}
			}
		})
	}
}

type principalInboxBrokenWriter struct{}

func (principalInboxBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
