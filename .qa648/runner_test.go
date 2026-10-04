// SPDX-License-Identifier: AGPL-3.0-only
package qa648

import (
	"os"
	"os/exec"
	"testing"
)

func npmReady(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("../web/node_modules"); err == nil {
		return
	}
	run(t, "npm", "ci", "--no-audit", "--no-fund")
}
func run(t *testing.T, command string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), command, args...)
	cmd.Dir = "../web"
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Errorf("%s %v: %v", command, args, err)
	}
}
func TestWebValidation(t *testing.T) {
	npmReady(t)
	run(t, "npm", "run", "build")
	run(t, "npm", "run", "lint")
	run(t, "npm", "run", "test:unit")
}
