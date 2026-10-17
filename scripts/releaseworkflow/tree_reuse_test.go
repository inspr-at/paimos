// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func treeWorkflow(t *testing.T, name string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root(t), ".github/workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}
func treeMap(value any) map[string]any  { m, _ := value.(map[string]any); return m }
func reuseSteps(j map[string]any) []any { steps, _ := j["steps"].([]any); return steps }
func reuseStep(t *testing.T, j map[string]any, name string) map[string]any {
	t.Helper()
	for _, value := range reuseSteps(j) {
		s := treeMap(value)
		if s["name"] == name {
			return s
		}
	}
	t.Fatalf("missing step %q", name)
	return nil
}

const fullQueueFallback = "needs.tree-reuse.outputs.reuse != 'merge_group'"
const verifiedQueueReuse = "needs.tree-reuse.outputs.reuse == 'merge_group'"
const mirroredPGVectorImage = "ghcr.io/inspr-at/paimos-ci/pgvector:pg18@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11"

func TestQueuePushReuseRetainsMainStructureAndFallback(t *testing.T) {
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	for _, id := range []string{"go", "web", "release-check", "e2e"} {
		j := treeMap(jobs[id])
		if j["if"] != "always()" || j["name"] != nil && j["name"] != id {
			t.Fatalf("required check %s must retain its identity and always report", id)
		}
		for _, need := range []string{"ci-plan", "tree-reuse", "cache-prime"} {
			if !containsNeed(j["needs"], need) {
				t.Fatalf("%s must gate %s", id, need)
			}
		}
	}
	for _, id := range []string{"go-test", "go-static", "go-timing", "web-catalogue", "web-setup", "web-unit", "web-shard", "release-check-run", "e2e-run"} {
		j := treeMap(jobs[id])
		condition, _ := j["if"].(string)
		if !strings.Contains(condition, "always()") || !strings.Contains(condition, "needs.ci-plan.result == 'success'") || !strings.Contains(condition, fullQueueFallback) || !containsNeed(j["needs"], "tree-reuse") {
			t.Fatalf("%s must skip the whole job only for a classified exemption or verified reuse, with full fallback after a skipped proof", id)
		}
		for _, v := range reuseSteps(j) {
			step := treeMap(v)
			if step["name"] == "Reuse the verified merge-group run" || strings.Contains(fmt.Sprint(step["if"]), "tree-reuse") {
				t.Fatalf("%s must gate reuse at job level, not allocate runners for reuse steps", id)
			}
		}
		for _, service := range treeMap(j["services"]) {
			if treeMap(service)["image"] != mirroredPGVectorImage {
				t.Fatalf("%s full fallback lost its database", id)
			}
		}
	}
	migration := treeMap(jobs["migration-compat"])
	if migration == nil || migration["if"] != nil || migration["needs"] != nil {
		t.Fatal("AEON-415 migration lane must remain independent and unconditional")
	}
	for _, v := range reuseSteps(migration) {
		if treeMap(v)["if"] != nil {
			t.Fatal("AEON-415 migration steps must remain unconditional")
		}
	}
}

func containsNeed(value any, name string) bool {
	if value == name {
		return true
	}
	for _, need := range anySlice(value) {
		if need == name {
			return true
		}
	}
	return false
}
func anySlice(value any) []any { a, _ := value.([]any); return a }

func TestQueueProofFailureCannotSkipRequiredJobs(t *testing.T) {
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	proof := treeMap(jobs["tree-reuse"])
	if proof["continue-on-error"] != true || proof["if"] != "github.event_name == 'push' && github.ref == 'refs/heads/main' || github.event_name == 'merge_group' && vars.CI_MG_REUSE == 'on'" {
		t.Fatal("proof must be main or opt-in merge-group only and fail back to full CI")
	}
	if timeout, ok := proof["timeout-minutes"].(int); !ok || timeout < 5 || timeout > 10 {
		t.Fatal("proof must have a bounded checkout/proof budget")
	}
	if !reflect.DeepEqual(treeMap(proof["permissions"]), map[string]any{"contents": "read", "actions": "read"}) {
		t.Fatal("proof token must be read-only")
	}
	if proof["runs-on"] != "ubuntu-latest" || treeMap(proof["outputs"])["reuse"] != "${{ steps.proof.outputs.reuse }}" || treeMap(proof["outputs"])["run"] != "${{ steps.proof.outputs.run }}" {
		t.Fatal("proof must bind hosted step outputs")
	}
	step := reuseStep(t, proof, "Verify the exact merge-group SHA")
	if treeMap(step["env"])["CI_TREE_REUSE"] != "${{ vars.CI_TREE_REUSE }}" || step["run"] != "node scripts/ci-tree-reuse.mjs" {
		t.Fatal("proof must invoke kill-switch aware verifier")
	}
	if _, err := os.Stat(filepath.Join(root(t), ".github/workflows/ci-tree-record.yml")); !os.IsNotExist(err) {
		t.Fatal("obsolete tree publisher must remain absent")
	}
}

func TestDirectPushCannotSkipWithoutQueueProof(t *testing.T) {
	cmd := exec.Command("node", "--test", "--test-name-pattern=direct main entry point", "scripts/ci-tree-reuse.test.mjs")
	cmd.Dir = root(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("direct push proof: %v\n%s", err, out)
	}
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	for _, id := range []string{"go-test", "go-static", "go-timing", "web-catalogue", "web-setup", "web-unit", "web-shard", "release-check-run", "e2e-run"} {
		condition, _ := treeMap(jobs[id])["if"].(string)
		if !strings.Contains(condition, fullQueueFallback) || !strings.Contains(condition, "always()") {
			t.Fatalf("%s must run full work after absent proof", id)
		}
	}
}

func TestReuseAlwaysPrimesMatchingMainCacheKeys(t *testing.T) {
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	prime := treeMap(jobs["cache-prime"])
	web := treeMap(jobs["web-setup"])
	goTests := treeMap(jobs["go-test"])
	if prime["if"] != "github.event_name == 'push' && github.ref == 'refs/heads/main' && needs.tree-reuse.outputs.reuse == 'merge_group'" || prime["needs"] != "tree-reuse" || prime["runs-on"] != "ubuntu-latest" || prime["strategy"] != nil {
		t.Fatal("reuse must schedule one main-only hosted priming job")
	}
	for _, name := range []string{"Resolve lockfile and Playwright cache keys", "Cache installed web dependencies", "Cache Playwright browsers"} {
		a := reuseStep(t, prime, name)
		b := reuseStep(t, web, name)
		for _, key := range []string{"id", "uses", "with", "env", "run"} {
			if !reflect.DeepEqual(a[key], b[key]) {
				t.Fatalf("priming %s differs at %s", name, key)
			}
		}
	}
	if reuseStep(t, prime, "Install web dependencies")["run"] != "npm ci" || reuseStep(t, prime, "Install Playwright browsers")["run"] != "npx playwright install chromium" || reuseStep(t, prime, "Warm Go module and build caches")["run"] != "go mod download\ngo build ./...\n" {
		t.Fatal("priming must install dependencies, browsers and compile Go")
	}
	for _, v := range reuseSteps(prime) {
		if treeMap(v)["if"] != nil {
			t.Fatal("priming must warm and save all cache surfaces")
		}
	}
	setup := func(j map[string]any, prefix string) map[string]any {
		for _, v := range reuseSteps(j) {
			s := treeMap(v)
			uses, _ := s["uses"].(string)
			if strings.HasPrefix(uses, prefix) {
				return treeMap(s["with"])
			}
		}
		t.Fatalf("missing %s", prefix)
		return nil
	}
	for _, prefix := range []string{"actions/setup-node@", "actions/setup-go@"} {
		source := web
		if prefix == "actions/setup-go@" {
			source = goTests
		}
		if !reflect.DeepEqual(setup(prime, prefix), setup(source, prefix)) {
			t.Fatalf("%s cache key inputs differ", prefix)
		}
	}
	cache := treeMap(reuseStep(t, goTests, "Persist Go build cache")["with"])
	for shard := 1; shard <= 7; shard++ {
		saved := treeMap(reuseStep(t, prime, fmt.Sprintf("Save main Go build cache for hosted shard %d", shard))["with"])
		key := cache["key"].(string)
		key = strings.ReplaceAll(key, "${{ strategy.job-total }}", "7")
		key = strings.ReplaceAll(key, "${{ matrix.shard }}", fmt.Sprint(shard))
		key = strings.ReplaceAll(key, "${{ github.head_ref || github.ref_name }}", "main")
		if saved["key"] != key || saved["path"] != cache["path"] {
			t.Fatalf("Go shard %d priming key/path differs from full lane", shard)
		}
		prefix := strings.Split(cache["restore-keys"].(string), "\n")[1]
		prefix = strings.ReplaceAll(prefix, "${{ strategy.job-total }}", "7")
		prefix = strings.ReplaceAll(prefix, "${{ matrix.shard }}", fmt.Sprint(shard))
		if !strings.HasPrefix(key, prefix) {
			t.Fatalf("PR/queue shard %d cannot restore primed key", shard)
		}
	}
	restored := treeMap(reuseStep(t, treeMap(jobs["web-shard"]), "Restore Playwright browsers")["with"])
	if restored["key"] != "${{ needs.web-setup.outputs.browser-cache-key }}" || treeMap(web["outputs"])["browser-cache-key"] != "${{ steps.web-cache-keys.outputs.browsers }}" {
		t.Fatal("web shards must restore the primed browser key")
	}
}

// planningEnabledEnv clears the AEON-1333 kill switch for one gate simulation.
// CI publishes CI_IMPACT=off on the test process. Reuse proof is a planning-
// enabled path; kill-switch scenarios set CI_IMPACT=off to reject reuse.
func planningEnabledEnv(t *testing.T, values map[string]string) []string {
	t.Helper()
	assigned := make(map[string]string, len(values)+2)
	for name, value := range values {
		assigned[name] = value
	}
	assigned["CI_IMPACT"] = ""
	assigned["GITHUB_STEP_SUMMARY"] = filepath.Join(t.TempDir(), "summary")
	return scenarioEnviron(assigned)
}

// scenarioEnviron isolates workflow switches from the caller's scenario.
// Explicit scenario values replace inherited entries, including CI_IMPACT=off,
// so ambient workflow settings cannot mask a lane or disable its kill switch.
func scenarioEnviron(scenario map[string]string) []string {
	blocked := map[string]struct{}{
		"CI_IMPACT":        {},
		"CI_AFFECTED_LANE": {},
		"CI_MG_REUSE":      {},
		"CI_TREE_REUSE":    {},
	}
	env := make([]string, 0, len(os.Environ())+len(scenario))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, drop := blocked[key]; drop {
			continue
		}
		if _, replaced := scenario[key]; replaced {
			continue
		}
		env = append(env, entry)
	}
	names := make([]string, 0, len(scenario))
	for name := range scenario {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env = append(env, name+"="+scenario[name])
	}
	return env
}

func TestQueueReuseAggregatesRejectPartialResults(t *testing.T) {
	// 2ffd1aee rejected verified merge-group reuse when the test process
	// inherited CI_IMPACT=off from the workflow env.
	t.Setenv("CI_IMPACT", "off")
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	for _, id := range []string{"go", "web", "release-check", "e2e"} {
		steps := reuseSteps(treeMap(jobs[id]))
		s := treeMap(steps[len(steps)-1])
		if id == "go" {
			s = reuseStep(t, treeMap(jobs[id]), "Require every Go shard and the static checks")
			if s["if"] != "needs.tree-reuse.outputs.reuse != 'pull_request'" {
				t.Fatal("main Go gate must run for every path except confirmed PR reuse")
			}
		}
		values := map[string]string{"REUSE": "merge_group", "REUSE_PROOF": "success", "SOURCE_RUN": "123", "CACHE_PRIME": "success", "CI_PLAN": "success", "CI_LANE": "full", "GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main"}
		if treeMap(s["env"])["TIER_PLAN"] != nil {
			values["TIER_PLAN"] = "success"
		}
		if treeMap(s["env"])["TIER_LAYOUT"] != nil {
			values["TIER_LAYOUT"] = "full" // planner output, not a job result
		}
		for key := range treeMap(s["env"]) {
			if _, exists := values[key]; !exists {
				values[key] = "skipped"
			}
		}
		execute := func(changed, result string) error {
			cmd := exec.Command("bash", "-c", s["run"].(string))
			cmd.Dir = root(t)
			current := values
			if changed != "" {
				current = make(map[string]string, len(values))
				for key, value := range values {
					current[key] = value
				}
				current[changed] = result
			}
			cmd.Env = planningEnabledEnv(t, current)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w: %s", err, out)
			}
			return nil
		}
		if err := execute("", ""); err != nil {
			t.Fatalf("%s rejects verified reuse: %v", id, err)
		}
		for key, expected := range values {
			if key == "REUSE" || key == "TIER_LAYOUT" || strings.HasPrefix(key, "PR_CONFIRM_") {
				continue
			} // no reuse falls back to the classified gate, covered separately
			wrong := "success"
			if expected == "success" {
				wrong = "skipped"
			}
			for _, value := range []string{"failure", "cancelled", "", wrong} {
				if value == expected {
					continue
				}
				if err := execute(key, value); err == nil {
					t.Fatalf("%s accepts invalid reuse %s=%q", id, key, value)
				}
			}
		}
	}
}

// Derive the actual merge-group job names from the workflow, rather than
// keeping a second hardcoded list that can drift together with the verifier.
func mergeGroupInventory(t *testing.T, w map[string]any) []string {
	t.Helper()
	var names []string
	for id, value := range treeMap(w["jobs"]) {
		j := treeMap(value)
		if id == "tree-reuse" || id == "cache-prime" {
			if !strings.Contains(fmt.Sprint(j["if"]), "github.event_name == 'push'") {
				t.Fatalf("%s must remain push-only", id)
			}
			continue
		}
		name := id
		if n, ok := j["name"].(string); ok {
			name = n
			if id == "tier-measurements" {
				name = "tier-measurements"
			}
		}
		if uses, ok := j["uses"].(string); ok {
			body, err := os.ReadFile(filepath.Join(root(t), uses))
			if err != nil {
				t.Fatal(err)
			}
			var child map[string]any
			if err := yaml.Unmarshal(body, &child); err != nil {
				t.Fatal(err)
			}
			for childID, v := range treeMap(child["jobs"]) {
				childName := childID
				if n, ok := treeMap(v)["name"].(string); ok {
					childName = n
				}
				names = append(names, name+" / "+childName)
			}
			continue
		}
		matrix := treeMap(treeMap(j["strategy"])["matrix"])
		if len(matrix) == 0 {
			names = append(names, name)
			continue
		}
		if len(matrix) != 1 || matrix["shard"] == nil {
			t.Fatalf("unreviewed matrix for %s", id)
		}
		var shards []int
		// Both matrices choose the final literal array for merge_group:
		// hosted seven Go shards and the full seventeen web shards.
		if expr, ok := matrix["shard"].(string); ok {
			arrays := regexp.MustCompile(`'\[([0-9, ]+)\]'`).FindAllStringSubmatch(expr, -1)
			if len(arrays) == 0 {
				t.Fatalf("unknown matrix expression for %s", id)
			}
			if err := json.Unmarshal([]byte("["+arrays[len(arrays)-1][1]+"]"), &shards); err != nil {
				t.Fatal(err)
			}
		} else {
			for _, n := range anySlice(matrix["shard"]) {
				shards = append(shards, n.(int))
			}
		}
		for _, n := range shards {
			names = append(names, fmt.Sprintf("%s (%d)", name, n))
		}
	}
	sort.Strings(names)
	return names
}

func TestQueueReuseFixturesCoverCurrentWorkflowJobs(t *testing.T) {
	if _, err := os.Stat(filepath.Join(root(t), "scripts/ci-tree-reuse.mjs")); err != nil {
		t.Fatalf("reuse verifier missing from the integrated workflow: %v", err)
	}
	cmd := exec.Command("node", "--input-type=module", "-e", `import {requiredJobs,executionSteps} from './scripts/ci-tree-reuse.mjs'; console.log(JSON.stringify({requiredJobs,executionSteps}))`)
	cmd.Dir = root(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		RequiredJobs   []string
		ExecutionSteps map[string]string
	}
	if err := json.Unmarshal(out, &proof); err != nil {
		t.Fatal(err)
	}
	w := treeWorkflow(t, "ci.yml")
	expected := mergeGroupInventory(t, w)
	sort.Strings(proof.RequiredJobs)
	if !reflect.DeepEqual(proof.RequiredJobs, expected) {
		t.Fatalf("verifier jobs differ from actual merge-group workflow/matrices: got %v want %v", proof.RequiredJobs, expected)
	}
	jobs := treeMap(w["jobs"])
	for id, name := range proof.ExecutionSteps {
		reuseStep(t, treeMap(jobs[id]), name)
	}
	for _, id := range []string{"ci-plan", "go-static", "go-timing", "web-catalogue", "web-setup", "release-check-run", "e2e-run", "migration-compat"} {
		if proof.ExecutionSteps[id] == "" {
			t.Fatalf("verifier lacks execution evidence for %s", id)
		}
	}
	reuseStep(t, treeMap(jobs["go-test"]), "Test this shard (essential plus changed area, or full on main)")
	reuseStep(t, treeMap(jobs["web-shard"]), "Run selected UI cases without retries")
	reuseStep(t, treeMap(jobs["web-unit"]), "Run selected web units without retries")
	// A new job or matrix row must invalidate the proof inventory.
	jobs["new-required-job"] = map[string]any{"runs-on": "ubuntu-latest"}
	if reflect.DeepEqual(proof.RequiredJobs, mergeGroupInventory(t, w)) {
		t.Fatal("new workflow job escaped drift check")
	}
	delete(jobs, "new-required-job")
	treeMap(treeMap(treeMap(jobs["web-shard"])["strategy"])["matrix"])["shard"] = []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18}
	if reflect.DeepEqual(proof.RequiredJobs, mergeGroupInventory(t, w)) {
		t.Fatal("new matrix row escaped drift check")
	}
}

// Restore the historical setup for its immutable hash check only after proving
// the new setup and parallel unit job preserve every moved check byte for byte.
func normalizeParallelWebSetup(t *testing.T, jobs map[string]any) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root(t), "scripts/releaseworkflow/testdata/ci-web-setup-before-parallel.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var historical map[string]any
	if err := yaml.Unmarshal(body, &historical); err != nil {
		t.Fatal(err)
	}
	before := treeMap(historical["web-setup"])
	setup, unit := treeMap(jobs["web-setup"]), treeMap(jobs["web-unit"])
	wantSteps := []any{}
	for _, value := range reuseSteps(before) {
		old := treeMap(value)
		name, _ := old["name"].(string)
		var moved map[string]any
		switch name {
		case "Install shells used by command round-trip unit tests":
			moved = cloneStep(t, reuseStep(t, unit, name))
			// AEON-933 bounds this install. Prove the bound, then compare the
			// accepted pre-parallel command.
			restoreBoundedShellInstall(t, moved)
		case "Web unit checks (once)":
			moved = cloneStep(t, reuseStep(t, unit, "Run selected web units without retries"))
			moved["name"] = name
			moved["run"] = strings.Replace(moved["run"].(string), `if [ "${{ matrix.shard }}" = 1 ]; then npm run ci:web:shard:test; fi`, "npm run ci:web:shard:test", 1)
			moved["run"] = strings.Replace(moved["run"].(string), "--unit --shard ${{ matrix.shard }}/${{ strategy.job-total }} --job web-unit-${{ matrix.shard }}", "--unit --job web-unit", 1)
		case "Confirm full tier execution":
			moved = cloneStep(t, reuseStep(t, unit, name))
			treeMap(moved["env"])["TIER_REPORT"] = strings.Replace(treeMap(moved["env"])["TIER_REPORT"].(string), "web-unit-${{ matrix.shard }}", "web-unit", 1)
		case "Preserve unit tier measurements":
			moved = cloneStep(t, reuseStep(t, unit, name))
			treeMap(moved["with"])["name"] = strings.Replace(treeMap(moved["with"])["name"].(string), "web-unit-${{ matrix.shard }}", "web-unit", 1)
		case "Browser runner safety (no browsers)":
			moved = cloneStep(t, reuseStep(t, unit, name))
			if moved["if"] != "matrix.shard == 1" {
				t.Fatal("Browser safety must run once")
			}
			delete(moved, "if")
		default:
			wantSteps = append(wantSteps, value)
			continue
		}
		normalizeEffectiveLane(t, "web-unit", moved)
		if !reflect.DeepEqual(moved, old) {
			t.Fatalf("Moved check %s changed: got %v want %v", name, moved, old)
		}
	}
	want := cloneStep(t, before)
	want["steps"] = wantSteps
	setup = cloneStep(t, setup)
	normalizeWebReproducibility(t, setup)
	normalizeWebCatalogueOverlap(t, jobs, setup)
	if condition, _ := setup["if"].(string); !strings.Contains(condition, " && needs.tree-reuse.outputs.reuse != 'pull_request'") {
		t.Fatal("missing PR tree skip")
	} else {
		setup["if"] = strings.Replace(condition, " && needs.tree-reuse.outputs.reuse != 'pull_request'", "", 1)
	}
	normalizeEffectiveLane(t, "web-setup", setup)
	normalizeAEON1018Classification(t, "web-setup", setup)
	if !reflect.DeepEqual(setup, want) {
		t.Fatal("Parallel setup changed beyond moving units and their prerequisites/proofs")
	}
	return before
}

// AEON-1276 adds a blocking rebuild proof and failure evidence to runtime
// preparation. Check both steps exactly before projecting onto the historical
// setup; every older check and the immutable job pins remain unchanged.
func normalizeWebReproducibility(t *testing.T, setup map[string]any) {
	t.Helper()
	steps := reuseSteps(setup)
	proofName := "Prove web build reproducibility"
	evidenceName := "Preserve web reproducibility failure evidence"
	proofAt, evidenceAt := -1, -1
	for i, value := range steps {
		switch treeMap(value)["name"] {
		case proofName:
			if proofAt != -1 {
				t.Fatal("web reproducibility proof must run exactly once")
			}
			proofAt = i
		case evidenceName:
			if evidenceAt != -1 {
				t.Fatal("web reproducibility evidence must be preserved exactly once")
			}
			evidenceAt = i
		}
	}
	if proofAt < 1 || evidenceAt != proofAt+1 || evidenceAt+1 >= len(steps) || treeMap(steps[proofAt-1])["name"] != "Build web" || treeMap(steps[evidenceAt+1])["name"] != "Cache Playwright browsers" {
		t.Fatal("web reproducibility proof and evidence must follow Build web before browser preparation")
	}
	wantProof := map[string]any{"name": proofName, "run": "node scripts/check-web-reproducibility.mjs"}
	wantEvidence := map[string]any{
		"name": evidenceName, "if": "failure()",
		"uses": "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02",
		"with": map[string]any{
			"name": "web-reproducibility", "path": "tmp/web-reproducibility\nweb/dist\n",
			"retention-days": 1, "if-no-files-found": "ignore",
		},
	}
	if !reflect.DeepEqual(treeMap(steps[proofAt]), wantProof) || !reflect.DeepEqual(treeMap(steps[evidenceAt]), wantEvidence) {
		t.Fatal("web reproducibility proof must remain unconditional and blocking with pinned failure evidence")
	}
	setup["steps"] = append(append([]any{}, steps[:proofAt]...), steps[evidenceAt+1:]...)
}

// Risk: moving native discovery off the runtime path could drop identities or
// accept a partial catalogue. Prove the complete blocking check and its parallel
// dependencies, then restore its old position for the immutable historical pin.
func normalizeWebCatalogueOverlap(t *testing.T, jobs, setup map[string]any) {
	t.Helper()
	catalogue := treeMap(jobs["web-catalogue"])
	check := reuseStep(t, catalogue, "Require every web case to be classified")
	wantCheck := map[string]any{"name": "Require every web case to be classified", "run": "node scripts/test-tiers/cli.mjs check web --strict"}
	if !reflect.DeepEqual(check, wantCheck) {
		t.Fatal("strict full web discovery must remain unconditional and blocking")
	}
	steps := reuseSteps(setup)
	inputAt := -1
	for i, value := range steps {
		if treeMap(value)["name"] == "Require every web case to be classified" {
			t.Fatal("runtime preparation must not wait for strict catalogue discovery")
		}
		if treeMap(value)["name"] == "Check Docker web inputs without Docker" {
			inputAt = i
		}
	}
	if inputAt < 1 || treeMap(steps[inputAt-1])["name"] != "Install web dependencies" {
		t.Fatal("catalogue discovery needs the same installed dependencies as runtime setup")
	}
	want := cloneStep(t, setup)
	delete(want, "outputs")
	want["steps"] = append(append([]any{}, steps[:inputAt]...), wantCheck)
	if !reflect.DeepEqual(catalogue, want) || catalogue["runs-on"] != "ubuntu-latest" || !reflect.DeepEqual(catalogue["needs"], []any{"ci-plan", "tree-reuse", "tier-plan"}) {
		t.Fatal("catalogue must run beside setup on hosted CI with the same lane/reuse guards and dependency installation")
	}
	web := treeMap(jobs["web"])
	gate := reuseStep(t, web, "Require catalogue, setup, units and every web shard")
	if web["if"] != "always()" || !containsNeed(web["needs"], "web-catalogue") || gate["if"] != nil || gate["continue-on-error"] != nil || treeMap(gate["env"])["WEB_CATALOGUE"] != "${{ needs.web-catalogue.result }}" {
		t.Fatal("the required web aggregate must join the catalogue and reject failure")
	}
	setup["steps"] = append(append(append([]any{}, steps[:inputAt+1]...), cloneStep(t, check)), steps[inputAt+1:]...)
}

// Prove the newly required classification checks before projecting onto the
// immutable pre-reuse CI pins. Every historical check still matches bytewise.
func normalizeAEON1018Classification(t *testing.T, id string, j map[string]any) {
	t.Helper()
	if id == "go-static" {
		check := reuseStep(t, j, "Validate every Go tier against native collection")
		want := map[string]any{"name": "Validate every Go tier against native collection", "run": "node scripts/test-tiers/cli.mjs check go --strict"}
		if !reflect.DeepEqual(check, want) {
			t.Fatal("strict Go classification must be unconditional and blocking")
		}
		check["run"] = "node scripts/test-tiers/cli.mjs check go"
	}
	if id == "web-setup" {
		steps := reuseSteps(j)
		var preserved []any
		found := 0
		for i, value := range steps {
			check := treeMap(value)
			if check["name"] != "Require every web case to be classified" {
				preserved = append(preserved, value)
				continue
			}
			found++
			want := map[string]any{"name": "Require every web case to be classified", "run": "node scripts/test-tiers/cli.mjs check web --strict"}
			if !reflect.DeepEqual(check, want) || i == 0 || treeMap(steps[i-1])["name"] != "Check Docker web inputs without Docker" {
				t.Fatal("strict web classification must block setup immediately after dependency/input checks")
			}
		}
		if found != 1 {
			t.Fatal("exactly one strict web classification check is required")
		}
		j["steps"] = preserved
	}
}

// Project the reviewed effective-lane wiring back onto immutable historical
// pins. Commands and historical fixtures remain byte-for-byte checked.
func normalizeEffectiveLane(t *testing.T, id string, value map[string]any) {
	t.Helper()
	var visit func(map[string]any)
	visit = func(m map[string]any) {
		for key, raw := range m {
			switch v := raw.(type) {
			case string:
				// Preserve historical pins after checking the added workflow-level
				// merge-group guards; never rewrite the immutable fixtures.
				if key == "if" && m["name"] == "Confirm full tier execution" && id != "web-setup" {
					old := "needs.tier-plan.outputs.lane == 'full' && needs.tier-plan.outputs.mode != 'essential'"
					if id == "go-test" {
						old = "needs.tier-plan.outputs.mode != 'essential'"
					}
					want := "github.event_name != 'pull_request' || (" + old + ")"
					if id == "go-test" {
						want = "github.event_name != 'pull_request' || " + old
					}
					if v != want {
						t.Fatalf("%s must prove full execution on every merge-group shard", id)
					}
					v = old
				}
				if key == "if" && strings.Contains(v, "needs.tier-plan.outputs.layout != 'static'") {
					guard := "(github.event_name != 'pull_request' || needs.tier-plan.outputs.layout != 'static')"
					if !strings.Contains(v, guard) {
						t.Fatalf("%s must restrict static skips to PRs", id)
					}
					v = strings.Replace(v, guard, "needs.tier-plan.outputs.layout != 'static'", 1)
				}
				if key == "shard" {
					// AEON-1389: PR widths come from the budgeted planner. Validate
					// that exact wiring and full-event fallback before projecting
					// onto the immutable historical two-shard execution pin.
					if id == "web-shard" {
						const planned = `${{ fromJSON(github.event_name == 'pull_request' && needs.tier-plan.outputs.web_shards || '[1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17]') }}`
						if v != planned {
							t.Fatal("PR web shards must consume the planned list and retain seventeen full-event rows")
						}
						v = `${{ fromJSON(github.event_name == 'pull_request' && needs.tier-plan.outputs.lane == 'spec-only' && '[1]' || github.event_name == 'pull_request' && needs.tier-plan.outputs.mode == 'essential' && '[1, 2]' || '[1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12]') }}`
					}
					v = strings.Replace(v, "github.event_name == 'pull_request' && needs.tier-plan.outputs.mode == 'essential'", `contains(fromJSON('["pull_request","merge_group"]'), github.event_name) && needs.tier-plan.outputs.mode == 'essential'`, 1)
					v = strings.Replace(v, "github.event_name == 'pull_request' && needs.tier-plan.outputs.lane == 'spec-only'", "needs.tier-plan.outputs.lane == 'spec-only'", 1)
				}
				m[key] = strings.ReplaceAll(v, "needs.tier-plan.outputs.lane", "needs.ci-plan.outputs.lane")
			case map[string]any:
				if layout, exists := v["AEON_TEST_TIER_LAYOUT"]; exists {
					if layout != "${{ needs.tier-plan.outputs.layout }}" {
						t.Fatal("unreviewed tier runner layout input")
					}
					delete(v, "AEON_TEST_TIER_LAYOUT")
				}
				visit(v)
			case []any:
				for _, child := range v {
					if nested, ok := child.(map[string]any); ok {
						visit(nested)
					}
				}
			}
		}
	}
	visit(value)
	if id == "go-static" || id == "release-check-run" {
		condition, _ := value["if"].(string)
		if !strings.Contains(condition, "needs.tier-plan.result == 'success' && ") || !containsNeed(value["needs"], "tier-plan") {
			t.Fatal("effective lane consumer must gate its publisher")
		}
		value["if"] = strings.Replace(condition, "needs.tier-plan.result == 'success' && ", "", 1)
		var needs []any
		for _, need := range anySlice(value["needs"]) {
			if need != "tier-plan" {
				needs = append(needs, need)
			}
		}
		value["needs"] = needs
	}
}

// AEON-933 bounds apt and Playwright dependency installs. Assert that bound,
// then restore the accepted-CI command so the historical job hash stays put.
func normalizeAEON933InstallBounds(t *testing.T, id string, job map[string]any) {
	t.Helper()
	switch id {
	case "go-test":
		step := reuseStep(t, job, "Install shells when this shard runs pairing path-proof tests")
		run, _ := step["run"].(string)
		const bounded = "  bash \"$GITHUB_WORKSPACE/scripts/ci/bounded-apt.sh\" apt zsh fish"
		const historical = "  sudo apt-get update -qq && sudo apt-get install -y -qq zsh fish"
		if step["timeout-minutes"] != 4 || !strings.Contains(run, bounded) {
			t.Fatalf("AEON-933: pairing shell install must skip, bound and retry apt:\n%s\ntimeout=%v", run, step["timeout-minutes"])
		}
		step["run"] = strings.Replace(run, bounded, historical, 1)
		delete(step, "timeout-minutes")
	case "web-shard":
		restorePlaywrightDeps(t, reuseStep(t, job, "Install Chromium system dependencies"), "npx playwright install-deps chromium", "bash \"$GITHUB_WORKSPACE/scripts/ci/bounded-apt.sh\" playwright install-deps chromium")
	case "e2e-run":
		restorePlaywrightDeps(t, reuseStep(t, job, "Install Playwright"), "npx playwright install --with-deps --only-shell chromium", "bash \"$GITHUB_WORKSPACE/scripts/ci/bounded-apt.sh\" playwright install --with-deps --only-shell chromium")
	}
}

func restoreBoundedShellInstall(t *testing.T, step map[string]any) {
	t.Helper()
	run, _ := step["run"].(string)
	if step["timeout-minutes"] != 4 || !strings.Contains(run, "bash \"$GITHUB_WORKSPACE/scripts/ci/bounded-apt.sh\" apt fish zsh") {
		t.Fatalf("AEON-933: web unit shell install must skip, bound and retry apt: %#v", step)
	}
	step["run"] = "sudo apt-get update -qq && sudo apt-get install -y -qq fish zsh"
	delete(step, "timeout-minutes")
}

func restorePlaywrightDeps(t *testing.T, step map[string]any, historical, marker string) {
	t.Helper()
	run, _ := step["run"].(string)
	if step["timeout-minutes"] != 13 || !strings.Contains(run, marker) {
		t.Fatalf("AEON-933: Playwright dependency install must use a root timeout: %#v", step)
	}
	step["run"] = historical
	delete(step, "timeout-minutes")
}

func cloneStep(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	body, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var copy map[string]any
	if err := yaml.Unmarshal(body, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func TestParallelWebUnitsShareRuntimeAndRunOnce(t *testing.T) {
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	normalizeParallelWebSetup(t, jobs)
	unit, browser := treeMap(jobs["web-unit"]), treeMap(jobs["web-shard"])
	for _, j := range []map[string]any{unit, browser} {
		if !reflect.DeepEqual(j["needs"], []any{"ci-plan", "web-setup", "tree-reuse", "tier-plan"}) {
			t.Fatal("Unit and browser shards must start together from setup, without depending on each other")
		}
		if treeMap(reuseStep(t, j, "Download shared web runtime")["with"])["name"] != "web-runtime" || reuseStep(t, j, "Restore web runtime")["run"] != `tar -xf "$RUNNER_TEMP/web-runtime/web-runtime.tar"` {
			t.Fatal("Unit and browser shards must restore the same prepared runtime")
		}
	}
	run := reuseStep(t, unit, "Run selected web units without retries")["run"].(string)
	if strings.Count(run, "npm run test:unit:core") != 1 || !strings.Contains(run, `if [ "${{ matrix.shard }}" = 1 ]; then npm run ci:web:shard:test; fi`) {
		t.Fatal("Spec-only units and full-lane shard regressions must each run once")
	}
	measure := treeMap(jobs["tier-measurements"])
	if !containsNeed(measure["needs"], "ci-plan") || treeMap(reuseStep(t, measure, "Report cases and complete job runner minutes")["env"])["CI_LANE"] != "${{ needs.tier-plan.outputs.lane }}" {
		t.Fatal("Tier measurements must report spec-only's untiered scope")
	}
}

func TestFullFallbackPreservesPinnedMainJobs(t *testing.T) {
	// Pin the accepted c72eb506 classification/full-execution configuration.
	// Keep that historical fixture and the original AEON-681 tier fixture.
	// Seven tier-adapted execution jobs use the additive merge-main pins (e2e-run
	// joined with the OPS-257 L4 static layout gate);
	// lane classification, unaffected jobs and unconditional migrations stay pinned.
	body, err := os.ReadFile(filepath.Join(root(t), "scripts/releaseworkflow/testdata/ci-main-before-reuse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		Source string            `json:"source_ci_sha"`
		Hashes map[string]string `json:"sha256"`
	}
	if err := json.Unmarshal(body, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Source) != 40 || len(baseline.Hashes) != 12 {
		t.Fatal("incomplete accepted-CI fixture")
	}
	w := treeWorkflow(t, "ci.yml")
	jobs := treeMap(w["jobs"])
	guard := regexp.MustCompile(`^always\(\) && needs.ci-plan.result == 'success' && (?:needs.tier-plan.result == 'success' && )?\((.*?)\) && needs.tree-reuse.outputs.reuse != 'merge_group'(?: && needs.tree-reuse.outputs.reuse != 'pull_request')?(?: && needs.(?:runner-route|web-setup).result == 'success')?$`)
	tierBody, err := os.ReadFile(filepath.Join(root(t), "scripts/releaseworkflow/testdata/ci-tiers-before-reuse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tierBaseline struct {
		MergeMain struct {
			Source string            `json:"source_main_sha"`
			Hashes map[string]string `json:"sha256"`
		} `json:"mergeMain"`
	}
	if err := json.Unmarshal(tierBody, &tierBaseline); err != nil {
		t.Fatal(err)
	}
	if len(tierBaseline.MergeMain.Source) != 40 || len(tierBaseline.MergeMain.Hashes) != 7 {
		t.Fatal("incomplete merge-main tier fixture")
	}
	for id, expected := range baseline.Hashes {
		if id == "release-list-comparison" {
			// AEON-679 retired this capture-only job. Keep the historical pin,
			// assert retirement, and verify every surviving job's hash below.
			if _, exists := jobs[id]; exists {
				t.Fatal("AEON-679 retired release-list-comparison; it must remain absent")
			}
			continue
		}
		if tierHash, changed := tierBaseline.MergeMain.Hashes[id]; changed {
			expected = tierHash
		}
		value := w["concurrency"]
		if id != "concurrency" {
			j := treeMap(jobs[id])
			if id == "web-setup" {
				j = normalizeParallelWebSetup(t, jobs)
			}
			j = cloneStep(t, j)
			if id == "web-shard" {
				// AEON-1336 supplies plans only to affected PR execution. Prove
				// that exact binding before pinning every older full-run field.
				stepEnv := treeMap(reuseStep(t, j, "Run selected UI cases without retries")["env"])
				if stepEnv["AEON_BROWSER_PLAN"] != "${{ github.event_name == 'pull_request' && needs.tier-plan.outputs.browser_scope == 'affected' && needs.tier-plan.outputs.browser_plan || '' }}" {
					t.Fatal("only an affected PR may supply a browser execution plan")
				}
				delete(stepEnv, "AEON_BROWSER_PLAN")
			}
			if id == "release-check-run" || id == "e2e-run" {
				// AEON-582 pins the remaining v7 actions. Assert the exact pins
				// before restoring their historical tags for the unchanged CI
				// fixture; every other job field remains covered by its hash.
				pins := map[string]string{
					"actions/checkout@v7":   "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
					"actions/setup-node@v7": "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020",
				}
				if id == "e2e-run" {
					pins["actions/setup-go@v7"] = "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
				}
				restored := make(map[string]bool)
				for _, value := range reuseSteps(j) {
					step := treeMap(value)
					uses, _ := step["uses"].(string)
					for tag, pin := range pins {
						if !strings.HasPrefix(uses, strings.SplitN(tag, "@", 2)[0]+"@") {
							continue
						}
						if uses != pin || restored[tag] {
							t.Fatalf("%s must retain one exact AEON-582 action pin for %s", id, tag)
						}
						step["uses"] = tag
						restored[tag] = true
					}
				}
				if len(restored) != len(pins) {
					t.Fatalf("%s is missing an AEON-582 action pin", id)
				}
			}
			if id == "go-static" {
				// AEON-1061 adds one public-source guard. Prove its exact command,
				// shape and placement before comparing every older field to the
				// unchanged accepted-CI pin.
				steps := reuseSteps(j)
				index := -1
				for i, value := range steps {
					step := treeMap(value)
					if step["name"] != "Keep operator names out of public source" {
						continue
					}
					if index != -1 || len(step) != 2 || step["run"] != "node --test scripts/check-public-names.test.mjs && node scripts/check-public-names.mjs" {
						t.Fatal("public-source guard must retain its exact command and run unconditionally")
					}
					index = i
				}
				if index < 1 || index+1 >= len(steps) || treeMap(steps[index-1])["name"] != "Forbid volatile CI identity reads in Go tests" || treeMap(steps[index+1])["run"] != "go vet ./..." {
					t.Fatal("public-source guard must run after the identity guard and before go vet")
				}
				j["steps"] = append(steps[:index], steps[index+1:]...)
				// AEON-1412 adds the mandatory body-read ownership check. Prove
				// its command and placement before comparing the older CI pin.
				steps = reuseSteps(j)
				index = -1
				for i, value := range steps {
					step := treeMap(value)
					if step["name"] != "Bound every HTTP request body read" {
						continue
					}
					if index != -1 || len(step) != 2 || step["run"] != "go test -p 2 ./internal/httpapi -run '^TestHTTP(ReadDeadlineOwnership|RequestBodyOwnership)$' -count=1" {
						t.Fatal("body-read guard must retain its exact command and run unconditionally")
					}
					index = i
				}
				if index < 1 || index+1 >= len(steps) || treeMap(steps[index-1])["run"] != "go vet ./..." || treeMap(steps[index+1])["name"] != "Check prepared rules bootstrap drift" {
					t.Fatal("body-read guard must run after go vet and before the rules bootstrap check")
				}
				j["steps"] = append(steps[:index], steps[index+1:]...)
			}
			if id == "go-test" || id == "go-timing" || id == "e2e-run" {
				// OPS-287: assert the verified mirror pin, then restore only the
				// image reference for comparison with the immutable CI fixture.
				postgres := treeMap(treeMap(j["services"])["postgres"])
				if postgres["image"] != mirroredPGVectorImage {
					t.Fatalf("%s must use the verified pgvector mirror digest", id)
				}
				postgres["image"] = "pgvector/pgvector:pg18"
			}
			if id == "go-test" {
				// AEON-1199 keeps hosted caching unchanged while pool shards use
				// their persistent disk. Assert both guards before projecting the
				// hosted path back to the immutable accepted-CI fixture.
				for _, value := range reuseSteps(j) {
					step := treeMap(value)
					if strings.HasPrefix(fmt.Sprint(step["uses"]), "actions/setup-go@") {
						with := treeMap(step["with"])
						if with["cache"] != "${{ runner.environment == 'github-hosted' }}" {
							t.Fatal("Go setup caches must be enabled only on GitHub-hosted runners")
						}
						with["cache"] = true
					}
				}
				cache := reuseStep(t, j, "Persist Go build cache")
				if cache["if"] != "runner.environment == 'github-hosted'" {
					t.Fatal("Go build cache restore and save must run only on GitHub-hosted runners")
				}
				delete(cache, "if")
				// AEON-777 broadens routing through the router's event switch.
				// Pin the exact new event/ref/head boundary before normalizing to
				// the historical main-only expressions; all other fields stay pinned.
				const oldRoute = `contains(fromJSON('["push","workflow_dispatch"]'), github.event_name) && github.ref == 'refs/heads/main' && `
				const poolRoute = `((contains(fromJSON('["push","workflow_dispatch"]'), github.event_name) && github.ref == 'refs/heads/main') || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository) || github.event_name == 'merge_group') && `
				matrix := treeMap(treeMap(j["strategy"])["matrix"])
				for _, field := range []struct {
					owner map[string]any
					key   string
				}{{j, "runs-on"}, {matrix, "shard"}} {
					expression, _ := field.owner[field.key].(string)
					if strings.Count(expression, poolRoute) != 1 {
						t.Fatalf("%s must retain the reviewed pool event/ref/head guards", field.key)
					}
					field.owner[field.key] = strings.Replace(expression, poolRoute, oldRoute, 1)
				}
				// AEON-707 binds backup clients to this job's actual service.
				// Assert the exact addition before comparing every older field
				// against the unchanged accepted-CI hash.
				step := reuseStep(t, j, "Test this shard (essential plus changed area, or full on main)")
				stepEnv := treeMap(step["env"])
				if stepEnv["AEON_TEST_POSTGRES_CONTAINER"] != "${{ job.services.postgres.id }}" {
					t.Fatal("backup drill clients must target this shard's PostgreSQL service")
				}
				delete(stepEnv, "AEON_TEST_POSTGRES_CONTAINER")
			}
			normalizeEffectiveLane(t, id, j)
			value = j
			normalizeNixVendorAdditions(t, id, j)
			if id == "ci-plan" {
				delete(treeMap(j["outputs"]), "pr_tree_proof")
				steps := reuseSteps(j)
				if treeMap(steps[len(steps)-1])["name"] != "Capture PR checkout identity" {
					t.Fatal("missing PR identity capture")
				}
				j["steps"] = steps[:len(steps)-1]
				// AEON-1333: CI_IMPACT=off skips the base classifier before a stale
				// base script can narrow the lane. Prove that guard, then restore
				// the accepted condition so every other byte stays pinned.
				classify := reuseStep(t, j, "Classify local PR diff")
				const impactGuard = `if [ "${CI_IMPACT:-}" != off ] && [ "$GITHUB_EVENT_NAME" = pull_request ] &&`
				const acceptedGuard = `if [ "$GITHUB_EVENT_NAME" = pull_request ] &&`
				run, _ := classify["run"].(string)
				if strings.Count(run, impactGuard) != 1 {
					t.Fatal("CI_IMPACT=off must skip the base classifier and retain full validation")
				}
				classify["run"] = strings.Replace(run, impactGuard, acceptedGuard, 1)
			}
			if id == "go-static" || id == "go-timing" {
				for _, v := range reuseSteps(j) {
					step := treeMap(v)
					if strings.HasPrefix(fmt.Sprint(step["uses"]), "actions/setup-go@") {
						if treeMap(step["with"])["cache"] != "${{ needs.tree-reuse.outputs.reuse != 'pull_request' }}" {
							t.Fatal("reused MG guard must not save caches")
						}
						treeMap(step["with"])["cache"] = true
					}
				}
			}
			if condition, ok := j["if"].(string); ok && strings.Contains(condition, "tree-reuse") {
				match := guard.FindStringSubmatch(condition)
				if len(match) != 2 {
					t.Fatalf("unexpected reuse guard in %s", id)
				}
				j["if"] = match[1]
				var needs []any
				for _, need := range anySlice(j["needs"]) {
					if need != "tree-reuse" {
						needs = append(needs, need)
					}
				}
				if len(needs) == 1 {
					j["needs"] = needs[0]
				} else {
					j["needs"] = needs
				}
			}
			for _, v := range reuseSteps(j) {
				s := treeMap(v)
				if s["name"] == "PR classification and exact-SHA reuse regressions" {
					if s["run"] != "node --test scripts/ci-pr-plan.test.mjs scripts/ci-tree-reuse.test.mjs scripts/ci-static.test.mjs" {
						t.Fatal("unreviewed verifier regression command")
					}
					s["name"] = "PR classification regressions"
					s["run"] = "node --test scripts/ci-pr-plan.test.mjs"
				}
			}
			normalizeAEON933InstallBounds(t, id, j)
			if id != "web-setup" { // Already proved and projected by normalizeParallelWebSetup.
				normalizeAEON1018Classification(t, id, j)
			}
		}
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(encoded.Bytes())); actual != expected {
			t.Fatalf("%s full fallback differs from accepted CI %s: %s", id, baseline.Source, actual)
		}
	}
}

func TestMergeGroupPRReuseAggregatesRequireConfirmedProof(t *testing.T) {
	// 2ffd1aee rejected confirmed PR reuse when CI_IMPACT=off leaked in.
	t.Setenv("CI_IMPACT", "off")
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	for _, id := range []string{"go", "web", "release-check", "e2e"} {
		j := treeMap(jobs[id])
		steps := reuseSteps(j)
		gate := treeMap(steps[len(steps)-1])
		confirm := reuseStep(t, j, "Revalidate PR execution proof")
		if id == "go" && (gate["name"] != "Require confirmed PR execution proof" || gate["if"] != "needs.tree-reuse.outputs.reuse == 'pull_request'" || treeMap(steps[len(steps)-2])["id"] != "pr-confirm") {
			t.Fatal("PR Go gate must run only after mandatory confirmation for PR reuse")
		}
		if confirm["continue-on-error"] != nil || confirm["id"] != "pr-confirm" || treeMap(confirm["env"])["CONFIRM_PR_RUN"] != "${{ needs.tree-reuse.outputs.run }}" {
			t.Fatal("revalidation must be mandatory and bind source run")
		}
		values := map[string]string{"REUSE": "pull_request", "REUSE_PROOF": "success", "PR_CONFIRM_REUSE": "pull_request", "PR_CONFIRM_RUN": "123", "SOURCE_RUN": "123", "CACHE_PRIME": "skipped", "CI_PLAN": "success", "CI_LANE": "full", "TIER_LAYOUT": "full", "GITHUB_EVENT_NAME": "merge_group"}
		for key := range treeMap(gate["env"]) {
			if _, exists := values[key]; exists {
				continue
			}
			values[key] = "skipped"
			if key == "GO_STATIC" || key == "GO_TIMING" || key == "TIER_PLAN" {
				values[key] = "success"
			}
		}
		execute := func(key, value string) error {
			cmd := exec.Command("bash", "-c", gate["run"].(string))
			current := values
			if key != "" {
				current = make(map[string]string, len(values))
				for name, result := range values {
					current[name] = result
				}
				current[key] = value
			}
			cmd.Env = planningEnabledEnv(t, current)
			return cmd.Run()
		}
		if err := execute("", ""); err != nil {
			t.Fatalf("%s rejects valid PR reuse: %v", id, err)
		}
		for key, expected := range values {
			wrong := "success"
			if expected == "success" {
				wrong = "skipped"
			}
			for _, value := range []string{"", "failure", "cancelled", wrong} {
				if value == expected {
					continue
				}
				if err := execute(key, value); err == nil {
					t.Fatalf("%s accepts invalid proof %s=%q", id, key, value)
				}
			}
		}
		if err := execute("PR_CONFIRM_RUN", "124"); err == nil {
			t.Fatalf("%s accepts confirmation of another run", id)
		}
	}
}

// The release-check job publishes CI_IMPACT=off. Reuse simulations must keep
// a verified result, and must still reject it when the scenario itself
// engages the kill switch.
func TestReuseAggregatesIgnoreAmbientImpactKillSwitch(t *testing.T) {
	t.Setenv("CI_IMPACT", "off")
	t.Setenv("CI_AFFECTED_LANE", "off")
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	goJob := treeMap(jobs["go"])
	verified := map[string]string{
		"REUSE": "merge_group", "REUSE_PROOF": "success", "SOURCE_RUN": "123",
		"CACHE_PRIME": "success", "CI_PLAN": "success", "CI_LANE": "full",
		"GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main",
		"TIER_PLAN": "success", "TIER_LAYOUT": "full",
		"GO_TEST": "skipped", "GO_STATIC": "skipped", "GO_TIMING": "skipped",
	}
	confirmed := map[string]string{
		"REUSE": "pull_request", "REUSE_PROOF": "success", "PR_CONFIRM_REUSE": "pull_request",
		"PR_CONFIRM_RUN": "123", "SOURCE_RUN": "123", "CACHE_PRIME": "skipped",
		"CI_PLAN": "success", "CI_LANE": "full", "TIER_LAYOUT": "full",
		"GITHUB_EVENT_NAME": "merge_group", "TIER_PLAN": "success",
		"GO_TEST": "skipped", "GO_STATIC": "success", "GO_TIMING": "success",
	}
	run := func(script string, values map[string]string) error {
		t.Helper()
		scenario := make(map[string]string, len(values)+1)
		for name, value := range values {
			scenario[name] = value
		}
		scenario["GITHUB_STEP_SUMMARY"] = filepath.Join(t.TempDir(), "summary")
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = root(t)
		cmd.Env = scenarioEnviron(scenario)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}
	mainGate := reuseStep(t, goJob, "Require every Go shard and the static checks")
	if err := run(mainGate["run"].(string), verified); err != nil {
		t.Fatalf("ambient CI_IMPACT=off rejected verified merge-group reuse: %v", err)
	}
	prGate := reuseStep(t, goJob, "Require confirmed PR execution proof")
	if err := run(prGate["run"].(string), confirmed); err != nil {
		t.Fatalf("ambient CI_IMPACT=off rejected confirmed PR reuse: %v", err)
	}
	killed := make(map[string]string, len(verified)+1)
	for name, value := range verified {
		killed[name] = value
	}
	killed["CI_IMPACT"] = "off"
	if err := run(mainGate["run"].(string), killed); err == nil {
		t.Fatal("scenario CI_IMPACT=off accepted merge-group reuse")
	}
}
