// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd/fixturebin"
	"github.com/inspr-at/paimos/internal/servicetier"
)

type runOnceAdapter struct {
	fakeAdapter
	request StartRequest
}

func (*runOnceAdapter) Name() string                  { return Claude }
func (*runOnceAdapter) ReadOnlyRunSupported() bool    { return true }
func (*runOnceAdapter) ManagedControlSupported() bool { return true }
func (a *runOnceAdapter) Start(_ context.Context, r StartRequest, _ func(AdapterEvent)) (Process, error) {
	a.request = r
	sum := sha256.Sum256([]byte(r.Rules))
	return &deliveryStartProcess{fakeProcess: a.proc, hash: hex.EncodeToString(sum[:])}, nil
}

// Risk: a generic no-mutation flag is ignored, the original work-order text
// replaces the exact task, or StartRun gives a read-only worker writable tools
// or its repository. Only fixture processes and a fake SDK participate.
func TestRunOnceReadOnlyCeilingAndInstruction(t *testing.T) {
	no := false
	run := Run{Purpose: "managed", RunOnce: true, RepositoryMutationAllowed: &no}
	for _, adapter := range []Adapter{NewCodexAdapter("/nonexistent", nil), NewPiAdapter("/nonexistent", nil), NewCursorAdapter("/nonexistent", nil)} {
		if err := validQueuedExecutionMode(run, adapter); !errors.Is(err, ErrReadOnlyRunUnavailable) {
			t.Fatalf("unqualified %s: %v", adapter.Name(), err)
		}
	}
	if err := validQueuedExecutionMode(run, NewClaudeAdapter("", "", "", nil)); err != nil {
		t.Fatal(err)
	}
	run.RepositoryMutationAllowed = nil
	if err := validQueuedExecutionMode(run, NewClaudeAdapter("", "", "", nil)); err == nil {
		t.Fatal("missing ceiling was accepted")
	}
	s, api, process := testPinnedSupervisor(t)
	api.profile.Harness, s.accounts[0].Harness = Claude, Claude
	adapter := &runOnceAdapter{fakeAdapter: fakeAdapter{proc: process}}
	s.adapters = map[string]Adapter{Claude: adapter}
	api.run.Purpose, api.run.RunOnce, api.run.RepositoryMutationAllowed = "managed", true, &no
	api.run.Instruction = "  Inspect this exact task.\nSecond line."
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal(err)
	}
	request := adapter.request
	if !strings.Contains(request.Prompt, api.run.Instruction) || strings.Contains(request.Prompt, "Coordinator task") || request.Tools != nil || request.ManagedPolicy || request.Run.ReadOnlyReview || request.Run.Purpose != "managed" {
		t.Fatalf("instruction or ceiling changed: %+v", request)
	}
	rel, err := filepath.Rel(s.workspace, request.Workspace)
	if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatal("read-only worker received repository workspace")
	}
	for _, capability := range []string{"inbox", "steer", managedControlCapability, servicetier.Capability} {
		if slices.Contains(request.Capabilities, capability) {
			t.Fatalf("read-only run acquired %s", capability)
		}
	}
	t.Run("bridge", func(t *testing.T) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Fatal(err)
		}
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		bridge, _ := claudeAssets.ReadFile("claudeassets/bridge.mjs")
		bridgePath, sdkPath := filepath.Join(root, "bridge.mjs"), filepath.Join(root, "sdk.mjs")
		if err := os.WriteFile(bridgePath, bridge, 0600); err != nil {
			t.Fatal(err)
		}
		const sdk = `export function query({prompt,options:o}) {
          if (o.tools.length || o.allowedTools.length || o.plugins.length || o.settingSources.length || Object.keys(o.mcpServers).length || !o.strictMcpConfig || o.maxTurns!==1 || o.additionalDirectories.length) throw new Error('unsafe options');
          return {streamInput:async()=>{},close:()=>{},async *[Symbol.asyncIterator]() {
            if ((await o.canUseTool('Bash',{})).behavior!=='deny') throw new Error('tool allowed');
            const hook=await o.hooks.PreToolUse[0].hooks[0]({tool_name:'Bash',tool_input:{command:'write'}});
            if(hook.hookSpecificOutput.permissionDecision!=='deny') throw new Error('hook allowed');
            const first=await prompt[Symbol.asyncIterator]().next();
            if(first.value.message.content!=='Inspect exactly this supplied task.') throw new Error('wrong task');
            yield {type:'system',subtype:'init',session_id:'once-session',model:'once-model',capabilities:[]};
            yield {type:'result',subtype:'success',is_error:false,result:'Inspection complete.',modelUsage:{model:{inputTokens:2,outputTokens:1}},total_cost_usd:0.000001};
          }};
        }`
		if err := os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, bridgePath, sdkPath, fixturebin.Require(t, "true"), root)
		cmd.Stdin = strings.NewReader(`{"op":"start","purpose":"managed","read_only_run":true,"prompt":"Inspect exactly this supplied task.","model":"once-model","effort":"high","tools":null,"capabilities":["status","stop"]}` + "\n")
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), `"kind":"chat_final","text":"Inspection complete."`) {
			t.Fatalf("read-only bridge failed ceiling/task: %v %s", err, output)
		}
	})
}
