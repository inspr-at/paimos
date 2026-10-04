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
	for _, id := range []string{"go-test", "go-static", "go-timing", "web-setup", "web-shard", "release-list-comparison", "release-check-run", "e2e-run"} {
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
			if treeMap(service)["image"] != "pgvector/pgvector:pg18" {
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
	if proof["continue-on-error"] != true || proof["if"] != "github.event_name == 'push' && github.ref == 'refs/heads/main'" {
		t.Fatal("proof must be push-only and fail back to full CI")
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
	for _, id := range []string{"go-test", "go-static", "go-timing", "web-setup", "web-shard", "release-check-run", "e2e-run"} {
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

func TestQueueReuseAggregatesRejectPartialResults(t *testing.T) {
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	for _, id := range []string{"go", "web", "release-check", "e2e"} {
		steps := reuseSteps(treeMap(jobs[id]))
		s := treeMap(steps[len(steps)-1])
		values := map[string]string{"REUSE": "merge_group", "REUSE_PROOF": "success", "SOURCE_RUN": "123", "CACHE_PRIME": "success", "CI_PLAN": "success", "CI_LANE": "full", "GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main"}
		for key := range treeMap(s["env"]) {
			if _, exists := values[key]; !exists {
				values[key] = "skipped"
			}
		}
		execute := func(changed, result string) error {
			cmd := exec.Command("bash", "-c", s["run"].(string))
			cmd.Dir = root(t)
			cmd.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+filepath.Join(t.TempDir(), "summary"))
			for key, value := range values {
				if key == changed {
					value = result
				}
				cmd.Env = append(cmd.Env, key+"="+value)
			}
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
			if key == "REUSE" {
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
		// hosted seven Go shards and the full twelve web shards.
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
	for _, id := range []string{"ci-plan", "go-static", "go-timing", "web-setup", "release-check-run", "e2e-run", "migration-compat"} {
		if proof.ExecutionSteps[id] == "" {
			t.Fatalf("verifier lacks execution evidence for %s", id)
		}
	}
	reuseStep(t, treeMap(jobs["go-test"]), "Test this shard")
	reuseStep(t, treeMap(jobs["web-shard"]), "Run balanced UI shard with merge-queue flake control")
	// A new job or matrix row must invalidate the proof inventory.
	jobs["new-required-job"] = map[string]any{"runs-on": "ubuntu-latest"}
	if reflect.DeepEqual(proof.RequiredJobs, mergeGroupInventory(t, w)) {
		t.Fatal("new workflow job escaped drift check")
	}
	delete(jobs, "new-required-job")
	treeMap(treeMap(treeMap(jobs["web-shard"])["strategy"])["matrix"])["shard"] = []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}
	if reflect.DeepEqual(proof.RequiredJobs, mergeGroupInventory(t, w)) {
		t.Fatal("new matrix row escaped drift check")
	}
}

func TestFullFallbackPreservesPinnedMainJobs(t *testing.T) {
	// Pin the accepted c72eb506 classification/full-execution configuration.
	// Normalize only new proof dependencies, job-level reuse guards and the
	// additive verifier regression command. Services, caches, test commands,
	// classification and unconditional migrations must keep their exact bytes.
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
	guard := regexp.MustCompile(`^always\(\) && needs.ci-plan.result == 'success' && \((.*?)\) && needs.tree-reuse.outputs.reuse != 'merge_group'(?: && needs.(?:runner-route|web-setup).result == 'success')?$`)
	for id, expected := range baseline.Hashes {
		value := w["concurrency"]
		if id != "concurrency" {
			j := treeMap(jobs[id])
			value = j
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
					if s["run"] != "node --test scripts/ci-pr-plan.test.mjs scripts/ci-tree-reuse.test.mjs" {
						t.Fatal("unreviewed verifier regression command")
					}
					s["name"] = "PR classification regressions"
					s["run"] = "node --test scripts/ci-pr-plan.test.mjs"
				}
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
