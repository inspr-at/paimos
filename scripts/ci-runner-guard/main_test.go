// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRepositoryWorkflows(t *testing.T) {
	problems, err := checkDirectory("../../.github/workflows")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
}

func TestCITriggersAndRequiredChecks(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCITriggersAndRequiredChecks(body); err != nil {
		t.Fatal(err)
	}
}

func checkCITriggersAndRequiredChecks(body []byte) error {
	var workflow map[string]any
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		return err
	}
	events := mapping(workflow["on"])
	var names []string
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"merge_group", "pull_request", "push", "workflow_dispatch"}) {
		return fmt.Errorf("CI must cover main, PRs, the merge queue and manual runs exactly once: %v", names)
	}
	if !reflect.DeepEqual(mapping(events["push"]), map[string]any{"branches": []any{"main"}}) {
		return fmt.Errorf("branch and tag pushes outside main must not duplicate PR CI: %v", events["push"])
	}
	if events["pull_request"] != nil {
		return fmt.Errorf("required PR checks must run without path or activity filters: %v", events["pull_request"])
	}
	if !reflect.DeepEqual(mapping(events["merge_group"]), map[string]any{"types": []any{"checks_requested"}}) {
		return fmt.Errorf("merge queue check requests must run CI: %v", events["merge_group"])
	}

	// YAML parsing rejects duplicate keys. Require the workflow-level mapping;
	// the Node tests evaluate its group and cancellation expressions by event.
	concurrency := mapping(workflow["concurrency"])
	group, groupOK := concurrency["group"].(string)
	cancel, cancelOK := concurrency["cancel-in-progress"].(string)
	if len(concurrency) != 2 || !groupOK || group == "" || !cancelOK || cancel == "" {
		return fmt.Errorf("CI must have one workflow-level concurrency mapping with group and cancel-in-progress expressions")
	}

	// These are the active main ruleset's contexts. Renaming or conditionally
	// skipping them would strand a PR or merge queue waiting for its checks.
	jobs := mapping(workflow["jobs"])
	for id, value := range jobs {
		if _, exists := mapping(value)["concurrency"]; exists {
			return fmt.Errorf("job %q must not override workflow-level concurrency", id)
		}
	}
	for _, id := range []string{"go-test", "go-static", "go-timing", "runner-route"} {
		job := mapping(jobs[id])
		if job == nil {
			return fmt.Errorf("required CI job %q is missing", id)
		}
		if id == "go-static" || id == "go-timing" {
			if _, exists := job["if"]; exists {
				return fmt.Errorf("required CI job %q must run without an if condition", id)
			}
		}
	}
	for _, context := range []string{"go", "web", "release-check", "e2e"} {
		job := mapping(jobs[context])
		if job == nil {
			return fmt.Errorf("required check %q is missing", context)
		}
		if name, exists := job["name"]; exists && name != context {
			return fmt.Errorf("required check %q renamed to %v", context, name)
		}
		if context == "go" {
			if job["if"] != "always()" {
				return fmt.Errorf("go must report failures even when its dependencies fail: %v", job["if"])
			}
			if !reflect.DeepEqual(job["needs"], []any{"go-test", "go-static", "go-timing"}) {
				return fmt.Errorf("go must gate every shard, static checks and timing: %v", job["needs"])
			}
		} else if job["if"] != nil {
			return fmt.Errorf("required check %q must run for every CI event: %v", context, job["if"])
		}
	}
	return nil
}

func TestCIContractRejectsMutatedWorkflows(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	type mutation struct {
		name string
		edit func(map[string]any)
		want string
	}
	cases := []mutation{
		{"missing-workflow-concurrency", func(w map[string]any) { delete(w, "concurrency") }, "workflow-level concurrency mapping"},
		{"scalar-workflow-concurrency", func(w map[string]any) { w["concurrency"] = "shared" }, "workflow-level concurrency mapping"},
		{"missing-cancel-expression", func(w map[string]any) { delete(mapping(w["concurrency"]), "cancel-in-progress") }, "workflow-level concurrency mapping"},
	}
	var original map[string]any
	if err := yaml.Unmarshal(body, &original); err != nil {
		t.Fatal(err)
	}
	for id := range mapping(original["jobs"]) {
		cases = append(cases, mutation{
			"job-concurrency-" + id,
			func(w map[string]any) {
				mapping(mapping(w["jobs"])[id])["concurrency"] = map[string]any{"group": "shared", "cancel-in-progress": true}
			},
			fmt.Sprintf("job %q must not override workflow-level concurrency", id),
		})
	}
	for _, id := range []string{"go-test", "go-static", "go-timing", "runner-route"} {
		cases = append(cases,
			mutation{"missing-" + id, func(w map[string]any) { delete(mapping(w["jobs"]), id) }, fmt.Sprintf("required CI job %q is missing", id)},
			mutation{"renamed-" + id, func(w map[string]any) {
				jobs := mapping(w["jobs"])
				jobs[id+"-renamed"] = jobs[id]
				delete(jobs, id)
			}, fmt.Sprintf("required CI job %q is missing", id)},
		)
	}
	for _, id := range []string{"go-static", "go-timing"} {
		for _, condition := range []any{false, "success()", nil} {
			cases = append(cases, mutation{
				fmt.Sprintf("conditional-%s-%v", id, condition),
				func(w map[string]any) { mapping(mapping(w["jobs"])[id])["if"] = condition },
				fmt.Sprintf("required CI job %q must run without an if condition", id),
			})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var workflow map[string]any
			if err := yaml.Unmarshal(body, &workflow); err != nil {
				t.Fatal(err)
			}
			tc.edit(workflow)
			mutated, err := yaml.Marshal(workflow)
			if err != nil {
				t.Fatal(err)
			}
			assertCIContractRejectsCopy(t, mutated, tc.want)
		})
	}
	t.Run("duplicate-workflow-concurrency", func(t *testing.T) {
		mutated := append(append([]byte(nil), body...), []byte("\nconcurrency:\n  group: shared\n  cancel-in-progress: true\n")...)
		assertCIContractRejectsCopy(t, mutated, `mapping key "concurrency" already defined`)
	})
}

func assertCIContractRejectsCopy(t *testing.T, body []byte, want string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ci.yml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	copy, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCITriggersAndRequiredChecks(copy); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("mutated workflow must fail with %q; got %v", want, err)
	}
}

func TestUntrustedRunnerSelections(t *testing.T) {
	for _, selection := range []string{
		"mbp2606", "[self-hosted, Linux, ARM64, mbp2606]", "[ubuntu-latest, mbp2606]",
		"mbp2606-push", "mbp2606-dispatch",
		"[self-hosted, Linux, ARM64, mbp2606, mbp2606-push]",
		"[self-hosted, Linux, ARM64, mbp2606, mbp2606-dispatch]",
		"ubuntu-2mbp2606", "macos-15-mbp2606", "windows-2025-mbp2606",
		"${{ needs.runner-route.outputs.runs_on }}", "${{ fromJSON(vars.RUNNER) }}",
		"${{ github.event_name != 'pull_request' && 'mbp2606' || 'ubuntu-latest' }}",
		"${{ github.event_name == 'pull_request' && 'mbp2606' || 'ubuntu-latest' }}",
	} {
		t.Run(selection, func(t *testing.T) {
			body := "on: [pull_request, push]\npermissions: {contents: read}\njobs:\n  tests:\n    runs-on: " + selection + "\n    steps: [{run: echo test}]\n"
			problems, err := checkWorkflow("ci.yml", []byte(body))
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
			problems, err := checkWorkflow(tc.file, []byte(routedWorkflow(tc.id, tc.extra)))
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
			problems, err := checkWorkflow("ci.yml", []byte(routedWorkflow("tests", extra)))
			if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "Linux ARM64") {
				t.Fatalf("incompatible artifact not rejected: %v %v", problems, err)
			}
			// The same artifact remains valid on a hosted runner.
			hosted := strings.Replace(routedWorkflow("tests", extra), routedRunner, "ubuntu-latest", 1)
			problems, err = checkWorkflow("ci.yml", []byte(hosted))
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
		problems, err := checkWorkflow("ci.yml", []byte(inherited+routedWorkflow("tests", "")))
		if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "Linux ARM64") {
			t.Fatalf("inherited incompatible artifact not rejected: %v %v", problems, err)
		}
	}
	compatible := "    # Hosted release jobs may use amd64/x86_64/x64.\n    steps: [{uses: 'actions/setup-node@sha', with: {architecture: arm64}}, {run: 'curl -fLO https://example.invalid/tool-linux-aarch64.tar.gz'}]\n    services: {postgres: {image: 'pgvector/pgvector:pg18'}}\n"
	problems, err := checkWorkflow("ci.yml", []byte(routedWorkflow("tests", compatible)))
	if err != nil || len(problems) != 0 {
		t.Fatalf("ARM64-compatible job rejected: %v %v", problems, err)
	}
	for _, extra := range []string{
		"    steps: [{run: 'curl -fLO https://example.invalid/tool-linux64.tar.gz'}]\n",
		"    env: {PLATFORM: LINUX64}\n",
		"    steps: [{run: 'echo prefix64 x64suffix x640'}]\n",
	} {
		t.Run(extra, func(t *testing.T) {
			problems, err := checkWorkflow("ci.yml", []byte(routedWorkflow("tests", extra)))
			if err != nil || len(problems) != 0 {
				t.Fatalf("harmless architecture substring rejected: %v %v", problems, err)
			}
		})
	}
}

func TestCanonicalEventGuard(t *testing.T) {
	body := routedWorkflow("tests", "    steps: [{run: go test ./...}]\n")
	problems, err := checkWorkflow("ci.yml", []byte(body))
	if err != nil || len(problems) != 0 {
		t.Fatalf("safe route rejected: %v %v", problems, err)
	}
	for _, replacement := range []string{
		strings.Replace(body, `"workflow_dispatch"`, `"pull_request"`, 1),
		strings.Replace(body, `"workflow_dispatch"`, `"merge_group"`, 1),
		strings.Replace(body, "github.ref == 'refs/heads/main' && ", "", 1),
		strings.Replace(body, "needs.runner-route.outputs.run_attempt == github.run_attempt && ", "", 1),
		strings.Replace(body, "outputs.run_attempt == github.run_attempt", "outputs.run_attempt != github.run_attempt", 1),
		strings.Replace(body, "needs: runner-route", "needs: wrong-route", 1),
		strings.Replace(body, "test-runner-route.yml", "wrong-route.yml", 1),
		strings.Replace(body, "contains(fromJSON", "!contains(fromJSON", 1),
		"env: {KEY: '${{ secrets.APP_KEY }}'}\n" + body,
	} {
		problems, err = checkWorkflow("ci.yml", []byte(replacement))
		if err != nil || len(problems) == 0 {
			t.Fatalf("broken route not rejected: %v %v", problems, err)
		}
	}
}

func TestRoutedShardMatrixGuard(t *testing.T) {
	for _, selection := range []string{
		routedGoShards,
		strings.Replace(routedGoShards, "outputs.run_attempt == github.run_attempt && ", "", 1),
		strings.Replace(routedGoShards, "github.ref == 'refs/heads/main' && ", "", 1),
		strings.Replace(routedGoShards, `"workflow_dispatch"`, `"pull_request"`, 1),
		strings.Replace(routedGoShards, "outputs.runner_class == 'mbp2606' && ", "", 1),
		"[1, 2, 3, 4]",
	} {
		body := routedWorkflow("go-test", "    strategy:\n      matrix:\n        shard: "+selection+"\n")
		problems, err := checkWorkflow("ci.yml", []byte(body))
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
		body := "on: pull_request\njobs:\n  tests:\n    runs-on: ${{ matrix.runner }}\n    strategy:\n      matrix: " + tc.matrix + "\n"
		problems, err := checkWorkflow("ci.yml", []byte(body))
		if err != nil || (len(problems) == 0) != tc.safe {
			t.Fatalf("matrix %s: %v %v", tc.matrix, problems, err)
		}
	}
}

func TestHostedLabelAllowlist(t *testing.T) {
	for _, label := range []string{"ubuntu-latest", "ubuntu-24.04", "ubuntu-24.04-arm", "macos-15", "macos-15-intel"} {
		body := "on: pull_request\njobs:\n  test:\n    runs-on: " + label + "\n"
		problems, err := checkWorkflow("ci.yml", []byte(body))
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
				if err := os.WriteFile(path, []byte("on: pull_request\njobs:\n  test:\n    runs-on: "+label+"\n"), 0600); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, "new.yaml"), []byte("on: pull_request\njobs:\n  test:\n    runs-on: mbp2606\n"), 0600); err != nil {
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
		"on: pull_request\njobs:\n  test:\n    uses: other/repo/.github/workflows/tests.yml@main\n",
		"on: push\njobs:\n  release:\n    uses: ./.github/workflows/test-runner-smoke.yml\n",
	} {
		problems, err := checkWorkflow("ci.yml", []byte(body))
		if err != nil || len(problems) == 0 {
			t.Fatalf("opaque/privileged workflow accepted: %v %v", problems, err)
		}
	}
}
