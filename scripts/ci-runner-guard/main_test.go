// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestRepositoryWorkflows(t *testing.T) {
	t.Run("CI accepts the retired comparison job's absence", func(t *testing.T) {
		body, err := os.ReadFile("../../.github/workflows/ci.yml")
		if err != nil {
			t.Fatal(err)
		}
		var workflow map[string]any
		if err := yaml.Unmarshal(body, &workflow); err != nil {
			t.Fatal(err)
		}
		if _, exists := mapping(workflow["jobs"])["release-list-comparison"]; exists {
			t.Fatal("AEON-679 retired the capture-only comparison job")
		}
		if err := checkCITriggersAndRequiredChecks(workflow); err != nil {
			t.Fatalf("current CI without the retired comparison job must pass: %v", err)
		}
	})
	problems, err := checkDirectory("../../.github/workflows")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
}

// Risk: a public-repository event or an edited route could reach the fixed
// renderer. GitHub's workflow-restricted org runner group owns admission;
// these controls keep our checked-in side of that boundary fail closed.
func TestVisualCaptureRunnerBoundary(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/visual-capture.yml")
	if err != nil {
		t.Fatal(err)
	}
	if problems, err := checkWorkflow("visual-capture.yml", body); err != nil || len(problems) != 0 {
		t.Fatalf("dedicated manual capture rejected: %v %v", problems, err)
	}
	mutations := map[string]struct {
		want string
		edit func(map[string]any)
	}{
		"missing manual trigger": {"only on workflow_dispatch", func(w map[string]any) { w["on"] = nil }},
		"scalar trigger":         {"only on workflow_dispatch", func(w map[string]any) { w["on"] = "workflow_dispatch" }},
		"writable token":         {"contents: read and no secrets", func(w map[string]any) { w["permissions"] = map[string]any{"contents": "write"} }},
		"inherited secret":       {"contents: read and no secrets", func(w map[string]any) { w["env"] = map[string]any{"KEY": "${{ secrets.APP_KEY }}"} }},
		"hosted setting": {"only the two prepared", func(w map[string]any) {
			mapping(mapping(mapping(mapping(w["on"])["workflow_dispatch"])["inputs"])["visual_renderer"])["options"] = []any{"baseline"}
		}},
		"wrong runner": {"prepared agm2 labels", func(w map[string]any) {
			mapping(mapping(w["jobs"])["visual-reference-capture"])["runs-on"] = "ubuntu-latest"
		}},
		"extra job": {"exactly one job", func(w map[string]any) {
			mapping(w["jobs"])["extra"] = map[string]any{"runs-on": []any{"self-hosted", "linux", "x64", "agm2"}}
		}},
		"job environment": {"no environment", func(w map[string]any) {
			mapping(mapping(w["jobs"])["visual-reference-capture"])["environment"] = "production"
		}},
		"job token override": {"inherited read-only", func(w map[string]any) {
			mapping(mapping(w["jobs"])["visual-reference-capture"])["permissions"] = map[string]any{"actions": "write"}
		}},
		"missing refusal": {"refuse a non-main ref", func(w map[string]any) {
			job := mapping(mapping(w["jobs"])["visual-reference-capture"])
			job["steps"] = job["steps"].([]any)[1:]
		}},
		"skipped refusal": {"refuse a non-main ref", func(w map[string]any) {
			mapping(mapping(mapping(w["jobs"])["visual-reference-capture"])["steps"].([]any)[0])["if"] = "false"
		}},
		"weakened ref check": {"refuse a non-main ref", func(w map[string]any) {
			step := mapping(mapping(mapping(w["jobs"])["visual-reference-capture"])["steps"].([]any)[0])
			step["run"] = strings.Replace(step["run"].(string), `"$GITHUB_REF" != refs/heads/main`, "false", 1)
		}},
		"persisted credentials": {"without persisted credentials", func(w map[string]any) {
			mapping(mapping(mapping(mapping(w["jobs"])["visual-reference-capture"])["steps"].([]any)[1])["with"])["persist-credentials"] = true
		}},
		"arbitrary checkout": {"dispatched main SHA", func(w map[string]any) {
			mapping(mapping(mapping(mapping(w["jobs"])["visual-reference-capture"])["steps"].([]any)[1])["with"])["ref"] = "work/unreviewed"
		}},
		"mutable image": {"digest-pinned Playwright", func(w map[string]any) {
			mapping(mapping(mapping(w["jobs"])["visual-reference-capture"])["container"])["image"] = "mcr.microsoft.com/playwright:latest"
		}},
	}
	for _, event := range []string{"pull_request", "pull_request_target", "push", "merge_group", "schedule", "workflow_run", "workflow_call", "repository_dispatch"} {
		mutations["added event/"+event] = struct {
			want string
			edit func(map[string]any)
		}{"only on workflow_dispatch", func(w map[string]any) { mapping(w["on"])[event] = nil }}
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			var workflow map[string]any
			if err := yaml.Unmarshal(body, &workflow); err != nil {
				t.Fatal(err)
			}
			mutation.edit(workflow)
			if got := strings.Join(checkWorkflowPolicy("visual-capture.yml", workflow), "\n"); !strings.Contains(got, mutation.want) {
				t.Fatalf("unsafe capture accepted or wrong rejection: %q, want %q", got, mutation.want)
			}
		})
	}
	for _, name := range []string{"ci.yml", "nightly-full.yml", "another.yml", "visual-capture.yaml"} {
		t.Run("runner reserved/"+name, func(t *testing.T) {
			problems, err := checkWorkflow(name, body)
			if err != nil || !strings.Contains(strings.Join(problems, "\n"), "agm2 is reserved to visual-capture.yml") {
				t.Fatalf("another workflow admitted the fixed renderer: %v %v", problems, err)
			}
		})
	}
	ci := readPolicyWorkflows(t)["ci.yml"]
	// Risk: hosted-only execution could accidentally remove the four-way
	// partition or accept it without the reviewed event/ref/attempt guards.
	for _, sample := range []struct {
		name, selection string
		allowed         bool
	}{
		{"guarded four-way", routedGoShards, true},
		{"four-way removed", `${{ fromJSON(github.event_name == 'pull_request' && needs.ci-plan.outputs.mode == 'essential' && '[1, 2]' || '[1, 2, 3, 4, 5, 6, 7]') }}`, false},
		{"stale attempt", strings.Replace(routedGoShards, "outputs.run_attempt == github.run_attempt && ", "", 1), false},
		{"non-main ref", strings.Replace(routedGoShards, "github.ref == 'refs/heads/main'", "true", 1), false},
		{"fork", strings.Replace(routedGoShards, " && github.event.pull_request.head.repo.full_name == github.repository", "", 1), false},
		{"wrong runner class", strings.Replace(routedGoShards, "outputs.runner_class == 'mbp2606' && ", "", 1), false},
	} {
		t.Run("hosted Go partition/"+sample.name, func(t *testing.T) {
			var changed map[string]any
			if err := yaml.Unmarshal(ci, &changed); err != nil {
				t.Fatal(err)
			}
			job := mapping(mapping(changed["jobs"])["go-test"])
			if job["runs-on"] != "ubuntu-latest" {
				t.Fatal("Go partition coverage requires literal hosted execution")
			}
			mapping(mapping(job["strategy"])["matrix"])["shard"] = sample.selection
			err := checkCITriggersAndRequiredChecks(changed)
			if sample.allowed {
				if err != nil {
					t.Fatalf("guarded hosted partition rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "hosted shard count must retain the guarded four-way layout") {
				t.Fatalf("unsafe partition accepted or wrong rejection: %v", err)
			}
		})
	}
	for _, selection := range []string{"self-hosted", "[self-hosted, linux, x64]", routedRunner, "${{ matrix.runner }}"} {
		t.Run("CI remains hosted/"+selection, func(t *testing.T) {
			changed := strings.Replace(string(ci), "runs-on: ubuntu-latest", "runs-on: "+selection, 1)
			problems, err := checkWorkflow("ci.yml", []byte(changed))
			if err != nil || !strings.Contains(strings.Join(problems, "\n"), "must use literal runs-on: ubuntu-latest") {
				t.Fatalf("CI self-hosted route admitted: %v %v", problems, err)
			}
		})
	}
	for _, mutation := range []struct {
		name, want string
		edit       func(map[string]any)
	}{
		{"root write token", "read-only token permissions", func(w map[string]any) { w["permissions"] = map[string]any{"contents": "write"} }},
		{"Go write token", "read-only token permissions", func(w map[string]any) {
			mapping(mapping(w["jobs"])["go-test"])["permissions"] = map[string]any{"contents": "write"}
		}},
		{"Go environment", "must not receive environments or secrets", func(w map[string]any) { mapping(mapping(w["jobs"])["go-test"])["environment"] = "production" }},
		{"Go secret", "must not receive environments or secrets", func(w map[string]any) {
			mapping(mapping(w["jobs"])["go-test"])["env"] = map[string]any{"KEY": "${{ secrets.APP_KEY }}"}
		}},
	} {
		t.Run("hosted Go preserves authority/"+mutation.name, func(t *testing.T) {
			var changed map[string]any
			if err := yaml.Unmarshal(ci, &changed); err != nil {
				t.Fatal(err)
			}
			mutation.edit(changed)
			if got := strings.Join(checkWorkflowPolicy("ci.yml", changed), "\n"); !strings.Contains(got, mutation.want) {
				t.Fatalf("hosted Go authority weakened: %q, want %q", got, mutation.want)
			}
		})
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	run := mapping(mapping(mapping(workflow["jobs"])["visual-reference-capture"])["steps"].([]any)[0])["run"].(string)
	for _, sample := range []struct {
		event, ref, workflowRef string
		allowed                 bool
	}{
		{"workflow_dispatch", "refs/heads/main", "inspr-at/paimos/.github/workflows/visual-capture.yml@refs/heads/main", true},
		{"workflow_dispatch", "refs/heads/work/candidate", "inspr-at/paimos/.github/workflows/visual-capture.yml@refs/heads/main", false},
		{"workflow_dispatch", "refs/tags/main", "inspr-at/paimos/.github/workflows/visual-capture.yml@refs/heads/main", false},
		{"merge_group", "refs/heads/gh-readonly-queue/main/pr-42", "inspr-at/paimos/.github/workflows/visual-capture.yml@refs/heads/main", false},
		{"pull_request", "refs/pull/42/merge", "inspr-at/paimos/.github/workflows/visual-capture.yml@refs/heads/main", false},
		{"push", "refs/heads/main", "inspr-at/paimos/.github/workflows/visual-capture.yml@refs/heads/main", false},
		{"workflow_dispatch", "refs/heads/main", "fork/paimos/.github/workflows/visual-capture.yml@refs/heads/main", false},
	} {
		t.Run("actual refusal/"+sample.event+"/"+sample.ref, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", run)
			cmd.Env = scenarioEnviron(map[string]string{"GITHUB_EVENT_NAME": sample.event, "GITHUB_REF": sample.ref, "GITHUB_WORKFLOW_REF": sample.workflowRef})
			out, err := cmd.CombinedOutput()
			if sample.allowed {
				if err != nil {
					t.Fatalf("main dispatch refused: %v %s", err, out)
				}
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "Fixed renderer capture requires") {
				t.Fatalf("unsafe event/ref did not fail with the refusal: %v %s", err, out)
			}
		})
	}
	// Risk: unset production CPU pins can hide an invalid prepare output path.
	// Run the workflow's real command with pins only in an isolated fixture.
	t.Run("actual prepare command", func(t *testing.T) {
		var prepare string
		for _, value := range mapping(mapping(workflow["jobs"])["visual-reference-capture"])["steps"].([]any) {
			step := mapping(value)
			if step["name"] == "Require OPS-pinned prepared renderer before installing dependencies" {
				prepare, _ = step["run"].(string)
			}
		}
		if prepare == "" {
			t.Fatal("capture workflow must execute renderer preparation")
		}
		script, err := os.ReadFile("../test-prune/visual-renderer.mjs")
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := os.ReadFile("../test-prune/visual-manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		for _, setting := range []string{"agm2-baseline", "agm2-skia-runtime-disabled"} {
			t.Run(setting, func(t *testing.T) {
				var visual map[string]any
				if err := json.Unmarshal(manifest, &visual); err != nil {
					t.Fatal(err)
				}
				policy := mapping(mapping(mapping(visual["rendererPreparation"])["settings"])[setting])
				machine := mapping(policy["machine"])
				if machine == nil {
					t.Fatal("prepared setting must define a fixed machine")
				}
				machine["cpuModel"] = "Synthetic CPU"
				machine["cpuFlagsSHA256"] = strings.Repeat("a", 64)
				pinned, err := json.Marshal(visual)
				if err != nil {
					t.Fatal(err)
				}
				directory := t.TempDir()
				for path, body := range map[string][]byte{
					"scripts/test-prune/visual-renderer.mjs":  script,
					"scripts/test-prune/visual-manifest.json": pinned,
					"web/package.json":                        []byte(`{"type":"module"}`),
				} {
					path = filepath.Join(directory, path)
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, body, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", prepare)
				cmd.Dir = directory
				cmd.Env = scenarioEnviron(map[string]string{
					"AEON_VISUAL_RENDERER_SETTING": setting,
					"RUNNER_TEMP":                  filepath.Join(directory, "runner-temp"),
				})
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("workflow prepare command failed with pinned fixture: %v\n%s", err, out)
				}
				body, err := os.ReadFile(filepath.Join(directory, "web/test-results/prepared-renderer.json"))
				if err != nil {
					t.Fatal(err)
				}
				var renderer map[string]any
				if err := json.Unmarshal(body, &renderer); err != nil {
					t.Fatal(err)
				}
				if renderer["rasterSetting"] != setting || !reflect.DeepEqual(renderer["machine"], machine) ||
					!reflect.DeepEqual(mapping(renderer["browser"])["args"], policy["browserArgs"]) {
					t.Fatal("prepared output must retain the selected setting, CPU pins and browser arguments")
				}
			})
		}
	})
}

func TestTreeProofFailureCannotSkipRequiredChecks(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	if err := checkCITriggersAndRequiredChecks(workflow); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(jobs map[string]any) { delete(mapping(jobs["tree-reuse"]), "continue-on-error") },
		func(jobs map[string]any) { mapping(jobs["tree-reuse"])["continue-on-error"] = false },
		func(jobs map[string]any) { mapping(jobs["tree-reuse"])["timeout-minutes"] = 1 },
		func(jobs map[string]any) { mapping(jobs["tree-reuse"])["if"] = "success()" },
	} {
		var changed map[string]any
		if err := yaml.Unmarshal(body, &changed); err != nil {
			t.Fatal(err)
		}
		mutate(mapping(changed["jobs"]))
		if err := checkCITriggersAndRequiredChecks(changed); err == nil {
			t.Fatal("proof failure could skip required checks but the guard accepted it")
		}
	}
	for _, id := range []string{"go", "web", "release-check", "e2e", "migration-compat", "go-test", "go-static", "go-timing"} {
		t.Run(id, func(t *testing.T) {
			var changed map[string]any
			if err := yaml.Unmarshal(body, &changed); err != nil {
				t.Fatal(err)
			}
			mapping(mapping(changed["jobs"])[id])["if"] = "success()"
			if err := checkCITriggersAndRequiredChecks(changed); err == nil {
				t.Fatalf("%s may skip after proof failure", id)
			}
		})
	}
}

func TestFullUIQALabelOptInBoundary(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/full-ui-qa.yml")
	if err != nil {
		t.Fatal(err)
	}
	// d24cf2b6 rejected this file. Its guard still required the bare shard
	// list, so the production performance row could not be required at all.
	if problems, err := checkWorkflow("full-ui-qa.yml", body); err != nil || len(problems) != 0 {
		t.Fatalf("eight UI shards plus the production performance suite must be accepted: %v %v", problems, err)
	}
	for _, mutation := range []struct{ old, new string }{
		{"    types: [opened, reopened, synchronize, labeled]", "    types: [opened, reopened, synchronize]"},
		{"  schedule:", "  push:\n  schedule:"},
		{"github.ref == 'refs/heads/main'", "true"},
		{"contains(github.event.pull_request.labels.*.name, 'full-qa')", "true"},
		{"contents: read", "contents: write"},
		{"    runs-on: ubuntu-latest", "    runs-on: mbp2606"},
		{"      fail-fast: false", "      fail-fast: true"},
		{"- { shard: 8, config: playwright.ui.config.ts, partition: 8/8 }", "- { shard: 8, config: playwright.ui.config.ts, partition: 8/8 }\n          - { shard: 9, config: playwright.ui.config.ts, partition: 9/9 }"},
		{"always() && needs.qa-source.result != 'skipped'", "success()"},
		{"    timeout-minutes: 90", "    permissions: {contents: write}\n    timeout-minutes: 90"},
		{"    timeout-minutes: 90", "    environment: production\n    timeout-minutes: 90"},
		{"    timeout-minutes: 90", "    continue-on-error: true\n    timeout-minutes: 90"},
		{"    timeout-minutes: 90", "    env: {KEY: '${{ secrets.APP_KEY }}'}\n    timeout-minutes: 90"},
	} {
		t.Run(mutation.new, func(t *testing.T) {
			changed := strings.Replace(string(body), mutation.old, mutation.new, 1)
			if changed == string(body) {
				t.Fatal("mutation did not change the workflow")
			}
			problems, err := checkWorkflow("full-ui-qa.yml", []byte(changed))
			if err != nil || len(problems) == 0 {
				t.Fatalf("unsafe full UI QA accepted: %v %v", problems, err)
			}
		})
	}
	for _, mutation := range []struct{ name, old, new string }{
		{"dropped UI shard", "          - { shard: 3, config: playwright.ui.config.ts, partition: 3/8 }\n", ""},
		{"dropped performance suite", "          - { shard: performance, config: playwright.perf.config.ts, partition: 1/1 }\n", ""},
		{"performance on the UI server", "config: playwright.perf.config.ts", "config: playwright.ui.config.ts"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := strings.Replace(string(body), mutation.old, mutation.new, 1)
			if changed == string(body) {
				t.Fatal("mutation did not change the workflow")
			}
			problems, err := checkWorkflow("full-ui-qa.yml", []byte(changed))
			joined := strings.Join(problems, "\n")
			if err != nil || !strings.Contains(joined, "all eight shards") || !strings.Contains(joined, "production performance suite") {
				t.Fatalf("narrowed full UI QA accepted: %v %v", problems, err)
			}
		})
	}
	// This filename-specific label exception must not admit other broad PR jobs.
	problems, err := checkWorkflow("another-ui-qa.yml", body)
	if err != nil || len(problems) == 0 {
		t.Fatalf("broad PR exception escaped owning workflow: %v %v", problems, err)
	}
}

func TestNightlyFullCannotNarrowTiersOrAcquirePRRunnerAuthority(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/nightly-full.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ old, new string }{
		{"  schedule:", "  pull_request:\n  schedule:"},
		{"run go --all --shard", "run go --shard"},
		{"run web --all --unit", "run web --unit"},
		{"run web --all --shard", "run web --shard"},
		{"  nightly-go-test:\n", "  nightly-go-test:\n    if: false\n"},
		{"  nightly-migration-compat:\n", "  nightly-migration-compat:\n    continue-on-error: true\n"},
		{"    runs-on: ubuntu-latest", "    runs-on: mbp2606"},
		{"  nightly-go:\n", "  go:\n"},
	} {
		changed := strings.Replace(string(body), mutation.old, mutation.new, 1)
		if changed == string(body) {
			t.Fatalf("mutation did not apply: %s", mutation.old)
		}
		problems, err := checkWorkflow("nightly-full.yml", []byte(changed))
		if err != nil || len(problems) == 0 {
			t.Fatalf("unsafe nightly accepted: %v %v", problems, err)
		}
	}
}

func TestNightlyFullRequiresAllThreeTiers(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/nightly-full.yml")
	if err != nil {
		t.Fatal(err)
	}
	// Normalize the old two-tier fixture too: this must fail against d8e9906c's
	// guard because --all is the only command that includes the nightly tier.
	all := strings.ReplaceAll(string(body), "cli.mjs run go --full", "cli.mjs run go --all")
	all = strings.ReplaceAll(all, "cli.mjs run web --full", "cli.mjs run web --all")
	if problems, err := checkWorkflow("nightly-full.yml", []byte(all)); err != nil || len(problems) != 0 {
		t.Fatalf("all three tiers must be accepted: %v %v", problems, err)
	}
	for _, command := range []string{"go --all --shard", "go --all --timing", "web --all --unit", "web --all --shard"} {
		t.Run(command, func(t *testing.T) {
			changed := strings.Replace(all, command, strings.Replace(command, "--all", "--full", 1), 1)
			if changed == all {
				t.Fatal("nightly command mutation did not apply")
			}
			problems, err := checkWorkflow("nightly-full.yml", []byte(changed))
			if err != nil || !strings.Contains(fmt.Sprint(problems), "must select every tier") {
				t.Fatalf("nightly must reject gated-full-only execution: %v %v", problems, err)
			}
		})
	}
}

func TestGatePreviewDoesNotReportRequiredStatusOnPushOrDispatch(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/cross-family-preview.yml")
	if err != nil {
		t.Fatal(err)
	}
	if problems, err := checkWorkflow("cross-family-preview.yml", body); err != nil || len(problems) != 0 {
		t.Fatalf("diagnostic preview with bootstrap skip must be accepted: %v %v", problems, err)
	}
	for _, changed := range []string{
		strings.Replace(string(body), "  pull_request:", "  push:\n  pull_request:", 1),
		strings.Replace(string(body), "  pull_request:", "  workflow_dispatch:\n  pull_request:", 1),
		strings.Replace(string(body), "    runs-on:", "    if: false\n    runs-on:", 1),
		strings.Replace(string(body), "name: gate/policy-preview", "name: gate/cross-family", 1),
		strings.Replace(string(body), "statuses: read", "statuses: write", 1),
	} {
		problems, err := checkWorkflow("cross-family-preview.yml", []byte(changed))
		if err != nil || len(problems) == 0 {
			t.Fatalf("unsafe gate preview accepted: %v %v", problems, err)
		}
	}
	for _, file := range []string{"ci.yml", "cross-family-preview.yml", "forged.yml"} {
		problems, err := checkWorkflow(file, []byte("on: pull_request\njobs:\n  forged:\n    name: gate/cross-family\n    runs-on: ubuntu-latest\n    steps: [{run: 'exit 0'}]\n"))
		if err != nil || len(problems) == 0 {
			t.Fatalf("Actions counterfeit accepted: %v %v", problems, err)
		}
	}
}

// Runner unit fixtures deliberately omit the CI trigger/job contract. Full
// workflow tests and directory mutations exercise checkWorkflow instead.
func checkRunnerWorkflow(name string, body []byte) ([]string, error) {
	var workflow map[string]any
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		return nil, err
	}
	if len(mapping(workflow["jobs"])) == 0 {
		return nil, fmt.Errorf("workflow has no jobs")
	}
	return checkRunnerJobs(name, workflow), nil
}

func TestUntrustedRunnerSelections(t *testing.T) {
	for _, selection := range []string{
		"mbp2606", "[self-hosted, Linux, ARM64, mbp2606]", "[ubuntu-latest, mbp2606]",
		"mbp2606-push", "mbp2606-dispatch", "mbp2606-pr", "mbp2606-mq",
		"[self-hosted, Linux, ARM64, mbp2606, mbp2606-push]",
		"[self-hosted, Linux, ARM64, mbp2606, mbp2606-dispatch]",
		"[self-hosted, Linux, ARM64, mbp2606, mbp2606-pr]",
		"[self-hosted, Linux, ARM64, mbp2606, mbp2606-mq]",
		"ubuntu-2mbp2606", "macos-15-mbp2606", "windows-2025-mbp2606",
		"${{ needs.runner-route.outputs.runs_on }}", "${{ fromJSON(vars.RUNNER) }}",
		"${{ github.event_name != 'pull_request' && 'mbp2606' || 'ubuntu-latest' }}",
		"${{ github.event_name == 'pull_request' && 'mbp2606' || 'ubuntu-latest' }}",
	} {
		t.Run(selection, func(t *testing.T) {
			body := "on: [pull_request, push]\npermissions: {contents: read}\njobs:\n  tests:\n    runs-on: " + selection + "\n    steps: [{run: echo test}]\n"
			problems, err := checkRunnerWorkflow("ci.yml", []byte(body))
			if err != nil || len(problems) == 0 {
				t.Fatalf("unsafe selection not rejected: %v %v", problems, err)
			}
		})
	}
}

func routedWorkflow(id, extra string) string {
	return "on: [pull_request, push, merge_group, workflow_dispatch]\npermissions: {contents: read}\njobs:\n  runner-route:\n    uses: ./.github/workflows/test-runner-route.yml\n  " + id + ":\n    needs: runner-route\n    runs-on: " + routedRunner + "\n" + extra
}

func TestProtectedJobsAndSecrets(t *testing.T) {
	for _, tc := range []struct{ file, id, extra string }{
		{"release.yml", "tests", ""}, {"release.yaml", "tests", ""},
		{"RELEASE.YML", "tests", ""}, {"release.YaMl", "tests", ""},
		{"release-completion.yml", "tests", ""}, {"RELEASE-COMPLETION.YAML", "tests", ""},
		{"test-runner-route.yml", "tests", ""}, {"test-runner-route.YML", "tests", ""},
		{"test-runner-route.yaml", "tests", ""},
		{"pairing-platform.yml", "tests", ""}, {"pairing-platform.yaml", "tests", ""},
		{"PAIRING-PLATFORM.YML", "tests", ""}, {"pairing-platform.YaMl", "tests", ""},
		{"ci.yml", "image", ""}, {"ci.yml", "attestation", ""}, {"ci.yml", "pin-gate", ""},
		{"ci.yml", "tests", "    environment: release-signing\n"},
		{"ci.yml", "tests", "    environment: homebrew-tap\n"},
		{"ci.yml", "tests", "    permissions: {contents: write}\n"},
		{"ci.yml", "tests", "    env: {KEY: '${{ secrets.APP_KEY }}'}\n"},
		{"ci.yml", "tests", "    env: {KEY: '${{ toJSON(secrets) }}'}\n"},
		{"ci.yml", "tests", "    env: {KEY: \"${{ SECRETS [ 'APP_KEY' ] }}\"}\n"},
		{"ci.yml", "tests", "    steps: [{uses: 'docker/build-push-action@sha'}]\n"},
		{"ci.yml", "tests", "    steps: [{uses: 'actions/attest-build-provenance@sha'}]\n"},
		{"ci.yml", "tests", "    steps: [{run: 'bash scripts/smoke-image.sh'}]\n"},
		{"ci.yml", "tests", "    steps: [{run: 'gh attestation verify --deny-self-hosted-runners'}]\n"},
	} {
		t.Run(tc.file+"/"+tc.id+tc.extra, func(t *testing.T) {
			problems, err := checkRunnerWorkflow(tc.file, []byte(routedWorkflow(tc.id, tc.extra)))
			if err != nil || len(problems) == 0 {
				t.Fatalf("protected job not rejected: %v %v", problems, err)
			}
		})
	}
}

func TestRoutedJobsRejectAMD64Artifacts(t *testing.T) {
	for _, extra := range []string{
		"    steps: [{run: 'curl -fLO https://example.invalid/tool-linux-amd64.tar.gz'}]\n",
		"    steps: [{run: 'curl -fLO https://example.invalid/tool_linux_x86_64.zip'}]\n",
		"    steps: [{run: 'curl -fLO https://example.invalid/tool-linux-x86-64.tar.gz'}]\n",
		"    steps: [{run: 'curl -fLO https://example.invalid/tool-linux-i386.tar.gz'}]\n",
		"    steps: [{run: 'curl -fLO https://example.invalid/tool-linux-i486.tar.gz'}]\n",
		"    steps: [{run: 'curl -fLO https://example.invalid/tool-linux-i586.tar.gz'}]\n",
		"    steps: [{run: 'curl -fLO https://example.invalid/tool-linux-i686.tar.gz'}]\n",
		"    steps: [{uses: 'actions/setup-node@sha', with: {architecture: x64}}]\n",
		"    steps: [{run: 'curl -fLO https://example.invalid/tool_linux_X64.zip'}]\n",
		"    steps: [{run: 'curl -fLO https://example.invalid/x64/tool.tar.gz'}]\n",
		"    steps: [{uses: 'actions/setup-go@sha', with: {architecture: AMD64}}]\n",
		"    services: {postgres: {image: 'example.invalid/postgres:x86_64'}}\n",
		"    services: {postgres: {image: 'example.invalid/postgres:X86-64'}}\n",
		"    env: {TOOL_ARCH: linux_amd64}\n",
		"    env: {TOOL_ARCH: I686}\n",
		"    strategy: {matrix: {artifact: [tool-arm64, tool-x64]}}\n",
	} {
		t.Run(extra, func(t *testing.T) {
			problems, err := checkRunnerWorkflow("ci.yml", []byte(routedWorkflow("tests", extra)))
			if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "Linux ARM64") {
				t.Fatalf("incompatible artifact not rejected: %v %v", problems, err)
			}
			// The same artifact remains valid on a hosted runner.
			hosted := strings.Replace(routedWorkflow("tests", extra), routedRunner, "ubuntu-latest", 1)
			problems, err = checkRunnerWorkflow("ci.yml", []byte(hosted))
			if err != nil || len(problems) != 0 {
				t.Fatalf("hosted artifact rejected: %v %v", problems, err)
			}
		})
	}
	for _, inherited := range []string{
		"env: {ARTIFACT: tool_linux_amd64}\n",
		"defaults: {run: {working-directory: artifacts/x86_64}}\n",
		"env: {ARTIFACT: tool_linux_x86-64}\n",
		"defaults: {run: {working-directory: artifacts/i686}}\n",
	} {
		problems, err := checkRunnerWorkflow("ci.yml", []byte(inherited+routedWorkflow("tests", "")))
		if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "Linux ARM64") {
			t.Fatalf("inherited incompatible artifact not rejected: %v %v", problems, err)
		}
	}
	compatible := "    # Hosted release jobs may use amd64/x86_64/x64.\n    steps: [{uses: 'actions/setup-node@sha', with: {architecture: arm64}}, {run: 'curl -fLO https://example.invalid/tool-linux-aarch64.tar.gz'}]\n    services: {postgres: {image: 'pgvector/pgvector:pg18'}}\n"
	problems, err := checkRunnerWorkflow("ci.yml", []byte(routedWorkflow("tests", compatible)))
	if err != nil || len(problems) != 0 {
		t.Fatalf("ARM64-compatible job rejected: %v %v", problems, err)
	}
	for _, extra := range []string{
		"    steps: [{run: 'curl -fLO https://example.invalid/tool-linux64.tar.gz'}]\n",
		"    env: {PLATFORM: LINUX64}\n",
		"    steps: [{run: 'echo prefix64 x64suffix x640'}]\n",
	} {
		t.Run(extra, func(t *testing.T) {
			problems, err := checkRunnerWorkflow("ci.yml", []byte(routedWorkflow("tests", extra)))
			if err != nil || len(problems) != 0 {
				t.Fatalf("harmless architecture substring rejected: %v %v", problems, err)
			}
		})
	}
}

func TestCanonicalEventGuard(t *testing.T) {
	body := routedWorkflow("tests", "    steps: [{run: go test ./...}]\n")
	problems, err := checkRunnerWorkflow("ci.yml", []byte(body))
	if err != nil || len(problems) != 0 {
		t.Fatalf("safe route rejected: %v %v", problems, err)
	}
	for _, replacement := range []string{
		strings.Replace(body, `"workflow_dispatch"`, `"pull_request"`, 1),
		strings.Replace(body, `"workflow_dispatch"`, `"merge_group"`, 1),
		strings.Replace(body, "github.ref == 'refs/heads/main'", "true", 1),
		strings.Replace(body, " && github.event.pull_request.head.repo.full_name == github.repository", "", 1),
		strings.Replace(body, "github.event.pull_request.head.repo.full_name", "github.event.pull_request.base.repo.full_name", 1),
		strings.Replace(body, "github.event_name == 'merge_group'", "true", 1),
		strings.Replace(body, "needs.runner-route.outputs.run_attempt == github.run_attempt && ", "", 1),
		strings.Replace(body, "outputs.run_attempt == github.run_attempt", "outputs.run_attempt != github.run_attempt", 1),
		strings.Replace(body, "needs: runner-route", "needs: wrong-route", 1),
		strings.Replace(body, "test-runner-route.yml", "wrong-route.yml", 1),
		strings.Replace(body, "contains(fromJSON", "!contains(fromJSON", 1),
		"env: {KEY: '${{ secrets.APP_KEY }}'}\n" + body,
	} {
		problems, err = checkRunnerWorkflow("ci.yml", []byte(replacement))
		if err != nil || len(problems) == 0 {
			t.Fatalf("broken route not rejected: %v %v", problems, err)
		}
	}
}

func TestRoutedShardMatrixGuard(t *testing.T) {
	for _, selection := range []string{
		routedGoShards,
		strings.Replace(routedGoShards, "outputs.run_attempt == github.run_attempt && ", "", 1),
		strings.Replace(routedGoShards, "github.ref == 'refs/heads/main'", "true", 1),
		strings.Replace(routedGoShards, " && github.event.pull_request.head.repo.full_name == github.repository", "", 1),
		strings.Replace(routedGoShards, `"workflow_dispatch"`, `"pull_request"`, 1),
		strings.Replace(routedGoShards, "outputs.runner_class == 'mbp2606' && ", "", 1),
		strings.Replace(routedGoShards, "github.event_name == 'pull_request'", `contains(fromJSON('["pull_request","merge_group"]'), github.event_name)`, 1),
		"[1, 2, 3, 4]",
	} {
		body := routedWorkflow("go-test", "    strategy:\n      matrix:\n        shard: "+selection+"\n")
		problems, err := checkRunnerWorkflow("ci.yml", []byte(body))
		if err != nil || (len(problems) == 0) != (selection == routedGoShards) {
			t.Fatalf("matrix selection %s: %v %v", selection, problems, err)
		}
	}
}

func TestMatrixRunnerLabels(t *testing.T) {
	for _, tc := range []struct {
		matrix string
		safe   bool
	}{
		{"{runner: [ubuntu-latest, macos-15, macos-15-intel, ubuntu-24.04-arm]}", true},
		{"{include: [{runner: ubuntu-latest}, {runner: macos-15}]}", true},
		{"{runner: [ubuntu-latest, mbp2606]}", false},
		{"{runner: [ubuntu-latest, ubuntu-2mbp2606]}", false},
		{"{runner: [ubuntu-latest], include: [{runner: mbp2606}]}", false},
		{"{include: [{runner: [self-hosted, mbp2606]}]}", false},
		{"${{ fromJSON(needs.matrix.outputs.runners) }}", false},
	} {
		body := "on: {pull_request: {paths: [src/**]}}\njobs:\n  tests:\n    runs-on: ${{ matrix.runner }}\n    strategy:\n      matrix: " + tc.matrix + "\n"
		problems, err := checkRunnerWorkflow("ci.yml", []byte(body))
		if err != nil || (len(problems) == 0) != tc.safe {
			t.Fatalf("matrix %s: %v %v", tc.matrix, problems, err)
		}
	}
}

func TestHostedLabelAllowlist(t *testing.T) {
	for _, label := range []string{"ubuntu-latest", "ubuntu-24.04", "ubuntu-24.04-arm", "macos-15", "macos-15-intel"} {
		body := "on: {pull_request: {paths: [src/**]}}\njobs:\n  test:\n    runs-on: " + label + "\n"
		problems, err := checkRunnerWorkflow("ci.yml", []byte(body))
		if err != nil || len(problems) != 0 {
			t.Fatalf("hosted label %s rejected: %v %v", label, problems, err)
		}
	}
}

func TestGuardScansCaseInsensitiveWorkflowExtensions(t *testing.T) {
	for _, extension := range []string{".yml", ".yaml", ".YML", ".YAML", ".YmL", ".YaMl"} {
		t.Run(extension, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "new"+extension)
			// Include a valid workflow so skipping the unsafe file cannot be
			// mistaken for a successful rejection of an empty directory.
			if err := os.WriteFile(filepath.Join(dir, "safe.yml"), []byte("jobs:\n  test:\n    runs-on: ubuntu-latest\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, label := range []string{"mbp2606", "ubuntu-2mbp2606"} {
				if err := os.WriteFile(path, []byte("on: {pull_request: {paths: [src/**]}}\njobs:\n  test:\n    runs-on: "+label+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				problems, err := checkDirectory(dir)
				if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "new"+extension) {
					t.Fatalf("unsafe workflow missed: %v %v", problems, err)
				}
			}
		})
	}
}

func TestGuardDiscoversNewWorkflowsAndRejectsInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if _, err := checkDirectory(dir); err == nil {
		t.Fatal("empty directory accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "new.yaml"), []byte("on: {pull_request: {paths: [src/**]}}\njobs:\n  test:\n    runs-on: mbp2606\n"), 0600); err != nil {
		t.Fatal(err)
	}
	problems, err := checkDirectory(dir)
	if err != nil || len(problems) != 1 {
		t.Fatalf("new workflow missed: %v %v", problems, err)
	}
	for _, body := range []string{
		"on: [", "jobs: {}", "jobs:\n  test:\n    runs-on: ubuntu-latest\n    runs-on: mbp2606\n",
	} {
		if _, err := checkWorkflow("ci.yml", []byte(body)); err == nil {
			t.Fatalf("invalid workflow accepted: %s", body)
		}
	}
	for _, body := range []string{
		"on: pull_request_target\njobs:\n  test:\n    runs-on: ubuntu-latest\n",
		"on: {pull_request: {paths: [src/**]}}\njobs:\n  test:\n    uses: other/repo/.github/workflows/tests.yml@main\n",
		"on: push\njobs:\n  release:\n    uses: ./.github/workflows/test-runner-smoke.yml\n",
	} {
		problems, err := checkWorkflow("ci.yml", []byte(body))
		if err != nil || len(problems) == 0 {
			t.Fatalf("opaque/privileged workflow accepted: %v %v", problems, err)
		}
	}
}

func TestMergeGroupProofGuardRejectsUntrustedInputs(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(jobs map[string]any) { mapping(jobs["tree-reuse"])["if"] = "github.event_name == 'merge_group'" },
		func(jobs map[string]any) {
			steps := mapping(jobs["tree-reuse"])["steps"].([]any)
			mapping(mapping(steps[0])["with"])["ref"] = "candidate"
		},
		func(jobs map[string]any) {
			mapping(jobs["go"])["permissions"] = map[string]any{"contents": "write", "actions": "read"}
		},
		func(jobs map[string]any) {
			steps := mapping(jobs["web"])["steps"].([]any)
			mapping(steps[2])["continue-on-error"] = true
		},
		func(jobs map[string]any) {
			steps := mapping(jobs["e2e"])["steps"].([]any)
			mapping(mapping(steps[2])["env"])["CONFIRM_PR_RUN"] = "another-run"
		},
		func(jobs map[string]any) {
			steps := mapping(jobs["release-check"])["steps"].([]any)
			delete(mapping(mapping(steps[3])["env"]), "PR_CONFIRM_REUSE")
		},
		func(jobs map[string]any) { mapping(jobs["go-static"])["if"] = mapping(jobs["go-test"])["if"] },
	} {
		var workflow map[string]any
		if err := yaml.Unmarshal(body, &workflow); err != nil {
			t.Fatal(err)
		}
		mutate(mapping(workflow["jobs"]))
		if err := checkCITriggersAndRequiredChecks(workflow); err == nil {
			t.Fatal("unsafe MG proof workflow accepted")
		}
	}
	var noFlag map[string]any
	if err := yaml.Unmarshal(body, &noFlag); err != nil {
		t.Fatal(err)
	}
	delete(noFlag, "env")
	if err := checkCITriggersAndRequiredChecks(noFlag); err == nil {
		t.Fatal("missing full-tier fallback flag accepted")
	}

}
