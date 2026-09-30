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
	if err != nil || !strings.Contains(prompt, "+package main") || !strings.Contains(prompt, base+".."+head) || !strings.Contains(prompt, "tenant boundary") {
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

func reviewPromptFixture(t *testing.T) (*launchedRepo, WorkOrder, Profile) {
	t.Helper()
	r := newLaunchedRepo(t)
	r.run("remote", "add", "origin", "https://github.com/example/review-fixture.git")
	family, profile := "anthropic", "review-profile"
	order := WorkOrder{Kind: "review", Review: &reviewgate.Binding{
		TicketSnapshot: "Ticket: review the sealed commit tree",
		Repository:     "example/review-fixture",
		BaseSHA:        workspaceHEAD(t.Context(), r.dir),
		AuthorFamily:   "openai", ReviewerFamily: &family, ProfileID: &profile,
	}}
	return r, order, Profile{ID: profile, Family: family}
}

func TestMutationReplaceRefHidesSealedTree(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	if err := os.WriteFile(filepath.Join(r.dir, "sealed.txt"), []byte("SEALED_TREE_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "sealed.txt")
	r.run("commit", "-m", "sealed change")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	r.run("switch", "-c", "replacement", order.Review.BaseSHA)
	if err := os.WriteFile(filepath.Join(r.dir, "replacement.txt"), []byte("REPLACEMENT_TREE_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "replacement.txt")
	r.run("commit", "-m", "innocent replacement")
	r.run("replace", order.Review.HeadSHA, workspaceHEAD(t.Context(), r.dir))
	prompt, err := reviewPrompt(t.Context(), r.dir, order, profile)
	if errors.Is(err, errReviewContext) {
		return
	}
	if err != nil || !strings.Contains(prompt, "+SEALED_TREE_MARKER") || strings.Contains(prompt, "REPLACEMENT_TREE_MARKER") {
		t.Fatal("replace ref hid the sealed tree")
	}
}

func TestMutationRenamedSecretPathIsRefused(t *testing.T) {
	for _, name := range []string{".env", "secrets/fixture.txt", "id_fixture"} {
		t.Run(name, func(t *testing.T) {
			r, order, profile := reviewPromptFixture(t)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(r.dir, name)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(r.dir, name), []byte("synthetic path fixture only\n"), 0600); err != nil {
				t.Fatal(err)
			}
			r.run("add", name)
			r.run("commit", "-m", "synthetic sensitive path")
			order.Review.BaseSHA = workspaceHEAD(t.Context(), r.dir)
			r.run("config", "diff.renames", "true")
			r.run("mv", name, "notes.txt")
			r.run("commit", "-m", "rename sensitive path")
			order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
			if prompt, err := reviewPrompt(t.Context(), r.dir, order, profile); !errors.Is(err, errReviewContext) || prompt != "" {
				t.Fatal("renamed sensitive path entered the review prompt")
			}
		})
	}
}

func TestMutationGraftsCannotHideSealedChange(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	if err := os.WriteFile(filepath.Join(r.dir, "evil.txt"), []byte("EVIL_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "evil.txt")
	r.run("commit", "-m", "change requiring review")
	evil := workspaceHEAD(t.Context(), r.dir)
	if err := os.WriteFile(filepath.Join(r.dir, "note.txt"), []byte("HARMLESS_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "note.txt")
	r.run("commit", "-m", "harmless follow-up")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	if err := os.WriteFile(filepath.Join(r.dir, ".git", "info", "grafts"), []byte(order.Review.BaseSHA+" "+evil+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prompt, err := reviewPrompt(t.Context(), r.dir, order, profile)
	if errors.Is(err, errReviewContext) && prompt == "" {
		return // An incomplete import must fail closed.
	}
	if err != nil || !strings.Contains(prompt, "+EVIL_MARKER") || !strings.Contains(prompt, "+HARMLESS_MARKER") {
		t.Fatal("workspace grafts hid the sealed change")
	}
}

func TestMutationSubmoduleDiffCannotExposeSensitiveContent(t *testing.T) {
	for _, operation := range []string{"add", "delete"} {
		t.Run(operation, func(t *testing.T) {
			r, order, profile := reviewPromptFixture(t)
			sub := newLaunchedRepo(t)
			if operation == "delete" {
				if err := os.WriteFile(filepath.Join(sub.dir, ".env"), []byte("SUBMODULE_ENV_MARKER\nLEAKED_ASSIGNMENT=synthetic\n"), 0600); err != nil {
					t.Fatal(err)
				}
				sub.run("add", ".env")
				sub.run("commit", "-m", "synthetic sensitive path")
			}
			r.run("-c", "protocol.file.allow=always", "submodule", "add", sub.dir, "vendor/lib")
			r.run("commit", "-m", "base gitlink")
			order.Review.BaseSHA = workspaceHEAD(t.Context(), r.dir)
			lib := filepath.Join(r.dir, "vendor", "lib")
			if operation == "add" {
				if err := os.WriteFile(filepath.Join(lib, ".env"), []byte("SUBMODULE_ENV_MARKER\nLEAKED_ASSIGNMENT=synthetic\n"), 0600); err != nil {
					t.Fatal(err)
				}
				r.git(lib, "add", ".env")
			} else {
				// Remove only this test's synthetic fixture through Git's index.
				r.git(lib, "update-index", "--force-remove", ".env")
			}
			r.git(lib, "commit", "-m", "change sensitive submodule content")
			r.run("add", "vendor/lib")
			r.run("commit", "-m", "update gitlink")
			order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
			r.run("config", "diff.submodule", "diff")
			prompt, err := reviewPrompt(t.Context(), r.dir, order, profile)
			if err != nil || !strings.Contains(prompt, "Subproject commit") || strings.Contains(prompt, "SUBMODULE_ENV_MARKER") || strings.Contains(prompt, "LEAKED_ASSIGNMENT") || strings.Contains(prompt, "vendor/lib/.env") {
				t.Fatal("workspace submodule settings exposed sensitive content")
			}
		})
	}
}

func TestReviewPromptIgnoresWorkspaceAttributesAndCoreConfig(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	if err := os.WriteFile(filepath.Join(r.dir, "sealed.txt"), []byte("SEALED_TREE_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "sealed.txt")
	r.run("commit", "-m", "sealed change")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	want, err := reviewPrompt(t.Context(), r.dir, order, profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{".gitattributes", filepath.Join(".git", "info", "attributes")} {
		if err := os.WriteFile(filepath.Join(r.dir, filename), []byte("*.txt -diff diff=hostile filter=hostile\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"diff.hostile.textconv": "false", "diff.external": "false", "diff.relative": "true",
		"filter.hostile.clean": "false", "filter.hostile.smudge": "false", "filter.hostile.required": "true",
		"core.worktree": filepath.Join(r.dir, "vendor"), "core.bare": "true",
		"core.attributesFile": filepath.Join(r.dir, ".gitattributes"), "core.fsmonitor": "false",
		"core.hooksPath": filepath.Join(r.dir, "hostile-hooks"), "core.abbrev": "4",
	} {
		r.run("config", key, value)
	}
	got, err := reviewPrompt(t.Context(), r.dir, order, profile)
	if err != nil || got != want {
		t.Fatal("workspace attributes or core config changed the sealed diff")
	}
}

func TestReviewPromptIgnoresCommittedAttributes(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	if err := os.WriteFile(filepath.Join(r.dir, ".gitattributes"), []byte("*.txt -diff diff=hostile filter=hostile\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", ".gitattributes")
	r.run("commit", "-m", "hostile committed attributes")
	order.Review.BaseSHA = workspaceHEAD(t.Context(), r.dir)
	if err := os.WriteFile(filepath.Join(r.dir, "sealed.txt"), []byte("SEALED_TREE_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "sealed.txt")
	r.run("commit", "-m", "sealed change")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	prompt, err := reviewPrompt(t.Context(), r.dir, order, profile)
	if err != nil || !strings.Contains(prompt, "+SEALED_TREE_MARKER") || strings.Contains(prompt, "Binary files ") {
		t.Fatal("committed attributes altered the sealed text diff")
	}
}

func TestReviewPromptRefusesUnrelatedGraftedHistory(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	r.run("switch", "--orphan", "unrelated")
	if err := os.WriteFile(filepath.Join(r.dir, "unrelated.txt"), []byte("UNRELATED_TREE_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "unrelated.txt")
	r.run("commit", "-m", "unrelated root")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	if err := os.WriteFile(filepath.Join(r.dir, ".git", "info", "grafts"), []byte(order.Review.HeadSHA+" "+order.Review.BaseSHA+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if prompt, err := reviewPrompt(t.Context(), r.dir, order, profile); !errors.Is(err, errReviewContext) || prompt != "" {
		t.Fatal("workspace graft manufactured sealed ancestry")
	}
}

func TestReviewPromptRefusesSensitiveGitlinkPath(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	r.run("update-index", "--add", "--cacheinfo", "160000,"+order.Review.BaseSHA+",secrets/lib")
	r.run("commit", "-m", "sensitive gitlink path")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	if prompt, err := reviewPrompt(t.Context(), r.dir, order, profile); !errors.Is(err, errReviewContext) || prompt != "" {
		t.Fatal("sensitive gitlink path entered the review prompt")
	}
}

func TestReviewPromptCleansScratchOnSuccessAndErrors(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	if err := os.WriteFile(filepath.Join(r.dir, "sealed.txt"), []byte("SEALED_TREE_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "sealed.txt")
	r.run("commit", "-m", "sealed change")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	scratchRoot := t.TempDir()
	t.Setenv("TMPDIR", scratchRoot)
	for _, scenario := range []string{"success", "missing-commit", "non-commit", "wrong-repository", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			binding := *order.Review
			order := order
			order.Review = &binding
			ctx := t.Context()
			switch scenario {
			case "missing-commit":
				binding.HeadSHA = strings.Repeat("f", 40)
			case "non-commit":
				sha, err := gitOutput(ctx, r.dir, "rev-parse", "HEAD:sealed.txt")
				if err != nil {
					t.Fatal(err)
				}
				binding.HeadSHA = strings.TrimSpace(sha)
			case "wrong-repository":
				binding.Repository = "other/repository"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			prompt, err := reviewPrompt(ctx, r.dir, order, profile)
			if scenario == "success" {
				if err != nil || !strings.Contains(prompt, "+SEALED_TREE_MARKER") {
					t.Fatal("valid sealed change refused")
				}
			} else if !errors.Is(err, errReviewContext) || prompt != "" {
				t.Fatal("invalid sealed context accepted")
			}
			entries, err := os.ReadDir(scratchRoot)
			if err != nil || len(entries) != 0 {
				t.Fatal("review left its private scratch repository behind")
			}
		})
	}
}

func TestReviewPromptUsesLinkedWorktreeAndPackedHEAD(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	if err := os.WriteFile(filepath.Join(r.dir, "sealed.txt"), []byte("SEALED_TREE_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "sealed.txt")
	r.run("commit", "-m", "sealed change")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	r.run("pack-refs", "--all")
	for _, workspace := range []string{r.dir, filepath.Join(t.TempDir(), "linked")} {
		if workspace != r.dir {
			r.run("worktree", "add", "--detach", workspace, order.Review.HeadSHA)
		}
		prompt, err := reviewPrompt(t.Context(), workspace, order, profile)
		if err != nil || !strings.Contains(prompt, "+SEALED_TREE_MARKER") {
			t.Fatal("linked worktree lost the sealed diff")
		}
		got := completedReviewRange(t.Context(), workspace, order.Review.BaseSHA)
		if got == nil || got.HeadSHA != order.Review.HeadSHA || got.Repository != order.Review.Repository {
			t.Fatal("raw packed or detached HEAD lost the completed range")
		}
	}
}

func TestReviewGitIgnoresInheritedObjectAndRefOverrides(t *testing.T) {
	r, order, profile := reviewPromptFixture(t)
	if err := os.WriteFile(filepath.Join(r.dir, "sealed.txt"), []byte("SEALED_TREE_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.run("add", "sealed.txt")
	r.run("commit", "-m", "sealed change")
	order.Review.HeadSHA = workspaceHEAD(t.Context(), r.dir)
	for _, name := range []string{
		"GIT_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
		"GIT_INDEX_FILE", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_REPLACE_REF_BASE", "GIT_CONFIG",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_NOSYSTEM",
		"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_ATTR_SOURCE",
		"GIT_EXTERNAL_DIFF", "GIT_DIFF_OPTS", "GIT_TEMPLATE_DIR", "GIT_SHALLOW_FILE",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, filepath.Join(r.dir, "nonexistent-override"))
			prompt, err := reviewPrompt(t.Context(), r.dir, order, profile)
			if err != nil || !strings.Contains(prompt, "+SEALED_TREE_MARKER") {
				t.Fatal("inherited Git override changed the sealed review context")
			}
			if got := completedReviewRange(t.Context(), r.dir, order.Review.BaseSHA); got == nil || got.HeadSHA != order.Review.HeadSHA || got.Repository != order.Review.Repository {
				t.Fatal("inherited Git override changed the completed review range")
			}
		})
	}
	t.Run("GIT_CONFIG_COUNT", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.url")
		t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/other/repository.git")
		prompt, err := reviewPrompt(t.Context(), r.dir, order, profile)
		if err != nil || !strings.Contains(prompt, "+SEALED_TREE_MARKER") {
			t.Fatal("inherited Git config redirected the review repository")
		}
	})
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
