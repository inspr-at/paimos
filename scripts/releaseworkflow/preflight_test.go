// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestPreflightRoutingPermissionsAndResultBehavior(t *testing.T) {
	// Risk: the additional workflow bypasses the runner or grants write
	// permissions, or a stale/partial local receipt authorizes a PR head.
	w := treeWorkflow(t, "ci-preflight.yml")
	if !reflect.DeepEqual(treeMap(w["permissions"]), map[string]any{"contents": "read", "actions": "read"}) {
		t.Fatal("preflight permissions changed")
	}
	if len(treeMap(w["on"])) != 1 || treeMap(w["on"])["workflow_dispatch"] == nil {
		t.Fatal("preflight must remain an explicit dispatch")
	}
	jobs := treeMap(w["jobs"])
	if treeMap(jobs["runner-route"])["uses"] != "./.github/workflows/test-runner-route.yml" {
		t.Fatal("router bypassed")
	}
	for _, id := range []string{"browser", "result"} {
		j := treeMap(jobs[id])
		condition, _ := j["if"].(string)
		if j["runs-on"] != "${{ fromJSON(needs.runner-route.outputs.runs_on) }}" || !strings.Contains(condition, "outputs.runner_class == 'hosted'") ||
			!strings.Contains(condition, "outputs.run_attempt == github.run_attempt") || j["permissions"] != nil {
			t.Fatal("unapproved runner or authority", id)
		}
		for _, value := range reuseSteps(j) {
			if treeMap(value)["continue-on-error"] != nil {
				t.Fatal("preflight errors ignored")
			}
		}
	}
	cmd := exec.Command("node", "--test", "scripts/ci-preflight.test.mjs")
	cmd.Dir = root(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("preflight behavior regressions: %v\n%s", err, output)
	}
	if binary, err := exec.LookPath("actionlint"); err == nil {
		cmd = exec.Command(binary, ".github/workflows/ci-preflight.yml")
		cmd.Dir = root(t)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("preflight actionlint: %v\n%s", err, output)
		}
	}
}
