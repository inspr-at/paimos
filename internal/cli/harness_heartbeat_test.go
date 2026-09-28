// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
