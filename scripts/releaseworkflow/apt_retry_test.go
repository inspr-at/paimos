// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"os/exec"
	"runtime"
	"testing"
)

// AEON-933: hosted web shards failed because an unprivileged timeout left root
// apt-get holding the lock. The node regression fails on 5d155476, whose
// workflow commands are that unprivileged retry.
// AEON-1652: also covers slow fetch timeouts (124/137), dpkg activity that
// forbids retry, and the shared budget including SIGKILL grace.
func TestRootAptTimeoutReapsBeforeRetry(t *testing.T) {
	program := "node"
	args := []string{"--test", "scripts/check-apt-bounds.test.mjs"}
	if runtime.GOOS == "linux" {
		args = append([]string{"scripts/testdata/fixture-reaper.py", program}, args...)
		program = "python3"
	}
	cmd := exec.Command(program, args...)
	cmd.Dir = root(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apt retry regression: %v\n%s", err, out)
	}
}

// AEON-1064: exercise real test-worker interruption and parent death; the
// fixture-only regression uses Node helpers and never launches a browser.
func TestInterruptedFixturesLeaveNoProcesses(t *testing.T) {
	program := "node"
	args := []string{"--test", "--test-concurrency=1", "--test-name-pattern=AEON-1064|stale|recovery|owned descendants|reused journal|detached|unverifiable live roots", "scripts/fixture-lifecycle.test.mjs", "scripts/playwright-safe.test.mjs"}
	if runtime.GOOS == "linux" {
		args = append([]string{"scripts/testdata/fixture-reaper.py", program}, args...)
		program = "python3"
	}
	cmd := exec.Command(program, args...)
	cmd.Dir = root(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture lifecycle regression: %v\n%s", err, out)
	}
}
