// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestCursorVerificationRequiresAskBeforePrompt(t *testing.T) {
	for _, variant := range []string{"cursor-verify", "cursor-verify-no-ask", "cursor-verify-no-ack", "cursor-verify-drift", "cursor-verify-permission"} {
		t.Run(variant, func(t *testing.T) {
			r := verificationRequest(t)
			r.Profile.Harness = Cursor
			a := NewCursorAdapter(fakeVendorPath(t, variant), map[string]string{"account": "42"})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			p, err := a.Start(ctx, r, func(AdapterEvent) {})
			if strings.Contains(variant, "no-") {
				if err == nil {
					_ = p.Stop(ctx)
					t.Fatal("verification launched without enforced ask acknowledgement")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			err = p.Wait()
			if variant == "cursor-verify" && err != nil {
				t.Fatal(err)
			}
			if variant != "cursor-verify" && err == nil {
				t.Fatal("permission/mode drift accepted")
			}
			if !p.(*cursorProcess).ProcessExited() {
				t.Fatal("fixture process did not exit")
			}
		})
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
