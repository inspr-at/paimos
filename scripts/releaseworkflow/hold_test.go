// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseHoldBeforeTagAndRolloutSideEffects(t *testing.T) {
	w := readWorkflow(t, "release.yml")
	for id, firstWrite := range map[string]string{
		"agentd-darwin":  "Sign and notarize paimos-agentd",
		"image-platform": "Build and push", "image": "Publish multi-arch index", "assets": "Create draft GitHub release with signed assets",
	} {
		j := w.Jobs[id]
		guardIndex, guard := named(t, j, "Require release hold clear")
		writeIndex, _ := named(t, j, firstWrite)
		if guardIndex >= writeIndex || guard.If != "" || guard.ContinueOnError ||
			guard.Run != "node scripts/release-hold.mjs check" || guard.Env["AEON_RELEASE_HOLD_READ_TOKEN"] != "${{ secrets.AEON_RELEASE_HOLD_READ_TOKEN }}" {
			t.Fatalf("%s can bypass the live hold guard", id)
		}
		if id != "agentd-darwin" {
			i, recheck := named(t, j, "Recheck release hold before publication")
			if i != writeIndex-1 || recheck.If != "" || recheck.ContinueOnError || recheck.Run != guard.Run {
				t.Fatalf("%s must recheck immediately before publication", id)
			}
		}
	}
	_, pin := named(t, w.Jobs["image"], "Propose verified nixcfg deployment pin")
	if pin.Env["AEON_RELEASE_HOLD_READ_TOKEN"] != "${{ secrets.AEON_RELEASE_HOLD_READ_TOKEN }}" {
		t.Fatal("pin proposal must use the dedicated read-only hold credential")
	}
}

func TestReleaseHoldWriterUsesTrustedCodeOnly(t *testing.T) {
	w := readWorkflow(t, "release-hold.yml")
	if len(w.On) != 3 || w.On["workflow_run"] == nil || w.On["schedule"] == nil || w.On["workflow_dispatch"] != nil {
		t.Fatal("hold writer needs completion, repair and manual triggers only")
	}
	j := w.Jobs["reconcile"]
	for _, binding := range []string{"github.repository == 'inspr-at/paimos'", "github.ref == 'refs/heads/main'", "workflow_run.event == 'push'", "workflow_run.head_repository.full_name == github.repository"} {
		if !strings.Contains(j.If, binding) {
			t.Fatalf("writer missing %s", binding)
		}
	}
	if j.RunsOn != "ubuntu-latest" || len(j.Steps) != 3 || j.Steps[0].With["ref"] != "${{ github.workflow_sha }}" ||
		j.Steps[0].With["persist-credentials"] != "false" || j.Steps[1].With["cache"] != "" {
		t.Fatal("writer must execute only trusted workflow-revision code without shared caches")
	}
	s := j.Steps[2]
	if s.Run != "node scripts/release-hold.mjs reconcile --write" || s.Env["AEON_RELEASE_HOLD_TOKEN"] != "${{ secrets.AEON_RELEASE_HOLD_TOKEN }}" || s.ContinueOnError {
		t.Fatal("writer token or failure propagation changed")
	}
}

func TestHoldStateAndLaneFixtures(t *testing.T) {
	cmd := exec.Command("node", "--test", "scripts/release-hold.test.mjs", "scripts/ci-lane.test.mjs", "scripts/release-lane.test.mjs", "scripts/create-release-tag.test.mjs", "scripts/release-pin-pr.test.mjs")
	cmd.Dir = root(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hold/lane/tag/rollout fixtures: %v\n%s", err, output)
	}
}

func TestMainRunsCompleteUIWhileQueueRemainsBounded(t *testing.T) {
	w := readWorkflow(t, "ci.yml")
	_, full := named(t, w.Jobs["web"], "Complete UI suite on main")
	if full.If != `contains(fromJSON('["push","workflow_dispatch"]'), github.event_name) && github.ref == 'refs/heads/main'` ||
		full.Run != "node ../scripts/ci-lane.mjs playwright -- npx playwright test -c playwright.ui.config.ts --workers=2 --retries=0" {
		t.Fatal("main must run the complete UI suite, without changed-file filters or retries")
	}
	for _, id := range []string{"web", "e2e", "footer-ui", "status-help-ui", "status-autopilot-ui"} {
		j := w.Jobs[id]
		if j.Steps[0].With["fetch-depth"] != "0" {
			t.Fatalf("%s lacks immutable queue base history", id)
		}
		for _, s := range j.Steps {
			if (strings.Contains(s.Run, "npx playwright test") || strings.Contains(s.Run, "npm test --") || strings.Contains(s.Run, "npm run e2e")) &&
				!strings.Contains(s.Run, "ci-lane.mjs playwright --") {
				t.Fatalf("%s bypasses queue selection", id)
			}
		}
	}
}

func TestPortableRolloutHoldGuard(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("rollout guard fixtures require jq")
	}
	for _, tc := range []struct {
		name, response string
		status         int
		ok             bool
	}{
		{"clear", `{"total_count":0,"variables":[]}`, 0, true},
		{"empty", `{"total_count":1,"variables":[{"name":"RELEASE_HOLD","value":""}]}`, 0, true},
		{"held", `{"total_count":1,"variables":[{"name":"RELEASE_HOLD","value":"` + strings.Repeat("a", 40) + `"}]}`, 0, false},
		{"malformed", `{"total_count":1,"variables":[{"name":"RELEASE_HOLD","value":"bad"}]}`, 0, false},
		{"null-value", `{"total_count":1,"variables":[{"name":"RELEASE_HOLD","value":null}]}`, 0, false},
		{"missing-value", `{"total_count":1,"variables":[{"name":"RELEASE_HOLD"}]}`, 0, false},
		{"null-record", `{"total_count":1,"variables":[null]}`, 0, false},
		{"numeric-value", `{"total_count":1,"variables":[{"name":"RELEASE_HOLD","value":0}]}`, 0, false},
		{"partial", `{"total_count":1,"variables":[]}`, 0, false},
		{"unavailable", `private upstream response`, 22, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// The stub records argv only: a credential must be supplied on stdin.
			stub := `#!/bin/bash
case "$*" in *fixture-only*) exit 99;; esac
cat >/dev/null
printf '%s' "$HOLD_FIXTURE_RESPONSE"
exit "$HOLD_FIXTURE_STATUS"
`
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", filepath.Join(root(t), "scripts/check-release-hold.sh"))
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "AEON_RELEASE_HOLD_READ_TOKEN=fixture-only", "HOLD_FIXTURE_RESPONSE="+tc.response)
			if tc.status == 0 {
				cmd.Env = append(cmd.Env, "HOLD_FIXTURE_STATUS=0")
			} else {
				cmd.Env = append(cmd.Env, "HOLD_FIXTURE_STATUS=22")
			}
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("guard=%v want success %v: %s", err, tc.ok, out)
			}
			if strings.Contains(string(out), "fixture-only") || strings.Contains(string(out), "private upstream response") {
				t.Fatal("guard leaked private input")
			}
		})
	}
}
