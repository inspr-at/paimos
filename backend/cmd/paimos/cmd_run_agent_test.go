// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// newRunServer serves a canned run detail for GET /api/runs/{id} and records
// the body of every PATCH so a test can assert the status transitions. PATCHes
// to the claim (if_status) succeed; override patchStatus to simulate a lost
// claim (409).
func newRunServer(t *testing.T, detail string, patchStatus int) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	patches := &[]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/runs/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(detail))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/issues/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":5,
				"issue_key":"PAI-5",
				"type":"ticket",
				"title":"Implement the demo change",
				"description":"Change VERSION to 0.2.0.",
				"acceptance_criteria":"npm test passes.",
				"notes":"Do not deploy from the agent.",
				"status":"new",
				"priority":"low"
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/42/agents/codex.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"project":{"id":42,"name":"PAIMOS","key":"PAI"},
				"agent":{
					"id":7,
					"project_id":42,
					"name":"codex",
					"description":"Implementation agent.",
					"slash_command_name":"codex",
					"lane_tags":["implementation","backend"],
					"metadata":{},
					"body":"Use bounded project context. token=supersecret123456",
					"bootstrap_steps":[{"title":"Check tree","command":"git status --short","rationale":"Understand local changes."}],
					"non_negotiable_rules":[{"title":"No secret output","body":"Never print password=hunter22222 in logs.","memory_ref":"memory:no_secret_output"}],
					"created_at":"",
					"updated_at":""
				},
				"repos":[{"label":"app","url":"https://github.com/example/app","default_branch":"main"}],
				"environments":[{"name":"local-dev","url":"http://localhost:5173","host_alias":"localhost","host_ip":""}],
				"deploy_recipes":[{"name":"local","summary":"Local check","command":"npm test"}]
			}`))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/runs/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			*patches = append(*patches, body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(patchStatus)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/attachments":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":99}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/attachments/link":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"linked":1}`))
		default:
			http.Error(w, `{"error":"unmocked"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, patches
}

func aJob() runJob { return runJob{runID: 1, issueKey: "PAI-5"} }

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func TestAgentRunnerSuccess(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusOK)
	spawned := false
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp/repo",
		autoConfirm: true,
		spawn: func(_ context.Context, root, _ string, env []string, _ io.Writer) error {
			spawned = true
			if root != "/tmp/repo" {
				t.Errorf("spawn root=%q, want /tmp/repo", root)
			}
			em := envMap(env)
			if em["PAIMOS_RUN_ID"] != "1" || em["PAIMOS_ISSUE_KEY"] != "PAI-5" || em["PAIMOS_ISSUE_TITLE"] != "Implement the demo change" {
				t.Errorf("spawn env=%v", env)
			}
			promptPath := em["PAIMOS_PROMPT_FILE"]
			if promptPath == "" {
				t.Fatal("spawn env missing PAIMOS_PROMPT_FILE")
			}
			prompt, err := os.ReadFile(promptPath)
			if err != nil {
				t.Fatalf("read prompt: %v", err)
			}
			for _, want := range []string{"PAIMOS local Implement-this worker", "Issue: PAI-5", "Change VERSION to 0.2.0.", "npm test passes."} {
				if !strings.Contains(string(prompt), want) {
					t.Fatalf("prompt %q missing %q", string(prompt), want)
				}
			}
			return nil
		},
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if !spawned {
		t.Error("spawn was not called")
	}
	// Claim (running, stamping the actual device) then report completed. No
	// configured test command ran, so tests_passed would be fabricated evidence.
	if len(*patches) != 2 ||
		(*patches)[0]["status"] != "running" ||
		(*patches)[0]["if_status"] != "queued" ||
		(*patches)[0]["device_id"] != "dev-1" ||
		(*patches)[0]["action_key"] != "claude_cli.implement" ||
		(*patches)[1]["status"] != "completed" {
		t.Fatalf("patches=%+v, want claim(running,if_status=queued,device_id=dev-1,action_key=claude_cli.implement) then completed", *patches)
	}
}

func TestAgentRunnerReportsDeclaredCommitRange(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusOK)
	root := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.name", "PAIMOS Test")
	runGit("config", "user.email", "test@example.invalid")
	runGit("remote", "add", "origin", "https://github.com/example/app.git")
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "result.txt")
	runGit("commit", "-m", "base")
	base := runGit("rev-parse", "HEAD")

	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: root,
		autoConfirm: true,
		spawn: func(_ context.Context, _, _ string, _ []string, _ io.Writer) error {
			if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("implemented\n"), 0o600); err != nil {
				return err
			}
			runGit("add", "result.txt")
			runGit("commit", "-m", "implement")
			return nil
		},
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	head := runGit("rev-parse", "HEAD")
	last := (*patches)[len(*patches)-1]
	if last["repo_url"] != "https://github.com/example/app" || last["branch_name"] != "main" ||
		last["commit_base_sha"] != base || last["commit_sha"] != head || base == head {
		t.Fatalf("commit evidence=%+v, want declared %s..%s on main", last, base, head)
	}
}

func TestAgentRunnerReportsEqualSHAsWhenNoCommitWasProduced(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusOK)
	root := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.name", "PAIMOS Test")
	runGit("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "result.txt")
	runGit("commit", "-m", "base")
	base := runGit("rev-parse", "HEAD")

	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: root,
		autoConfirm: true,
		spawn:       func(_ context.Context, _, _ string, _ []string, _ io.Writer) error { return nil },
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	last := (*patches)[len(*patches)-1]
	if last["commit_base_sha"] != base || last["commit_sha"] != base {
		t.Fatalf("commit evidence=%+v, want equal base/head %s", last, base)
	}
}

func TestBrowserRepoURLNeverCarriesRemoteCredentials(t *testing.T) {
	got := browserRepoURL("https://user:token@github.com/example/app.git?credential=secret#fragment")
	if got != "https://github.com/example/app" {
		t.Fatalf("browserRepoURL=%q, want credential-free canonical URL", got)
	}
}

func TestAgentRunnerSelectedAgentContextPromptAndEnv(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"project_id":42,"device_id":"","agent_name":"codex","context_pack":"issue","status":"queued"}`, http.StatusOK)
	spawned := false
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp/repo",
		autoConfirm: true,
		spawn: func(_ context.Context, _, _ string, env []string, _ io.Writer) error {
			spawned = true
			em := envMap(env)
			if em["PAIMOS_AGENT_NAME"] != "codex" {
				t.Fatalf("PAIMOS_AGENT_NAME=%q, want codex", em["PAIMOS_AGENT_NAME"])
			}
			if em["PAIMOS_CONTEXT_PACK"] != "issue" || em["PAIMOS_CONTEXT_PACK_LABEL"] != "Issue only" {
				t.Fatalf("context env=%v", em)
			}
			artifactPath := em["PAIMOS_AGENT_ARTIFACT_FILE"]
			if artifactPath == "" {
				t.Fatal("spawn env missing PAIMOS_AGENT_ARTIFACT_FILE")
			}
			artifact, err := os.ReadFile(artifactPath)
			if err != nil {
				t.Fatalf("read artifact file: %v", err)
			}
			if !strings.Contains(string(artifact), `"name":"codex"`) {
				t.Fatalf("artifact file missing selected agent: %s", string(artifact))
			}
			if strings.Contains(string(artifact), "supersecret123456") || strings.Contains(string(artifact), "hunter22222") {
				t.Fatalf("artifact file carried obvious secret-like text: %s", string(artifact))
			}
			promptPath := em["PAIMOS_PROMPT_FILE"]
			if promptPath == "" {
				t.Fatal("spawn env missing PAIMOS_PROMPT_FILE")
			}
			prompt, err := os.ReadFile(promptPath)
			if err != nil {
				t.Fatalf("read prompt: %v", err)
			}
			for _, want := range []string{
				"Context pack: issue (Issue only)",
				"Project agent: codex",
				"Lane tags: implementation, backend",
				"Use bounded project context.",
				"Agent bootstrap steps",
				"git status --short",
				"Agent rules",
				"memory:no_secret_output",
				"Agent repos",
				"Agent environments",
				"Agent deploy recipes",
			} {
				if !strings.Contains(string(prompt), want) {
					t.Fatalf("prompt %q missing %q", string(prompt), want)
				}
			}
			if strings.Contains(string(prompt), "supersecret123456") || strings.Contains(string(prompt), "hunter22222") {
				t.Fatalf("prompt carried obvious secret-like text: %s", string(prompt))
			}
			return nil
		},
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if !spawned {
		t.Error("spawn was not called")
	}
	if len(*patches) != 2 || (*patches)[0]["status"] != "running" || (*patches)[1]["status"] != "completed" {
		t.Fatalf("patches=%+v, want running then completed", *patches)
	}
}

func TestResolveRunnerActionInfersCodexFromExec(t *testing.T) {
	key, label, err := resolveRunnerAction("", "codex exec --full-auto")
	if err != nil {
		t.Fatalf("resolveRunnerAction: %v", err)
	}
	if key != "codex_cli.implement" || label != "Codex CLI" {
		t.Fatalf("action=%s label=%s, want Codex CLI", key, label)
	}
	key, label, err = resolveRunnerAction("", "claude")
	if err != nil {
		t.Fatalf("resolveRunnerAction claude: %v", err)
	}
	if key != "claude_cli.implement" || label != "Claude Code" {
		t.Fatalf("action=%s label=%s, want Claude Code", key, label)
	}
}

func TestRunnerDoesNotAdvertiseUnsupportedMidRunControls(t *testing.T) {
	path := appendRunnerActionQuery("/api/projects/7/agents/events?implement=1", "claude_cli.implement", true, true)
	for _, unsupported := range []string{"pause", "resume", "answer", "cancel"} {
		if strings.Contains(path, unsupported) {
			t.Fatalf("runner advertised unsupported %q control in %q", unsupported, path)
		}
	}
}

func TestAgentRunnerSkipsMismatchedAction(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","action_key":"codex_cli.implement","provider_label":"Codex CLI","status":"queued"}`, http.StatusOK)
	spawned := false
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp/repo",
		execCmd: "claude", autoConfirm: true,
		spawn: func(_ context.Context, _, _ string, _ []string, _ io.Writer) error {
			spawned = true
			return nil
		},
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if spawned {
		t.Fatal("runner spawned for a mismatched action")
	}
	if len(*patches) != 0 {
		t.Fatalf("patches=%+v, want none for mismatched action", *patches)
	}
}

func TestAgentRunnerTestExecReportsVersionAndSummary(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusOK)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("0.2.0\n"), 0o600); err != nil {
		t.Fatalf("seed VERSION: %v", err)
	}
	var calls []string
	a := withSpawnSupervisor(&agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: root,
		execCmd: "claude", testExec: "npm test", autoConfirm: true,
		spawn: func(_ context.Context, _, cmd string, _ []string, logSink io.Writer) error {
			calls = append(calls, cmd)
			if cmd != "npm test" && logSink != nil {
				t.Fatalf("agent command should not capture logs by default")
			}
			return nil
		},
	})
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if strings.Join(calls, ",") != "claude,npm test" {
		t.Fatalf("spawn calls = %v, want claude then npm test", calls)
	}
	last := (*patches)[len(*patches)-1]
	if last["status"] != "tests_passed" || last["version"] != "0.2.0" {
		t.Fatalf("final patch = %+v, want tests_passed v0.2.0", last)
	}
	summary, _ := last["tests_summary"].(string)
	if summary != "configured test command passed" {
		t.Fatalf("tests_summary=%q, want allowlisted test evidence", summary)
	}
	if last["log_attachment_id"] != nil {
		t.Fatalf("test summary must not imply log attachment by default, got %+v", last)
	}
}

func TestCompletedRunStatusRequiresTestEvidence(t *testing.T) {
	if got := completedRunStatus(false); got != "completed" {
		t.Fatalf("without test evidence status=%q, want completed", got)
	}
	if got := completedRunStatus(true); got != "tests_passed" {
		t.Fatalf("with successful configured test status=%q, want tests_passed", got)
	}
}

func TestAgentRunnerTestExecFailureReportsTestsFailed(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","deploy_target":"ppm","status":"queued"}`, http.StatusOK)
	var calls []string
	a := withSpawnSupervisor(&agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: t.TempDir(),
		execCmd: "claude", testExec: "npm test", autoConfirm: true,
		allowDeploy: true, deployExec: "just deploy-ppm", autoConfirmDep: true,
		spawn: func(_ context.Context, _, cmd string, _ []string, logSink io.Writer) error {
			calls = append(calls, cmd)
			if cmd == "npm test" {
				return errors.New("exit status 1")
			}
			return nil
		},
	})
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("test failure is a reported result, not a runner error: %v", err)
	}
	if strings.Join(calls, ",") != "claude,npm test" {
		t.Fatalf("spawn calls = %v, want no deploy after failed tests", calls)
	}
	last := (*patches)[len(*patches)-1]
	if last["status"] != "tests_failed" {
		t.Fatalf("final patch = %+v, want tests_failed", last)
	}
	if last["error"] != "provider_failure: configured command exited unsuccessfully" {
		t.Fatalf("error = %v, want safe test failure", last["error"])
	}
	summary, _ := last["tests_summary"].(string)
	if summary != "configured test command failed: provider_failure" {
		t.Fatalf("tests_summary=%q, want allowlisted failed test evidence", summary)
	}
}

func TestAgentRunnerAttachesLog(t *testing.T) {
	// When the spawn produces output, the runner uploads it as an attachment and
	// stamps log_attachment_id on the terminal report (PAI-617).
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusOK)
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp",
		autoConfirm: true, attachLogs: true,
		spawn: func(_ context.Context, _, _ string, _ []string, logSink io.Writer) error {
			if logSink != nil {
				_, _ = logSink.Write([]byte("build output\n"))
			}
			return nil
		},
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	last := (*patches)[len(*patches)-1]
	if last["status"] != "completed" {
		t.Fatalf("final patch = %+v, want completed", last)
	}
	if last["log_attachment_id"] == nil {
		t.Fatalf("expected log_attachment_id to be set after upload, got %+v", last)
	}
}

// TestDefaultSpawnTeesOutput proves the REAL defaultSpawn runs via a shell
// (PAI-619) and tees combined output to the log sink (PAI-617) — the end-to-end
// capture the AttachesLog test's fake spawn bypasses (audit F6).
func TestDefaultSpawnTeesOutput(t *testing.T) {
	var sink bytes.Buffer
	if err := defaultSpawn(context.Background(), t.TempDir(), "echo hello-from-spawn", nil, &sink); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !strings.Contains(sink.String(), "hello-from-spawn") {
		t.Fatalf("log sink = %q, want it to contain the command output", sink.String())
	}
}

func TestClaudeDefaultIsNonInteractivePromptMode(t *testing.T) {
	argv, err := claudeRunnerArgv(defaultClaudePermissionMode, defaultClaudeAllowedTools, false)
	if err != nil {
		t.Fatal(err)
	}
	command := strings.Join(argv, " ")
	if !commandReadsPromptOnStdin(command) {
		t.Fatal("normalized claude command should read prompt from stdin")
	}
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("implement PAI-5"), 0o600); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	prompt, err := promptForCommand(command, []string{"PAIMOS_PROMPT_FILE=" + promptFile})
	if err != nil {
		t.Fatalf("promptForCommand: %v", err)
	}
	if prompt != "implement PAI-5" {
		t.Fatalf("prompt=%q", prompt)
	}
	if prompt, err := promptForCommand("npm test", []string{"PAIMOS_PROMPT_FILE=" + promptFile}); err != nil || prompt != "" {
		t.Fatalf("non-agent command prompt=%q err=%v, want empty nil", prompt, err)
	}
}

func TestClaudePermissionModeAndAllowedToolsAreConfigurable(t *testing.T) {
	if defaultClaudePermissionMode != "dontAsk" || defaultClaudeAllowedTools != "Read,Glob,Grep,Edit,Write" {
		t.Fatalf("least-privilege defaults drifted: mode=%q tools=%q", defaultClaudePermissionMode, defaultClaudeAllowedTools)
	}
	validModes := []string{"acceptEdits", "auto", "bypassPermissions", "manual", "dontAsk", "plan"}
	for _, mode := range validModes {
		unsafe := mode == "bypassPermissions"
		argv, err := claudeRunnerArgv(mode, "Read,Grep,Bash", unsafe)
		if err != nil {
			t.Fatalf("valid mode %q: %v", mode, err)
		}
		got := " " + strings.Join(argv, " ") + " "
		for _, required := range []string{" --safe-mode ", " --permission-mode " + mode + " ", " --tools Read,Grep,Bash ", " --allowedTools Read,Grep,Bash "} {
			if !strings.Contains(got, required) {
				t.Fatalf("mode %q argv=%v missing %q", mode, argv, required)
			}
		}
		if strings.Contains(got, "--dangerously-skip-permissions") {
			t.Fatalf("argv enabled unconditional dangerous bypass: %v", argv)
		}
		if unsafe != strings.Contains(got, " --allow-dangerously-skip-permissions ") {
			t.Fatalf("mode %q dangerous acknowledgement mismatch: %v", mode, argv)
		}
	}
	for _, tc := range []struct{ mode, tools string }{
		{"unknown", "Read"}, {"default", "Read"}, {"dontAsk", ""},
		{"dontAsk", "Read,$(unsafe)"}, {"dontAsk", "Read;touch"}, {"dontAsk", "Read\nWrite"},
	} {
		if err := validateClaudeRunnerConfig(tc.mode, tc.tools); err == nil {
			t.Fatalf("accepted unsafe config mode=%q tools=%q", tc.mode, tc.tools)
		}
	}
	if err := validateClaudeRunnerConfig("bypassPermissions", "Read"); err == nil {
		t.Fatal("bypassPermissions was accepted without the separate unsafe opt-in")
	}
	if err := validateClaudeRunnerConfig("bypassPermissions", "Read", true); err != nil {
		t.Fatalf("explicit unsafe bypass opt-in rejected: %v", err)
	}
}

// TestAgentRunnerDefaultDoesNotAttachLog: without --attach-logs the runner must
// not capture or upload the job output (audit MED-2 — logs can carry secrets).
func TestAgentRunnerDefaultDoesNotAttachLog(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusOK)
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp",
		autoConfirm: true, // attachLogs defaults to false
		spawn: func(_ context.Context, _, _ string, _ []string, logSink io.Writer) error {
			if logSink != nil {
				_, _ = logSink.Write([]byte("secret output"))
			}
			return nil
		},
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if last := (*patches)[len(*patches)-1]; last["log_attachment_id"] != nil {
		t.Fatalf("no log should be attached by default, got %+v", last)
	}
}

func TestAgentRunnerSpawnFailure(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusOK)
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp",
		autoConfirm: true,
		spawn:       func(_ context.Context, _, _ string, _ []string, _ io.Writer) error { return errors.New("exit 1") },
	}
	if err := a.handleRun(context.Background(), aJob()); err == nil {
		t.Fatal("expected an error when the spawned command fails")
	}
	if len(*patches) != 2 || (*patches)[0]["status"] != "running" || (*patches)[1]["status"] != "failed" {
		t.Fatalf("patches=%+v, want running then failed", *patches)
	}
}

func TestAgentRunnerClaimLost(t *testing.T) {
	// The claim PATCH returns 409 — another runner won. We must NOT spawn.
	srv, _ := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusConflict)
	spawned := false
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp",
		autoConfirm: true,
		spawn:       func(_ context.Context, _, _ string, _ []string, _ io.Writer) error { spawned = true; return nil },
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("a lost claim should not be a hard error: %v", err)
	}
	if spawned {
		t.Error("a run claimed by another runner must not spawn")
	}
}

func TestAgentRunnerSkipsNonQueued(t *testing.T) {
	// A run already past 'queued' (claimed/handled) is not ours.
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"running"}`, http.StatusOK)
	spawned := false
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp",
		autoConfirm: true,
		spawn:       func(_ context.Context, _, _ string, _ []string, _ io.Writer) error { spawned = true; return nil },
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if spawned || len(*patches) != 0 {
		t.Errorf("a non-queued run must be skipped (spawned=%v patches=%+v)", spawned, *patches)
	}
}

func TestAgentRunnerDeviceTargetingSkips(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"other-device","status":"queued"}`, http.StatusOK)
	spawned := false
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp",
		autoConfirm: true,
		spawn:       func(_ context.Context, _, _ string, _ []string, _ io.Writer) error { spawned = true; return nil },
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if spawned {
		t.Error("a run targeted at another device must not spawn")
	}
	if len(*patches) != 0 {
		t.Errorf("no patches expected for a skipped run, got %+v", *patches)
	}
}

func TestAgentRunnerDeclineCancels(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","status":"queued"}`, http.StatusOK)
	spawned := false
	a := &agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp",
		autoConfirm: false,
		confirm:     func(_ string, _ int64, _ string) bool { return false },
		spawn:       func(_ context.Context, _, _ string, _ []string, _ io.Writer) error { spawned = true; return nil },
	}
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if spawned {
		t.Error("a declined run must not spawn")
	}
	if len(*patches) != 1 || (*patches)[0]["status"] != "cancelled" {
		t.Fatalf("patches=%+v, want a single cancelled", *patches)
	}
}

func TestAgentRunnerDeployGated(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","deploy_target":"ppm","status":"queued"}`, http.StatusOK)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("4.6.1\n"), 0o600); err != nil {
		t.Fatalf("seed VERSION: %v", err)
	}
	var calls []string
	a := withSpawnSupervisor(&agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: root,
		execCmd: "claude", autoConfirm: true,
		allowDeploy: true, deployExec: "just deploy-ppm", autoConfirmDep: true,
		spawn: func(_ context.Context, _, cmd string, _ []string, _ io.Writer) error {
			calls = append(calls, cmd)
			return nil
		},
	})
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if len(calls) != 2 || calls[0] != "claude" || calls[1] != "just deploy-ppm" {
		t.Fatalf("spawn calls = %v, want [claude, just deploy-ppm]", calls)
	}
	last := (*patches)[len(*patches)-1]
	if last["status"] != "deployed" || last["version"] != "4.6.1" || last["deploy_target"] != "ppm" {
		t.Fatalf("final patch = %+v, want deployed v4.6.1 ppm", last)
	}
}

func TestAgentRunnerDeployCarriesTestSummary(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","deploy_target":"local-dev","status":"queued"}`, http.StatusOK)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("1.2.3\n"), 0o600); err != nil {
		t.Fatalf("seed VERSION: %v", err)
	}
	var calls []string
	a := withSpawnSupervisor(&agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: root,
		execCmd: "claude", testExec: "npm test", autoConfirm: true,
		allowDeploy: true, deployExec: "npm run deploy:local", autoConfirmDep: true,
		spawn: func(_ context.Context, _, cmd string, _ []string, logSink io.Writer) error {
			calls = append(calls, cmd)
			if cmd == "npm test" && logSink != nil {
				_, _ = logSink.Write([]byte("all demo tests passed\n"))
			}
			return nil
		},
	})
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if strings.Join(calls, ",") != "claude,npm test,npm run deploy:local" {
		t.Fatalf("spawn calls = %v, want agent, tests, deploy", calls)
	}
	last := (*patches)[len(*patches)-1]
	if last["status"] != "deployed" || last["version"] != "1.2.3" || last["deploy_target"] != "local-dev" {
		t.Fatalf("final patch = %+v, want deployed v1.2.3 local-dev", last)
	}
	if fmt.Sprint(last["tests_summary"]) != "configured test command passed" {
		t.Fatalf("tests_summary=%v, want allowlisted deploy test evidence", last["tests_summary"])
	}
}

func TestAgentRunnerDeployNeedsItsOwnConsent(t *testing.T) {
	// --allow-deploy + --deploy-exec + deploy_target, but the deploy confirm is
	// declined (and --yes-deploy not set) → no deploy, report completed because
	// no test command ran.
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","deploy_target":"ppm","status":"queued"}`, http.StatusOK)
	var calls []string
	a := withSpawnSupervisor(&agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: t.TempDir(),
		execCmd: "claude", autoConfirm: true,
		allowDeploy: true, deployExec: "just deploy-ppm", autoConfirmDep: false,
		confirmDeploy: func(_ string, _ int64, _ string) bool { return false },
		spawn: func(_ context.Context, _, cmd string, _ []string, _ io.Writer) error {
			calls = append(calls, cmd)
			return nil
		},
	})
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if len(calls) != 1 || calls[0] != "claude" {
		t.Fatalf("spawn calls = %v, want just [claude] (deploy declined)", calls)
	}
	if last := (*patches)[len(*patches)-1]; last["status"] != "completed" {
		t.Fatalf("final patch = %+v, want completed (deploy declined)", last)
	}
}

func TestAgentRunnerDeployStaysGatedOff(t *testing.T) {
	srv, patches := newRunServer(t, `{"issue_id":5,"device_id":"","deploy_target":"ppm","status":"queued"}`, http.StatusOK)
	var calls []string
	a := withSpawnSupervisor(&agentRunner{
		client: newClientForTest(srv.URL), deviceID: "dev-1", repoRoot: "/tmp",
		execCmd: "claude", autoConfirm: true,
		allowDeploy: false, deployExec: "just deploy-ppm",
		spawn: func(_ context.Context, _, cmd string, _ []string, _ io.Writer) error {
			calls = append(calls, cmd)
			return nil
		},
	})
	if err := a.handleRun(context.Background(), aJob()); err != nil {
		t.Fatalf("handleRun: %v", err)
	}
	if len(calls) != 1 || calls[0] != "claude" {
		t.Fatalf("spawn calls = %v, want just [claude] (deploy gated off)", calls)
	}
	if last := (*patches)[len(*patches)-1]; last["status"] != "completed" {
		t.Fatalf("final patch = %+v, want completed (no deploy)", last)
	}
}

func TestAgentRunnerQueuedRunIDsCatchUp(t *testing.T) {
	var seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.String()
		if r.URL.Path != "/api/projects/7/runs" || r.URL.Query().Get("status") != "queued" {
			http.Error(w, `{"error":"unexpected route"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[{"id":11},{"id":12}]}`))
	}))
	t.Cleanup(srv.Close)

	a := &agentRunner{client: newClientForTest(srv.URL)}
	got := a.queuedRunIDs(context.Background(), 7)
	if len(got) != 2 || got[0] != 11 || got[1] != 12 {
		t.Fatalf("queuedRunIDs=%v, want [11 12]", got)
	}
	if seenPath != "/api/projects/7/runs?status=queued" {
		t.Fatalf("catch-up path=%q", seenPath)
	}
}

func TestAgentRunnerQueuedRunIDsDedupesPollErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls += 1
		if calls < 3 {
			http.Error(w, `{"error":"temporary"}`, http.StatusInternalServerError)
			return
		}
		http.Error(w, `{"error":"still temporary"}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	oldStderr := stderr
	var errOut bytes.Buffer
	stderr = &errOut
	t.Cleanup(func() { stderr = oldStderr })

	a := &agentRunner{client: newClientForTest(srv.URL)}
	_ = a.queuedRunIDs(context.Background(), 7)
	_ = a.queuedRunIDs(context.Background(), 7)
	if got := strings.Count(errOut.String(), "catch-up poll failed"); got != 1 {
		t.Fatalf("same catch-up error logged %d times, want 1; stderr=%q", got, errOut.String())
	}
	_ = a.queuedRunIDs(context.Background(), 7)
	if got := strings.Count(errOut.String(), "catch-up poll failed"); got != 2 {
		t.Fatalf("distinct catch-up error logged %d times, want 2; stderr=%q", got, errOut.String())
	}
}

func TestAgentRunnerFinalConflictRefetchesAuthoritativeLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wanted    string
		getStatus int
		authority string
		wantErr   bool
	}{
		{name: "equivalent replay", wanted: "completed", getStatus: http.StatusOK, authority: "completed"},
		{name: "reaper won", wanted: "completed", getStatus: http.StatusOK, authority: "failed", wantErr: true},
		{name: "cancelled won", wanted: "tests_passed", getStatus: http.StatusOK, authority: "cancelled", wantErr: true},
		{name: "missing", wanted: "completed", getStatus: http.StatusNotFound, wantErr: true},
		{name: "refetch failure", wanted: "deployed", getStatus: http.StatusServiceUnavailable, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				status := tc.getStatus
				body := `{"error":"unavailable"}`
				if r.Method == http.MethodPatch {
					status = http.StatusConflict
					body = `{"error":"terminal conflict"}`
				} else if tc.getStatus == http.StatusOK {
					body = fmt.Sprintf(`{"status":%q}`, tc.authority)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}}
			runner := &agentRunner{client: client}
			err := runner.report(42, map[string]any{"status": tc.wanted})
			if (err != nil) != tc.wantErr {
				t.Fatalf("report error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestAgentRunnerFinalConflictRejectsSameStatusWithDifferentEvidence(t *testing.T) {
	client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		status := http.StatusConflict
		body := `{"error":"terminal conflict"}`
		if r.Method == http.MethodGet {
			status = http.StatusOK
			body = `{"status":"tests_passed","tests_summary":"different evidence","version":"1.0.0"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	runner := &agentRunner{client: client}
	err := runner.report(42, map[string]any{
		"status": "tests_passed", "tests_summary": "configured test command passed", "version": "1.0.0",
	})
	if err == nil || !strings.Contains(err.Error(), "not equivalent") {
		t.Fatalf("same-status evidence conflict error=%v", err)
	}
}

func TestAgentRunnerDoesNotPrintSuccessWhenReaperWonFinalConflict(t *testing.T) {
	patches := 0
	respond := func(r *http.Request, status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runs/1" && patches < 2:
			return respond(r, http.StatusOK, `{"issue_id":5,"device_id":"","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/runs/1":
			return respond(r, http.StatusOK, `{"status":"failed"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-5":
			return respond(r, http.StatusOK, `{"id":5,"issue_key":"PAI-5","type":"ticket","title":"Lifecycle race","status":"in-progress","priority":"medium"}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/runs/1":
			patches++
			if patches == 1 {
				return respond(r, http.StatusOK, `{}`)
			}
			return respond(r, http.StatusConflict, `{"error":"terminal conflict"}`)
		default:
			return respond(r, http.StatusNotFound, `{"error":"unexpected route"}`)
		}
	})}}
	runner := newAgentRunner(client, "race-device", t.TempDir(), "printf ok", "claude_cli.implement", "", true, false, "", false, false)
	runner.reporter = &recordingRunnerReporter{}
	runner.supervise = func(context.Context, supervisorRequest) supervisorResult {
		return supervisorResult{Outcome: outcomeNormalExit, Summary: "provider exited normally"}
	}
	oldStdout := stdout
	var output bytes.Buffer
	stdout = &output
	t.Cleanup(func() { stdout = oldStdout })
	err := runner.handleRun(context.Background(), aJob())
	if err == nil || !strings.Contains(err.Error(), "report failure") {
		t.Fatalf("handleRun error=%v", err)
	}
	if strings.Contains(output.String(), "run 1 complete") || strings.Contains(output.String(), "deployed to") {
		t.Fatalf("printed local success after reaper won: %q", output.String())
	}
}

type recordingRunnerReporter struct {
	mu      sync.Mutex
	reports []supervisorReport
	err     error
}

func withSpawnSupervisor(a *agentRunner) *agentRunner {
	a.supervise = func(ctx context.Context, req supervisorRequest) supervisorResult {
		command := req.ExecCmd
		if req.InitialPhase == "" {
			command = a.execCmd
		}
		if err := a.spawn(ctx, req.RepoRoot, command, req.Env, req.LogSink); err != nil {
			return supervisorResult{Outcome: outcomeProviderFailure, Summary: "configured command exited unsuccessfully"}
		}
		return supervisorResult{Outcome: outcomeNormalExit, Summary: "configured command exited normally"}
	}
	return a
}

func (r *recordingRunnerReporter) Report(_ context.Context, _ int64, report supervisorReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
	return r.err
}

func (r *recordingRunnerReporter) snapshot() []supervisorReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]supervisorReport(nil), r.reports...)
}

func supervisorFixture(command string) supervisorRequest {
	return supervisorRequest{
		RunID:             7,
		RepoRoot:          os.TempDir(),
		ExecCmd:           command,
		ExecutionTimeout:  2 * time.Second,
		SilenceTimeout:    time.Second,
		HeartbeatInterval: 20 * time.Millisecond,
	}
}

func TestSupervisorOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*supervisorRequest) context.Context
		command string
		want    supervisorOutcome
	}{
		{name: "normal raw exit", command: "printf ok", want: outcomeNormalExit},
		{name: "missing provider command", command: "paimos-command-that-does-not-exist", want: outcomeSpawnFailure},
		{name: "provider failure", command: "exit 7", want: outcomeProviderFailure},
		{name: "malformed stream", command: "printf 'not-json\\n'", want: outcomeMalformedStream, mutate: func(r *supervisorRequest) context.Context {
			r.StructuredClaude = true
			return context.Background()
		}},
		{name: "provider-declared failure", command: `printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true}'`, want: outcomeProviderFailure, mutate: func(r *supervisorRequest) context.Context {
			r.StructuredClaude = true
			return context.Background()
		}},
		{name: "silent child", command: "sleep 2", want: outcomeSilentChild, mutate: func(r *supervisorRequest) context.Context {
			r.SilenceTimeout = 40 * time.Millisecond
			return context.Background()
		}},
		{name: "execution timeout", command: "while true; do printf .; sleep 0.01; done", want: outcomeTimeout, mutate: func(r *supervisorRequest) context.Context {
			r.ExecutionTimeout = 60 * time.Millisecond
			r.SilenceTimeout = time.Second
			return context.Background()
		}},
		{name: "cancellation", command: "while true; do printf .; sleep 0.01; done", want: outcomeCancellation, mutate: func(r *supervisorRequest) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(40*time.Millisecond, cancel)
			return ctx
		}},
		{name: "cancelled context cannot race into success", command: "true", want: outcomeCancellation, mutate: func(r *supervisorRequest) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := supervisorFixture(tt.command)
			ctx := context.Background()
			if tt.mutate != nil {
				ctx = tt.mutate(&req)
			}
			got := superviseAgentProcess(ctx, req)
			if got.Outcome != tt.want {
				t.Fatalf("outcome=%s summary=%q, want %s", got.Outcome, got.Summary, tt.want)
			}
		})
	}
}

func TestSupervisorSpawnFailure(t *testing.T) {
	req := supervisorFixture("printf ok")
	req.RepoRoot = filepath.Join(t.TempDir(), "missing")
	got := superviseAgentProcess(context.Background(), req)
	if got.Outcome != outcomeSpawnFailure {
		t.Fatalf("outcome=%s summary=%q, want spawn_failure", got.Outcome, got.Summary)
	}
}

func TestSupervisorHeartbeatDoesNotDependOnProviderCallbacks(t *testing.T) {
	reporter := &recordingRunnerReporter{}
	req := supervisorFixture("sleep 0.12")
	req.Reporter = reporter
	req.SilenceTimeout = time.Second
	got := superviseAgentProcess(context.Background(), req)
	if got.Outcome != outcomeNormalExit {
		t.Fatalf("outcome=%s summary=%q", got.Outcome, got.Summary)
	}
	heartbeats := 0
	for _, report := range reporter.snapshot() {
		if report.Event == "heartbeat" {
			heartbeats++
		}
	}
	if heartbeats == 0 {
		t.Fatalf("reports=%+v, want an independent heartbeat", reporter.snapshot())
	}
}

func TestSupervisorProviderSuccessReviewsBeforeLifecycleCompletion(t *testing.T) {
	reporter := &recordingRunnerReporter{}
	req := supervisorFixture("printf ok")
	req.Reporter = reporter
	got := superviseAgentProcess(context.Background(), req)
	if got.Outcome != outcomeNormalExit {
		t.Fatalf("outcome=%s summary=%q", got.Outcome, got.Summary)
	}
	seenReviewing := false
	for _, report := range reporter.snapshot() {
		if report.Phase == "completed" {
			t.Fatalf("provider success claimed completed before lifecycle verification: %+v", report)
		}
		if report.Event == "result" && report.Phase == "reviewing" {
			seenReviewing = true
		}
		if report.Event == "heartbeat" && report.Summary != "" {
			t.Fatalf("timer heartbeat carried semantic activity: %+v", report)
		}
	}
	if !seenReviewing {
		t.Fatalf("reports=%+v", reporter.snapshot())
	}
}

func TestSupervisorDistinguishesRunnerDisappearanceAndReportFailure(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want supervisorOutcome
	}{
		{name: "runner disappearance", err: errRunnerDisappeared, want: outcomeRunnerDisappearance},
		{name: "remote cancellation", err: errRunCancelled, want: outcomeCancellation},
		{name: "report failure", err: errors.New("temporary report outage"), want: outcomeReportFailure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := supervisorFixture("sleep 1")
			req.Reporter = &recordingRunnerReporter{err: tt.err}
			got := superviseAgentProcess(context.Background(), req)
			if got.Outcome != tt.want {
				t.Fatalf("outcome=%s summary=%q, want %s", got.Outcome, got.Summary, tt.want)
			}
		})
	}
}

func TestClaudeStreamAdapterOnlyEmitsAllowlistedSummaries(t *testing.T) {
	adapter := &claudeStreamAdapter{}
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo sensitive-provider-content"}},{"type":"text","text":"source text sensitive-provider-content"}]}}`)
	progress, err := adapter.Consume(line)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if progress == nil || progress.phase != "testing" || progress.summary != "Provider is running an allowlisted command step" {
		t.Fatalf("progress=%+v, want generic allowlisted progress", progress)
	}
	if strings.Contains(fmt.Sprintf("%+v", progress), "sensitive-provider-content") {
		t.Fatalf("progress leaked provider content: %+v", progress)
	}
}

func TestClaudeStreamAdapterNeedsInputIsTelemetryOnly(t *testing.T) {
	adapter := &claudeStreamAdapter{}
	progress, err := adapter.Consume([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"question":"sensitive prompt content"}}]}}`))
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if progress == nil || progress.phase != "waiting" || !progress.needsInput || progress.blockerState != "input" || strings.Contains(progress.summary, "sensitive prompt content") {
		t.Fatalf("progress=%+v, want safe needs_input telemetry", progress)
	}
}

func TestStructuredProviderNeedsInputSupersedesSaturatedProgress(t *testing.T) {
	progress := make(chan safeProviderProgress, 16)
	for i := 0; i < cap(progress); i++ {
		progress <- safeProviderProgress{phase: "implementing", summary: "ordinary progress"}
	}
	failures := make(chan error, 1)
	consumeProviderStdout(
		strings.NewReader("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"AskUserQuestion\"}]}}\n"),
		&claudeStreamAdapter{}, io.Discard, io.Discard, make(chan struct{}, 1), progress, failures,
	)
	select {
	case err := <-failures:
		t.Fatalf("needs-input stream failed: %v", err)
	default:
	}
	if len(progress) != 1 {
		t.Fatalf("priority queue length=%d, want exactly the critical fact", len(progress))
	}
	got := <-progress
	if !got.needsInput || got.phase != "waiting" || got.blockerState != "input" {
		t.Fatalf("priority progress=%+v", got)
	}
}

func TestSupervisorNeedsInputSurvivesProgressFloodAndImmediateExit(t *testing.T) {
	var script strings.Builder
	for i := 0; i < 64; i++ {
		script.WriteString("printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Read\"}]}}'; ")
	}
	script.WriteString("printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"AskUserQuestion\"}]}}'")
	reporter := &recordingRunnerReporter{}
	req := supervisorFixture("structured fixture")
	req.ExecArgv = []string{"sh", "-c", script.String()}
	req.StructuredClaude = true
	req.Reporter = reporter
	got := superviseAgentProcess(context.Background(), req)
	if got.Outcome != outcomeProviderFailure || !strings.Contains(got.Summary, "requested input") {
		t.Fatalf("outcome=%s summary=%q", got.Outcome, got.Summary)
	}
	found := false
	for _, report := range reporter.snapshot() {
		if report.Event == "needs_input" && report.NeedsInput && report.BlockerState == "input" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lossless needs-input report missing: %+v", reporter.snapshot())
	}
}

func TestStructuredProviderEventAndOutputBudgetsAreBounded(t *testing.T) {
	activity := make(chan struct{}, 1)
	progress := make(chan safeProviderProgress, 1)
	failures := make(chan error, 1)
	oversized := strings.Repeat("x", maxProviderEventBytes+1) + "\n"
	consumeProviderStdout(strings.NewReader(oversized), &claudeStreamAdapter{}, io.Discard, io.Discard, activity, progress, failures)
	select {
	case err := <-failures:
		if !strings.Contains(safeStreamErrorSummary(err), "bounded size") {
			t.Fatalf("error=%v, want bounded-size failure", err)
		}
	default:
		t.Fatal("oversized provider event was accepted")
	}

	var dst bytes.Buffer
	w := newOutputBudgetWriter(&dst, 8)
	if n, err := w.Write([]byte("0123456789abcdef")); err != nil || n != 16 {
		t.Fatalf("bounded writer n=%d err=%v", n, err)
	}
	if dst.String() != "01234567" || !w.truncated {
		t.Fatalf("bounded output=%q truncated=%v", dst.String(), w.truncated)
	}

	var flood strings.Builder
	for i := 0; i <= maxProviderEvents; i++ {
		flood.WriteString("{\"type\":\"user\"}\n")
	}
	failures = make(chan error, 1)
	consumeProviderStdout(strings.NewReader(flood.String()), &claudeStreamAdapter{}, io.Discard, io.Discard,
		make(chan struct{}, 1), make(chan safeProviderProgress, 1), failures)
	select {
	case err := <-failures:
		if !strings.Contains(safeStreamErrorSummary(err), "bounded budget") {
			t.Fatalf("aggregate flood error=%v", err)
		}
	default:
		t.Fatal("aggregate provider stream flood was accepted")
	}
}

type runnerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f runnerRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPRunnerReportTransportUsesTelemetryRESTContract(t *testing.T) {
	var got map[string]any
	client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runs/77/telemetry" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accepted":true}`)), Request: r}, nil
	})}}
	transport := &httpRunnerReportTransport{client: client, provider: "anthropic", adapter: "claude-code"}
	err := transport.Report(context.Background(), 77, supervisorReport{
		Event: "progress", Phase: "implementing", Summary: "Provider is editing the repository",
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	wantKeys := map[string]bool{
		"sequence": true, "correlation_id": true, "provider": true, "adapter": true,
		"agent_reported_at": true, "kind": true, "heartbeat": true, "phase": true,
		"activity": true, "needs_input": true, "blocker_state": true,
	}
	for key := range got {
		if !wantKeys[key] {
			t.Fatalf("unexpected report field %q in %+v", key, got)
		}
	}
	if got["kind"] != "phase" || got["phase"] != "implementing" || got["provider"] != "anthropic" || got["adapter"] != "claude-code" || got["sequence"] != float64(1) {
		t.Fatalf("report=%+v", got)
	}
	progress := 50.0
	eta, etaMin, etaMax := int64(300), int64(240), int64(420)
	confidence := 0.8
	got = nil
	err = transport.Report(context.Background(), 77, supervisorReport{
		Event: "progress", Phase: "testing", Summary: "Reached a named verification checkpoint",
		ProgressPercent: &progress, ETASeconds: &eta, ETAMinSeconds: &etaMin, ETAMaxSeconds: &etaMax,
		EstimateSource: "adapter", EstimateConfidence: &confidence,
		EstimateBasis: "Two of four named verification checkpoints completed",
	})
	if err != nil {
		t.Fatalf("estimate report: %v", err)
	}
	if got["kind"] != "progress" || got["sequence"] != float64(2) || got["estimate_revision"] != float64(1) ||
		got["progress_percent"] != float64(50) || got["eta_seconds"] != float64(300) || got["estimate_source"] != "adapter" {
		t.Fatalf("estimate report=%+v", got)
	}
}

func TestHTTPRunnerReportTransportTruncatesUTF8AtServerByteBoundary(t *testing.T) {
	var activity string
	client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body runTelemetryReport
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		activity = body.Activity
		if !utf8.ValidString(activity) || len(activity) > maxTelemetryActivityBytes {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"invalid byte bound"}`)), Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accepted":true}`)), Request: r}, nil
	})}}
	transport := &httpRunnerReportTransport{client: client, provider: "paimos", adapter: "custom-runner"}
	// 277 ASCII bytes followed by two multibyte runes forces truncation exactly
	// where a byte slice could split UTF-8. The ASCII ellipsis fills byte 280.
	summary := strings.Repeat("a", 277) + "éé"
	if err := transport.Report(context.Background(), 78, supervisorReport{Event: "progress", Phase: "implementing", Summary: summary}); err != nil {
		t.Fatalf("multibyte telemetry was rejected: %v", err)
	}
	if len(activity) != maxTelemetryActivityBytes || !utf8.ValidString(activity) || !strings.HasSuffix(activity, "...") {
		t.Fatalf("activity bytes=%d valid=%v suffix=%q", len(activity), utf8.ValidString(activity), activity[len(activity)-3:])
	}
}

func TestRunnerTelemetryIdentityComesFromExecutionMode(t *testing.T) {
	for _, tt := range []struct {
		command, provider, adapter string
	}{
		{"claude", "anthropic", "claude-code"},
		{"codex exec --full-auto", "openai", "codex-cli"},
		{"aider --yes", "paimos", "custom-runner"},
		{"opencode run", "paimos", "custom-runner"},
		{"codex-wrapper run", "paimos", "custom-runner"},
		{"codex exec | tee runner.log", "paimos", "custom-runner"},
	} {
		provider, adapter := runnerTelemetryIdentityForExecution(tt.command)
		if provider != tt.provider || adapter != tt.adapter {
			t.Fatalf("identity(%q)=(%q,%q), want (%q,%q)", tt.command, provider, adapter, tt.provider, tt.adapter)
		}
	}
}

func TestAgentRunnerRawCommandsEmitNeutralTelemetryEndToEnd(t *testing.T) {
	for _, command := range []string{"aider --yes", "opencode run", "./arbitrary-wrapper --job PAI-5"} {
		t.Run(strings.Fields(command)[0], func(t *testing.T) {
			var telemetry map[string]any
			respond := func(r *http.Request, status int, body string) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}
			client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/runs/1":
					return respond(r, http.StatusOK, `{"issue_id":5,"project_id":9,"device_id":"","action_key":"claude_cli.implement","status":"queued"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-5":
					return respond(r, http.StatusOK, `{"id":5,"issue_key":"PAI-5","type":"ticket","title":"Raw identity","status":"in-progress","priority":"medium"}`)
				case r.Method == http.MethodPatch && r.URL.Path == "/api/runs/1":
					return respond(r, http.StatusOK, `{}`)
				case r.Method == http.MethodPost && r.URL.Path == "/api/runs/1/telemetry":
					if err := json.NewDecoder(r.Body).Decode(&telemetry); err != nil {
						return nil, err
					}
					return respond(r, http.StatusCreated, `{"accepted":true,"duplicate":false}`)
				default:
					return respond(r, http.StatusNotFound, `{"error":"unexpected route"}`)
				}
			})}}

			runner := newAgentRunner(client, "raw-device", t.TempDir(), command,
				"claude_cli.implement", "", true, false, "", false, false)
			var childEnv map[string]string
			runner.supervise = func(ctx context.Context, req supervisorRequest) supervisorResult {
				childEnv = envMap(req.Env)
				if err := req.Reporter.Report(ctx, req.RunID, supervisorReport{Event: "liveness", Phase: "starting"}); err != nil {
					return supervisorResult{Outcome: outcomeReportFailure, Summary: err.Error()}
				}
				return supervisorResult{Outcome: outcomeNormalExit, Summary: "raw command exited normally"}
			}
			if err := runner.handleRun(context.Background(), aJob()); err != nil {
				t.Fatal(err)
			}
			if telemetry["provider"] != "paimos" || telemetry["adapter"] != "custom-runner" {
				t.Fatalf("command %q telemetry=%+v", command, telemetry)
			}
			if childEnv["PAIMOS_RUN_PROVIDER"] != "paimos" || childEnv["PAIMOS_RUN_ADAPTER"] != "custom-runner" {
				t.Fatalf("command %q env=%+v", command, childEnv)
			}
		})
	}
}

func TestHTTPRunnerReportTransportSerializesConcurrentSequence(t *testing.T) {
	var mu sync.Mutex
	var sequences []int
	client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		mu.Lock()
		sequences = append(sequences, int(body["sequence"].(float64)))
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accepted":true}`)), Request: r}, nil
	})}}
	transport := &httpRunnerReportTransport{client: client, provider: "openai", adapter: "codex-cli"}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := transport.Report(context.Background(), 88, supervisorReport{Event: "heartbeat", Phase: "implementing"}); err != nil {
				t.Errorf("report: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(sequences) != 20 {
		t.Fatalf("arrival sequences=%v, want 20 reports", sequences)
	}
	for i, sequence := range sequences {
		if sequence != i+1 {
			t.Fatalf("arrival sequences=%v", sequences)
		}
	}
}

func TestHTTPRunnerReportTransportRetriesExactAmbiguousBodyBeforeAdvancing(t *testing.T) {
	var bodies [][]byte
	attempt := 0
	client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, append([]byte(nil), body...))
		attempt++
		status := http.StatusOK
		response := `{"accepted":true,"duplicate":true}`
		if attempt == 1 {
			status = http.StatusInternalServerError
			response = `{"error":"ambiguous upstream failure"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})}}
	transport := &httpRunnerReportTransport{client: client, provider: "openai", adapter: "codex-cli"}
	if err := transport.Report(context.Background(), 91, supervisorReport{Event: "heartbeat", Phase: "testing"}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry bodies differ: %q / %q", bodies[0], bodies[1])
	}
	var fact runTelemetryReport
	if err := json.Unmarshal(bodies[0], &fact); err != nil || fact.Sequence != 1 {
		t.Fatalf("retry fact=%+v err=%v", fact, err)
	}
	if err := transport.Report(context.Background(), 91, supervisorReport{Event: "heartbeat", Phase: "reviewing"}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bodies[2], &fact); err != nil || fact.Sequence != 2 {
		t.Fatalf("post-acceptance fact=%+v err=%v", fact, err)
	}
}

func TestHTTPRunnerReportTransportClassifiesConflictFromRunTruth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		getStatus int
		runStatus string
		want      error
	}{
		{name: "cancelled", getStatus: http.StatusOK, runStatus: "cancelled", want: errRunCancelled},
		{name: "reaped", getStatus: http.StatusOK, runStatus: "failed", want: errRunStatusLost},
		{name: "completed", getStatus: http.StatusOK, runStatus: "completed", want: errRunStatusLost},
		{name: "missing", getStatus: http.StatusNotFound, want: errRunnerDisappeared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				status, body := http.StatusConflict, `{"error":"conflict"}`
				if r.Method == http.MethodGet {
					status = tc.getStatus
					body = fmt.Sprintf(`{"status":%q}`, tc.runStatus)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}}
			transport := &httpRunnerReportTransport{client: client}
			err := transport.Report(context.Background(), 92, supervisorReport{Event: "heartbeat", Phase: "implementing"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want %v", err, tc.want)
			}
		})
	}
}

func TestRunnerCorrelationIdentityIsRandomStableAndDistinct(t *testing.T) {
	transport := &httpRunnerReportTransport{}
	first, _, _, err := transport.Identity(1)
	if err != nil {
		t.Fatal(err)
	}
	again, _, _, err := transport.Identity(1)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := transport.Identity(2)
	if err != nil {
		t.Fatal(err)
	}
	if first != again || first == second || strings.HasPrefix(first, "run-") {
		t.Fatalf("correlations first=%q again=%q second=%q", first, again, second)
	}
}

func TestAgentRunnerEndToEndSupervisorTelemetryREST(t *testing.T) {
	var mu sync.Mutex
	var telemetry []map[string]any
	var patches []map[string]any
	respond := func(r *http.Request, status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runs/1":
			return respond(r, http.StatusOK, `{"issue_id":5,"device_id":"","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-5":
			return respond(r, http.StatusOK, `{"id":5,"issue_key":"PAI-5","type":"ticket","title":"Telemetry E2E","status":"in-progress","priority":"medium"}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/runs/1":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			patches = append(patches, body)
			mu.Unlock()
			return respond(r, http.StatusOK, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/runs/1/telemetry":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			telemetry = append(telemetry, body)
			mu.Unlock()
			return respond(r, http.StatusCreated, `{"accepted":true,"duplicate":false}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			return respond(r, http.StatusNotFound, `{}`)
		}
	})}}
	runner := newAgentRunner(client, "device-e2e", t.TempDir(), "printf ok", "claude_cli.implement", "", true, false, "", false, false)
	runner.heartbeatInterval = 5 * time.Millisecond
	runner.heartbeatTimeout = time.Second
	runner.executionTimeout = time.Second
	if err := runner.handleRun(context.Background(), aJob()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(patches) < 2 || patches[0]["expects_supervisor_telemetry"] != true || patches[len(patches)-1]["status"] != "completed" {
		t.Fatalf("patches=%+v", patches)
	}
	if len(telemetry) < 2 {
		t.Fatalf("telemetry=%+v", telemetry)
	}
	correlation := telemetry[0]["correlation_id"]
	for i, fact := range telemetry {
		if fact["sequence"] != float64(i+1) || fact["correlation_id"] != correlation || fact["provider"] != "paimos" || fact["adapter"] != "custom-runner" {
			t.Fatalf("fact %d=%+v", i, fact)
		}
		for _, forbidden := range []string{"prompt", "provider_payload", "command_output", "tool_arguments", "source", "environment"} {
			if _, ok := fact[forbidden]; ok {
				t.Fatalf("fact leaked %q: %+v", forbidden, fact)
			}
		}
	}
}

func TestAgentRunnerSupervisesLongTestAndDeployPhases(t *testing.T) {
	var mu sync.Mutex
	var telemetry []map[string]any
	var patches []map[string]any
	respond := func(r *http.Request, status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runs/1":
			return respond(r, http.StatusOK, `{"issue_id":5,"project_id":9,"device_id":"","deploy_target":"staging","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-5":
			return respond(r, http.StatusOK, `{"id":5,"issue_key":"PAI-5","type":"ticket","title":"Supervised phases","status":"in-progress","priority":"medium"}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/runs/1":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			mu.Lock()
			patches = append(patches, body)
			mu.Unlock()
			return respond(r, http.StatusOK, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/runs/1/telemetry":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			mu.Lock()
			telemetry = append(telemetry, body)
			mu.Unlock()
			return respond(r, http.StatusCreated, `{"accepted":true,"duplicate":false}`)
		default:
			return respond(r, http.StatusNotFound, `{}`)
		}
	})}}
	runner := newAgentRunner(client, "device-phases", t.TempDir(),
		`test "$PAIMOS_PROJECT_ID" = 9 && test -n "$PAIMOS_RUN_CORRELATION_ID" && printf provider`,
		"claude_cli.implement", `printf test-start; sleep 0.08; printf test-end`, true, true,
		`printf deploy-start; sleep 0.08; printf deploy-end`, true, false)
	runner.heartbeatInterval = 10 * time.Millisecond
	runner.heartbeatTimeout = time.Second
	runner.executionTimeout = time.Second
	if err := runner.handleRun(context.Background(), aJob()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(patches) < 2 || patches[len(patches)-1]["status"] != "deployed" || patches[len(patches)-1]["tests_summary"] != "configured test command passed" {
		t.Fatalf("patches=%+v", patches)
	}
	seenReviewing, seenTestingHeartbeat, seenDeployingHeartbeat := false, false, false
	var correlation any
	for i, fact := range telemetry {
		if fact["sequence"] != float64(i+1) {
			t.Fatalf("telemetry sequence %d=%+v", i, fact)
		}
		if i == 0 {
			correlation = fact["correlation_id"]
		} else if fact["correlation_id"] != correlation {
			t.Fatalf("correlation changed: %+v", telemetry)
		}
		phase, _ := fact["phase"].(string)
		if phase == "completed" {
			t.Fatalf("pre-lifecycle telemetry claimed completion: %+v", fact)
		}
		if phase == "reviewing" {
			seenReviewing = true
		}
		if fact["kind"] == "heartbeat" && phase == "testing" {
			seenTestingHeartbeat = true
		}
		if fact["kind"] == "heartbeat" && phase == "deploying" {
			seenDeployingHeartbeat = true
		}
	}
	if !seenReviewing || !seenTestingHeartbeat || !seenDeployingHeartbeat {
		t.Fatalf("reviewing=%v testing heartbeat=%v deploying heartbeat=%v telemetry=%+v", seenReviewing, seenTestingHeartbeat, seenDeployingHeartbeat, telemetry)
	}
}

func TestAgentRunnerDeployFailurePreservesPassedTestEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		outcome    supervisorOutcome
		wantStatus string
	}{
		{name: "failed", outcome: outcomeProviderFailure, wantStatus: "failed"},
		{name: "cancelled", outcome: outcomeCancellation, wantStatus: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var patches []map[string]any
			respond := func(r *http.Request, status int, body string) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}
			client := &Client{baseURL: "http://paimos.test", http: &http.Client{Transport: runnerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/runs/1":
					return respond(r, http.StatusOK, `{"issue_id":5,"project_id":9,"device_id":"","deploy_target":"staging","status":"queued"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-5":
					return respond(r, http.StatusOK, `{"id":5,"issue_key":"PAI-5","type":"ticket","title":"Deploy evidence","status":"in-progress","priority":"medium"}`)
				case r.Method == http.MethodPatch && r.URL.Path == "/api/runs/1":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						return nil, err
					}
					patches = append(patches, body)
					return respond(r, http.StatusOK, `{}`)
				default:
					return respond(r, http.StatusNotFound, `{"error":"unexpected route"}`)
				}
			})}}
			runner := newAgentRunner(client, "device-deploy-evidence", t.TempDir(), "provider",
				"claude_cli.implement", "tests", true, true, "deploy", true, false)
			runner.reporter = &recordingRunnerReporter{}
			runner.supervise = func(_ context.Context, req supervisorRequest) supervisorResult {
				if req.InitialPhase == "deploying" {
					return supervisorResult{Outcome: tc.outcome, Summary: "configured deploy did not complete"}
				}
				return supervisorResult{Outcome: outcomeNormalExit, Summary: "configured command passed"}
			}
			err := runner.handleRun(context.Background(), aJob())
			if err == nil || !strings.Contains(err.Error(), "deploy failed") {
				t.Fatalf("handleRun error=%v", err)
			}
			if len(patches) < 2 {
				t.Fatalf("patches=%+v", patches)
			}
			final := patches[len(patches)-1]
			if final["status"] != tc.wantStatus || final["tests_summary"] != "configured test command passed" || final["deploy_target"] != "staging" {
				t.Fatalf("final patch=%+v", final)
			}
			if _, ok := final["version"]; !ok {
				t.Fatalf("final patch lost version evidence: %+v", final)
			}
		})
	}
}
