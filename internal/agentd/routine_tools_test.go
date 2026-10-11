// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Risk: a routine reaches inherited hooks/MCP or account configuration before
// its tool ceiling is checked. Reuse the functioning vendor startup sentinel;
// the positive control runs only our test binary, never a model CLI.
func TestRoutineToolsRefuseUnqualifiedNativeStartup(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "startup-sentinel")
	t.Setenv("AEON_VERIFICATION_FIXTURE", Codex)
	t.Setenv("AEON_VERIFICATION_SENTINEL", sentinel)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	path := filepath.Join(root, "fake-vendor")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("#!/bin/sh\nexec %s -test.run=^TestVerificationVendorFixture$\n", quote(executable))), 0700); err != nil {
		t.Fatal(err)
	}
	g := routineTestGrant("20")
	raw, err := json.Marshal(map[string]any{"routine_launch": g, "routine_run_id": g.RoutineRunID})
	if err != nil {
		t.Fatal(err)
	}
	run := Run{ID: g.AgentRunID, WorkOrderID: "order", AgentPrincipalID: g.PrincipalID,
		RoutineLaunchID: &g.LaunchID, Trace: raw, AccountID: g.AccountID, Status: "queued", Purpose: "managed"}
	for _, adapter := range []Adapter{
		NewCodexAdapter(path, map[string]string{"chosen-login": root}),
		NewClaudeAdapter(path, path, path, map[string]string{"chosen-login": root}),
		NewCursorAdapter(path, map[string]string{"chosen-login": "fixture-identity"}),
		NewPiAdapter(path, map[string]string{"chosen-login": root}),
		NewGeminiAdapter(path, map[string]string{"chosen-login": root}),
		NewOpenCodeAdapter(path, map[string]string{"chosen-login": root}),
		NewGrokAdapter(),
	} {
		t.Run(adapter.Name(), func(t *testing.T) {
			if err := validQueuedExecutionMode(run, adapter); !errors.Is(err, ErrRoutineToolsUnavailable) {
				t.Fatalf("queued routine did not refuse tool isolation: %v", err)
			}
			request := StartRequest{Run: run, Profile: Profile{Harness: adapter.Name()}, AccountKey: "chosen-login", Workspace: root}
			process, err := adapter.Start(t.Context(), request, func(AdapterEvent) { t.Error("vendor activity before refusal") })
			if process != nil || !errors.Is(err, ErrRoutineToolsUnavailable) {
				t.Fatalf("native routine escaped pre-start refusal: process=%v error=%v", process != nil, err)
			}
			if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("vendor fixture started before refusal")
			}
			if err := validQueuedExecutionMode(Run{Purpose: "managed"}, adapter); err != nil {
				t.Fatalf("ordinary managed execution changed: %v", err)
			}
			supervisor, api, _ := claimFixture(t)
			boundary := &verificationBoundaryCounter{Adapter: adapter}
			supervisor.adapters = map[string]Adapter{adapter.Name(): boundary}
			supervisor.principalID = g.PrincipalID
			api.profile.Harness = adapter.Name()
			queued := run
			queued.ModelProfileID = api.profile.ID
			if err := supervisor.StartRun(t.Context(), queued); !errors.Is(err, ErrRoutineToolsUnavailable) {
				t.Fatalf("supervisor did not refuse before admission: %v", err)
			}
			if boundary.probes != 0 || boundary.starts != 0 || api.routes != 0 || len(api.claimIDs) != 0 {
				t.Fatal("unqualified routine reached a probe, route, claim or start")
			}
		})
	}
	if NewCodexAdapter(path, nil).VerificationSupported() {
		t.Fatal("routine changes incorrectly qualified Codex verification")
	}
	// A working executable would expose the eager startup effect, so a missing
	// path or broken fixture cannot explain the negative control above.
	command := exec.CommandContext(t.Context(), path)
	command.Stdin = strings.NewReader("{\"id\":1,\"method\":\"initialize\"}\n")
	if _, err := command.Output(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "started" {
		t.Fatal("startup sentinel positive control failed")
	}
}

// Risk: the builder's MCP credential forwards unguarded legacy API writes or
// edits its own rules/runtime; workspace escapes mutate another tree. Exercise
// the actual HTTP/MCP boundary and retained files, not just tool-name strings.
func TestRoutineBuilderToolsConstrainEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "foreign.txt")
	if err := os.WriteFile(foreign, []byte("foreign original"), 0600); err != nil {
		t.Fatal(err)
	}
	g := routineTestGrant("21")
	api := &fakeRunTools{}
	var active atomic.Bool
	active.Store(true)
	done := false
	const credential = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // gitleaks:allow synthetic test token
	binding := toolBinding{routine: &g, api: api, workOrderID: "order", runID: g.AgentRunID,
		workspace: workspace, branch: "routine/" + g.LaunchID, active: active.Load, requestDone: func() { done = true }}
	wrong := binding
	wrong.runID = "foreign-run"
	if host, err := startManagedTools(credential, wrong); host != nil || !errors.Is(err, ErrRoutineToolsUnavailable) {
		t.Fatal("foreign run received routine tools", err)
	}
	host, err := startManagedTools(credential, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if !host.tools.Routine {
		t.Fatal("bridge inventory lost routine binding")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "routine-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: host.tools.URL,
		HTTPClient: &http.Client{Transport: headerTransport{token: credential}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"aeon_browser_capture", "aeon_edit", "aeon_glob", "aeon_grep", "aeon_read", "aeon_terminal", "aeon_write"}) {
		t.Fatalf("routine builder inventory changed: %v", names)
	}
	call := func(name string, args map[string]any, reason string) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if reason == "" {
			if err != nil || result == nil || result.IsError {
				raw, _ := json.Marshal(result)
				t.Fatalf("%s failed: result=%s error=%v", name, raw, err)
			}
			return
		}
		if err != nil {
			if reason == "unknown tool" && strings.Contains(strings.ToLower(err.Error()), reason) {
				return
			}
			t.Fatalf("%s failed for wrong reason: %v", name, err)
		}
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil || result == nil || !result.IsError || !strings.Contains(strings.ToLower(string(raw)), reason) {
			t.Fatalf("%s did not refuse %q: %s", name, reason, raw)
		}
	}
	call("aeon_write", map[string]any{"file_path": "source.txt", "content": "before"}, "")
	call("aeon_edit", map[string]any{"file_path": "source.txt", "old_string": "before", "new_string": "after"}, "")
	call("aeon_read", map[string]any{"file_path": "source.txt"}, "")
	call("aeon_glob", map[string]any{"pattern": "*.txt"}, "")
	call("aeon_grep", map[string]any{"pattern": "after"}, "")
	for _, name := range []string{"aeon_comment", "aeon_status", "aeon_check_criterion", "aeon_evidence", "aeon_request_approval", "aeon_reply", "aeon_activity", "inherited_mcp_write"} {
		call(name, map[string]any{}, "unknown tool")
	}
	for _, name := range []string{"AGENTS.md", "internal/routineguard/policy.go", "scripts/ci-static.mjs", ".codex/config.toml", ".claude/settings.json", ".mcp.json", "hooks/pre-commit"} {
		full := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("protected original"), 0600); err != nil {
			t.Fatal(err)
		}
		call("aeon_write", map[string]any{"file_path": name, "content": "changed"}, ErrRoutineToolsUnavailable.Error())
		call("aeon_edit", map[string]any{"file_path": name, "old_string": "original", "new_string": "changed"}, ErrRoutineToolsUnavailable.Error())
		if data, err := os.ReadFile(full); err != nil || string(data) != "protected original" {
			t.Fatal("protected file changed", name)
		}
	}
	// Recursive searches deliberately refuse a tree containing symlinks.
	// Introduce the escape only after the ordinary read/search controls above.
	if err := os.Symlink(foreign, filepath.Join(workspace, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"linked.txt", "../foreign.txt", foreign} {
		call("aeon_write", map[string]any{"file_path": path, "content": "changed"}, "workspace file unavailable")
		call("aeon_read", map[string]any{"file_path": path}, "workspace file unavailable")
	}
	call("aeon_terminal", map[string]any{"command": "git", "args": []string{"push"}}, "terminal command denied")
	call("aeon_terminal", map[string]any{"command": "sh", "args": []string{"-c", "mutation"}}, "terminal command denied")
	call("aeon_terminal", map[string]any{"command": "go", "args": []string{"test", "./..."}}, ErrRoutineToolsUnavailable.Error())
	call("aeon_terminal", map[string]any{"command": "git", "args": []string{"commit", "-m", "local"}}, ErrRoutineToolsUnavailable.Error())
	call("aeon_browser_capture", map[string]any{"url": "https://example.invalid/", "viewport": "desktop", "output_id": g.LaunchID}, ErrBrowserCaptureUnavailable.Error())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, host.tools.URL+"/api/nodes/order/comments", strings.NewReader(`{"body_markdown":"bypass"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+credential)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("direct HTTP write did not refuse at the capability boundary: %d", response.StatusCode)
	}
	active.Store(false)
	request, err = http.NewRequestWithContext(ctx, http.MethodPost, host.tools.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aeon_write","arguments":{"file_path":"source.txt","content":"late"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+credential)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expired routine accepted HTTP tool call: %d", response.StatusCode)
	}
	if !reflect.DeepEqual(api, &fakeRunTools{}) || done {
		t.Fatal("routine builder forwarded a legacy outward effect")
	}
	for path, want := range map[string]string{foreign: "foreign original", filepath.Join(workspace, "source.txt"): "after"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Fatal("workspace confinement or expiry failed", path)
		}
	}
}
