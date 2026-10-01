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

func webSelection(t *testing.T) map[string]any {
	t.Helper()
	body, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := mapping(mapping(workflow["jobs"])["web"])["steps"].([]any)
	checkout := mapping(steps[0])
	with := mapping(checkout["with"])
	if with["fetch-depth"] != 0 || with["persist-credentials"] != false {
		t.Fatal("PR selection requires full history without persisted credentials")
	}
	for _, value := range steps {
		step := mapping(value)
		if run, _ := step["run"].(string); strings.Contains(run, "--only-changed=origin/main") {
			if step["working-directory"] != "web" || step["if"] != nil {
				t.Fatal("selection must run in web on every CI event")
			}
			return step
		}
	}
	t.Fatal("missing PR UI selection step")
	return nil
}

// Execute the real workflow shell with an inert Playwright stub: no browsers,
// server or credentials are needed to prove event routing and failure handling.
func runWebSelection(t *testing.T, event string, failList bool) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	summary := filepath.Join(dir, "summary")
	stub := `#!/bin/bash
printf 'CALL\n' >> "$INVOCATION_LOG"
printf '%s\n' "$@" >> "$INVOCATION_LOG"
if [[ " $* " == *" --list "* ]]; then
  if [ "$FAIL_LIST" = 1 ]; then exit 17; fi
  echo 'Total: 0 tests in 0 files'
fi
`
	if err := os.WriteFile(filepath.Join(dir, "npx"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	fail := "0"
	if failList {
		fail = "1"
	}
	cmd := exec.Command("bash", "-c", webSelection(t)["run"].(string))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"),
		"GITHUB_EVENT_NAME="+event, "GITHUB_STEP_SUMMARY="+summary,
		"INVOCATION_LOG="+log, "FAIL_LIST="+fail)
	err := cmd.Run()
	calls, readErr := os.ReadFile(log)
	if readErr != nil {
		t.Fatal(readErr)
	}
	text, readErr := os.ReadFile(summary)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(calls), string(text), err
}

func TestWebSelectionByEvent(t *testing.T) {
	for _, event := range []string{"pull_request", "push", "merge_group", "workflow_dispatch"} {
		t.Run(event, func(t *testing.T) {
			calls, summary, err := runWebSelection(t, event, false)
			if err != nil || strings.Count(calls, "CALL\n") != 2 {
				t.Fatalf("must list then run the same selection: %v, %s", err, calls)
			}
			for _, flag := range []string{"playwright.ui.config.ts", "--workers=2", "--retries=0"} {
				if strings.Count(calls, flag+"\n") != 2 {
					t.Fatalf("list and run must retain %s: %s", flag, calls)
				}
			}
			if strings.Contains(calls, "--pass-with-no-tests") {
				t.Fatal("must not hide an empty ordinary regression suite")
			}
			if event == "pull_request" {
				if strings.Count(calls, "--only-changed=origin/main\n") != 2 || strings.Contains(calls, "tests/attach-") {
					t.Fatal("PR must select from every UI spec, not intersect with the legacy regressions")
				}
			} else {
				if strings.Contains(calls, "--only-changed") {
					t.Fatal("non-PR coverage must not be filtered")
				}
				for _, spec := range []string{"attach-discoverable", "attach-watch", "attach-security"} {
					if strings.Count(calls, "tests/"+spec+".spec.ts\n") != 2 {
						t.Fatalf("lost existing regression %s", spec)
					}
				}
			}
			if !strings.Contains(summary, "Total: 0 tests in 0 files") {
				t.Fatal("selected tests, including a legitimate empty PR selection, must be visible")
			}
		})
	}
}

func TestWebSelectionLoadFailureStopsTheRun(t *testing.T) {
	calls, _, err := runWebSelection(t, "pull_request", true)
	if err == nil || strings.Count(calls, "CALL\n") != 1 {
		t.Fatalf("a load or missing-base error must fail before running tests: %v, %s", err, calls)
	}
}
