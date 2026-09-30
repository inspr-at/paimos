// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

func TestReviewPromptUsesExactDiffAndFamily(t *testing.T) {
	r := newLaunchedRepo(t)
	r.run("remote", "add", "origin", "https://github.com/example/review-fixture.git")
	base := workspaceHEAD(t.Context(), r.dir)
	if err := os.WriteFile(filepath.Join(r.dir, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "main.go")
	r.run("commit", "-m", "implement")
	head := workspaceHEAD(t.Context(), r.dir)
	family, profile := "anthropic", "review-profile"
	order := WorkOrder{Kind: "review", Review: &reviewgate.Binding{TicketSnapshot: "Ticket: test the tenant boundary", Repository: "example/review-fixture", BaseSHA: base, HeadSHA: head, AuthorFamily: "openai", ReviewerFamily: &family, ProfileID: &profile}}
	prompt, err := reviewPrompt(t.Context(), r.dir, order, Profile{ID: profile, Family: family})
	if err != nil || !strings.Contains(prompt, "+package main") || !strings.Contains(prompt, base+"..."+head) || !strings.Contains(prompt, "tenant boundary") {
		t.Fatal("review context was not bound to ticket and exact diff")
	}
	order.Review.AuthorFamily = family
	if _, err := reviewPrompt(t.Context(), r.dir, order, Profile{ID: profile, Family: family}); !errors.Is(err, errReviewContext) {
		t.Fatal("author reviewed itself")
	}
	order.Review.AuthorFamily = "openai"
	order.Review.Repository = "another/repository"
	if _, err := reviewPrompt(t.Context(), r.dir, order, Profile{ID: profile, Family: family}); err == nil {
		t.Fatal("wrong repository reviewed")
	}
	order.Review.Repository = "example/review-fixture"
	if err := os.WriteFile(filepath.Join(r.dir, "test.env.key"), []byte("fixture only"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "test.env.key")
	r.run("commit", "-m", "credential path fixture")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	if _, err := reviewPrompt(t.Context(), r.dir, order, Profile{ID: profile, Family: family}); err == nil {
		t.Fatal("credential path entered review prompt")
	}
}

func TestReadOnlyReviewRefusesUnqualifiedAdaptersBeforeStart(t *testing.T) {
	no := false
	run := Run{Purpose: "managed", ReadOnlyReview: true, RequestedAccountID: "account", RepositoryMutationAllowed: &no}
	for _, a := range []Adapter{NewCodexAdapter("/nonexistent", nil), NewCursorAdapter("/nonexistent", nil), NewPiAdapter("/nonexistent", nil)} {
		if err := validQueuedExecutionMode(run, a); !errors.Is(err, ErrVerificationUnavailable) {
			t.Fatal("unqualified review accepted")
		}
	}
	if err := validQueuedExecutionMode(run, NewClaudeAdapter("", "", "", nil)); err != nil {
		t.Fatal(err)
	}
	yes := true
	run.RepositoryMutationAllowed = &yes
	if err := validQueuedExecutionMode(run, NewClaudeAdapter("", "", "", nil)); err == nil {
		t.Fatal("write-enabled review accepted")
	}
}

func TestClaudeReviewBridgeHasNoToolsAndCapturesFinalAnswer(t *testing.T) {
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
	if err = os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	const sdk = `export function query({options:o}) {
      if (o.tools.length || o.allowedTools.length || o.plugins.length || o.settingSources.length || Object.keys(o.mcpServers).length || !o.strictMcpConfig || o.maxTurns!==1) throw new Error('unsafe options');
      return {streamInput:async()=>{},interrupt:async()=>({still_queued:[]}),close:()=>{},async *[Symbol.asyncIterator]() {
        if ((await o.canUseTool('Bash',{})).behavior!=='deny') throw new Error('tool allowed');
        yield {type:'system',subtype:'init',session_id:'review-session',model:'review-model',capabilities:[]};
        yield {type:'result',subtype:'success',is_error:false,result:'VERDICT: ok',modelUsage:{model:{inputTokens:2,outputTokens:1}},total_cost_usd:0.000001};
      }};
    }`
	if err = os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, bridgePath, sdkPath, "/bin/true", root)
	cmd.Stdin = strings.NewReader(`{"op":"start","purpose":"managed","read_only_review":true,"prompt":"Review supplied diff","model":"review-model","effort":"xhigh","tools":null,"capabilities":["status","stop"]}` + "\n")
	output, err := cmd.Output()
	if err != nil || !strings.Contains(string(output), `"kind":"review_result","answer":"VERDICT: ok"`) || strings.Contains(string(output), `"control_failed"`) {
		t.Fatal("review failed its no-tools or final-answer boundary")
	}
}
