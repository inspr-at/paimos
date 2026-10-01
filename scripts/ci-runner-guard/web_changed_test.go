// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func webSelection(t *testing.T) (string, string) {
	t.Helper()
	body, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	job := mapping(mapping(workflow["jobs"])["web"])
	if job["if"] != nil || job["name"] != nil {
		t.Fatal("required web check must always run with its existing identity")
	}
	steps := job["steps"].([]any)
	with := mapping(mapping(steps[0])["with"])
	if with["fetch-depth"] != 0 || with["persist-credentials"] != false {
		t.Fatal("PR selection requires full history without persisted credentials")
	}
	var snapshot, run string
	sealed := false
	for index, value := range steps {
		step := mapping(value)
		command, _ := step["run"].(string)
		if step["id"] == "playwright-seal" {
			if sealed || step["if"] != nil || step["working-directory"] != "web" ||
				!strings.Contains(command, "npm ci\n") || !strings.Contains(command, "python3 -I ../scripts/ci-web-seal.py") ||
				mapping(step["env"])["SNAPSHOT_SEAL_HASH"] != "${{ steps.snapshot.outputs.seal_hash }}" ||
				mapping(step["env"])["SNAPSHOT_LOCK_HASH"] != "${{ steps.snapshot.outputs.lock_hash }}" {
				t.Fatal("Playwright must be sealed immediately after npm ci with snapshot-bound inputs")
			}
			sealed = true
		}
		if strings.Contains(command, "npm run") || strings.Contains(command, "npx playwright") || strings.Contains(command, "node --experimental") {
			if !sealed {
				t.Fatal("repository Node commands must follow the Playwright seal")
			}
		}
		if command == "bash scripts/ci-web-tests.sh snapshot" {
			if index != 1 || step["id"] != "snapshot" || step["if"] != nil || mapping(step["env"])["PR_BASE_SHA"] != "${{ github.event.pull_request.base.sha }}" {
				t.Fatal("snapshot must bind the event base immediately after checkout, before Node")
			}
			snapshot = command
		}
		if strings.Contains(command, "bash ../scripts/ci-web-tests.sh run") {
			if !sealed || mapping(step["env"])["PLAYWRIGHT_MANIFEST"] != "${{ steps.playwright-seal.outputs.manifest }}" {
				t.Fatal("run must consume the runner-sealed Playwright manifest")
			}
			if step["working-directory"] != "web" || step["if"] != nil {
				t.Fatal("selection must run in web on every CI event")
			}
			for _, output := range []string{"event", "base_sha", "refs_hash", "paths_hash", "require_tests", "selected_specs", "script_hash", "guard_hash"} {
				if mapping(step["env"])["SNAPSHOT_"+strings.ToUpper(output)] != "${{ steps.snapshot.outputs."+output+" }}" {
					t.Fatalf("run must consume the sealed snapshot output %s", output)
				}
			}
			run = command
		}
	}
	if snapshot == "" || run == "" {
		t.Fatal("missing UI snapshot or selection step")
	}
	return snapshot, run
}

type webScenario struct {
	event, path, list, mutation, base string
	forgedSource                      string
	failList, failRun                 bool
}

type webResult struct {
	calls, summary, output, base string
	err                          error
}

// Execute the actual workflow commands in a tiny Git fixture with an inert
// Playwright stub. No browser, server, network or credentials are needed.
func runWebSelection(t *testing.T, scenario webScenario) webResult {
	t.Helper()
	dir := t.TempDir()
	for _, path := range []string{"web/src", "web/tests", "web/node_modules/@playwright/test", "scripts", "bin", "temp"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "fixture")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("web/src/fixture.vue", "base\n")
	write("web/tests/existing.spec.ts", "base spec\n")
	git("add", "web/src/fixture.vue", "web/tests/existing.spec.ts")
	git("-c", "user.name=Markus Barta", "-c", "user.email=markus@barta.com", "commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	path := scenario.path
	if path == "" {
		path = "README.txt"
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0700); err != nil {
		t.Fatal(err)
	}
	write(path, "changed\n")
	git("add", path)
	git("-c", "user.name=Markus Barta", "-c", "user.email=markus@barta.com", "commit", "-m", "change")
	// main deliberately points at the current content, not the PR base.
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	body, err := os.ReadFile("../ci-web-tests.sh")
	if err != nil {
		t.Fatal(err)
	}
	write("scripts/ci-web-tests.sh", string(body))
	body, err = os.ReadFile("../ci-web-exit-guard.cjs")
	if err != nil {
		t.Fatal(err)
	}
	write("scripts/ci-web-exit-guard.cjs", string(body))
	body, err = os.ReadFile("../ci-web-seal.py")
	if err != nil {
		t.Fatal(err)
	}
	write("scripts/ci-web-seal.py", string(body))
	write("web/package-lock.json", "{}")
	launcher := `const { spawnSync } = require('node:child_process');
const result = spawnSync(process.env.STUB, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) throw result.error;
process.exit(result.status === null ? 1 : result.status);
`
	write("web/node_modules/@playwright/test/cli.js", launcher)
	manifest, err := json.Marshal(map[string]string{"node_modules/@playwright/test/cli.js": fmt.Sprintf("%x", sha256.Sum256([]byte(launcher)))})
	if err != nil {
		t.Fatal(err)
	}
	forgedSource := scenario.forgedSource
	if forgedSource == "" {
		forgedSource = "tests"
	}
	forgedScript := "web/" + forgedSource + "/forged.cjs"
	write(forgedScript, `
'use strict';
if (process.argv[2] === 'list') console.log('Total: 1 test in 1 file');
else console.log('{"stats":{"expected":1,"skipped":0,"unexpected":0,"flaky":0},"errors":[]}');
if (process.argv[3].endsWith('-cleared-stack')) {
  Error.prepareStackTrace = () => 'no repository frames';
  Error.stackTraceLimit = 0;
}
if (process.argv[3].endsWith('-replace-reallyExit')) process.reallyExit = () => {};
if (process.argv[3].includes('-eval-') || process.argv[3].includes('-function-')) {
  const sourceURL = process.argv[3].endsWith('-outside') ? '/tmp/fake-runner.cjs' : process.cwd() + '/node_modules/@playwright/test/cli.js';
  const code = 'setImmediate(() => process.exit(0));\n//# sourceURL=' + sourceURL;
  if (process.argv[3].includes('-function-')) new Function(code)();
  else eval(code);
  return;
}
if (process.argv[3].endsWith('-kill')) process.kill(process.pid, 'SIGTERM');
else if (process.argv[3].endsWith('-reallyExit')) process.reallyExit(0);
else process.exit(0);
`)
	write("scripts/shrink.cjs", `
const fs = require('node:fs');
const cp = require('node:child_process');
const index = '../.git/index';
const originalIndex = fs.readFileSync(index);
const originalSource = fs.readFileSync('src/fixture.vue');
process.on('exit', () => {
  fs.writeFileSync(index, originalIndex);
  fs.writeFileSync('src/fixture.vue', originalSource);
});
cp.execFileSync('git', ['read-tree', process.env.PR_BASE_SHA]);
fs.writeFileSync('src/fixture.vue', 'base\n');
// Mutable diff selection sees only a new passing spec, then restores on exit.
fs.writeFileSync('tests/passing.spec.ts', 'passing\n');
cp.execFileSync('git', ['add', 'tests/passing.spec.ts']);
const mutableSelection = process.argv.some(arg => arg.startsWith('--only-changed='));
if (process.argv.includes('--list')) console.log('Total: ' + (mutableSelection ? '1 test in 1 file' : '2 tests in 2 files'));
else console.log('{"stats":{"expected":1,"skipped":0,"unexpected":0,"flaky":0},"errors":[]}');
`)
	stub := `#!/bin/bash
printf 'CALL\n' >> "$INVOCATION_LOG"
printf '%s\n' "$@" >> "$INVOCATION_LOG"
phase=run
if [[ " $* " == *" --list "* ]]; then phase=list; fi
case "$MUTATION" in
  "$phase-ref") git update-ref refs/remotes/origin/main "$PR_BASE_SHA" ;;
  "$phase-path") printf 'changed\n' > src/other.vue; git add src/other.vue ;;
  "$phase-exit") exit 0 ;;
  "$phase-forged-"*) exec node --require "$GUARD" "$FORGED_SCRIPT" "$phase" "$MUTATION" ;;
  "$phase-snapshot")
    git read-tree "$PR_BASE_SHA"
    git show "$PR_BASE_SHA:web/src/fixture.vue" > src/fixture.vue
    git diff --no-renames --name-only -z "$PR_BASE_SHA" -- > "$RUNNER_TEMP/aeon-web-selection/paths"
    ;;
  "$phase-guard") printf 'process.exit(0);\n' > ../scripts/ci-web-exit-guard.cjs ;;
  "$phase-copy-guard") printf 'process.exit(0);\n' > "$RUNNER_TEMP/aeon-web-selection/guard.cjs" ;;
  "$phase-launcher")
    printf 'console.log(JSON.stringify({stats:{expected:1,skipped:0,unexpected:0,flaky:0},errors:[]}));\n' > node_modules/@playwright/test/cli.js
    ;;
  "$phase-restore")
    node ../scripts/shrink.cjs "$@"
    exit 0
    ;;
  "$phase-rewrite-only") printf '' > "$RUNNER_TEMP/aeon-web-selection/paths" ;;
esac
if [ "$phase" = list ]; then
  if [ "$FAIL_LIST" = 1 ]; then exit 17; fi
  printf '%s\n' "$LIST_OUTPUT"
else
  if [ "$FAIL_RUN" = 1 ]; then exit 18; fi
  echo '{"stats":{"expected":1,"skipped":0,"unexpected":0,"flaky":0},"errors":[]}'
fi
`
	if err := os.WriteFile(filepath.Join(dir, "bin/npx"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	list := scenario.list
	if list == "" {
		list = "Total: 1 test in 1 file"
	}
	baseInput := base
	if scenario.base != "" {
		baseInput = scenario.base
	}
	flag := func(value bool) string {
		if value {
			return "1"
		}
		return "0"
	}
	log := filepath.Join(dir, "calls")
	summary := filepath.Join(dir, "summary")
	outputs := filepath.Join(dir, "outputs")
	vars := append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"),
		"GUARD="+filepath.Join(dir, "temp/aeon-web-selection/guard.cjs"),
		"PLAYWRIGHT_MANIFEST="+string(manifest), "STUB="+filepath.Join(dir, "bin/npx"),
		"GITHUB_EVENT_NAME="+scenario.event, "GITHUB_STEP_SUMMARY="+summary,
		"GITHUB_OUTPUT="+outputs, "FORGED_SCRIPT="+filepath.Join(dir, forgedScript),
		"RUNNER_TEMP="+filepath.Join(dir, "temp"), "PR_BASE_SHA="+baseInput,
		"INVOCATION_LOG="+log, "FAIL_LIST="+flag(scenario.failList),
		"FAIL_RUN="+flag(scenario.failRun), "LIST_OUTPUT="+list, "MUTATION="+scenario.mutation)
	snapshot, run := webSelection(t)
	cmd := exec.Command("bash", "-c", snapshot)
	cmd.Dir, cmd.Env = dir, vars
	out, err := cmd.CombinedOutput()
	if err == nil {
		// Simulate the runner sealing the completed step outputs. Subsequent
		// writes to GITHUB_OUTPUT or scratch files cannot change these inputs.
		body, readErr := os.ReadFile(outputs)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			key, value, found := strings.Cut(line, "=")
			if !found {
				t.Fatalf("invalid step output %q", line)
			}
			vars = append(vars, "SNAPSHOT_"+strings.ToUpper(key)+"="+value)
		}
		if scenario.mutation == "before-script" || scenario.mutation == "before-both" {
			write("scripts/ci-web-tests.sh", "exit 0\n")
		}
		if scenario.mutation == "before-guard" || scenario.mutation == "before-both" {
			write("scripts/ci-web-exit-guard.cjs", "process.exit(0);\n")
		}
		if scenario.mutation == "before-launcher" {
			write("web/node_modules/@playwright/test/cli.js", "console.log('Total: 1 test in 1 file');\n")
		}
		if strings.HasPrefix(scenario.mutation, "before-") {
			write("temp/aeon-web-selection/paths", "")
			write("temp/aeon-web-selection/base", base)
			write("temp/aeon-web-selection/refs", git("show-ref", "--head"))
			write("temp/aeon-web-selection/event", scenario.event+"\n")
			write("temp/aeon-web-selection/require_tests", "0\n")
			write("outputs", "require_tests=0\npaths_hash=forged\n")
			if scenario.mutation == "before-snapshot" {
				git("read-tree", base)
				write("web/src/fixture.vue", "base\n")
			}
		}
		cmd = exec.Command("bash", "-c", run)
		cmd.Dir, cmd.Env = filepath.Join(dir, "web"), vars
		var next []byte
		next, err = cmd.CombinedOutput()
		out = append(out, next...)
	}
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(body)
	}
	return webResult{calls: read(log), summary: read(summary), output: string(out), base: base, err: err}
}

func TestWebSelectionByEvent(t *testing.T) {
	for _, event := range []string{"pull_request", "push", "merge_group", "workflow_dispatch"} {
		t.Run(event, func(t *testing.T) {
			got := runWebSelection(t, webScenario{event: event, path: "web/tests/theme.spec.ts"})
			if got.err != nil || strings.Count(got.calls, "CALL\n") != 2 {
				t.Fatalf("must list then run the same selection: %v, %s, %s", got.err, got.calls, got.output)
			}
			for _, flag := range []string{"playwright.ui.config.ts", "--workers=2", "--retries=0"} {
				if strings.Count(got.calls, flag+"\n") != 2 {
					t.Fatalf("list and run must retain %s: %s", flag, got.calls)
				}
			}
			if strings.Contains(got.calls, "--pass-with-no-tests") || strings.Contains(got.calls, "--only-changed") {
				t.Fatal("must not allow empty suites or mutable diff selection")
			}
			if event == "pull_request" {
				if strings.Count(got.calls, "/web/tests/theme\\.spec\\.ts$\n") != 2 || strings.Contains(got.calls, "existing") {
					t.Fatalf("spec-only PR must pass the sealed literal file to both invocations: %s", got.calls)
				}
			} else if strings.Contains(got.calls, "tests/") {
				t.Fatal("non-PR coverage, including merge_group, must run the full suite")
			}
			if !strings.Contains(got.summary, "Total: 1 test in 1 file") {
				t.Fatal("selected test totals must be visible")
			}
		})
	}
}

func TestWebSelectionEmptyPRWithoutUIChanges(t *testing.T) {
	got := runWebSelection(t, webScenario{event: "pull_request", list: "Total: 0 tests in 0 files"})
	if got.err != nil || got.calls != "" || !strings.Contains(got.summary, "empty selection accepted") {
		t.Fatalf("only non-UI PRs may accept an empty selection: %+v", got)
	}
}

func TestWebSelectionRejectsEmptyUIAndFullSuite(t *testing.T) {
	for _, scenario := range []webScenario{
		{event: "pull_request", path: "web/src/fixture.vue"},
		{event: "pull_request", path: "web/tests/new.spec.ts"},
		{event: "pull_request", path: "web/playwright.ui.config.ts"},
		{event: "pull_request", path: "other/new.spec.ts"},
		{event: "push"}, {event: "merge_group"}, {event: "workflow_dispatch"},
	} {
		t.Run(scenario.event+"/"+scenario.path, func(t *testing.T) {
			scenario.list = "Total: 0 tests in 0 files"
			got := runWebSelection(t, scenario)
			if got.err == nil || strings.Count(got.calls, "CALL\n") != 1 || !strings.Contains(got.output, "require a non-empty test selection") {
				t.Fatalf("empty coverage must fail before running: %+v", got)
			}
		})
	}
}

func TestWebSelectionInvalidInputsFailBeforePlaywright(t *testing.T) {
	for _, base := range []string{"origin/main", "$(exit 0)", strings.Repeat("0", 40)} {
		t.Run(base, func(t *testing.T) {
			got := runWebSelection(t, webScenario{event: "pull_request", base: base})
			if got.err == nil || got.calls != "" {
				t.Fatalf("invalid or missing base must fail before loading specs: %+v", got)
			}
		})
	}
}

func TestWebSelectionRejectsMissingOrInvalidTotals(t *testing.T) {
	for _, list := range []string{"Listing tests:", "Total: 0", "Total: 1 test in 1 file\nTotal: 0 tests in 0 files"} {
		t.Run(list, func(t *testing.T) {
			got := runWebSelection(t, webScenario{event: "pull_request", path: "web/tests/new.spec.ts", list: list})
			if got.err == nil || strings.Count(got.calls, "CALL\n") != 1 || !strings.Contains(got.output, "invalid Playwright list total") {
				t.Fatalf("a real unambiguous list total is required even for non-UI PRs: %+v", got)
			}
		})
	}
}

func TestWebSelectionStopsOnLoadExitAndGitTampering(t *testing.T) {
	for _, mutation := range []string{"list-exit", "run-exit", "list-ref", "run-ref", "list-path", "run-path"} {
		t.Run(mutation, func(t *testing.T) {
			got := runWebSelection(t, webScenario{event: "pull_request", path: "web/tests/new.spec.ts", mutation: mutation})
			wantCalls := 1
			if strings.HasPrefix(mutation, "run-") {
				wantCalls = 2
			}
			if got.err == nil || strings.Count(got.calls, "CALL\n") != wantCalls {
				t.Fatalf("early exit or mutation must not silently pass: %+v", got)
			}
		})
	}
}

func TestWebSelectionRejectsSnapshotRewrite(t *testing.T) {
	for _, mutation := range []string{"before-records", "before-snapshot", "list-snapshot", "run-snapshot"} {
		t.Run(mutation, func(t *testing.T) {
			scenario := webScenario{event: "pull_request", path: "web/src/fixture.vue", mutation: mutation}
			wantCalls, want := 1, "Changed paths changed"
			switch mutation {
			case "before-records":
				scenario.list = "Total: 0 tests in 0 files"
				want = "require a non-empty test selection"
			case "before-snapshot":
				wantCalls = 0
			case "run-snapshot":
				wantCalls = 2
			}
			got := runWebSelection(t, scenario)
			if got.err == nil || strings.Count(got.calls, "CALL\n") != wantCalls || !strings.Contains(got.output, want) {
				t.Fatalf("rewriting scratch data must not forge trusted coverage or paths: %+v", got)
			}
		})
	}
	for _, mutation := range []string{"list-rewrite-only", "run-rewrite-only"} {
		got := runWebSelection(t, webScenario{event: "pull_request", path: "web/src/fixture.vue", mutation: mutation})
		if got.err != nil {
			t.Fatalf("scratch path files must not be used as trusted run inputs: %+v", got)
		}
	}
}

func TestWebSelectionRejectsForgedCompletion(t *testing.T) {
	for _, source := range []string{"tests", "src", ".", "../scripts"} {
		for _, phase := range []string{"list", "run"} {
			for _, operation := range []string{"exit", "kill", "reallyExit", "cleared-stack", "replace-reallyExit", "eval-outside", "eval-node_modules", "function-outside", "function-node_modules"} {
				t.Run(source+"/"+phase+"/"+operation, func(t *testing.T) {
					got := runWebSelection(t, webScenario{event: "pull_request", path: "web/tests/new.spec.ts",
						mutation: phase + "-forged-" + operation, forgedSource: source})
					wantCalls := 1
					if phase == "run" {
						wantCalls = 2
					}
					diagnostic := operation
					if operation == "cleared-stack" {
						diagnostic = "exit"
					}
					if strings.HasPrefix(operation, "eval-") || strings.HasPrefix(operation, "function-") {
						diagnostic = "exit"
					}
					if operation == "replace-reallyExit" {
						diagnostic = "reallyExit"
					}
					if got.err == nil || strings.Count(got.calls, "CALL\n") != wantCalls || !strings.Contains(got.output, "UI check rejected process."+diagnostic) {
						t.Fatalf("forged list/JSON plus process termination must fail: %+v", got)
					}
				})
			}
		}
	}
}

func TestWebExitGuardLocksFunctionsAndAllowsRunnerShutdown(t *testing.T) {
	hook, err := filepath.Abs("../ci-web-exit-guard.cjs")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "web/node_modules/@playwright/test/cli.js")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	source := `
const assert = require('node:assert/strict');
for (const name of ['exit', 'kill', 'reallyExit']) {
  const descriptor = Object.getOwnPropertyDescriptor(process, name);
  if (name === 'reallyExit') {
    assert.equal(typeof descriptor.get, 'function');
    assert.equal(typeof descriptor.set, 'function');
  } else assert.equal(descriptor.writable, false);
  assert.equal(descriptor.configurable, false);
  assert.throws(() => Object.defineProperty(process, name, { value: () => {} }));
}
// Dependency lifecycle replacement/restoration must remain guarded and work.
const original = process.reallyExit;
process.reallyExit = function(code) { return original(code); };
process.reallyExit = original;
assert.equal(process.kill(process.pid, 0), true);
process.exit(0);
`
	if err := os.WriteFile(launcher, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]string{"node_modules/@playwright/test/cli.js": fmt.Sprintf("%x", sha256.Sum256([]byte(source)))})
	cmd := exec.Command("node", "--require", hook, launcher)
	cmd.Env = append(os.Environ(), "AEON_CI_REPO_ROOT="+dir, "PLAYWRIGHT_MANIFEST="+string(manifest))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guard must lock replacements while allowing runner shutdown: %v, %s", err, out)
	}
}

func TestWebSelectionRejectsPlaywrightLauncherReplacement(t *testing.T) {
	for _, phase := range []string{"before", "list", "run"} {
		t.Run(phase, func(t *testing.T) {
			got := runWebSelection(t, webScenario{event: "pull_request", path: "web/tests/new.spec.ts", mutation: phase + "-launcher"})
			calls := map[string]int{"before": 0, "list": 1, "run": 2}[phase]
			if got.err == nil || strings.Count(got.calls, "CALL\n") != calls ||
				!(strings.Contains(got.output, "Playwright code changed after seal") || strings.Contains(got.output, "UI check rejected process.exit")) {
				t.Fatalf("returning forged JSON from a replaced launcher must fail: %+v", got)
			}
		})
	}
}

func TestWebExitGuardChecksExactCompilerSource(t *testing.T) {
	hook, err := filepath.Abs("../ci-web-exit-guard.cjs")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "web/node_modules/@playwright/test/cli.js")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	source := `const Module = require('node:module');
	const loaded = new Module(__filename);
	loaded._compile("console.log('forged report');", __filename);
`
	if err := os.WriteFile(launcher, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]string{"node_modules/@playwright/test/cli.js": fmt.Sprintf("%x", sha256.Sum256([]byte(source)))})
	cmd := exec.Command("node", "--require", hook, launcher)
	cmd.Env = append(os.Environ(), "AEON_CI_REPO_ROOT="+dir, "PLAYWRIGHT_MANIFEST="+string(manifest))
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "changed Playwright compiler input") || strings.Contains(string(out), "forged report") {
		t.Fatalf("unchanged path bytes must not authorize a different compiler input: %v, %s", err, out)
	}
}

func TestWebPlaywrightTarballSeal(t *testing.T) {
	cmd := exec.Command("python3", "-I", "../ci-web-seal.test.py")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lockfile archive seal: %v, %s", err, out)
	}
}

func TestWebSelectionPropagatesPlaywrightFailures(t *testing.T) {
	for _, stage := range []string{"list", "run"} {
		t.Run(stage, func(t *testing.T) {
			got := runWebSelection(t, webScenario{event: "pull_request", path: "web/tests/new.spec.ts", failList: stage == "list", failRun: stage == "run"})
			wantCalls := 2
			if stage == "list" {
				wantCalls = 1
			}
			if got.err == nil || strings.Count(got.calls, "CALL\n") != wantCalls {
				t.Fatalf("Playwright failure must fail the check: %+v", got)
			}
		})
	}
}

func TestWebSelectionRejectsCIFileReplacement(t *testing.T) {
	for _, mutation := range []string{"before-script", "before-guard", "before-both", "list-guard", "list-copy-guard", "run-guard", "run-copy-guard"} {
		t.Run(mutation, func(t *testing.T) {
			got := runWebSelection(t, webScenario{event: "pull_request", path: "web/tests/new.spec.ts", mutation: mutation})
			calls := 0
			if strings.HasPrefix(mutation, "list-") {
				calls = 1
			}
			if strings.HasPrefix(mutation, "run-") {
				calls = 2
			}
			if got.err == nil || strings.Count(got.calls, "CALL\n") != calls || !strings.Contains(got.output, "changed after snapshot") {
				t.Fatalf("replaced verifier or guard must fail before the next invocation: %+v", got)
			}
		})
	}
}

func TestWebSelectionRejectsDiffShrinkRestoredOnExit(t *testing.T) {
	got := runWebSelection(t, webScenario{event: "pull_request", path: "web/src/fixture.vue", mutation: "list-restore"})
	if got.err == nil || strings.Contains(got.calls, "--only-changed") || !strings.Contains(got.calls, "existing\\.spec\\.ts$") ||
		!strings.Contains(got.output, "Playwright did not complete") || strings.Contains(got.output, "Changed paths changed") {
		t.Fatalf("restored Git inputs must not shrink sealed selection or forge completed coverage: %+v", got)
	}
}

func TestWebSelectionSharedInputsRunAllSpecs(t *testing.T) {
	for _, path := range []string{"web/src/fixture.vue", "web/tests/shared-fixture.ts", "web/playwright.ui.config.ts", "scripts/ci-web-tests.sh", ".github/workflows/ci.yml"} {
		got := runWebSelection(t, webScenario{event: "pull_request", path: path})
		if got.err != nil || strings.Count(got.calls, "/web/tests/existing\\.spec\\.ts$\n") != 2 {
			t.Fatalf("shared inputs must select the complete UI tree: %+v", got)
		}
	}
}
