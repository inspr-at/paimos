// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestQueueCorrelationPreservesEvidenceAndReadOnlyPublication(t *testing.T) {
	// Risk: a guessed queue cause becomes tuning evidence, or daily reporting
	// grants queue/check write authority. Node cases replay the real W2 window.
	w := treeWorkflow(t, "queue-correlation.yml")
	if !reflect.DeepEqual(treeMap(w["permissions"]), map[string]any{"contents": "read", "actions": "read", "pull-requests": "read"}) {
		t.Fatal("queue reporter permissions must remain read-only")
	}
	triggers := treeMap(w["on"])
	if len(triggers) != 2 || triggers["schedule"] == nil || triggers["workflow_dispatch"] == nil {
		t.Fatal("reporter must publish daily outside PR/merge-group checks")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--test", "scripts/ci-queue-correlate.test.mjs")
	cmd.Dir = root(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("queue correlation evidence: %v\n%s", err, output)
	}
	if binary, err := exec.LookPath("actionlint"); err == nil {
		cmd = exec.CommandContext(ctx, binary, ".github/workflows/queue-correlation.yml")
		cmd.Dir = root(t)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("queue reporter actionlint: %v\n%s", err, output)
		}
	}
}
