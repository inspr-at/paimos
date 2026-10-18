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
