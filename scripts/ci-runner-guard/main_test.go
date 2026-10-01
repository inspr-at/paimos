// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestGatePreviewDoesNotReportRequiredStatusOnPushOrDispatch(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/cross-family-preview.yml")
	if err != nil {
		t.Fatal(err)
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
		strings.Replace(body, "github.ref == 'refs/heads/main' && ", "", 1),
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
		strings.Replace(routedGoShards, "github.ref == 'refs/heads/main' && ", "", 1),
		strings.Replace(routedGoShards, `"workflow_dispatch"`, `"pull_request"`, 1),
		strings.Replace(routedGoShards, "outputs.runner_class == 'mbp2606' && ", "", 1),
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
