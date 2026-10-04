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
	w := treeWorkflow(t, "ci.yml")
	jobs := treeMap(w["jobs"])
	expected := []string{"tree-reuse", "cache-prime", "tier-plan", "tier-measurements", "runner-route", "go-test", "go-static", "go-timing", "go", "web-setup", "web-shard", "release-list-comparison", "web", "release-check", "e2e", "migration-compat"}
	if len(jobs) != len(expected) {
		t.Fatalf("job inventory changed: %d", len(jobs))
	}
	for _, id := range expected {
		if jobs[id] == nil {
			t.Fatalf("missing %s", id)
		}
	}
	for _, id := range []string{"go", "web", "release-check", "e2e", "migration-compat"} {
		j := treeMap(jobs[id])
		if j["name"] != nil && j["name"] != id {
			t.Fatalf("required check %s renamed", id)
		}
	}
	for _, id := range []string{"go-test", "go-static", "go-timing", "web-setup", "web-shard", "release-list-comparison", "release-check", "e2e"} {
		j := treeMap(jobs[id])
		if j["if"] != nil {
			t.Fatalf("%s could skip after proof failure", id)
		}
		first := treeMap(reuseSteps(j)[0])
		if first["if"] != verifiedQueueReuse || !strings.Contains(first["run"].(string), "reused merge_group run $SOURCE_RUN") || treeMap(first["env"])["SOURCE_RUN"] != "${{ needs.tree-reuse.outputs.run }}" {
			t.Fatalf("%s must report verified source run", id)
		}
		for _, value := range reuseSteps(j)[1:] {
			condition, _ := treeMap(value)["if"].(string)
			if !strings.Contains(condition, fullQueueFallback) || strings.Contains(condition, verifiedQueueReuse) {
				t.Fatalf("%s missing full fallback: %s", id, condition)
			}
		}
		for _, service := range treeMap(j["services"]) {
			if treeMap(service)["image"] != "${{ "+fullQueueFallback+" && 'pgvector/pgvector:pg18' || '' }}" {
				t.Fatalf("%s starts database on reuse", id)
			}
		}
	}
	if treeMap(jobs["go-test"])["needs"] == nil || !reflect.DeepEqual(treeMap(jobs["web-shard"])["needs"], []any{"web-setup", "tier-plan", "tree-reuse"}) {
		t.Fatal("shard prerequisites lost")
	}
	matrix := treeMap(treeMap(treeMap(jobs["web-shard"])["strategy"])["matrix"])
	if expression, ok := matrix["shard"].(string); !ok || !strings.Contains(expression, "needs.tier-plan.outputs.mode == 'essential' && '[1, 2]'") || !strings.HasSuffix(expression, "|| '[1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12]') }}") {
		t.Fatal("OPS-257 shard layout changed")
	}
	for id, expectedNeeds := range map[string][]any{"go": {"go-test", "go-static", "go-timing", "tree-reuse", "cache-prime"}, "web": {"web-setup", "web-shard", "tree-reuse", "cache-prime"}} {
		j := treeMap(jobs[id])
		if j["if"] != "always()" || !reflect.DeepEqual(j["needs"], expectedNeeds) {
			t.Fatalf("%s must gate tests and cache priming", id)
		}
	}
	migration := treeMap(jobs["migration-compat"])
	if migration["if"] != nil || migration["needs"] != nil {
		t.Fatal("AEON-415 migration lane must be independent and unconditional")
	}
	for _, v := range reuseSteps(migration) {
		if treeMap(v)["if"] != nil {
			t.Fatal("AEON-415 migration steps must be unconditional")
		}
	}
}

func TestQueueProofFailureCannotSkipRequiredJobs(t *testing.T) {
	w := treeWorkflow(t, "ci.yml")
	jobs := treeMap(w["jobs"])
	proof := treeMap(jobs["tree-reuse"])
	if proof["continue-on-error"] != true || proof["if"] != nil {
		t.Fatal("proof failure must leave full fallback eligible")
	}
	if timeout, ok := proof["timeout-minutes"].(int); !ok || timeout < 5 || timeout > 10 {
		t.Fatal("checkout/proof budget must be bounded at 5–10 minutes")
	}
	if !reflect.DeepEqual(treeMap(proof["permissions"]), map[string]any{"contents": "read", "actions": "read"}) {
		t.Fatal("proof token must only read contents and actions")
	}
	if proof["runs-on"] != "ubuntu-latest" || treeMap(proof["outputs"])["reuse"] != "${{ steps.proof.outputs.reuse }}" || treeMap(proof["outputs"])["run"] != "${{ steps.proof.outputs.run }}" {
		t.Fatal("proof must bind hosted read-only step outputs")
	}
	step := reuseStep(t, proof, "Verify the exact merge-group SHA")
	if treeMap(step["env"])["CI_TREE_REUSE"] != "${{ vars.CI_TREE_REUSE }}" || step["run"] != "node scripts/ci-tree-reuse.mjs" {
		t.Fatal("proof must invoke the kill-switch aware verifier")
	}
	if _, err := os.Stat(filepath.Join(root(t), ".github/workflows/ci-tree-record.yml")); !os.IsNotExist(err) {
		t.Fatal("tree publisher must be removed with secondary scope")
	}
}

func TestDirectPushCannotSkipWithoutQueueProof(t *testing.T) {
	// Exercise the actual main entry point with a bounded empty API response.
	// Then inspect every workflow condition that can avoid heavy work.
	cmd := exec.Command("node", "--test", "--test-name-pattern=direct main entry point", "scripts/ci-tree-reuse.test.mjs")
	cmd.Dir = root(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("direct push proof: %v\n%s", err, out)
	}
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	for _, id := range []string{"go-test", "go-static", "go-timing", "web-setup", "web-shard", "release-check", "e2e"} {
		if treeMap(jobs[id])["if"] != nil {
			t.Fatalf("direct push may skip %s", id)
		}
		for _, v := range reuseSteps(treeMap(jobs[id]))[1:] {
			condition, _ := treeMap(v)["if"].(string)
			if !strings.Contains(condition, fullQueueFallback) {
				t.Fatalf("direct push fallback missing in %s", id)
			}
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
	for _, id := range []string{"go", "web"} {
		j := treeMap(jobs[id])
		s := treeMap(reuseSteps(j)[0])
		command := s["run"].(string)
		for _, fixture := range []struct {
			reuse, cache, test string
			ok                 bool
		}{{"merge_group", "success", "success", true}, {"merge_group", "failure", "success", false}, {"merge_group", "skipped", "success", false}, {"none", "skipped", "success", true}, {"none", "skipped", "failure", false}, {"merge_group", "success", "skipped", false}} {
			cmd := exec.Command("bash", "-c", command)
			cmd.Env = append(os.Environ(), "REUSE="+fixture.reuse, "SOURCE_RUN=123", "CACHE_PRIME="+fixture.cache, "GITHUB_STEP_SUMMARY="+filepath.Join(t.TempDir(), "summary"))
			for key := range treeMap(s["env"]) {
				if key != "REUSE" && key != "SOURCE_RUN" && key != "CACHE_PRIME" {
					cmd.Env = append(cmd.Env, key+"="+fixture.test)
				}
			}
			if out, err := cmd.CombinedOutput(); (err == nil) != fixture.ok {
				t.Fatalf("%s %+v: %v\n%s", id, fixture, err, out)
			}
		}
	}
}

func TestQueueReuseFixturesCoverCurrentWorkflowJobs(t *testing.T) {
	cmd := exec.Command("node", "--input-type=module", "-e", `import {requiredJobs} from './scripts/ci-tree-reuse.mjs'; console.log(JSON.stringify(requiredJobs))`)
	cmd.Dir = root(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal(out, &names); err != nil {
		t.Fatal(err)
	}
	expected := []string{"tree-reuse", "tier-plan", "tier-measurements", "runner-route / route", "go", "go-static", "go-timing", "web", "web-setup", "release-list-comparison", "release-check", "e2e", "migration-compat"}
	for n := 1; n <= 7; n++ {
		expected = append(expected, fmt.Sprintf("go-test (%d)", n))
	}
	for n := 1; n <= 12; n++ {
		expected = append(expected, fmt.Sprintf("web-shard (%d)", n))
	}
	if len(names) != len(expected) {
		t.Fatalf("verifier coverage: %v", names)
	}
	for _, name := range expected {
		found := false
		for _, actual := range names {
			if name == actual {
				found = true
			}
		}
		if !found {
			t.Fatalf("verifier lacks %s", name)
		}
	}
	cmd = exec.Command("node", "--test", "scripts/ci-tree-reuse.test.mjs")
	cmd.Dir = root(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("queue reuse fixtures: %v\n%s", err, out)
	}
}

func TestFullFallbackPreservesPinnedMainJobs(t *testing.T) {
	// An immutable small digest fixture keeps this regression active in shallow
	// CI checkouts. Only proof dependencies/guards and the new fixture step are
	// normalized away; test commands, services, caches and original conditions
	// must still equal main's reviewed OPS-257/AEON-585 configuration.
	body, err := os.ReadFile(filepath.Join(root(t), "scripts/releaseworkflow/testdata/ci-main-before-reuse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		Source string            `json:"source_main_sha"`
		Hashes map[string]string `json:"sha256"`
	}
	if err := json.Unmarshal(body, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Source) != 40 || len(baseline.Hashes) != 11 {
		t.Fatal("incomplete main fixture")
	}
	w := treeWorkflow(t, "ci.yml")
	jobs := treeMap(w["jobs"])
	// Tier selection intentionally changes five execution jobs. Pin those to
	// AEON-681's pre-reuse implementation; keep the original reviewed hashes
	// for every unaffected job, including migration compatibility.
	tierBody, err := os.ReadFile(filepath.Join(root(t), "scripts/releaseworkflow/testdata/ci-tiers-before-reuse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tierBaseline struct {
		Source string            `json:"source_main_sha"`
		Hashes map[string]string `json:"sha256"`
	}
	if err := json.Unmarshal(tierBody, &tierBaseline); err != nil {
		t.Fatal(err)
	}
	if len(tierBaseline.Hashes) != 5 || len(tierBaseline.Source) != 40 {
		t.Fatal("incomplete tier fixture")
	}
	for id, expected := range baseline.Hashes {
		source := baseline.Source
		if tierHash, changed := tierBaseline.Hashes[id]; changed {
			expected = tierHash
			source = tierBaseline.Source
		}
		value := w["concurrency"]
		if id != "concurrency" {
			j := treeMap(jobs[id])
			value = j
			switch id {
			case "go-test":
				j["needs"] = []any{"runner-route", "tier-plan"}
			case "web-shard":
				j["needs"] = []any{"web-setup", "tier-plan"}
			case "web-setup":
				j["needs"] = "tier-plan"
			case "release-list-comparison":
				j["needs"] = "web-setup"
			default:
				delete(j, "needs")
			}
			for _, service := range treeMap(j["services"]) {
				treeMap(service)["image"] = "pgvector/pgvector:pg18"
			}
			var steps []any
			for _, v := range reuseSteps(j) {
				s := treeMap(v)
				if s["name"] == "Reuse the verified merge-group run" || s["name"] == "Exact-SHA reuse provenance and workflow regressions" {
					continue
				}
				if condition, ok := s["if"].(string); ok {
					if condition == fullQueueFallback {
						delete(s, "if")
					} else {
						suffix := ") && " + fullQueueFallback
						if !strings.HasPrefix(condition, "(") || !strings.HasSuffix(condition, suffix) {
							t.Fatalf("unexpected fallback condition in %s: %s", id, condition)
						}
						s["if"] = strings.TrimSuffix(strings.TrimPrefix(condition, "("), suffix)
					}
				}
				steps = append(steps, s)
			}
			if _, exists := j["steps"]; exists {
				j["steps"] = steps
			}
		}
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(encoded.Bytes())); actual != expected {
			t.Fatalf("%s full fallback differs from main %s: %s", id, source, actual)
		}
	}
}
