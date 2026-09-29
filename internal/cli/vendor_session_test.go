// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/inbox"
)

func TestVendorSessionRefPreference(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	if vendorSessionRef("claude") != "" || vendorSessionRef("codex") != "" || ambientVendorSessionRef() != "" {
		t.Fatal("empty environment produced a reference")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "short")
	t.Setenv("CODEX_SESSION_ID", "also-short")
	t.Setenv("CODEX_THREAD_ID", "codex-thread-0123456789")
	if vendorSessionRef("claude") != "" || vendorSessionRef("codex") != "" {
		t.Fatal("short or shadowed reference was accepted")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", " claude-session-0123456789\n")
	t.Setenv("CODEX_SESSION_ID", "codex-session-012345678")
	if vendorSessionRef("claude") != "claude-session-0123456789" || vendorSessionRef("codex") != "codex-session-012345678" || ambientVendorSessionRef() != "claude-session-0123456789" {
		t.Fatal("vendor reference preference changed")
	}
	t.Setenv("CODEX_SESSION_ID", "")
	if vendorSessionRef("codex") != "codex-thread-0123456789" {
		t.Fatal("codex thread was not the fallback")
	}
	if normalizeVendorRef("line\nbreak-session-0001") != "" {
		t.Fatal("newline reference accepted")
	}
}

func TestHarnessRegisterSendsVendorSessionRef(t *testing.T) {
	isolate(t)
	const vendor = "claude-session-register-0001"
	const ref = "local-reference-0000000000000001"
	const lease = "local-lease-00000000000000000000000001"
	t.Setenv("CLAUDE_CODE_SESSION_ID", vendor)
	t.Setenv("CODEX_THREAD_ID", "codex-thread-should-not-win1")
	var calls []transcriptRequest
	srv := transcriptFixture(t, "ticket", "note", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	path := filepath.Join(t.TempDir(), "registration.json")
	if err := os.WriteFile(path, []byte(`{"harness_session_ref":"`+ref+`","worker_lease":"`+lease+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "harness", "register", "--project", "AEON", "--agent", "worker", "--harness", "claude", "--host", "local", "--registration-file", path}
	code, out, stderr := runCLI(args, "")
	if code != 0 || stderr != "" {
		t.Fatalf("register exit %d out %q stderr %q", code, out, stderr)
	}
	body := registrationBody(t, calls)
	if body["harness_session_ref"] != ref || body["vendor_session_ref"] != vendor || body["worker_lease"] != lease {
		t.Fatalf("registration body %#v", body)
	}
	if strings.Contains(out+stderr, vendor) || strings.Contains(out+stderr, ref) || strings.Contains(out+stderr, lease) {
		t.Fatal("registration secret appeared in output")
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", ref)
	calls = nil
	code, out, stderr = runCLI(args, "")
	if code != 0 || registrationBody(t, calls)["vendor_session_ref"] != nil || strings.Contains(out+stderr, ref) {
		t.Fatalf("equal vendor ref was sent: exit %d out %q stderr %q", code, out, stderr)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_SESSION_ID", "codex-session-register-001")
	t.Setenv("CODEX_THREAD_ID", "codex-thread-register-0002")
	codex := append([]string{}, args...)
	for i, arg := range codex {
		if arg == "claude" {
			codex[i] = "codex"
		}
	}
	calls = nil
	code, out, stderr = runCLI(codex, "")
	if code != 0 || registrationBody(t, calls)["vendor_session_ref"] != "codex-session-register-001" || strings.Contains(out+stderr, "codex-session-register-001") {
		t.Fatalf("codex vendor ref: exit %d out %q stderr %q body %#v", code, out, stderr, registrationBody(t, calls))
	}
}

func TestRunHeartbeatRecordsVendorSessionRef(t *testing.T) {
	dir := t.TempDir()
	const vendor = "claude-session-heartbeat01"
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, stdout, stderr := heartbeatRuntime(t, srv)
	t.Setenv("CLAUDE_CODE_SESSION_ID", vendor)
	opts := heartbeatTestOptions(dir)
	if err := rt.runHeartbeat(t.Context(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	for _, call := range calls {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/harness-sessions") {
			body = call.body
		}
	}
	if body["vendor_session_ref"] != vendor || body["harness_session_ref"] == vendor {
		t.Fatalf("heartbeat registration %#v", body)
	}
	if strings.Contains(stdout.String()+stderr.String(), vendor) {
		t.Fatal("heartbeat output contained the vendor session")
	}
}

func TestInboxHookBindsVendorSession(t *testing.T) {
	const (
		vendorA = "claude-session-aaaaaaaa"
		vendorB = "claude-session-bbbbbbbb"
		aeonA   = "00000000-0000-4000-8000-0000000000a1"
		aeonB   = "00000000-0000-4000-8000-0000000000b2"
		msgA    = "00000000-0000-4000-8000-0000000000c3"
		msgB    = "00000000-0000-4000-8000-0000000000d4"
	)
	config := setupHookTest(t)
	var lookups, pulls int
	var acked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/inbox/session-binding":
			lookups++
			var in struct {
				Ref string `json:"harness_session_ref"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			session := map[string]string{vendorA: aeonA, vendorB: aeonB}[in.Ref]
			if session == "" {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":"not found"}`)
				return
			}
			fmt.Fprintf(w, `{"session_id":%q}`, session)
		case "GET /api/inbox/messages":
			pulls++
			if r.URL.Query().Get("exact_session") != "true" {
				t.Error("hook pull was not exact")
			}
			msg := inbox.Message{SenderLabel: "lead", SenderPrincipalID: "00000000-0000-4000-8000-000000000093", CreatedAt: hookFixtureMessage().CreatedAt, SentEventID: 42}
			switch r.URL.Query().Get("session") {
			case aeonA:
				msg.ID, msg.Body, msg.RecipientSessionID = msgA, "for the first session", ptrHook(aeonA)
			case aeonB:
				msg.ID, msg.Body, msg.RecipientSessionID = msgB, "for the second session", ptrHook(aeonB)
			default:
				t.Errorf("pull for %s", r.URL.Query().Get("session"))
				msg.ID = msgA
			}
			_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{msg}, NextAfter: 42})
		default:
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack") {
				acked = append(acked, r.URL.Path)
				fmt.Fprint(w, `{}`)
				return
			}
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "fixture-hook-key")
	run := func(harness, session, input string) (string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := RunMessaging([]string{"aeon", "--config", config, "hook", harness, "UserPromptSubmit"}, strings.NewReader(input), &out, &errOut)
		if code != 0 {
			t.Fatalf("hook exit %d %s", code, errOut.String())
		}
		return out.String(), errOut.String()
	}
	input := func(session, extra string) string {
		return `{"hook_event_name":"UserPromptSubmit","session_id":"` + session + `"` + extra + `}`
	}
	out, errOut := run("claude", vendorA, input(vendorA, ""))
	if !strings.Contains(out, msgA) || strings.Contains(out, msgB) || errOut != "" || lookups != 1 || pulls != 1 || len(acked) != 1 || !strings.HasSuffix(acked[0], "/"+msgA+"/ack") {
		t.Fatalf("first session out %q err %q lookups %d pulls %d acks %v", out, errOut, lookups, pulls, acked)
	}
	out, errOut = run("codex", vendorB, input(vendorB, ""))
	if !strings.Contains(out, msgB) || strings.Contains(out, msgA) || lookups != 2 || pulls != 2 || len(acked) != 2 || !strings.HasSuffix(acked[1], "/"+msgB+"/ack") {
		t.Fatalf("second session out %q err %q acks %v", out, errOut, acked)
	}
	before := lookups + pulls
	out, errOut = run("claude", "claude-session-unknown1", input("claude-session-unknown1", ""))
	if out != "" || errOut != "" || lookups+pulls != before+1 || len(acked) != 2 {
		t.Fatalf("unknown vendor fetched: out %q err %q lookups %d pulls %d", out, errOut, lookups, pulls)
	}
	before = lookups + pulls
	out, errOut = run("claude", vendorA, input(vendorA, `,"agent_id":"sub"`))
	if out != "" || errOut != "" || lookups+pulls != before || len(acked) != 2 {
		t.Fatal("subagent consumed the parent inbox")
	}
	t.Setenv("AEON_SESSION_ID", aeonA)
	before = lookups
	out, errOut = run("claude", vendorB, input(vendorB, ""))
	if !strings.Contains(out, msgA) || strings.Contains(out, msgB) || lookups != before {
		t.Fatalf("explicit session lost to vendor id: out %q lookups %d", out, lookups)
	}
	t.Setenv("AEON_SESSION_ID", "")
	t.Setenv("AEON_SESSION_FILE", filepath.Join(t.TempDir(), "missing-session"))
	before = lookups
	out, errOut = run("claude", vendorA, input(vendorA, ""))
	if out != "" || errOut != "" || lookups != before {
		t.Fatalf("missing explicit file fell through: out %q err %q lookups %d", out, errOut, lookups)
	}
}

func TestTellFillsResolvedSenderSession(t *testing.T) {
	const (
		projectID = "00000000-0000-4000-8000-000000000002"
		messageID = "00000000-0000-4000-8000-000000000004"
		sessionID = "00000000-0000-4000-8000-000000000091"
		vendor    = "claude-session-tell-00001"
	)
	const callerID = "00000000-0000-4000-8000-000000000003"
	type sessionFixture struct {
		projectID string
		agent     string
		status    int
		stopped   bool
		archived  bool
	}
	serve := func(t *testing.T, binding int, fixtures ...sessionFixture) (string, *[]string) {
		t.Helper()
		session := sessionFixture{projectID: projectID, status: http.StatusOK}
		if len(fixtures) > 0 {
			session = fixtures[0]
		}
		seen := []string{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := map[string]any{}
			raw, _ := io.ReadAll(r.Body)
			if len(bytes.TrimSpace(raw)) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
			switch {
			case r.URL.Path == "/api/kinds":
				fmt.Fprint(w, `{"items":[{"id":"00000000-0000-4000-8000-000000000001","slug":"project"}]}`)
			case r.URL.Path == "/api/me":
				fmt.Fprintf(w, `{"principal":{"id":%q,"name":"receiver"}}`, callerID)
			case strings.HasPrefix(r.URL.Path, "/api/nodes"):
				fmt.Fprint(w, `{"items":[{"id":"`+projectID+`","kind_id":"00000000-0000-4000-8000-000000000001","key":"AEON-1","title":"AEON","fields":{"project_key":"AEON"}}]}`)
			case r.URL.Path == "/api/inbox/session-binding":
				seen = append(seen, "binding "+fmt.Sprint(body["harness_session_ref"]))
				if binding != http.StatusOK {
					w.WriteHeader(binding)
					fmt.Fprint(w, `{"error":"not found"}`)
					return
				}
				fmt.Fprintf(w, `{"session_id":%q}`, sessionID)
			case r.Method == http.MethodGet && r.URL.Path == "/api/projects/"+projectID+"/harness-sessions/"+sessionID+"/lookup":
				seen = append(seen, "session check")
				if session.status != http.StatusOK || session.projectID != projectID {
					status := session.status
					if session.projectID != projectID {
						status = http.StatusNotFound
					}
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":"session unavailable"}`)
					return
				}
				agent := session.agent
				if agent == "" {
					agent = callerID
				}
				out := map[string]any{"id": sessionID, "project_id": session.projectID, "agent_principal_id": agent, "stopped_at": nil, "archived_at": nil}
				if session.stopped {
					out["stopped_at"] = "2026-09-29T12:00:00Z"
				}
				if session.archived {
					out["archived_at"] = "2026-09-29T12:00:00Z"
				}
				_ = json.NewEncoder(w).Encode(out)
			default:
				item := r.Method + " " + r.URL.Path
				if len(raw) > 0 {
					item += " " + string(raw)
				}
				seen = append(seen, item)
				if strings.HasSuffix(r.URL.Path, "/receipt") {
					fmt.Fprintf(w, `{"message_id":%q,"state":"queued"}`, messageID)
					return
				}
				if body["sender_session_id"] != nil {
					if session.projectID != projectID {
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `{"error":"not found"}`)
						return
					}
					if session.stopped || session.archived {
						w.WriteHeader(http.StatusConflict)
						fmt.Fprint(w, `{"error":{"code":"session_ended","message":"This session has ended."}}`)
						return
					}
				}
				fmt.Fprintf(w, `{"id":%q,"sender_principal_id":"00000000-0000-4000-8000-000000000008","recipient_principal_id":"00000000-0000-4000-8000-000000000003","from":"paimos:sender","to":"codex:receiver","body":"hello","thread_id":%q,"hop":1,"sent_event_id":42,"is_action_request":false,"expects_reply":false,"delivery_level":"simple","status":"accepted","reply_obligation":"none","human_resolution_outcome":null,"created_at":"2026-09-25T00:00:00Z"}`, messageID, messageID)
			}
		}))
		t.Cleanup(srv.Close)
		t.Setenv("AEON_URL", srv.URL)
		t.Setenv("AEON_API_KEY", "fixture-key")
		return srv.URL, &seen
	}
	run := func(t *testing.T, extra []string) (int, string, string, []string) {
		t.Helper()
		isolate(t)
		t.Setenv("CLAUDE_CODE_SESSION_ID", vendor)
		_, seen := serve(t, http.StatusOK)
		var out, errOut bytes.Buffer
		args := append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}, extra...)
		code := RunMessaging(args, strings.NewReader(""), &out, &errOut)
		return code, out.String(), errOut.String(), *seen
	}
	t.Run("reply", func(t *testing.T) {
		code, out, errOut, seen := run(t, []string{"tell", "codex:receiver", "--project", "AEON", "--reply-to", messageID, "--idempotency-key", "retry", "-m", "hello"})
		if code != 0 || errOut != "" || strings.Contains(out, vendor) || len(seen) < 3 || seen[0] != "binding "+vendor || seen[1] != "session check" || !strings.Contains(seen[2], `"sender_session_id":"`+sessionID+`"`) || strings.Contains(seen[2], vendor) {
			t.Fatalf("exit %d out %q err %q seen %q", code, out, errOut, seen)
		}
	})
	t.Run("ordinary", func(t *testing.T) {
		code, out, errOut, seen := run(t, []string{"tell", "codex:receiver", "--project", "AEON", "--idempotency-key", "retry", "-m", "hello"})
		if code != 0 || errOut != "" || strings.Contains(out, vendor) || len(seen) < 3 || seen[0] != "binding "+vendor || seen[1] != "session check" || !strings.Contains(seen[2], `"sender_session_id":"`+sessionID+`"`) {
			t.Fatalf("ordinary tell missed its binding: exit %d out %q err %q seen %q", code, out, errOut, seen)
		}
	})
	for _, source := range []string{"flag", "id", "file", "state-dir", "codex-session", "codex-thread", "unbound", "missing-file", "invalid-id", "invalid-file", "symlink", "unavailable"} {
		t.Run(source, func(t *testing.T) {
			isolate(t)
			t.Setenv("CLAUDE_CODE_SESSION_ID", vendor)
			dir := t.TempDir()
			file := filepath.Join(dir, "session.id")
			if err := os.WriteFile(file, []byte(sessionID+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"aeon", "--config", filepath.Join(dir, "missing"), "tell", "00000000-0000-4000-8000-000000000003", "--project", "AEON", "-m", "hello"}
			bound, lookup, fail := true, false, false
			bindingStatus := http.StatusOK
			switch source {
			case "flag":
				args = append(args, "--sender-session", sessionID)
				t.Setenv("AEON_SESSION_ID", "invalid") // Explicit flag wins.
			case "id":
				t.Setenv("AEON_SESSION_ID", sessionID)
				t.Setenv("AEON_SESSION_FILE", filepath.Join(dir, "missing"))
			case "file":
				t.Setenv("AEON_SESSION_FILE", file)
			case "state-dir":
				t.Setenv("AEON_SESSION_STATE_DIR", dir)
			case "codex-session", "codex-thread":
				t.Setenv("CLAUDE_CODE_SESSION_ID", "")
				name := "CODEX_SESSION_ID"
				if source == "codex-thread" {
					name = "CODEX_THREAD_ID"
				}
				t.Setenv(name, vendor)
				lookup = true
			case "unbound":
				t.Setenv("CLAUDE_CODE_SESSION_ID", "")
				bound = false
			case "missing-file":
				t.Setenv("AEON_SESSION_FILE", filepath.Join(dir, "missing"))
				bound = false
			case "invalid-id":
				t.Setenv("AEON_SESSION_ID", "invalid")
				bound = false
			case "invalid-file":
				if err := os.WriteFile(file, []byte("invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AEON_SESSION_FILE", file)
				bound = false
			case "symlink":
				link := filepath.Join(dir, "link")
				if err := os.Symlink(file, link); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AEON_SESSION_FILE", link)
				bound = false
			case "unavailable":
				bindingStatus, lookup, fail = http.StatusServiceUnavailable, true, true
			}
			_, seen := serve(t, bindingStatus)
			var out, errOut bytes.Buffer
			code := RunMessaging(args, strings.NewReader(""), &out, &errOut)
			joined := strings.Join(*seen, "\n")
			if (code != 0) != fail || strings.Contains(out.String()+errOut.String(), vendor) || strings.Contains(joined, "binding "+vendor) != lookup {
				t.Fatalf("exit %d; binding lookup or output disagrees with %s", code, source)
			}
			if fail {
				if strings.Contains(joined, "POST /api/projects/") {
					t.Fatal("invalid binding sent a message")
				}
			} else if strings.Contains(joined, `"sender_session_id":"`+sessionID+`"`) != bound {
				t.Fatalf("wrong attribution for %s", source)
			}
			if (source == "invalid-id" || source == "invalid-file" || source == "symlink") && (!strings.Contains(errOut.String(), "source is unusable") || strings.Count(errOut.String(), "\n") != 1 || strings.Contains(joined, "session check") || strings.Contains(joined, "binding ")) {
				t.Fatalf("corrupt ambient source: exit %d err %q calls %q", code, errOut.String(), *seen)
			}
		})
	}
	for _, state := range []struct {
		name    string
		session sessionFixture
		note    string
		fail    bool
	}{
		{"same-project", sessionFixture{projectID: projectID, status: http.StatusOK}, "", false},
		{"other-project", sessionFixture{projectID: "00000000-0000-4000-8000-000000000099", status: http.StatusOK}, "unavailable in the target project", false},
		{"stopped", sessionFixture{projectID: projectID, status: http.StatusOK, stopped: true}, "has ended", false},
		{"archived", sessionFixture{projectID: projectID, status: http.StatusOK, archived: true}, "has ended", false},
		{"missing", sessionFixture{projectID: projectID, status: http.StatusNotFound}, "unavailable in the target project", false},
		{"read-forbidden", sessionFixture{projectID: projectID, status: http.StatusForbidden}, "not readable", false},
		{"other-agent", sessionFixture{projectID: projectID, status: http.StatusOK, agent: "00000000-0000-4000-8000-0000000000aa"}, "another agent", false},
		{"read-failed", sessionFixture{projectID: projectID, status: http.StatusServiceUnavailable}, "", true},
	} {
		for _, source := range []string{"id", "file", "state-dir", "claude", "codex-session", "codex-thread", "flag"} {
			t.Run(state.name+"/"+source, func(t *testing.T) {
				isolate(t)
				dir := t.TempDir()
				file := filepath.Join(dir, "session.id")
				if err := os.WriteFile(file, []byte(sessionID+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				// A configured source must not fall through to this vendor binding.
				t.Setenv("CLAUDE_CODE_SESSION_ID", vendor)
				args := []string{"aeon", "--config", filepath.Join(dir, "missing"), "--json", "tell", "codex:receiver", "--project", "AEON", "-m", "hello"}
				switch source {
				case "id":
					t.Setenv("AEON_SESSION_ID", sessionID)
				case "file":
					t.Setenv("AEON_SESSION_FILE", file)
				case "state-dir":
					t.Setenv("AEON_SESSION_STATE_DIR", dir)
				case "codex-session", "codex-thread":
					t.Setenv("CLAUDE_CODE_SESSION_ID", "")
					if source == "codex-session" {
						t.Setenv("CODEX_SESSION_ID", vendor)
					} else {
						t.Setenv("CODEX_THREAD_ID", vendor)
					}
				case "flag":
					args = append(args, "--sender-session", sessionID)
					t.Setenv("AEON_SESSION_ID", "invalid")
				}
				_, seen := serve(t, http.StatusOK, state.session)
				var out, errOut bytes.Buffer
				code := RunMessaging(args, strings.NewReader(""), &out, &errOut)
				joined := strings.Join(*seen, "\n")
				bound, fail, note := state.name == "same-project", state.fail, state.note
				if source == "flag" {
					bound, note = true, ""
					fail = state.name == "other-project" || state.session.stopped || state.session.archived
					if strings.Contains(joined, "session check") || strings.Contains(joined, "binding ") {
						t.Fatal("explicit sender session used ambient resolution")
					}
					if fail && (!strings.Contains(errOut.String(), "--sender-session") || !strings.Contains(errOut.String(), "active session in the target project")) {
						t.Fatalf("explicit session failure lacks context: %q", errOut.String())
					}
				}
				if (code != 0) != fail || strings.Contains(joined, `"sender_session_id":"`+sessionID+`"`) != bound {
					t.Fatalf("exit %d, out %q, stderr %q, calls %q", code, out.String(), errOut.String(), *seen)
				}
				if !fail {
					if !json.Valid(out.Bytes()) || !strings.Contains(joined, "POST /api/projects/"+projectID+"/messages") || !strings.Contains(joined, "/receipt") {
						t.Fatalf("tell did not succeed: out %q, calls %q", out.String(), *seen)
					}
					if note == "" && errOut.Len() != 0 || note != "" && (!strings.Contains(errOut.String(), note) || strings.Count(errOut.String(), "\n") != 1) {
						t.Fatalf("unexpected note: %q", errOut.String())
					}
				} else if source != "flag" && strings.Contains(joined, "POST /api/projects/") {
					t.Fatal("failed session check sent a message")
				}
				wantLookup := source == "claude" || strings.HasPrefix(source, "codex-")
				if strings.Contains(joined, "binding ") != wantLookup || strings.Contains(out.String()+errOut.String(), vendor) {
					t.Fatal("vendor binding lookup or output was incorrect")
				}
			})
		}
	}
	t.Run("miss", func(t *testing.T) {
		isolate(t)
		t.Setenv("CLAUDE_CODE_SESSION_ID", vendor)
		_, seen := serve(t, http.StatusNotFound)
		var out, errOut bytes.Buffer
		code := RunMessaging([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "tell", "codex:receiver", "--project", "AEON", "--reply-to", messageID, "--idempotency-key", "retry", "-m", "hello"}, strings.NewReader(""), &out, &errOut)
		joined := strings.Join(*seen, "\n")
		if code != 0 || strings.Contains(out.String()+errOut.String(), vendor) || !strings.Contains(joined, "binding "+vendor) || strings.Contains(joined, "sender_session_id") {
			t.Fatalf("miss exit %d out %q err %q seen %q", code, out.String(), errOut.String(), *seen)
		}
	})
}

func registrationBody(t *testing.T, calls []transcriptRequest) map[string]any {
	t.Helper()
	for _, call := range calls {
		if call.method == http.MethodPost && strings.Contains(call.path, "/harness-sessions") && !strings.Contains(call.path, "/harness-sessions/") {
			return call.body
		}
	}
	t.Fatal("registration request missing")
	return nil
}
