// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"os/exec"
	"testing"
)

// AEON-933: hosted web shards failed because an unprivileged timeout left root
// apt-get holding the lock. The node regression fails on 5d155476, whose
// workflow commands are that unprivileged retry.
func TestRootAptTimeoutReapsBeforeRetry(t *testing.T) {
	cmd := exec.Command("node", "--test", "scripts/check-apt-bounds.test.mjs")
	cmd.Dir = root(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apt retry regression: %v\n%s", err, out)
	}
}

// AEON-1064: exercise real test-worker interruption and parent death; the
// fixture-only regression uses Node helpers and never launches a browser.
func TestInterruptedFixturesLeaveNoProcesses(t *testing.T) {
	cmd := exec.Command("node", "--test", "scripts/fixture-lifecycle.test.mjs")
	cmd.Dir = root(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture lifecycle regression: %v\n%s", err, out)
	}
}
