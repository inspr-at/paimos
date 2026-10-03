// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/rules"
)

type hbCall struct {
	method string
	path   string
	lease  string
	query  string
	body   map[string]any
}

func heartbeatFixture(t *testing.T, calls *[]hbCall, status, inbox string) *httptest.Server {
	t.Helper()
	project := map[string]any{"id": transcriptProjectID, "key": "PRJ-1", "kind_id": "project-kind", "title": "AEON", "fields": map[string]any{"project_key": "AEON"}}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		var body map[string]any
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("decode %s %s: %v", r.Method, r.URL.Path, err)
			}
		}
		*calls = append(*calls, hbCall{method: r.Method, path: r.URL.Path, lease: r.Header.Get("X-Aeon-Worker-Lease"), query: r.URL.RawQuery, body: body})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			_, _ = w.Write([]byte(`{"items":[{"id":"project-kind","slug":"project"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{project}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/models":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": transcriptEntryID, "harness": "codex", "model": "fixture-model", "effort": "high", "enabled": true}})
		case r.URL.Path == "/api/me/leaving-at" || r.URL.Path == "/api/me/agent-pause-settings":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/me":
			_, _ = w.Write([]byte(`{"principal":{"id":"44444444-4444-4444-8444-444444444444","name":"worker"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/"+transcriptProjectID+"/harness-sessions":
			_, _ = w.Write([]byte(`{"id":"` + transcriptSessionID + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/"+transcriptProjectID+"/harness-sessions/"+transcriptSessionID:
			if status == "" {
				status = `{"activity_sequence":0}`
			}
			_, _ = w.Write([]byte(status))
		case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages":
			if inbox == "" {
				inbox = `{"items":[],"next_after":0}`
			}
			_, _ = w.Write([]byte(inbox))
		case strings.HasPrefix(r.URL.Path, "/api/projects/"+transcriptProjectID+"/harness-sessions/"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func heartbeatRuntime(t *testing.T, srv *httptest.Server) (*runtime, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	isolate(t)
	// Session-index maintenance must not touch the operator's real home.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	var stdout, stderr bytes.Buffer
	return &runtime{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr, program: "aeon"}, &stdout, &stderr
}

func claudeUsagePath(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, "projects", "-work-slug", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func heartbeatTestOptions(dir string) heartbeatOptions {
	return heartbeatOptions{
		OwnerPID:       4242,
		Interval:       1,
		StateDir:       filepath.Join(dir, "state"),
		Project:        "AEON",
		Agent:          "worker",
		Harness:        "claude",
		Host:           "test-host",
		Phase:          "working",
		Activity:       "busy",
		Management:     "unmanaged",
		Role:           "worker",
		CodexIndex:     filepath.Join(dir, "missing-index.jsonl"),
		ClaudeProjects: filepath.Join(dir, "missing-projects"),
	}
}

func hbWhere(calls []hbCall, method, suffix string) []hbCall {
	var out []hbCall
	for _, call := range calls {
		if call.method == method && strings.HasSuffix(call.path, suffix) {
			out = append(out, call)
		}
	}
	return out
}

func TestRunHeartbeatRecordsWorktreeInstructionHashes(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	agentsBody := []byte("do-not-leak-this-instruction-body")
	claudeBody := []byte("claude-instruction-stays-local")
	if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), agentsBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "CLAUDE.md"), claudeBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "nested", "AGENTS.md"), []byte("nested-instruction-not-recorded"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Worktree = work
	if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	}); err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	posts := hbWhere(calls, http.MethodPost, "/provenance")
	if len(posts) != 1 || posts[0].lease == "" {
		t.Fatalf("provenance posts %d", len(posts))
	}
	raw, err := json.Marshal(posts[0].body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	agentsHash := sha256.Sum256(agentsBody)
	claudeHash := sha256.Sum256(claudeBody)
	nestedHash := sha256.Sum256([]byte("nested-instruction-not-recorded"))
	if !strings.Contains(text, hex.EncodeToString(agentsHash[:])) || !strings.Contains(text, hex.EncodeToString(claudeHash[:])) || !strings.Contains(text, "AGENTS.md") || !strings.Contains(text, "CLAUDE.md") {
		t.Fatal("instruction hashes were not recorded")
	}
	if strings.Contains(text, "do-not-leak-this-instruction-body") || strings.Contains(text, "claude-instruction-stays-local") || strings.Contains(text, "nested-instruction-not-recorded") || strings.Contains(text, hex.EncodeToString(nestedHash[:])) || strings.Contains(text, work) || strings.Contains(text, posts[0].lease) {
		t.Fatal("provenance request leaked content, a nested file, a path, or the lease")
	}
	if strings.Contains(stderr.String(), work) || strings.Contains(stderr.String(), "do-not-leak") {
		t.Fatal("stderr leaked the worktree")
	}

	emptyDir := filepath.Join(dir, "empty")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var quiet []hbCall
	quietSrv := heartbeatFixture(t, &quiet, "", "")
	defer quietSrv.Close()
	quietRT, _, _ := heartbeatRuntime(t, quietSrv)
	quietRoot := filepath.Join(dir, "quiet-root")
	if err := os.Mkdir(quietRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	quietOpts := heartbeatTestOptions(quietRoot)
	quietOpts.Worktree = emptyDir
	if err := quietRT.runHeartbeat(context.Background(), quietOpts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	}); err != nil {
		t.Fatal(err)
	}
	if len(hbWhere(quiet, http.MethodPost, "/provenance")) != 0 {
		t.Fatal("a worktree without instruction files recorded provenance")
	}
}

func TestRunHeartbeatOwnerExitMarksStopped(t *testing.T) {
	t.Run("owner already gone", func(t *testing.T) {
		var calls []hbCall
		srv := heartbeatFixture(t, &calls, "", "")
		defer srv.Close()
		rt, _, stderr := heartbeatRuntime(t, srv)
		err := rt.runHeartbeat(context.Background(), heartbeatTestOptions(t.TempDir()), heartbeatDeps{alive: func(int) bool { return false }})
		if !errors.Is(err, errOwnerGone) {
			t.Fatalf("run: %v stderr %s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "heartbeat: owner 4242 failed the start check") {
			t.Fatalf("stderr %s", stderr.String())
		}
		if len(calls) != 0 {
			t.Fatalf("dead owner reached the server: %d calls", len(calls))
		}
	})
	t.Run("signal", func(t *testing.T) {
		var calls []hbCall
		srv := heartbeatFixture(t, &calls, "", "")
		defer srv.Close()
		rt, _, stderr := heartbeatRuntime(t, srv)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		defer cancel()
		err := rt.runHeartbeat(ctx, heartbeatTestOptions(t.TempDir()), heartbeatDeps{alive: func(int) bool { return true }})
		if err != nil {
			t.Fatalf("run: %v stderr %s", err, stderr.String())
		}
		if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 {
			t.Fatal("heartbeat ran after the owner was gone")
		}
		stops := hbWhere(calls, http.MethodPost, "/stop")
		if len(stops) != 1 || !strings.HasSuffix(stops[0].path, "/"+transcriptSessionID+"/stop") {
			t.Fatalf("stop calls: %d", len(stops))
		}
		if stops[0].body["reason"] != "stopped" || len(stops[0].lease) < 32 {
			t.Fatal("stop did not mark the session stopped with its lease")
		}
		if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 1 {
			t.Fatal("session was not registered")
		}
	})
}

func TestCoordinatorHeartbeatRetriesActiveGeneration(t *testing.T) {
	for _, message := range []string{"active generation conflicts with registration", harness.RegistrationLeaseConflict} {
		t.Run(message, func(t *testing.T) {
			testCoordinatorHeartbeatRetriesActiveGeneration(t, message)
		})
	}
}

func testCoordinatorHeartbeatRetriesActiveGeneration(t *testing.T, message string) {
	var calls []hbCall
	registrations, waits := 0, 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/harness-sessions") {
			return false
		}
		registrations++
		if registrations <= 3 {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
		} else {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + transcriptSessionID + `"}`))
		}
		return true
	})
	defer srv.Close()
	rt, stdout, stderr := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(t.TempDir())
	o.Role, o.SourceSession, o.Interval = "coordinator", transcriptSessionID, 50
	err := rt.runHeartbeat(context.Background(), o, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(_ context.Context, pid int, interval time.Duration) error {
			if pid != o.OwnerPID || interval != 50*time.Second {
				t.Fatal("retry did not preserve owner and heartbeat interval")
			}
			if registrations == 4 {
				return errOwnerExited
			}
			waits++
			if waits != registrations || len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 {
				t.Fatal("must wait once after each conflict before heartbeating")
			}
			hold, lockErr := openHeartbeatHold(o.StateDir)
			if lockErr == nil {
				hold.release()
			}
			if !errors.Is(lockErr, errHeartbeatBusy) {
				t.Fatal("registration retry released the private state lock")
			}
			// No real sleep: simulate the predecessor remaining healthy for
			// three intervals, then becoming eligible for server-side adoption.
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	regs := hbWhere(calls, http.MethodPost, "/harness-sessions")
	if len(regs) != 4 || waits != 3 {
		t.Fatalf("registrations=%d waits=%d", len(regs), waits)
	}
	wantBody, _ := json.Marshal(regs[0].body)
	for _, reg := range regs[1:] {
		gotBody, _ := json.Marshal(reg.body)
		if !bytes.Equal(gotBody, wantBody) {
			t.Fatal("retry changed registration identity or metadata")
		}
	}
	if regs[0].body["harness_session_ref"] != "claude:"+transcriptSessionID {
		t.Fatal("retry lost the native coordinator reference")
	}
	if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 1 || len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatal("successful retry did not enter the normal heartbeat/stop lifecycle")
	}
	wantLog := strings.Repeat("heartbeat: predecessor generation is still active; retrying registration in 50s\n", 3)
	if stderr.String() != wantLog || stdout.Len() != 0 {
		t.Fatal("each retry must log exactly once without exposing registration data")
	}
}

func TestCoordinatorHeartbeatRetryStopsWithOwner(t *testing.T) {
	for _, mode := range []string{"owner already gone", "owner exits during wait", "owner gone after wait", "already cancelled", "cancelled during wait"} {
		t.Run(mode, func(t *testing.T) {
			var calls []hbCall
			srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/harness-sessions") {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"error":"active generation conflicts with registration"}`))
					return true
				}
				return false
			})
			defer srv.Close()
			rt, _, _ := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(t.TempDir())
			o.Role, o.SourceSession = "coordinator", transcriptSessionID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "already cancelled" {
				cancel()
			}
			alive, waits := mode != "owner already gone", 0
			err := rt.runHeartbeat(ctx, o, heartbeatDeps{
				alive: func(int) bool { return alive },
				wait: func(ctx context.Context, pid int, interval time.Duration) error {
					waits++
					switch mode {
					case "owner exits during wait":
						alive = false
						return waitHeartbeat(ctx, pid, func(int) bool { return alive }, interval)
					case "owner gone after wait":
						alive = false
						return nil
					case "cancelled during wait":
						cancel()
						return waitHeartbeat(ctx, pid, func(int) bool { return alive }, interval)
					default:
						t.Fatal("waited after owner exit or cancellation")
						return errOwnerExited
					}
				},
			})
			// AEON-343: an owner already gone fails the start check before
			// registration, loudly, and registers nothing.
			wantRegistrations := 1
			if mode == "owner already gone" {
				wantRegistrations = 0
				if !errors.Is(err, errOwnerGone) {
					t.Fatalf("run: %v", err)
				}
			} else if err != nil {
				t.Fatalf("run: %v", err)
			}
			wantWaits := 1
			if mode == "owner already gone" || mode == "already cancelled" {
				wantWaits = 0
			}
			if waits != wantWaits || len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != wantRegistrations {
				t.Fatal("registration retried after owner exit or cancellation")
			}
			if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 || len(hbWhere(calls, http.MethodPost, "/stop")) != 0 {
				t.Fatal("failed registration must not heartbeat or stop the predecessor")
			}
			if _, err := os.Stat(filepath.Join(o.StateDir, "session.id")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed registration persisted a session")
			}
			hold, err := openHeartbeatHold(o.StateDir)
			if err != nil {
				t.Fatalf("retry retained state lock after exit: %v", err)
			}
			hold.release()
		})
	}
}

func TestCoordinatorHeartbeatRetryRejectsOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name, role, source, message string
		status                      int
	}{
		{"worker", "worker", transcriptSessionID, "active generation conflicts with registration", 409},
		{"no native reference", "coordinator", "", "active generation conflicts with registration", 409},
		{"other conflict", "coordinator", transcriptSessionID, "successor registration conflicts", 409},
		{"vendor conflict", "coordinator", transcriptSessionID, "vendor_session_ref is already bound to an active generation for this agent", 409},
		{"vendor fingerprint conflict", "coordinator", transcriptSessionID, "vendor_session_ref is already bound to an active generation for this agent (sha256:0123456789abcdef)", 409},
		{"metadata conflict", "coordinator", transcriptSessionID, "active generation conflicts with registration: harness_session_ref is already active with different registration metadata", 409},
		{"worker lease conflict", "worker", transcriptSessionID, harness.RegistrationLeaseConflict, 409},
		{"lease conflict without source", "coordinator", "", harness.RegistrationLeaseConflict, 409},
		{"archived", "coordinator", transcriptSessionID, "archived generation revoked; use a new session reference and worker lease", 409},
		{"forbidden", "coordinator", transcriptSessionID, "active generation conflicts with registration", 403},
		{"server error", "coordinator", transcriptSessionID, "active generation conflicts with registration", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []hbCall
			srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/harness-sessions") {
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.message})
					return true
				}
				return false
			})
			defer srv.Close()
			rt, _, stderr := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(t.TempDir())
			o.Role, o.SourceSession = tc.role, tc.source
			err := rt.runHeartbeat(context.Background(), o, heartbeatDeps{
				alive: func(int) bool { return true },
				wait: func(context.Context, int, time.Duration) error {
					t.Fatal("unrelated registration failure was retried")
					return errOwnerExited
				},
			})
			if err == nil || len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 1 || stderr.Len() != 0 {
				t.Fatal("must return unrelated registration failure without retrying")
			}
		})
	}
}

func TestRunHeartbeatCLIOwnerExit(t *testing.T) {
	isolate(t)
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	dir := t.TempDir()
	code, stdout, stderr := runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "harness", "run-heartbeat",
		"--owner-pid", "2147483646",
		"--state-dir", filepath.Join(dir, "state"),
		"--project", "AEON", "--agent", "worker", "--harness", "claude", "--host", "test-host",
		"--codex-index", filepath.Join(dir, "missing-index.jsonl"),
		"--claude-projects", filepath.Join(dir, "missing-projects"),
		"--model", "unknown", "--effort", "unknown", "--account-label", "unknown",
	}, "")
	if code != 1 {
		t.Fatalf("exit %d stdout %s stderr %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "heartbeat: owner 2147483646 failed the start check") || !strings.Contains(stderr, "paimos: owner process is not alive") {
		t.Fatalf("stderr %s", stderr)
	}
	if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 || len(hbWhere(calls, http.MethodPost, "/stop")) != 0 || len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 0 {
		t.Fatalf("dead owner calls heartbeat %d stop %d register %d", len(hbWhere(calls, http.MethodPost, "/heartbeat")), len(hbWhere(calls, http.MethodPost, "/stop")), len(hbWhere(calls, http.MethodPost, "/harness-sessions")))
	}

	var registered []hbCall
	regSrv := heartbeatFixture(t, &registered, "", "")
	defer regSrv.Close()
	regRT, _, regErr := heartbeatRuntime(t, regSrv)
	opts := heartbeatTestOptions(dir)
	opts.Model = "unknown"
	opts.Effort = "unknown"
	opts.AccountLabel = "unknown"
	if err := regRT.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	}); err != nil {
		t.Fatalf("registration: %v stderr %s", err, regErr.String())
	}
	reg := hbWhere(registered, http.MethodPost, "/harness-sessions")
	if len(reg) != 1 {
		t.Fatal("missing registration")
	}
	if reg[0].body["max_session_file_bytes"] != float64(rules.MaxBytes) || reg[0].body["rules_client_version"] == nil {
		t.Fatal("CLI registration lacks capability")
	}
	if _, ok := reg[0].body["model"]; ok {
		t.Fatal("registration sent an unknown model")
	}
	if _, ok := reg[0].body["reasoning_effort"]; ok {
		t.Fatal("registration sent an unknown effort")
	}
	if _, ok := reg[0].body["account_label"]; ok {
		t.Fatal("registration sent an unknown account label")
	}
}

func TestCodexHeartbeatReportsProjectDocLimit(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.Harness = "codex"
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		label: func() (string, bool) { return "worker", true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	want := float64(rules.SessionFileLimit("codex"))
	if want != 32768 {
		t.Fatalf("Codex default project_doc_max_bytes changed: %v", want)
	}
	reg := hbWhere(calls, http.MethodPost, "/harness-sessions")
	beats := hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(reg) != 1 || reg[0].body["max_session_file_bytes"] != want || len(beats) != 1 || beats[0].body["max_session_file_bytes"] != want {
		t.Fatalf("codex capability register %#v beats %#v", reg, beats)
	}
}

func TestRunHeartbeatLabelChangeSentOnce(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	n := 0
	// Register reads the label, then each beat reads it again.
	labels := []string{"Alpha", "Alpha", "Beta", "Beta"}
	err := rt.runHeartbeat(context.Background(), heartbeatTestOptions(t.TempDir()), heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			if n >= len(labels) {
				return errOwnerExited
			}
			return nil
		},
		label: func() (string, bool) {
			if n >= len(labels) {
				return "", false
			}
			label := labels[n]
			n++
			return label, true
		},
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	reg := hbWhere(calls, http.MethodPost, "/harness-sessions")
	if len(reg) != 1 || reg[0].body["display_label"] != "Alpha" {
		t.Fatalf("register label: %#v", reg)
	}
	beats := hbWhere(calls, http.MethodPost, "/heartbeat")
	for _, beat := range beats {
		if beat.body["max_session_file_bytes"] != float64(rules.MaxBytes) || beat.body["rules_client_version"] == nil {
			t.Fatal("CLI heartbeat lacks capability")
		}
	}
	if len(beats) != 3 {
		t.Fatalf("beats %d", len(beats))
	}
	if _, ok := beats[0].body["display_label"]; ok {
		t.Fatal("unchanged label was sent again")
	}
	if beats[1].body["display_label"] != "Beta" {
		t.Fatalf("label change: %#v", beats[1].body["display_label"])
	}
	if _, ok := beats[2].body["display_label"]; ok {
		t.Fatal("label change was sent twice")
	}
}

func TestRunHeartbeatCommitsDeduplicated(t *testing.T) {
	repo, base := gitRepo(t)
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.Worktree = repo
	opts.Label = "worker"
	made := false
	beats := 0
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		label: func() (string, bool) { return "worker", true },
		wait: func(context.Context, int, time.Duration) error {
			beats++
			if beats == 1 && !made {
				gitCommit(t, repo, "second change")
				made = true
			}
			if beats >= 3 {
				return errOwnerExited
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	heartbeats := hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(heartbeats) != 3 {
		t.Fatalf("beats %d", len(heartbeats))
	}
	if _, ok := heartbeats[0].body["commits"]; ok {
		t.Fatal("commits from before the start were sent")
	}
	items, ok := heartbeats[1].body["commits"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("new commits: %#v", heartbeats[1].body["commits"])
	}
	item := items[0].(map[string]any)
	if item["subject"] != "second change" || item["sha"] == base || item["sha"] == "" {
		t.Fatalf("commit item %#v base %s", item, base)
	}
	if _, ok := heartbeats[2].body["commits"]; ok {
		t.Fatal("commit was sent again")
	}
}

func TestRunHeartbeatMissingNameSource(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	t.Setenv("AEON_MODEL", "claude-test")
	t.Setenv("AEON_EFFORT", "high")
	opts := heartbeatTestOptions(t.TempDir())
	opts.AccountLabel = "Unknown"
	n := 0
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			n++
			if n >= 1 {
				return errOwnerExited
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("missing name source failed: %v stderr %s", err, stderr.String())
	}
	beats := hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(beats) != 1 {
		t.Fatalf("beats %d", len(beats))
	}
	if _, ok := beats[0].body["display_label"]; ok {
		t.Fatal("missing name source sent a label")
	}
	if beats[0].body["model"] != "claude-test" {
		t.Fatalf("model %#v", beats[0].body["model"])
	}
	if _, ok := beats[0].body["account_label"]; ok {
		t.Fatal("unknown account label was sent")
	}
	if beats[0].body["reasoning_effort"] != "high" {
		t.Fatalf("effort %#v", beats[0].body["reasoning_effort"])
	}
}

func TestRunHeartbeatUsageAndControls(t *testing.T) {
	dir := t.TempDir()
	transcript := claudeUsagePath(t, dir, "33333333-3333-4333-8333-333333333331.jsonl")
	body := strings.Join([]string{
		`{"uuid":"11111111-1111-4111-8111-111111111111","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3}}}`,
		`{"uuid":"11111111-1111-4111-8111-111111111111","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3}}}`,
		`{"type":"user","message":{"usage":{"input_tokens":100,"output_tokens":100,"cache_read_input_tokens":100}}}`,
		`{"type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":1}}}`,
		`{"type":"custom-title","customTitle":"Transcript name"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcript, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	status := `{"activity_sequence":0,"controls":[{"id":"22222222-2222-4222-8222-222222222222","kind":"stop","state":"pending"},{"id":"22222222-2222-4222-8222-222222222223","kind":"interrupt","state":"completed"}]}`
	inbox := `{"items":[{"id":"55555555-5555-4555-8555-555555555555","acked_at":null,"body":"SECRET-BODY"},{"id":"55555555-5555-4555-8555-555555555556","acked_at":"2026-09-28T00:00:00Z"}],"next_after":2}`
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, status, inbox)
	defer srv.Close()
	rt, stdout, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Transcript = transcript
	opts.PrintControls = true
	opts.Harness = "claude"
	n := 0
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			n++
			if n >= 2 {
				return errOwnerExited
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	usage := hbWhere(calls, http.MethodPost, "/usage")
	if len(usage) != 1 {
		t.Fatalf("usage posts %d stderr %s", len(usage), stderr.String())
	}
	if usage[0].body["model"] != "claude-opus" || usage[0].body["input_tokens"] != float64(19) || usage[0].body["output_tokens"] != float64(6) || usage[0].body["cached_input_tokens"] != float64(4) {
		t.Fatalf("usage %#v", usage[0].body)
	}
	if usage[0].body["billing_mode"] != "unknown" || usage[0].body["provisional"] != true {
		t.Fatalf("usage flags %#v", usage[0].body)
	}
	if !validUUID(usage[0].body["report_id"].(string)) {
		t.Fatalf("report id %#v", usage[0].body["report_id"])
	}
	reg := hbWhere(calls, http.MethodPost, "/harness-sessions")
	if len(reg) != 1 || reg[0].body["display_label"] != "Transcript name" {
		t.Fatalf("title from transcript: %#v", reg)
	}
	out := stdout.String()
	if !strings.Contains(out, "control 22222222-2222-4222-8222-222222222222 stop pending\n") || strings.Contains(out, "completed") {
		t.Fatalf("controls:\n%s", out)
	}
	// The inbox read names this generation so its bound messages are included (AEON-280).
	pulls := hbWhere(calls, http.MethodGet, "/api/inbox/messages")
	if len(pulls) == 0 || pulls[0].query != "wait_ms=0&session="+transcriptSessionID {
		t.Fatalf("inbox pulls %#v", pulls)
	}
	if !strings.Contains(out, "message 55555555-5555-4555-8555-555555555555\n") || strings.Contains(out, "555555555556") || strings.Contains(out, "SECRET-BODY") {
		t.Fatalf("messages:\n%s", out)
	}
}

func TestRunHeartbeatResumeSkipsRegister(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	dir := t.TempDir()
	opts := heartbeatTestOptions(dir)
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := strings.Repeat("ab", 32)
	if err := os.WriteFile(filepath.Join(opts.StateDir, "lease.key"), []byte(lease+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.StateDir, "session.id"), []byte(transcriptSessionID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }})
	if !errors.Is(err, errOwnerGone) {
		t.Fatalf("resume: %v stderr %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "heartbeat: owner 4242 failed the start check") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 0 {
		t.Fatal("resume registered a second session")
	}
	stops := hbWhere(calls, http.MethodPost, "/stop")
	if len(stops) != 1 || stops[0].lease != lease {
		t.Fatal("resume did not stop the existing session")
	}
}

func TestHeartbeatNameSources(t *testing.T) {
	id := "0199a213-81c0-7800-8aa1-bbab2a035a53"
	dir := t.TempDir()
	index := filepath.Join(dir, "session_index.jsonl")
	raw := strings.Join([]string{
		`{"id":"` + id + `","thread_name":"First"}`,
		`{"id":"0199a213-81c0-7800-8aa1-bbab2a035a54","thread_name":"Other"}`,
		`{"id":"` + id + `","thread_name":"Second"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(index, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	label, ok := codexSessionLabel(index, id)
	if !ok || label != "Second" {
		t.Fatalf("codex label %q %v", label, ok)
	}
	if _, ok := codexSessionLabel(filepath.Join(dir, "missing.jsonl"), id); ok {
		t.Fatal("missing codex index returned a label")
	}
	blank := filepath.Join(dir, "blank.jsonl")
	if err := os.WriteFile(blank, []byte(`{"id":"`+id+`","thread_name":"Named"}`+"\n"+`{"id":"`+id+`","thread_name":""}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := codexSessionLabel(blank, id); ok {
		t.Fatal("blank current name was treated as a label")
	}

	projects := filepath.Join(dir, "projects")
	sessionDir := filepath.Join(projects, "encoded-cwd")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(sessionDir, id+".jsonl")
	claude := `{"type":"ai-title","aiTitle":"Generated"}` + "\n" + `{"type":"custom-title","customTitle":"Mine"}` + "\n" + `{"type":"ai-title","aiTitle":"Later"}` + "\n"
	if err := os.WriteFile(transcript, []byte(claude), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := claudeSessionLabel(heartbeatOptions{ClaudeProjects: projects}, id)
	if !ok || got != "Mine" {
		t.Fatalf("claude title %q %v", got, ok)
	}
	if _, ok := claudeSessionLabel(heartbeatOptions{ClaudeProjects: filepath.Join(dir, "absent")}, id); ok {
		t.Fatal("missing claude projects returned a label")
	}
}

func TestSelectHeartbeatCommitsDedup(t *testing.T) {
	old := strings.Repeat("a", 40)
	next := strings.Repeat("b", 40)
	got := selectHeartbeatCommits([]heartbeatCommit{
		{SHA: old, Subject: "old"},
		{SHA: strings.ToUpper(next), Subject: "new"},
		{SHA: next, Subject: "duplicate"},
		{SHA: "zz", Subject: "bad"},
	}, map[string]bool{old: true})
	if len(got) != 1 || got[0].SHA != next || got[0].Subject != "new" {
		t.Fatalf("commits %#v", got)
	}
}

func gitRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "f")
	gitCmd(t, dir, "commit", "-m", "base")
	return dir, strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))
}

func gitCommit(t *testing.T, dir, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "f")
	gitCmd(t, dir, "commit", "-m", message)
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	template := t.TempDir()
	cmd := exec.Command("git", append([]string{"-c", "user.email=heartbeat@example.com", "-c", "user.name=Heartbeat Test"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Heartbeat Test",
		"GIT_AUTHOR_EMAIL=heartbeat@example.com",
		"GIT_COMMITTER_NAME=Heartbeat Test",
		"GIT_COMMITTER_EMAIL=heartbeat@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TEMPLATE_DIR="+template,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func hbServer(t *testing.T, calls *[]hbCall, handle func(r *http.Request, body map[string]any, w http.ResponseWriter) bool) *httptest.Server {
	t.Helper()
	project := map[string]any{"id": transcriptProjectID, "key": "PRJ-1", "kind_id": "project-kind", "title": "AEON", "fields": map[string]any{"project_key": "AEON"}}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("decode %s %s: %v", r.Method, r.URL.Path, err)
			}
		}
		*calls = append(*calls, hbCall{method: r.Method, path: r.URL.Path, lease: r.Header.Get("X-Aeon-Worker-Lease"), query: r.URL.RawQuery, body: body})
		w.Header().Set("Content-Type", "application/json")
		if handle != nil && handle(r, body, w) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			_, _ = w.Write([]byte(`{"items":[{"id":"project-kind","slug":"project"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{project}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/models":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": transcriptEntryID, "harness": "codex", "model": "fixture-model", "effort": "high", "enabled": true}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/me":
			_, _ = w.Write([]byte(`{"principal":{"id":"44444444-4444-4444-8444-444444444444","name":"worker"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/"+transcriptProjectID+"/harness-sessions":
			_, _ = w.Write([]byte(`{"id":"` + transcriptSessionID + `"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/usage"):
			_, _ = w.Write([]byte(`{"items":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/"+transcriptProjectID+"/harness-sessions/"+transcriptSessionID:
			_, _ = w.Write([]byte(`{"activity_sequence":0}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages":
			_, _ = w.Write([]byte(`{"items":[],"next_after":0}`))
		case strings.HasPrefix(r.URL.Path, "/api/projects/"+transcriptProjectID+"/harness-sessions/"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func TestRunHeartbeatTerminalStatusExits(t *testing.T) {
	for _, code := range []int{http.StatusGone, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls []hbCall
			srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/heartbeat") {
					w.WriteHeader(code)
					_, _ = w.Write([]byte(`{"error":"closed"}`))
					return true
				}
				return false
			})
			defer srv.Close()
			rt, _, stderr := heartbeatRuntime(t, srv)
			dir := t.TempDir()
			err := rt.runHeartbeat(context.Background(), heartbeatTestOptions(dir), heartbeatDeps{
				alive: func(int) bool { return true },
				wait: func(context.Context, int, time.Duration) error {
					t.Fatal("heartbeat waited after a terminal response")
					return errOwnerExited
				},
			})
			if err != nil {
				t.Fatalf("run: %v stderr %s", err, stderr.String())
			}
			if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 1 {
				t.Fatalf("heartbeats %d", len(hbWhere(calls, http.MethodPost, "/heartbeat")))
			}
			if len(hbWhere(calls, http.MethodPost, "/stop")) != 0 {
				t.Fatal("terminal generation was stopped again")
			}
			if strings.Contains(stderr.String(), "will not resume") {
				t.Fatalf("first run explained a closed generation: %s", stderr.String())
			}
			before := len(calls)
			err = rt.runHeartbeat(context.Background(), heartbeatTestOptions(dir), heartbeatDeps{alive: func(int) bool { return true }})
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if len(calls) != before {
				t.Fatalf("resume of a closed generation made %d requests", len(calls)-before)
			}
			want := "is stopped and will not resume"
			if code == http.StatusGone {
				want = "is archived and will not resume"
			}
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("stderr %s", stderr.String())
			}
		})
	}
}

func TestRunHeartbeatResumeRejectsReusedPID(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	dir := t.TempDir()
	opts := heartbeatTestOptions(dir)
	opts.OwnerPID = os.Getpid()
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := strings.Repeat("ab", 32)
	if err := os.WriteFile(filepath.Join(opts.StateDir, "lease.key"), []byte(lease+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.StateDir, "session.id"), []byte(transcriptSessionID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	disk := heartbeatDisk{Schema: heartbeatSchema, SessionID: transcriptSessionID, OwnerPID: os.Getpid(), OwnerStart: "1.2"}
	raw, err := json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.StateDir, "state.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{})
	if !errors.Is(err, errOwnerGone) {
		t.Fatalf("resume: %v stderr %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "failed the start check") || strings.Contains(stderr.String(), "will not resume") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 || len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 0 {
		t.Fatal("reused pid resumed the generation")
	}
	if len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatal("reused pid did not stop the generation")
	}
	before := len(calls)
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{})
	if err != nil {
		t.Fatalf("closed resume: %v stderr %s", err, stderr.String())
	}
	if len(calls) != before {
		t.Fatalf("closed resume made %d requests", len(calls)-before)
	}
	if !strings.Contains(stderr.String(), "is closed and will not resume") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestRunHeartbeatDeadOwnerStopForbiddenStaysOpen(t *testing.T) {
	var calls []hbCall
	stops := 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
			stops++
			if stops == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"harness worker proof rejected"}`))
				return true
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	dir := t.TempDir()
	opts := heartbeatTestOptions(dir)
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := strings.Repeat("ab", 32)
	if err := os.WriteFile(filepath.Join(opts.StateDir, "lease.key"), []byte(lease+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.StateDir, "session.id"), []byte(transcriptSessionID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }})
	if err == nil || errors.Is(err, errOwnerGone) {
		t.Fatalf("forbidden stop: %v stderr %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "failed the start check") || strings.Contains(stderr.String(), "will not resume") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 0 || len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 || len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatal("forbidden stop did not stay a single stop of the open generation")
	}
	disk := loadHeartbeatDisk(t, opts.StateDir)
	if disk.Closed {
		t.Fatal("forbidden stop marked the generation closed")
	}
	intent, err := os.ReadFile(filepath.Join(opts.StateDir, "stop.intent"))
	if err != nil || !strings.Contains(string(intent), "strict") {
		t.Fatalf("strict intent %q err %v", intent, err)
	}
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }})
	if err != nil {
		t.Fatalf("retry: %v stderr %s", err, stderr.String())
	}
	if stops != 2 {
		t.Fatalf("stops %d", stops)
	}
	if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 0 || len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 {
		t.Fatal("retry registered or heartbeated")
	}
	disk = loadHeartbeatDisk(t, opts.StateDir)
	if !disk.Closed {
		t.Fatal("a completed stop left the generation open")
	}
	if !strings.Contains(stderr.String(), "is closed and will not resume") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

// TestStopReconcilesGenerationAlreadyStopped is the crash after the server
// commits /stop and before the client persists closed. A later strict stop
// is 403 or 409; the status read must close the generation and exit 0.
func TestStopReconcilesGenerationAlreadyStopped(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusConflict} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			rt, opts, stderr, calls := stoppedGenerationServer(t, code, func(stopped bool) string {
				if !stopped {
					return `{"id":"` + transcriptSessionID + `","phase":"starting"}`
				}
				return `{"id":"` + transcriptSessionID + `","phase":"stopped","stopped_at":"2026-09-29T16:00:00Z"}`
			})
			crashAfterServerStop(t, rt, opts)
			stderr.Reset()
			if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err != nil {
				t.Fatalf("rerun: %v stderr %s", err, stderr.String())
			}
			if !loadHeartbeatDisk(t, opts.StateDir).Closed {
				t.Fatal("already-stopped generation stayed open")
			}
			if !strings.Contains(stderr.String(), "will not resume") {
				t.Fatalf("stderr %s", stderr.String())
			}
			stops := len(hbWhere(*calls, http.MethodPost, "/stop"))
			stderr.Reset()
			if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err != nil {
				t.Fatalf("second rerun: %v stderr %s", err, stderr.String())
			}
			if got := len(hbWhere(*calls, http.MethodPost, "/stop")); got != stops {
				t.Fatalf("closed rerun called /stop again (%d to %d)", stops, got)
			}
			if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "stop.intent")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("reconciled stop left an intent")
			}
		})
	}
	t.Run("strict intent", func(t *testing.T) {
		rt, opts, stderr, calls := stoppedGenerationServer(t, http.StatusForbidden, func(stopped bool) string {
			if !stopped {
				return `{"id":"` + transcriptSessionID + `","phase":"starting"}`
			}
			return `{"id":"` + transcriptSessionID + `","phase":"stopped","stopped_at":"2026-09-29T16:00:00Z"}`
		})
		crashAfterServerStop(t, rt, opts)
		if err := os.WriteFile(filepath.Join(opts.StateDir, "stop.intent"), []byte(transcriptSessionID+"\nstrict\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stderr.Reset()
		if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }}); err != nil {
			t.Fatalf("rerun: %v stderr %s", err, stderr.String())
		}
		if !loadHeartbeatDisk(t, opts.StateDir).Closed {
			t.Fatal("strict recovery left the generation open")
		}
		if len(hbWhere(*calls, http.MethodPost, "/heartbeat")) != 0 {
			t.Fatal("strict recovery heartbeated a stopped generation")
		}
		if !strings.Contains(stderr.String(), "will not resume") {
			t.Fatalf("stderr %s", stderr.String())
		}
	})
	t.Run("still starting", func(t *testing.T) {
		rt, opts, stderr, _ := stoppedGenerationServer(t, http.StatusForbidden, func(bool) string {
			return `{"id":"` + transcriptSessionID + `","phase":"starting"}`
		})
		saveOpenHeartbeat(t, rt, opts)
		if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err == nil || errors.Is(err, errOwnerGone) {
			t.Fatalf("rejected stop: %v stderr %s", err, stderr.String())
		}
		if loadHeartbeatDisk(t, opts.StateDir).Closed {
			t.Fatal("unconfirmed 403 marked the generation closed")
		}
		intent, err := os.ReadFile(filepath.Join(opts.StateDir, "stop.intent"))
		if err != nil || !strings.Contains(string(intent), "strict") {
			t.Fatalf("strict intent %q err %v", intent, err)
		}
	})
	t.Run("other generation", func(t *testing.T) {
		rt, opts, stderr, _ := stoppedGenerationServer(t, http.StatusForbidden, func(bool) string {
			return `{"id":"11111111-1111-4111-8111-111111111111","phase":"stopped","stopped_at":"2026-09-29T16:00:00Z"}`
		})
		saveOpenHeartbeat(t, rt, opts)
		if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err == nil || errors.Is(err, errOwnerGone) {
			t.Fatalf("other generation: %v stderr %s", err, stderr.String())
		}
		if loadHeartbeatDisk(t, opts.StateDir).Closed {
			t.Fatal("another generation's stop closed this one")
		}
	})
	t.Run("archived", func(t *testing.T) {
		rt, opts, stderr, _ := stoppedGenerationServer(t, http.StatusForbidden, func(bool) string {
			return `{"id":"` + transcriptSessionID + `","phase":"stopped","archived_at":"2026-09-29T16:00:00Z"}`
		})
		saveOpenHeartbeat(t, rt, opts)
		if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err != nil {
			t.Fatalf("archived: %v stderr %s", err, stderr.String())
		}
		if !loadHeartbeatDisk(t, opts.StateDir).Closed {
			t.Fatal("archived generation stayed open")
		}
	})
}

func stoppedGenerationServer(t *testing.T, code int, status func(stopped bool) string) (*runtime, heartbeatOptions, *bytes.Buffer, *[]hbCall) {
	t.Helper()
	var calls []hbCall
	stopped := false
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		sessionPath := "/api/projects/" + transcriptProjectID + "/harness-sessions/" + transcriptSessionID
		if r.Method == http.MethodGet && r.URL.Path == sessionPath {
			_, _ = w.Write([]byte(status(stopped)))
			return true
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
			// A status that changes once the server commits distinguishes the
			// crash (first stop succeeds) from a stop the server never accepts.
			commits := status(false) != status(true)
			if !stopped && commits {
				stopped = true
				_, _ = w.Write([]byte(`{"ok":true}`))
				return true
			}
			stopped = true
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"harness worker proof rejected"}`))
			return true
		}
		return false
	})
	t.Cleanup(srv.Close)
	rt, _, stderr := heartbeatRuntime(t, srv)
	return rt, heartbeatTestOptions(t.TempDir()), stderr, &calls
}

func saveOpenHeartbeat(t *testing.T, rt *runtime, opts heartbeatOptions) {
	t.Helper()
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if err := saveHeartbeatSession(&session); err != nil {
		session.hold.release()
		t.Fatal(err)
	}
	session.hold.release()
}

func crashAfterServerStop(t *testing.T, rt *runtime, opts heartbeatOptions) {
	t.Helper()
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if err := saveHeartbeatSession(&session); err != nil {
		session.hold.release()
		t.Fatal(err)
	}
	if err := rt.stopHeartbeatStrict(context.Background(), opts.Project, session); err != nil {
		session.hold.release()
		t.Fatal(err)
	}
	session.hold.release()
	if loadHeartbeatDisk(t, opts.StateDir).Closed {
		t.Fatal("stop persisted closed before the crash")
	}
}

func TestStopRetryBoundAndPermanentRejection(t *testing.T) {
	t.Run("bound", func(t *testing.T) {
		prev := heartbeatStopRetryBound
		heartbeatStopRetryBound = 2
		t.Cleanup(func() { heartbeatStopRetryBound = prev })
		var calls []hbCall
		srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				return true
			}
			return false
		})
		defer srv.Close()
		rt, _, stderr := heartbeatRuntime(t, srv)
		opts := heartbeatTestOptions(t.TempDir())
		err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
			alive: func(int) bool { return true },
			wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
		})
		if err == nil {
			t.Fatal("transient stop failure was ignored")
		}
		noBeat := heartbeatDeps{
			alive: func(int) bool { return true },
			wait: func(context.Context, int, time.Duration) error {
				t.Fatal("recovery heartbeated")
				return errOwnerExited
			},
		}
		if err := rt.runHeartbeat(context.Background(), opts, noBeat); err == nil {
			t.Fatal("second transient failure was ignored")
		}
		stderr.Reset()
		err = rt.runHeartbeat(context.Background(), opts, noBeat)
		if !errors.Is(err, errHeartbeatStopBound) {
			t.Fatalf("bound: %v stderr %s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "stop retries are exhausted") {
			t.Fatalf("stderr %s", stderr.String())
		}
		if got := len(hbWhere(calls, http.MethodPost, "/stop")); got != 2 {
			t.Fatalf("stops %d", got)
		}
		if loadHeartbeatDisk(t, opts.StateDir).Closed {
			t.Fatal("exhausted retries marked the generation closed")
		}
	})
	t.Run("permanent", func(t *testing.T) {
		var calls []hbCall
		srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid stop reason"}`))
				return true
			}
			return false
		})
		defer srv.Close()
		rt, _, stderr := heartbeatRuntime(t, srv)
		opts := heartbeatTestOptions(t.TempDir())
		err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
			alive: func(int) bool { return true },
			wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
		})
		if err == nil {
			t.Fatal("permanent stop failure was ignored")
		}
		stderr.Reset()
		err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
			alive: func(int) bool { return true },
			wait: func(context.Context, int, time.Duration) error {
				t.Fatal("permanent rejection was retried into a heartbeat")
				return errOwnerExited
			},
		})
		if !errors.Is(err, errHeartbeatStopRejected) {
			t.Fatalf("permanent: %v stderr %s", err, stderr.String())
		}
		if got := len(hbWhere(calls, http.MethodPost, "/stop")); got != 1 {
			t.Fatalf("stops %d", got)
		}
		if !strings.Contains(stderr.String(), "stop will not be retried") {
			t.Fatalf("stderr %s", stderr.String())
		}
	})
}

func TestRunHeartbeatStateDirIsPrivate(t *testing.T) {
	t.Run("loose directory", func(t *testing.T) {
		var calls []hbCall
		srv := heartbeatFixture(t, &calls, "", "")
		defer srv.Close()
		rt, _, _ := heartbeatRuntime(t, srv)
		opts := heartbeatTestOptions(t.TempDir())
		if err := os.Mkdir(opts.StateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err == nil {
			t.Fatal("accepted a directory other users can read")
		}
		if len(calls) != 0 {
			t.Fatal("loose directory reached the server")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		var calls []hbCall
		srv := heartbeatFixture(t, &calls, "", "")
		defer srv.Close()
		rt, _, _ := heartbeatRuntime(t, srv)
		dir := t.TempDir()
		real := filepath.Join(dir, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		opts := heartbeatTestOptions(dir)
		opts.StateDir = link
		if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err == nil {
			t.Fatal("accepted a symlink state directory")
		}
		if len(calls) != 0 {
			t.Fatal("symlink directory reached the server")
		}
	})
	t.Run("loose session file", func(t *testing.T) {
		var calls []hbCall
		srv := heartbeatFixture(t, &calls, "", "")
		defer srv.Close()
		rt, _, _ := heartbeatRuntime(t, srv)
		opts := heartbeatTestOptions(t.TempDir())
		if err := os.Mkdir(opts.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(opts.StateDir, "session.id"), []byte(transcriptSessionID+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }}); err == nil {
			t.Fatal("accepted a group-readable session id")
		}
		if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 0 {
			t.Fatal("loose session file registered a generation")
		}
	})
}

func TestRunHeartbeatLockIsExclusive(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
			alive: func(int) bool { return true },
			wait: func(context.Context, int, time.Duration) error {
				once.Do(func() { close(started) })
				<-release
				return errOwnerExited
			},
		})
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("first helper exited early: %v stderr %s", err, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("first helper did not reach the lock hold")
	}
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }})
	if !errors.Is(err, errHeartbeatBusy) {
		t.Fatalf("second start: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first helper: %v stderr %s", err, stderr.String())
	}
	if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 1 {
		t.Fatal("concurrent start registered a second generation")
	}
}

func TestRunHeartbeatUsageReplayIsStable(t *testing.T) {
	dir := t.TempDir()
	transcript := claudeUsagePath(t, dir, "33333333-3333-4333-8333-333333333331.jsonl")
	line := `{"uuid":"11111111-1111-4111-8111-111111111111","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3}}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	usageN := 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage") {
			usageN++
			if usageN == 1 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return true
				}
				_ = conn.Close()
				return true
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Transcript = transcript
	n := 0
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			n++
			if n == 1 {
				extra := `{"uuid":"22222222-2222-4222-8222-222222222222","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":9,"output_tokens":9,"cache_read_input_tokens":1}}}` + "\n"
				f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return err
				}
				_, _ = f.Write([]byte(extra))
				_ = f.Close()
				return nil
			}
			return errOwnerExited
		},
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	usage := hbWhere(calls, http.MethodPost, "/usage")
	if len(usage) < 2 {
		t.Fatalf("usage posts %d stderr %s", len(usage), stderr.String())
	}
	if usage[0].body["report_id"] != usage[1].body["report_id"] || usage[0].body["sequence"] != usage[1].body["sequence"] || usage[0].body["input_tokens"] != usage[1].body["input_tokens"] {
		t.Fatalf("replay changed the report: %#v then %#v", usage[0].body, usage[1].body)
	}
	if len(usage) > 2 && usage[2].body["report_id"] == usage[0].body["report_id"] {
		t.Fatal("grown transcript reused the acknowledged report id")
	}
}

func TestRunHeartbeatUsageReconcilesServerSequence(t *testing.T) {
	dir := t.TempDir()
	transcript := claudeUsagePath(t, dir, "33333333-3333-4333-8333-333333333331.jsonl")
	body := strings.Join([]string{
		`{"uuid":"11111111-1111-4111-8111-111111111111","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3}}}`,
		`{"type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":1}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcript, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"usage sequence must increase"}`))
			return true
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/usage"):
			_, _ = w.Write([]byte(`{"items":[{"model":"claude-opus","sequence":1,"input_tokens":19,"output_tokens":6,"cached_input_tokens":4}]}`))
			return true
		default:
			return false
		}
	})
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Transcript = transcript
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	if len(hbWhere(calls, http.MethodPost, "/usage")) != 1 || len(hbWhere(calls, http.MethodGet, "/usage")) != 1 {
		t.Fatalf("usage post %d get %d", len(hbWhere(calls, http.MethodPost, "/usage")), len(hbWhere(calls, http.MethodGet, "/usage")))
	}
}

func TestRunHeartbeatFinalUsageFlush(t *testing.T) {
	dir := t.TempDir()
	transcript := claudeUsagePath(t, dir, "33333333-3333-4333-8333-333333333331.jsonl")
	body := `{"uuid":"11111111-1111-4111-8111-111111111111","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3}}}` + "\n"
	if err := os.WriteFile(transcript, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Transcript = transcript
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			extra := `{"uuid":"22222222-2222-4222-8222-222222222222","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0}}}` + "\n"
			f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, _ = f.Write([]byte(extra))
			_ = f.Close()
			return errOwnerExited
		},
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	usage := hbWhere(calls, http.MethodPost, "/usage")
	if len(usage) != 2 {
		t.Fatalf("usage posts %d stderr %s", len(usage), stderr.String())
	}
	if usage[1].body["input_tokens"] != float64(14) || usage[1].body["output_tokens"] != float64(5) || usage[1].body["sequence"] != float64(2) {
		t.Fatalf("final flush %#v", usage[1].body)
	}
	stopAt := -1
	flushAt := -1
	for i, call := range calls {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/stop") && stopAt < 0 {
			stopAt = i
		}
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/usage") && call.body["sequence"] == float64(2) {
			flushAt = i
		}
	}
	if flushAt < 0 || stopAt < 0 || flushAt > stopAt {
		t.Fatalf("flush %d stop %d", flushAt, stopAt)
	}
}

func TestRunHeartbeatCommitCursorDoesNotStarve(t *testing.T) {
	// Keep the fixture independent of the host's Git configuration and clock.
	// In particular, no background maintenance may outlive its temporary repo.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "gc.auto")
	t.Setenv("GIT_CONFIG_VALUE_0", "0")
	t.Setenv("GIT_CONFIG_KEY_1", "maintenance.auto")
	t.Setenv("GIT_CONFIG_VALUE_1", "false")
	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	commitDate := started.Add(time.Second).Format(time.RFC3339)
	t.Setenv("GIT_AUTHOR_DATE", commitDate)
	t.Setenv("GIT_COMMITTER_DATE", commitDate)
	repo, base := gitRepo(t)
	branch := strings.TrimSpace(gitCmd(t, repo, "symbolic-ref", "HEAD"))
	for i := 1; i <= 21; i++ {
		gitCommit(t, repo, fmt.Sprintf("c%02d", i))
	}
	tip := strings.TrimSpace(gitCmd(t, repo, "rev-parse", "HEAD"))
	gitCmd(t, repo, "update-ref", "-m", "fixture: hide history", branch, base, tip)
	// Retain the real oldest-first Git walk and reflog attribution, but inject
	// its time cutoff rather than depending on registration's wall clock.
	disk := heartbeatDisk{
		BoundWorktree: repo,
		BoundBranch:   strings.TrimPrefix(branch, "refs/heads/"),
		StartRev:      base,
		StartedUnix:   started.Unix(),
	}
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.Worktree = repo
	beats := 0
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		label: func() (string, bool) { return "worker", true },
		commits: func(worktree, since string) ([]heartbeatCommit, error) {
			disk.CommitCursor = since
			batch, _ := localHeartbeatCommits(context.Background(), worktree, &disk)
			return batch, nil
		},
		wait: func(context.Context, int, time.Duration) error {
			beats++
			if beats == 1 {
				// The wait callback is a synchronous barrier: all objects and
				// reflog entries exist before the next beat can read them.
				gitCmd(t, repo, "update-ref", "-m", "fixture: expose history", branch, tip, base)
			}
			if beats >= 3 {
				return errOwnerExited
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	heartbeats := hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(heartbeats) != 3 {
		t.Fatalf("beats %d", len(heartbeats))
	}
	if _, ok := heartbeats[0].body["commits"]; ok {
		t.Fatal("history was sent before the wait barrier exposed it")
	}
	first := commitSubjects(t, heartbeats[1].body["commits"])
	second := commitSubjects(t, heartbeats[2].body["commits"])
	if len(first) != 20 || first[0] != "c01" || first[19] != "c20" {
		t.Fatalf("first batch %v", first)
	}
	if len(second) != 1 || second[0] != "c21" {
		t.Fatalf("second batch %v", second)
	}
	for i, subject := range first {
		if want := fmt.Sprintf("c%02d", i+1); subject != want {
			t.Fatalf("first batch commit %d: got %q, want %q", i, subject, want)
		}
	}
}

func TestRunHeartbeatSkipsImportedCommits(t *testing.T) {
	repo, _ := gitRepo(t)
	branch := strings.TrimSpace(gitCmd(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.Worktree = repo
	done := false
	beats := 0
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		label: func() (string, bool) { return "worker", true },
		wait: func(context.Context, int, time.Duration) error {
			beats++
			if beats == 1 && !done {
				gitCmd(t, repo, "checkout", "-b", "side")
				gitCommit(t, repo, "side-work")
				gitCmd(t, repo, "checkout", branch)
				gitCmd(t, repo, "merge", "--no-ff", "side", "-m", "merge side")
				gitCmd(t, repo, "checkout", "-b", "imported")
				gitCommit(t, repo, "imported-history")
				gitCmd(t, repo, "checkout", branch)
				gitCmd(t, repo, "merge", "--ff-only", "imported")
				gitCommit(t, repo, "local-work")
				done = true
			}
			if beats >= 2 {
				return errOwnerExited
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	heartbeats := hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(heartbeats) != 2 {
		t.Fatalf("beats %d", len(heartbeats))
	}
	subjects := commitSubjects(t, heartbeats[1].body["commits"])
	joined := strings.Join(subjects, "\n")
	if !strings.Contains(joined, "local-work") || strings.Contains(joined, "side-work") || strings.Contains(joined, "imported-history") {
		t.Fatalf("commits %v", subjects)
	}
}

func TestRunHeartbeatInitFailureClosesGeneration(t *testing.T) {
	dir := t.TempDir()
	opts := heartbeatTestOptions(dir)
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(opts.StateDir, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Run("stop succeeds", func(t *testing.T) {
		var calls []hbCall
		srv := heartbeatFixture(t, &calls, "", "")
		defer srv.Close()
		rt, _, _ := heartbeatRuntime(t, srv)
		err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
		if err == nil {
			t.Fatal("save failure was ignored")
		}
		if len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 1 || len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
			t.Fatalf("register %d stop %d", len(hbWhere(calls, http.MethodPost, "/harness-sessions")), len(hbWhere(calls, http.MethodPost, "/stop")))
		}
		if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 {
			t.Fatal("failed init heartbeated")
		}
		if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "stop.intent")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("successful close kept a stop intent")
		}
	})
}

func TestRunHeartbeatInitFailureKeepsStopIntent(t *testing.T) {
	dir := t.TempDir()
	opts := heartbeatTestOptions(dir)
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(opts.StateDir, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	stops := 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
			stops++
			if stops == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				return true
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err == nil {
		t.Fatal("save failure was ignored")
	}
	if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "stop.intent")); statErr != nil {
		t.Fatalf("stop intent: %v", statErr)
	}
	if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 {
		t.Fatal("failed init heartbeated")
	}
	// The planted directory blocked the first save. Recovery can register only after it is gone.
	if err := os.Remove(filepath.Join(opts.StateDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }})
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	heartbeats := hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(heartbeats) != 0 {
		t.Fatal("recovery heartbeated before the generation was closed")
	}
	foundStop := 0
	for _, call := range calls {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/stop") {
			foundStop++
		}
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/heartbeat") {
			t.Fatal("heartbeat before recovery finished")
		}
	}
	if foundStop < 2 {
		t.Fatalf("stops %d", foundStop)
	}
}

func TestUsageScanBoundedAndCancellable(t *testing.T) {
	dir := t.TempDir()
	path := claudeUsagePath(t, dir, "usage.jsonl")
	line1 := `{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":0}}}` + "\n"
	line2 := `{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":3,"output_tokens":1,"cache_read_input_tokens":0}}}` + "\n"
	if err := os.WriteFile(path, []byte(line1+line2), 0o600); err != nil {
		t.Fatal(err)
	}
	sums, next, _, _, err := scanUsageWindow(context.Background(), path, "", 0, int64(len(line1)), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if next != int64(len(line1)) || sums["claude-opus"].input != 2 || sums["claude-opus"].output != 1 {
		t.Fatalf("first window next %d sums %#v", next, sums)
	}
	rest, end, _, _, err := scanUsageWindow(context.Background(), path, "", next, 1<<20, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if end != int64(len(line1+line2)) || rest["claude-opus"].input != 3 {
		t.Fatalf("second window end %d sums %#v", end, rest)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, _, err := scanUsageWindow(ctx, path, "", 0, 1<<20, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan: %v", err)
	}
}

func TestParseProcStatRejectsZombieShape(t *testing.T) {
	state, start, err := parseProcStat([]byte("42 (agent) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 4242 0 0\n"))
	if err != nil || state != "S" || start != 4242 {
		t.Fatalf("state %s start %d err %v", state, start, err)
	}
	state, start, err = parseProcStat([]byte("42 (a) b) Z 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 99 0 0\n"))
	if err != nil || state != "Z" || start != 99 {
		t.Fatalf("zombie state %s start %d err %v", state, start, err)
	}
}

func heartbeatProbeUsageLine(i int) string {
	return fmt.Sprintf(`{"uuid":"%08d-0000-4000-8000-000000000000","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":0}}}`+"\n", i)
}

type heartbeatErrAfter struct {
	context.Context
	left int
}

func (c *heartbeatErrAfter) Err() error {
	if c.left <= 0 {
		return context.Canceled
	}
	c.left--
	return c.Context.Err()
}

func TestUsagePartialLineKeepsStartOffset(t *testing.T) {
	path := claudeUsagePath(t, t.TempDir(), "usage.jsonl")
	line := heartbeatProbeUsageLine(1)
	cut := len(line) / 2
	if err := os.WriteFile(path, []byte(line[:cut]), 0o600); err != nil {
		t.Fatal(err)
	}
	first, offset, recent, _, err := scanUsageWindow(context.Background(), path, "", 0, heartbeatUsageWindow, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(line[cut:])
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, _, err := scanUsageWindow(context.Background(), path, "", offset, heartbeatUsageWindow, recent, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := first["claude-opus"].input + second["claude-opus"].input; got != 2 {
		t.Fatalf("partial line lost: first cursor=%d, total input=%d, want 2", offset, got)
	}
}

func TestUsageOversizedLineStaysInsideBudget(t *testing.T) {
	path := claudeUsagePath(t, t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 8<<20)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, offset, _, _, err := scanUsageWindow(context.Background(), path, "", 0, heartbeatUsageWindow, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if offset > heartbeatUsageWindow+heartbeatUsageLineMax+1 {
		t.Fatalf("1 MiB scan consumed %d bytes through one oversized line", offset)
	}
}

func TestReadLimitedLineHonorsCancelAndBudget(t *testing.T) {
	payload := strings.Repeat("x", 8<<20) + "\n"
	ctx := &heartbeatErrAfter{Context: context.Background(), left: 1}
	reader := bufio.NewReader(strings.NewReader(payload))
	_, _, _, err := readLimitedLine(ctx, reader, heartbeatUsageLineMax, int64(heartbeatUsageLineMax)+1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) < 7<<20 {
		t.Fatalf("cancelled line read consumed too much, left %d", len(rest))
	}

	budgetReader := bufio.NewReader(strings.NewReader(payload))
	_, overflow, remainder, err := readLimitedLine(context.Background(), budgetReader, heartbeatUsageLineMax, 4096)
	if err != nil || !overflow || !remainder {
		t.Fatalf("budget overflow=%v err=%v", overflow, err)
	}
	left, err := io.ReadAll(budgetReader)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) < 7<<20 {
		t.Fatalf("budget read consumed the rest of the line, left %d", len(left))
	}
}

func TestHeartbeatBeatLookupHonorsCancellation(t *testing.T) {
	var calls []hbCall
	slow := false
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if slow && r.URL.Path == "/api/nodes" {
			time.Sleep(250 * time.Millisecond)
		}
		return false
	})
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer session.hold.release()
	slow = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = rt.heartbeatBeat(ctx, opts, heartbeatDeps{}, &session)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("deadline did not return an error")
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("20 ms cancellation took %v in project lookup", elapsed)
	}
}

func TestFinalUsageDrainsBacklog(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.Transcript = claudeUsagePath(t, filepath.Dir(opts.StateDir), "usage.jsonl")
	if err := os.WriteFile(opts.Transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const count = 12000
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			var content strings.Builder
			for i := 1; i <= count; i++ {
				content.WriteString(heartbeatProbeUsageLine(i))
			}
			if err := os.WriteFile(opts.Transcript, []byte(content.String()), 0o600); err != nil {
				return err
			}
			return errOwnerExited
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	usage := hbWhere(calls, http.MethodPost, "/usage")
	got := float64(0)
	if len(usage) > 0 {
		got, _ = usage[len(usage)-1].body["input_tokens"].(float64)
	}
	if got != count*2 {
		t.Fatalf("stopped after flushing %.0f input tokens; want %d", got, count*2)
	}
}

func TestFinalUsageTimeoutStillStops(t *testing.T) {
	prev := heartbeatUsageFlushTimeout
	heartbeatUsageFlushTimeout = 200 * time.Millisecond
	t.Cleanup(func() { heartbeatUsageFlushTimeout = prev })
	var calls []hbCall
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage") {
			<-r.Context().Done()
			return true
		}
		return false
	})
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.Transcript = claudeUsagePath(t, filepath.Dir(opts.StateDir), "usage.jsonl")
	if err := os.WriteFile(opts.Transcript, []byte(heartbeatProbeUsageLine(1)), 0o600); err != nil {
		t.Fatal(err)
	}
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer session.hold.release()
	started := time.Now()
	err = rt.finishHeartbeat(opts, &session)
	if got := len(hbWhere(calls, http.MethodPost, "/stop")); got != 1 {
		t.Fatalf("after %v usage timeout, /stop requests=%d (want 1); finish error=%v", time.Since(started), got, err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("stop waited on the usage budget: %v", time.Since(started))
	}
}

func TestFinishHeartbeatStopIntentIsRetried(t *testing.T) {
	var calls []hbCall
	stops := 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
			stops++
			if stops == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				return true
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	err = rt.finishHeartbeat(opts, &session)
	if err == nil {
		t.Fatal("stop failure was ignored")
	}
	if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "stop.intent")); statErr != nil {
		t.Fatalf("stop intent: %v", statErr)
	}
	session.hold.release()
	resumed, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.hold.release()
	if stops < 2 {
		t.Fatalf("stops %d", stops)
	}
	if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "stop.intent")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("retried stop left the intent in place")
	}
}

func TestStopIntentFlushesPendingUsage(t *testing.T) {
	dir := t.TempDir()
	transcript := claudeUsagePath(t, dir, "usage.jsonl")
	if err := os.WriteFile(transcript, []byte(heartbeatProbeUsageLine(1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	posts := 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage") {
			posts++
			if posts == 1 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return true
				}
				_ = conn.Close()
				return true
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Transcript = transcript
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, &session)
	if err == nil {
		session.hold.release()
		t.Fatal("first report should lose its response")
	}
	if len(session.disk.PendingUsage) != 1 {
		session.hold.release()
		t.Fatalf("pending %d", len(session.disk.PendingUsage))
	}
	if err := session.hold.writeFile("stop.intent", []byte(session.id+"\n")); err != nil {
		session.hold.release()
		t.Fatal(err)
	}
	session.hold.release()
	resumed, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.hold.release()
	usage := hbWhere(calls, http.MethodPost, "/usage")
	if len(usage) < 2 || usage[0].body["report_id"] != usage[1].body["report_id"] || usage[1].body["input_tokens"] != float64(2) {
		t.Fatalf("pending usage was not retried: %#v", usage)
	}
	if len(hbWhere(calls, http.MethodPost, "/stop")) < 1 {
		t.Fatal("retried start did not stop")
	}
}

func TestClaudeTitleSkipsOversizedLine(t *testing.T) {
	for _, size := range []int{6000, 8192} {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			id := "0199a213-81c0-7800-8aa1-bbab2a035a53"
			projects := filepath.Join(t.TempDir(), "projects")
			sessionDir := filepath.Join(projects, "encoded-cwd")
			if err := os.MkdirAll(sessionDir, 0o700); err != nil {
				t.Fatal(err)
			}
			var body strings.Builder
			body.WriteString(`{"type":"custom-title","customTitle":"Before"}` + "\n")
			body.WriteString(`{"type":"assistant","message":{"content":"` + strings.Repeat("x", size) + `"}}` + "\n")
			body.WriteString(`{"type":"custom-title","customTitle":"After"}` + "\n")
			transcript := filepath.Join(sessionDir, id+".jsonl")
			if err := os.WriteFile(transcript, []byte(body.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			got, ok := claudeSessionLabel(heartbeatOptions{ClaudeProjects: projects}, id)
			if !ok || got != "After" {
				t.Fatalf("title %q ok %v", got, ok)
			}
		})
	}
}

func TestSuccessfulStopRetriesPendingUsageWithoutHeartbeat(t *testing.T) {
	dir := t.TempDir()
	transcript := claudeUsagePath(t, dir, "usage.jsonl")
	if err := os.WriteFile(transcript, []byte(heartbeatProbeUsageLine(1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	posts := 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage") {
			posts++
			if posts <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				return true
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Transcript = transcript
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			return errOwnerExited
		},
	})
	if err != nil {
		t.Fatalf("stop: %v stderr %s", err, stderr.String())
	}
	if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "settle.intent")); statErr != nil {
		t.Fatalf("settle intent: %v", statErr)
	}
	heartbeats := len(hbWhere(calls, http.MethodPost, "/heartbeat"))
	registrations := len(hbWhere(calls, http.MethodPost, "/harness-sessions"))
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			t.Fatal("restart heartbeated a stopped generation")
			return errOwnerExited
		},
	})
	if err != nil {
		t.Fatalf("restart: %v stderr %s", err, stderr.String())
	}
	if got := len(hbWhere(calls, http.MethodPost, "/heartbeat")); got != heartbeats {
		t.Fatalf("heartbeats %d, want %d", got, heartbeats)
	}
	if got := len(hbWhere(calls, http.MethodPost, "/harness-sessions")); got != registrations {
		t.Fatalf("registrations %d", got)
	}
	if posts < 3 {
		t.Fatalf("usage posts %d, pending report was not retried", posts)
	}
	usage := hbWhere(calls, http.MethodPost, "/usage")
	if usage[len(usage)-1].body["input_tokens"] != float64(2) {
		t.Fatalf("settled usage %#v", usage[len(usage)-1].body)
	}
}

func TestForbiddenHeartbeatSettlesPartialRecordAfterPendingReplay(t *testing.T) {
	dir := t.TempDir()
	transcript := claudeUsagePath(t, dir, "usage.jsonl")
	line1 := heartbeatProbeUsageLine(1)
	line2 := heartbeatProbeUsageLine(2)
	partial := line2[:len(line2)/2]
	if err := os.WriteFile(transcript, []byte(line1+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	heartbeats := 0
	usagePosts := 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/heartbeat") {
			heartbeats++
			if heartbeats >= 2 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"stopped"}`))
				return true
			}
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage") {
			usagePosts++
			if usagePosts == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				return true
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Transcript = transcript
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("stop: %v stderr %s", err, stderr.String())
	}
	if heartbeats != 2 {
		t.Fatalf("heartbeats %d", heartbeats)
	}
	if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "settle.intent")); statErr != nil {
		t.Fatalf("settle intent after forbidden heartbeat: %v", statErr)
	}
	stopped := loadHeartbeatDisk(t, opts.StateDir)
	if !stopped.Terminal || stopped.Closed || len(stopped.PendingUsage) != 1 {
		t.Fatalf("after forbidden heartbeat terminal=%v closed=%v pending=%d", stopped.Terminal, stopped.Closed, len(stopped.PendingUsage))
	}

	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			t.Fatal("replay heartbeated a stopped generation")
			return errOwnerExited
		},
	})
	if err != nil {
		t.Fatalf("replay: %v stderr %s", err, stderr.String())
	}
	if heartbeats != 2 {
		t.Fatalf("heartbeats after replay %d", heartbeats)
	}
	if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "settle.intent")); statErr != nil {
		t.Fatalf("settle intent cleared while the next record was partial: %v", statErr)
	}
	replayed := loadHeartbeatDisk(t, opts.StateDir)
	info, err := os.Lstat(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Terminal || replayed.Closed || len(replayed.PendingUsage) != 0 || replayed.UsageOffset >= info.Size() || replayed.UsageDiscard {
		t.Fatalf("after replay terminal=%v closed=%v pending=%d cursor=%d size=%d discard=%v", replayed.Terminal, replayed.Closed, len(replayed.PendingUsage), replayed.UsageOffset, info.Size(), replayed.UsageDiscard)
	}
	usage := hbWhere(calls, http.MethodPost, "/usage")
	if len(usage) != 2 || usage[1].body["input_tokens"] != float64(2) || usage[0].body["report_id"] != usage[1].body["report_id"] {
		t.Fatalf("pending replay %#v", usage)
	}

	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(line2[len(partial):])
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			t.Fatal("recovery heartbeated a stopped generation")
			return errOwnerExited
		},
	})
	if err != nil {
		t.Fatalf("recovery: %v stderr %s", err, stderr.String())
	}
	if heartbeats != 2 || len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 1 || len(hbWhere(calls, http.MethodPost, "/stop")) != 0 {
		t.Fatalf("heartbeats %d registrations %d stops %d", heartbeats, len(hbWhere(calls, http.MethodPost, "/harness-sessions")), len(hbWhere(calls, http.MethodPost, "/stop")))
	}
	if _, statErr := os.Lstat(filepath.Join(opts.StateDir, "settle.intent")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("settle intent after the partial record completed: %v", statErr)
	}
	settled := loadHeartbeatDisk(t, opts.StateDir)
	if !settled.Terminal || settled.Closed || len(settled.PendingUsage) != 0 {
		t.Fatalf("settled terminal=%v closed=%v pending=%d", settled.Terminal, settled.Closed, len(settled.PendingUsage))
	}
	usage = hbWhere(calls, http.MethodPost, "/usage")
	if len(usage) != 3 || usage[2].body["input_tokens"] != float64(4) {
		t.Fatalf("partial record was not settled: %#v", usage)
	}
}

func loadHeartbeatDisk(t *testing.T, stateDir string) heartbeatDisk {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk heartbeatDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	return disk
}

func TestStopRecoveryDoesNotDoubleCountUsage(t *testing.T) {
	dir := t.TempDir()
	transcript := claudeUsagePath(t, dir, "usage.jsonl")
	if err := os.WriteFile(transcript, []byte(heartbeatProbeUsageLine(1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	stops := 0
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
			stops++
			if stops == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				return true
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(dir)
	opts.Transcript = transcript
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			return errOwnerExited
		},
	})
	if err == nil {
		t.Fatal("stop failure was ignored")
	}
	raw, err := os.ReadFile(filepath.Join(opts.StateDir, "session.id"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(raw))
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			t.Fatal("recovery heartbeated the completed generation")
			return errOwnerExited
		},
	})
	if err != nil {
		t.Fatalf("recovery: %v stderr %s", err, stderr.String())
	}
	if got := len(hbWhere(calls, http.MethodPost, "/harness-sessions")); got != 1 {
		t.Fatalf("registrations %d", got)
	}
	usage := hbWhere(calls, http.MethodPost, "/usage")
	if len(usage) != 1 || usage[0].body["input_tokens"] != float64(2) {
		t.Fatalf("usage posts %#v", usage)
	}
	kept, err := os.ReadFile(filepath.Join(opts.StateDir, "session.id"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(kept)) != id {
		t.Fatalf("generation changed from %s to %s", id, strings.TrimSpace(string(kept)))
	}
	stateRaw, err := os.ReadFile(filepath.Join(opts.StateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk heartbeatDisk
	if err := json.Unmarshal(stateRaw, &disk); err != nil {
		t.Fatal(err)
	}
	if !disk.Closed || disk.UsageOffset == 0 {
		t.Fatalf("closed %v cursor %d", disk.Closed, disk.UsageOffset)
	}
}

func TestUsageOversizedSuffixIsNotAnotherRecord(t *testing.T) {
	good := heartbeatProbeUsageLine(1)
	suffix := strings.TrimSuffix(heartbeatProbeUsageLine(2), "\n")
	limit := heartbeatUsageWindow + int64(heartbeatUsageLineMax) + 1
	if int64(len(good)) >= limit {
		t.Fatal("probe line is longer than the scan limit")
	}
	pad := strings.Repeat("x", int(limit)-len(good))
	path := claudeUsagePath(t, t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(path, []byte(good+pad+suffix+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sums, next, recent, discard, err := scanUsageWindow(context.Background(), path, "", 0, heartbeatUsageWindow, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if sums["claude-opus"].input != 2 || !discard || next != limit {
		t.Fatalf("first window input %d discard %v cursor %d limit %d", sums["claude-opus"].input, discard, next, limit)
	}
	if got := good + pad; int64(len(got)) != limit || !strings.HasPrefix(suffix, "{") {
		t.Fatal("suffix is not aligned to the scan limit")
	}
	rest, _, _, still, err := scanUsageWindow(context.Background(), path, "", next, heartbeatUsageWindow, recent, true)
	if err != nil {
		t.Fatal(err)
	}
	if rest["claude-opus"].input != 0 || still {
		t.Fatalf("discarded suffix counted input %d discard %v", rest["claude-opus"].input, still)
	}
	again, _, _, _, err := scanUsageWindow(context.Background(), path, "", next, heartbeatUsageWindow, recent, false)
	if err != nil {
		t.Fatal(err)
	}
	if again["claude-opus"].input != 2 {
		t.Fatalf("suffix without discard state counted %d, want 2", again["claude-opus"].input)
	}

	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.Transcript = path
	beats := 0
	err = rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			beats++
			if beats >= 2 {
				return errOwnerExited
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v stderr %s", err, stderr.String())
	}
	usage := hbWhere(calls, http.MethodPost, "/usage")
	if len(usage) != 1 || usage[0].body["input_tokens"] != float64(2) {
		t.Fatalf("usage across beats %#v", usage)
	}
}

func TestRebaseFastForwardIsNotLocal(t *testing.T) {
	repo, _ := gitRepo(t)
	opts := heartbeatOptions{Worktree: repo}
	disk := heartbeatDisk{}
	bindHeartbeatWorktree(context.Background(), opts, &disk)
	branch := disk.BoundBranch
	gitCmd(t, repo, "checkout", "-b", "upstream")
	gitCommit(t, repo, "imported-upstream")
	gitCmd(t, repo, "checkout", branch)
	gitCmd(t, repo, "rebase", "upstream")
	commits, _ := localHeartbeatCommits(context.Background(), repo, &disk)
	for _, c := range commits {
		if c.Subject == "imported-upstream" {
			t.Fatal("rebase fast-forward attributed upstream's commit to the worker")
		}
	}
}

func TestRebaseReplayKeepsLocalCommits(t *testing.T) {
	repo, base := gitRepo(t)
	opts := heartbeatOptions{Worktree: repo}
	disk := heartbeatDisk{}
	bindHeartbeatWorktree(context.Background(), opts, &disk)
	branch := disk.BoundBranch
	if err := os.WriteFile(filepath.Join(repo, "local"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "local")
	gitCmd(t, repo, "commit", "-m", "local-work")
	gitCmd(t, repo, "checkout", "-b", "upstream", base)
	gitCommit(t, repo, "upstream-work")
	gitCmd(t, repo, "checkout", branch)
	gitCmd(t, repo, "rebase", "upstream")
	commits, _ := localHeartbeatCommits(context.Background(), repo, &disk)
	subjects := make([]string, 0, len(commits))
	for _, c := range commits {
		subjects = append(subjects, c.Subject)
	}
	joined := strings.Join(subjects, "\n")
	if !strings.Contains(joined, "local-work") || strings.Contains(joined, "upstream-work") {
		t.Fatalf("rebase replay commits %v", subjects)
	}
}

func commitSubjects(t *testing.T, raw any) []string {
	t.Helper()
	items, _ := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		row, _ := item.(map[string]any)
		subject, _ := row["subject"].(string)
		out = append(out, subject)
	}
	return out
}
