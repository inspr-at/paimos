// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// AEON-933: hosted web shards failed because an unprivileged timeout left root
// apt-get holding the lock. The node regression fails on 5d155476, whose
// workflow commands are that unprivileged retry.
// AEON-1652: also covers slow fetch timeouts (124/137), dpkg activity that
// forbids retry, offline bundles, and the shared budget including SIGKILL grace.
func TestRootAptTimeoutReapsBeforeRetry(t *testing.T) {
	checkOfflineAptWorkflow(t)
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

// Risk: a missing artifact must not skip tests, and image rollouts need this
// run's bundle with offline preflights. Check the actual job wiring.
func checkOfflineAptWorkflow(t *testing.T) {
	t.Helper()
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	bundle := treeMap(jobs["apt-bundle"])
	if bundle["runs-on"] != "ubuntu-latest" || bundle["timeout-minutes"] != 25 || bundle["continue-on-error"] != true || bundle["strategy"] != nil {
		t.Fatal("apt-bundle must run once with a generous bounded, optional budget")
	}
	for _, need := range []string{"ci-plan", "tree-reuse"} {
		if !containsNeed(bundle["needs"], need) {
			t.Fatalf("apt-bundle must follow %s", need)
		}
	}
	condition := fmt.Sprint(bundle["if"])
	for _, guard := range []string{"always()", "needs.ci-plan.result == 'success'", "needs.ci-plan.outputs.lane != 'docs-only'", fullQueueFallback, "needs.tree-reuse.outputs.reuse != 'pull_request'"} {
		if !strings.Contains(condition, guard) {
			t.Fatalf("apt-bundle lost planning/reuse guard %s", guard)
		}
	}
	const identity = `artifact=apt-bundle-${ImageOS}-${ImageVersion}-${RUNNER_ARCH}`
	if !strings.Contains(fmt.Sprint(reuseStep(t, bundle, "Identify apt bundle for this runner image")["run"]), identity) {
		t.Fatal("bundle artifact must identify the producer's exact image and architecture")
	}
	fetch := reuseStep(t, bundle, "Download Chromium and shell system packages once")
	if fetch["timeout-minutes"] != 22 || !strings.Contains(fmt.Sprint(fetch["run"]), `require('./web/package-lock.json').packages['node_modules/@playwright/test'].version`) || !strings.Contains(fmt.Sprint(fetch["run"]), `bash "$GITHUB_WORKSPACE/scripts/ci/bounded-apt.sh" bundle "$RUNNER_TEMP/apt-bundle"`) {
		t.Fatal("bundle fetch must use lockfile Playwright and the bounded bundle helper")
	}
	upload := reuseStep(t, bundle, "Share apt bundle for this run")
	if !strings.HasPrefix(fmt.Sprint(upload["uses"]), "actions/upload-artifact@") || treeMap(upload["with"])["name"] != "${{ steps.apt-image.outputs.artifact }}" || treeMap(upload["with"])["path"] != "${{ runner.temp }}/apt-bundle" || treeMap(upload["with"])["if-no-files-found"] != "error" {
		t.Fatal("packages and their indexes must be shared as this run's image-keyed artifact")
	}
	for _, value := range reuseSteps(bundle) {
		if strings.Contains(fmt.Sprint(treeMap(value)["uses"]), "actions/cache") {
			t.Fatal("apt bundle must not depend on the evicting actions cache")
		}
	}
	for id, installName := range map[string]string{
		"go-test":   "Install shells when this shard runs pairing path-proof tests",
		"web-unit":  "Install shells used by command round-trip unit tests",
		"web-shard": "Install Chromium system dependencies",
	} {
		j := treeMap(jobs[id])
		reuseStep(t, j, installName)
		if !containsNeed(j["needs"], "apt-bundle") || strings.Contains(fmt.Sprint(j["if"]), "needs.apt-bundle.result") || !strings.Contains(fmt.Sprint(j["if"]), "always()") {
			t.Fatalf("%s must await the optional producer without requiring artifact success", id)
		}
		identityStep := reuseStep(t, j, "Identify optional apt bundle for this runner image")
		if identityStep["id"] != "apt-image" || !strings.Contains(fmt.Sprint(identityStep["run"]), identity) {
			t.Fatalf("%s must request its own image's bundle", id)
		}
		download := reuseStep(t, j, "Download optional apt bundle")
		if download["continue-on-error"] != true || download["timeout-minutes"] != 2 || !strings.HasPrefix(fmt.Sprint(download["uses"]), "actions/download-artifact@") || treeMap(download["with"])["name"] != nil || treeMap(download["with"])["pattern"] != "apt-bundle-*" || treeMap(download["with"])["merge-multiple"] != false || treeMap(download["with"])["path"] != "${{ runner.temp }}/apt-bundle" {
			t.Fatalf("%s must tolerate missing artifacts and download into the helper's private path", id)
		}
		if id == "go-test" && (!strings.Contains(fmt.Sprint(identityStep["if"]), "runner.os == 'Linux'") || !strings.Contains(fmt.Sprint(download["if"]), "runner.os == 'Linux'")) {
			t.Fatal("self-hosted Mac Go shards must not request an apt bundle")
		}
		seen := false
		for _, value := range reuseSteps(j) {
			s := treeMap(value)
			if s["name"] == "Download optional apt bundle" {
				seen = true
			}
			if s["name"] == installName && (!seen || !strings.Contains(fmt.Sprint(s["run"]), `scripts/ci/bounded-apt.sh"`)) {
				t.Fatalf("%s must download before invoking the bounded installer", id)
			}
		}
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
