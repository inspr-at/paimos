// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func verificationRequest(t *testing.T) StartRequest {
	r := adapterRequest(t)
	no := false
	seconds := int64(60)
	r.Run.Purpose = VerificationPurpose
	r.Run.AccountID = "account"
	r.Run.VerificationTask = VerificationTask
	r.Run.VerificationPolicy = "read_only"
	r.Run.MaxDurationSeconds = &seconds
	r.Run.RepositoryMutationAllowed = &no
	r.Prompt = VerificationTask
	return r
}

func TestCodexVerificationBlocksBeforeAnyVendorProcess(t *testing.T) {
	testUnsupportedVerificationBlocksBeforeAnyVendorProcess(t, Codex)
}

func TestCursorVerificationBlocksBeforeAnyVendorProcess(t *testing.T) {
	testUnsupportedVerificationBlocksBeforeAnyVendorProcess(t, Cursor)
}

func TestGrokVerificationPlatformGate(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		want         bool
	}{
		{"darwin", "arm64", true},
		{"darwin", "amd64", false},
		{"linux", "arm64", false},
		{"linux", "amd64", false},
	} {
		if got := grokVerificationSupported(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("Grok verification on %s/%s: got %t, want %t", tc.goos, tc.goarch, got, tc.want)
		}
	}
	a := NewGrokAdapter()
	if got, want := a.VerificationSupported(), grokVerificationSupported(runtime.GOOS, runtime.GOARCH); got != want {
		t.Fatalf("Grok adapter capability: got %t, want %t", got, want)
	}
	if !a.VerificationSupported() {
		r := verificationRequest(t)
		r.Profile = Profile{Harness: Grok, Model: grokModel, Effort: grokEffort}
		if _, err := a.Start(t.Context(), r, func(AdapterEvent) {}); !errors.Is(err, ErrVerificationUnavailable) {
			t.Fatalf("unsupported platform reached Grok account or process: %v", err)
		}
	}
}

func testUnsupportedVerificationBlocksBeforeAnyVendorProcess(t *testing.T, harness string) {
	t.Helper()
	// This fixture models a vendor startup hook or an inherited stdio MCP
	// command. Merely launching it mutates an unrelated file; waiting to inspect
	// thread settings or the MCP inventory afterward would already be too late.
	r := verificationRequest(t)
	r.Profile.Harness = harness
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(r.Workspace, "repository-sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "vendor-startup-fixture")
	// Paths come only from Go's private test directory; quote as shell literals.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := fmt.Sprintf("#!/bin/sh\nprintf changed > %s\nexit 1\n", quote(sentinel))
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var a Adapter
	if harness == Codex {
		codex := NewCodexAdapter(path, map[string]string{"account": home})
		codex.SetExpectedEmails(map[string]string{"account": "agent@example.test"})
		a = codex
	} else if harness == Pi {
		a = NewPiAdapter(path, map[string]string{"account": home})
	} else {
		a = NewCursorAdapter(path, map[string]string{"account": "42"})
	}
	observed := false
	p, err := a.Start(t.Context(), r, func(AdapterEvent) { observed = true })
	if p != nil {
		_ = p.Stop(t.Context())
		t.Fatal("blocked verification returned a process")
	}
	if !errors.Is(err, ErrVerificationUnavailable) {
		t.Fatalf("verification must fail before probe/startup: %v", err)
	}
	if observed {
		t.Fatal("blocked verification reported vendor activity")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("blocked verification launched a vendor process")
	}
}

func TestClaudeVerificationSDKEnforcesNoTools(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge, _ := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	bridgePath := filepath.Join(root, "bridge.mjs")
	sdkPath := filepath.Join(root, "sdk.mjs")
	if err = os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	const sdk = `export function query({options:o}) {
 if (o.tools.length || o.allowedTools.length || o.plugins.length || o.settingSources.length || Object.keys(o.mcpServers).length || !o.strictMcpConfig || o.maxTurns!==1 || o.permissionMode!=='dontAsk' || o.additionalDirectories.length) throw new Error('unsafe options');
 return {streamInput:async()=>{},interrupt:async()=>({still_queued:[]}),close:()=>{},async *[Symbol.asyncIterator]() {
  if ((await o.canUseTool('Bash',{})).behavior!=='deny') throw new Error('tool permission');
  if ((await o.hooks.PreToolUse[0].hooks[0]({tool_name:'Write'})).hookSpecificOutput.permissionDecision!=='deny') throw new Error('hook permission');
  yield {type:'system',subtype:'init',session_id:'verification-session',model:'test-model',capabilities:[]};
  yield {type:'result',subtype:'success',is_error:false,modelUsage:{model:{inputTokens:2,outputTokens:1}},total_cost_usd:0.000001};
 }};
}`
	if err = os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, bridgePath, sdkPath, "/bin/true", root)
	cmd.Stdin = strings.NewReader(`{"op":"start","purpose":"pairing_verification","prompt":"AEON_VERIFIED","model":"test-model","effort":"high","tools":null}` + "\n")
	output, err := cmd.Output()
	if err != nil || !strings.Contains(string(output), `"turn_completed"`) || strings.Contains(string(output), `"control_failed"`) {
		t.Fatal("SDK confinement or normal verification completion failed")
	}
}

func TestDaemonLockRejectsSymlinkAndHardlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err = os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, "aeon-agentd-symbolic.lock")); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(outside, filepath.Join(root, "aeon-agentd-hard.lock")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"symbolic", "hard"} {
		if f, err := acquireInstanceLock(root, id); err == nil {
			f.Close()
			t.Fatal("unsafe instance lock adopted")
		}
	}
	raw, _ := os.ReadFile(outside)
	if string(raw) != "unchanged" {
		t.Fatal("unrelated inode modified")
	}
}

func TestApprovedDeadlineDoesNotWaitForTelemetryLock(t *testing.T) {
	s, _, p := testSupervisor(t)
	entry := &owned{}
	entry.mu.Lock() // model a stalled journal/report operation
	done := make(chan struct{})
	go s.runDeadline(entry, p, done, 20*time.Millisecond)
	select {
	case <-p.stopped:
	case <-time.After(time.Second):
		entry.mu.Unlock()
		t.Fatal("API lock extended approved execution deadline")
	}
	if !entry.deadlineExpired.Load() {
		entry.mu.Unlock()
		t.Fatal("deadline lacked truthful stop attribution")
	}
	entry.mu.Unlock()
}

func TestPiVerificationBlocksBeforeAnyVendorProcess(t *testing.T) {
	testUnsupportedVerificationBlocksBeforeAnyVendorProcess(t, Pi)
}
