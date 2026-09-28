// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"bytes"
	"context"
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
)

type hbCall struct {
	method string
	path   string
	lease  string
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
		*calls = append(*calls, hbCall{method: r.Method, path: r.URL.Path, lease: r.Header.Get("X-Aeon-Worker-Lease"), body: body})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			_, _ = w.Write([]byte(`{"items":[{"id":"project-kind","slug":"project"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{project}})
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
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	var stdout, stderr bytes.Buffer
	return &runtime{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr, program: "aeon"}, &stdout, &stderr
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

func TestRunHeartbeatOwnerExitMarksStopped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alive bool
		ctx   func() (context.Context, context.CancelFunc)
	}{
		{name: "owner already gone", alive: false, ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }},
		{name: "signal", alive: true, ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			rt, _, stderr := heartbeatRuntime(t, srv)
			ctx, cancel := tc.ctx()
			defer cancel()
			err := rt.runHeartbeat(ctx, heartbeatTestOptions(t.TempDir()), heartbeatDeps{alive: func(int) bool { return tc.alive }})
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
	if code != 0 {
		t.Fatalf("exit %d stdout %s stderr %s", code, stdout, stderr)
	}
	if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 || len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatalf("calls heartbeat %d stop %d", len(hbWhere(calls, http.MethodPost, "/heartbeat")), len(hbWhere(calls, http.MethodPost, "/stop")))
	}
	reg := hbWhere(calls, http.MethodPost, "/harness-sessions")
	if len(reg) != 1 {
		t.Fatal("missing registration")
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
	transcript := filepath.Join(dir, "33333333-3333-4333-8333-333333333331.jsonl")
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
	if err != nil {
		t.Fatalf("resume: %v stderr %s", err, stderr.String())
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
		*calls = append(*calls, hbCall{method: r.Method, path: r.URL.Path, lease: r.Header.Get("X-Aeon-Worker-Lease"), body: body})
		w.Header().Set("Content-Type", "application/json")
		if handle != nil && handle(r, body, w) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			_, _ = w.Write([]byte(`{"items":[{"id":"project-kind","slug":"project"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{project}})
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
			before := len(calls)
			err = rt.runHeartbeat(context.Background(), heartbeatTestOptions(dir), heartbeatDeps{alive: func(int) bool { return true }})
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if len(calls) != before {
				t.Fatalf("resume of a closed generation made %d requests", len(calls)-before)
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
	if err != nil {
		t.Fatalf("resume: %v stderr %s", err, stderr.String())
	}
	if len(hbWhere(calls, http.MethodPost, "/heartbeat")) != 0 || len(hbWhere(calls, http.MethodPost, "/harness-sessions")) != 0 {
		t.Fatal("reused pid resumed the generation")
	}
	if len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatal("reused pid did not stop the generation")
	}
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
	transcript := filepath.Join(dir, "33333333-3333-4333-8333-333333333331.jsonl")
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
	transcript := filepath.Join(dir, "33333333-3333-4333-8333-333333333331.jsonl")
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
	transcript := filepath.Join(dir, "33333333-3333-4333-8333-333333333331.jsonl")
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
	repo, _ := gitRepo(t)
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	opts.Worktree = repo
	made := false
	beats := 0
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		label: func() (string, bool) { return "worker", true },
		wait: func(context.Context, int, time.Duration) error {
			beats++
			if beats == 1 && !made {
				for i := 1; i <= 21; i++ {
					gitCommit(t, repo, fmt.Sprintf("c%02d", i))
				}
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
	first := commitSubjects(t, heartbeats[1].body["commits"])
	second := commitSubjects(t, heartbeats[2].body["commits"])
	if len(first) != 20 || first[0] != "c01" || first[19] != "c20" {
		t.Fatalf("first batch %v", first)
	}
	if len(second) != 1 || second[0] != "c21" {
		t.Fatalf("second batch %v", second)
	}
	for _, subject := range first {
		if subject == "c21" {
			t.Fatal("later commit was sent before the cursor advanced")
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
	err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return false }})
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
	path := filepath.Join(dir, "usage.jsonl")
	line1 := `{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":0}}}` + "\n"
	line2 := `{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","type":"assistant","message":{"model":"claude-opus","usage":{"input_tokens":3,"output_tokens":1,"cache_read_input_tokens":0}}}` + "\n"
	if err := os.WriteFile(path, []byte(line1+line2), 0o600); err != nil {
		t.Fatal(err)
	}
	sums, next, _, err := scanUsageWindow(context.Background(), path, "", 0, int64(len(line1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if next != int64(len(line1)) || sums["claude-opus"].input != 2 || sums["claude-opus"].output != 1 {
		t.Fatalf("first window next %d sums %#v", next, sums)
	}
	rest, end, _, err := scanUsageWindow(context.Background(), path, "", next, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if end != int64(len(line1+line2)) || rest["claude-opus"].input != 3 {
		t.Fatalf("second window end %d sums %#v", end, rest)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := scanUsageWindow(ctx, path, "", 0, 1<<20, nil); !errors.Is(err, context.Canceled) {
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
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	line := heartbeatProbeUsageLine(1)
	cut := len(line) / 2
	if err := os.WriteFile(path, []byte(line[:cut]), 0o600); err != nil {
		t.Fatal(err)
	}
	first, offset, recent, err := scanUsageWindow(context.Background(), path, "", 0, heartbeatUsageWindow, nil)
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
	second, _, _, err := scanUsageWindow(context.Background(), path, "", offset, heartbeatUsageWindow, recent)
	if err != nil {
		t.Fatal(err)
	}
	if got := first["claude-opus"].input + second["claude-opus"].input; got != 2 {
		t.Fatalf("partial line lost: first cursor=%d, total input=%d, want 2", offset, got)
	}
}

func TestUsageOversizedLineStaysInsideBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 8<<20)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, offset, _, err := scanUsageWindow(context.Background(), path, "", 0, heartbeatUsageWindow, nil)
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
	_, _, err := readLimitedLine(ctx, reader, heartbeatUsageLineMax, int64(heartbeatUsageLineMax)+1)
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
	_, overflow, err := readLimitedLine(context.Background(), budgetReader, heartbeatUsageLineMax, 4096)
	if err != nil || !overflow {
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
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{})
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
	opts.Transcript = filepath.Join(filepath.Dir(opts.StateDir), "usage.jsonl")
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
	opts.Transcript = filepath.Join(filepath.Dir(opts.StateDir), "usage.jsonl")
	if err := os.WriteFile(opts.Transcript, []byte(heartbeatProbeUsageLine(1)), 0o600); err != nil {
		t.Fatal(err)
	}
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{})
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
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{})
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
	resumed, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{})
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
	transcript := filepath.Join(dir, "usage.jsonl")
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
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{})
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
	resumed, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{})
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
