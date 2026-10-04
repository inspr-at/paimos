// SPDX-License-Identifier: AGPL-3.0-only
package qa648fix6

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Temporary adapter for the coordinator's HEAD-only Go remote test transport.
// Removed after collecting evidence; browser work stays on its approved lane.
func TestWebValidation(t *testing.T) {
	web, err := filepath.Abs("../web")
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "npm", args...)
		cmd.Dir = web
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("npm %v: %v", args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(web, "node_modules")); os.IsNotExist(err) {
		run("ci", "--no-audit", "--no-fund")
	} else if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"build", "lint", "test:unit"} {
		t.Run(script, func(t *testing.T) {
			run("run", script)
		})
	}
}
