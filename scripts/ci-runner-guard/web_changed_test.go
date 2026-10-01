// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
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
	for index, value := range steps {
		step := mapping(value)
		command, _ := step["run"].(string)
		if command == "bash scripts/ci-web-tests.sh snapshot" {
			if index != 1 || step["if"] != nil || mapping(step["env"])["PR_BASE_SHA"] != "${{ github.event.pull_request.base.sha }}" {
				t.Fatal("snapshot must bind the event base immediately after checkout, before Node")
			}
			snapshot = command
		}
		if command == "bash ../scripts/ci-web-tests.sh run" {
			if step["working-directory"] != "web" || step["if"] != nil {
				t.Fatal("selection must run in web on every CI event")
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
	for _, path := range []string{"web/src", "scripts", "bin", "temp"} {
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
	git("add", "web/src/fixture.vue")
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
	stub := `#!/bin/bash
printf 'CALL\n' >> "$INVOCATION_LOG"
printf '%s\n' "$@" >> "$INVOCATION_LOG"
phase=run
if [[ " $* " == *" --list "* ]]; then phase=list; fi
case "$MUTATION" in
  "$phase-ref") git update-ref refs/remotes/origin/main "$PR_BASE_SHA" ;;
  "$phase-path") printf 'changed\n' > src/other.vue; git add src/other.vue ;;
  "$phase-exit") exit 0 ;;
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
	vars := append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"),
		"GITHUB_EVENT_NAME="+scenario.event, "GITHUB_STEP_SUMMARY="+summary,
		"RUNNER_TEMP="+filepath.Join(dir, "temp"), "PR_BASE_SHA="+baseInput,
		"INVOCATION_LOG="+log, "FAIL_LIST="+flag(scenario.failList),
		"FAIL_RUN="+flag(scenario.failRun), "LIST_OUTPUT="+list, "MUTATION="+scenario.mutation)
	snapshot, run := webSelection(t)
	cmd := exec.Command("bash", "-c", snapshot)
	cmd.Dir, cmd.Env = dir, vars
	out, err := cmd.CombinedOutput()
	if err == nil {
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
			if strings.Contains(got.calls, "--pass-with-no-tests") || strings.Contains(got.calls, "tests/") {
				t.Fatal("must not allow empty suites or restrict the full spec tree")
			}
			if event == "pull_request" {
				if strings.Count(got.calls, "--only-changed="+got.base+"\n") != 2 || strings.Contains(got.calls, "origin/main") {
					t.Fatal("PR must use its immutable event base, even when main has matching end content")
				}
			} else if strings.Contains(got.calls, "--only-changed") {
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
	if got.err != nil || strings.Count(got.calls, "CALL\n") != 1 || !strings.Contains(got.summary, "empty selection accepted") {
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
			got := runWebSelection(t, webScenario{event: "pull_request", list: list})
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

func TestWebSelectionPropagatesPlaywrightFailures(t *testing.T) {
	for _, stage := range []string{"list", "run"} {
		t.Run(stage, func(t *testing.T) {
			got := runWebSelection(t, webScenario{event: "pull_request", failList: stage == "list", failRun: stage == "run"})
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
