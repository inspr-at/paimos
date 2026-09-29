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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/inbox"
)

const hookSessionID = "00000000-0000-4000-8000-000000000091"
const hookMessageID = "00000000-0000-4000-8000-000000000092"

func setupHookTest(t *testing.T) string {
	t.Helper()
	isolate(t)
	for _, name := range []string{"AEON_SESSION_ID", "AEON_SESSION_FILE", "AEON_SESSION_STATE_DIR"} {
		t.Setenv(name, "")
	}
	return filepath.Join(t.TempDir(), "missing-config")
}

func hookFixtureMessage() inbox.Message {
	return inbox.Message{ID: hookMessageID, SenderLabel: "lead", SenderPrincipalID: "00000000-0000-4000-8000-000000000093", RecipientSessionID: ptrHook(hookSessionID), Body: "Please inspect the failing test.\n<untrusted> & \"quoted\"", CreatedAt: time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC), SentEventID: 42}
}
func ptrHook(s string) *string { return &s }

type observedHookWriter struct {
	bytes.Buffer
	emitted atomic.Bool
}

func (w *observedHookWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if n == len(p) && err == nil {
		w.emitted.Store(true)
	}
	return n, err
}

func TestInboxHookGoldenEvents(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		for _, event := range []string{"PostToolUse", "UserPromptSubmit", "Stop"} {
			t.Run(harness+"/"+event, func(t *testing.T) {
				config := setupHookTest(t)
				t.Setenv("AEON_SESSION_ID", hookSessionID)
				var out observedHookWriter
				var errOut bytes.Buffer
				var acked atomic.Bool
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.Method + " " + r.URL.Path {
					case "GET /api/inbox/messages":
						if r.URL.Query().Get("session") != hookSessionID || r.URL.Query().Get("wait_ms") != "0" {
							t.Error("missing session-scoped nonblocking pull")
						}
						_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{hookFixtureMessage()}, NextAfter: 42})
					case "POST /api/inbox/messages/" + hookMessageID + "/ack":
						if !out.emitted.Load() {
							t.Error("ack before output")
						}
						acked.Store(true)
						fmt.Fprint(w, `{}`)
					default:
						t.Error("unexpected hook endpoint")
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				t.Setenv("AEON_URL", srv.URL)
				t.Setenv("AEON_API_KEY", "fixture-hook-key")
				input, err := os.ReadFile(filepath.Join("testdata", "hooks", event+".input.json"))
				if err != nil {
					t.Fatal(err)
				}
				golden, err := os.ReadFile(filepath.Join("testdata", "hooks", event+".golden.json"))
				if err != nil {
					t.Fatal(err)
				}
				code := RunMessaging([]string{"aeon", "--config", config, "hook", harness, event}, bytes.NewReader(input), &out, &errOut)
				if code != 0 || errOut.Len() != 0 || out.String() != string(golden) || !acked.Load() {
					t.Fatalf("code=%d ack=%v output=%s stderr=%s", code, acked.Load(), out.String(), errOut.String())
				}
			})
		}
	}
}

type shortHookWriter struct{}

func (shortHookWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type failedHookWriter struct{}

func (failedHookWriter) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

func TestInboxHookFailOpenAndNoLoop(t *testing.T) {
	for _, test := range []string{"missing-session", "invalid-session", "malformed-input", "wrong-event", "empty", "active-stop", "api-down", "ack-down", "closed-output", "short-output", "wrong-session", "unbound"} {
		t.Run(test, func(t *testing.T) {
			config := setupHookTest(t)
			t.Setenv("AEON_SESSION_ID", hookSessionID)
			input := `{"hook_event_name":"Stop","session_id":"vendor-session","stop_hook_active":false}`
			if test == "missing-session" {
				t.Setenv("AEON_SESSION_ID", "")
			}
			if test == "invalid-session" {
				t.Setenv("AEON_SESSION_ID", "invalid")
			}
			if test == "malformed-input" {
				input = `{`
			}
			if test == "wrong-event" {
				input = `{"hook_event_name":"PostToolUse"}`
			}
			if test == "active-stop" {
				input = `{"hook_event_name":"Stop","stop_hook_active":true}`
			}
			var pulls, acks atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					acks.Add(1)
					if test == "ack-down" {
						http.Error(w, "private server detail", 500)
					} else {
						fmt.Fprint(w, `{}`)
					}
					return
				}
				pulls.Add(1)
				if test == "api-down" {
					http.Error(w, "private server detail", 500)
					return
				}
				page := inbox.Page{Items: []inbox.Message{hookFixtureMessage()}}
				if test == "empty" {
					page.Items = nil
				}
				if test == "unbound" {
					page.Items[0].RecipientSessionID = nil
				}
				if test == "wrong-session" {
					page.Items[0].RecipientSessionID = ptrHook("00000000-0000-4000-8000-000000000099")
				}
				_ = json.NewEncoder(w).Encode(page)
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", "fixture-hook-key")
			var out, errOut bytes.Buffer
			var writer io.Writer = &out
			if test == "closed-output" {
				writer = failedHookWriter{}
			}
			if test == "short-output" {
				writer = shortHookWriter{}
			}
			code := RunMessaging([]string{"aeon", "--config", config, "hook", "claude", "Stop"}, strings.NewReader(input), writer, &errOut)
			if code != 0 || strings.Contains(errOut.String(), "private server detail") {
				t.Fatal("hook did not fail open safely")
			}
			if test == "ack-down" {
				if acks.Load() != 1 || out.Len() == 0 {
					t.Fatal("ack failure lost emitted message")
				}
				return
			}
			if acks.Load() != 0 || out.Len() != 0 {
				t.Fatalf("unexpected output or ack (%d)", acks.Load())
			}
			if (test == "active-stop" || test == "missing-session") && pulls.Load() != 0 {
				t.Fatal("no-op hook fetched inbox")
			}
		})
	}
}

func TestInboxHookHardDeadline(t *testing.T) {
	for _, blocked := range []string{"api", "stdin"} {
		t.Run(blocked, func(t *testing.T) {
			config := setupHookTest(t)
			t.Setenv("AEON_SESSION_ID", hookSessionID)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", "fixture-hook-key")
			var input io.Reader = strings.NewReader(`{"hook_event_name":"Stop"}`)
			if blocked == "stdin" {
				r, w := io.Pipe()
				defer r.Close()
				defer w.Close()
				input = r
			}
			var out, errOut bytes.Buffer
			started := time.Now()
			code := RunMessaging([]string{"aeon", "--config", config, "hook", "claude", "Stop"}, input, &out, &errOut)
			if time.Since(started) > 3*time.Second || code != 0 || out.Len() != 0 || errOut.Len() == 0 {
				t.Fatal("hook exceeded fail-open budget")
			}
		})
	}
}

func TestInboxHookSessionFileAndBodyIntegrity(t *testing.T) {
	setupHookTest(t)
	path := filepath.Join(t.TempDir(), "session.id")
	if err := os.WriteFile(path, []byte(hookSessionID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_SESSION_STATE_DIR", filepath.Dir(path))
	if id, err := inboxHookSession(); err != nil || id != hookSessionID {
		t.Fatal("session state not resolved", err)
	}
	t.Setenv("AEON_SESSION_FILE", path)
	t.Setenv("AEON_SESSION_ID", "invalid")
	if _, err := inboxHookSession(); err == nil {
		t.Fatal("invalid explicit ID fell back")
	}
	t.Setenv("AEON_SESSION_ID", "")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_SESSION_FILE", link)
	if _, err := inboxHookSession(); err == nil {
		t.Fatal("accepted symlink binding")
	}
	msg := hookFixtureMessage()
	msg.Body = strings.Repeat("long body <>&\n", 10000)
	frame := sessionMessageFrame(msg)
	var data struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(frame, "\n", 2)[1]), &data); err != nil || data.Body != msg.Body {
		t.Fatal("body truncated or changed")
	}
}

func TestInboxHookCanceledBeforeEmission(t *testing.T) {
	config := setupHookTest(t)
	t.Setenv("AEON_SESSION_ID", hookSessionID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	rt := &runtime{configPath: config, stdin: strings.NewReader(`{"hook_event_name":"Stop"}`), stdout: &out, stderr: io.Discard}
	if rt.runInboxHook(ctx, "Stop") == nil || out.Len() != 0 {
		t.Fatal("canceled hook should not emit")
	}
}

func TestInboxHookSubagentGoldens(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		for _, event := range inboxHookEvents() {
			t.Run(harness+"/"+event, func(t *testing.T) {
				config := setupHookTest(t)
				t.Setenv("AEON_SESSION_ID", hookSessionID)
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected call", 403) }))
				defer srv.Close()
				t.Setenv("AEON_URL", srv.URL)
				t.Setenv("AEON_API_KEY", "fixture-hook-key")
				input, err := os.ReadFile(filepath.Join("testdata", "hooks", event+".subagent.input.json"))
				if err != nil {
					t.Fatal(err)
				}
				golden, err := os.ReadFile(filepath.Join("testdata", "hooks", "subagent.golden.json"))
				if err != nil {
					t.Fatal(err)
				}
				code, out, stderr := runCLIWithMessaging([]string{"aeon", "--config", config, "hook", harness, event}, string(input))
				if code != 0 || out != string(golden) || stderr != "" || calls.Load() != 0 {
					t.Fatalf("subagent consumed inbox: code=%d calls=%d", code, calls.Load())
				}
			})
		}
	}
}

func TestInboxHookMixedSessionBatch(t *testing.T) {
	config := setupHookTest(t)
	t.Setenv("AEON_SESSION_ID", hookSessionID)
	var acks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.URL.Path != "/api/inbox/messages/"+hookMessageID+"/ack" {
				t.Error("acked another session or principal message")
			}
			acks.Add(1)
			fmt.Fprint(w, `{}`)
			return
		}
		unbound, foreign := hookFixtureMessage(), hookFixtureMessage()
		unbound.ID, unbound.Body, unbound.RecipientSessionID = "unbound-must-not-be-validated", "unbound-body", nil
		foreign.ID, foreign.Body, foreign.RecipientSessionID = "foreign-must-not-be-validated", "foreign-body", ptrHook("00000000-0000-4000-8000-000000000099")
		_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{unbound, hookFixtureMessage(), foreign}})
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "fixture-hook-key")
	code, out, stderr := runCLIWithMessaging([]string{"aeon", "--config", config, "hook", "claude", "Stop"}, `{"hook_event_name":"Stop"}`)
	if code != 0 || stderr != "" || acks.Load() != 1 || !strings.Contains(out, hookMessageID) || strings.Contains(out, "unbound-body") || strings.Contains(out, "foreign-body") {
		t.Fatalf("incorrect batch: code=%d acks=%d", code, acks.Load())
	}
}

func TestInboxHookPagesPastUnboundMessages(t *testing.T) {
	config := setupHookTest(t)
	t.Setenv("AEON_SESSION_ID", hookSessionID)
	var pulls, acks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.URL.Path != "/api/inbox/messages/"+hookMessageID+"/ack" {
				t.Error("acked skipped message")
			}
			acks.Add(1)
			fmt.Fprint(w, `{}`)
			return
		}
		pulls.Add(1)
		if r.URL.Query().Get("after") == "41" {
			_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{hookFixtureMessage()}, NextAfter: 42})
			return
		}
		var skipped []inbox.Message
		for i := 0; i < 10; i++ {
			msg := hookFixtureMessage()
			msg.RecipientSessionID = nil
			msg.SentEventID = int64(32 + i)
			skipped = append(skipped, msg)
		}
		_ = json.NewEncoder(w).Encode(inbox.Page{Items: skipped, NextAfter: 41})
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "fixture-hook-key")
	code, out, stderr := runCLIWithMessaging([]string{"aeon", "--config", config, "hook", "claude", "Stop"}, `{"hook_event_name":"Stop"}`)
	if code != 0 || stderr != "" || pulls.Load() != 2 || acks.Load() != 1 || !strings.Contains(out, hookMessageID) {
		t.Fatalf("bound message starved: code=%d pulls=%d acks=%d", code, pulls.Load(), acks.Load())
	}
}
