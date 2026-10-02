// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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

func treeMap(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}

func TestTreeReuseRetainsChecksAndFullFallback(t *testing.T) {
	w := treeWorkflow(t, "ci.yml")
	jobs := treeMap(w["jobs"])
	if len(jobs) != 13 {
		t.Fatalf("job inventory changed; review verifier's full-suite coverage: %d", len(jobs))
	}
	for _, id := range []string{"go", "web", "release-check", "e2e", "migration-compat"} {
		j := treeMap(jobs[id])
		if j == nil || (j["name"] != nil && j["name"] != id) {
			t.Fatalf("required check %s renamed or removed", id)
		}
	}
	if treeMap(jobs["go"])["if"] != "always()" || !reflect.DeepEqual(treeMap(jobs["go"])["needs"], []any{"go-test", "go-static", "go-timing"}) {
		t.Fatal("Go aggregation must still fail on any missing or failed shard/static/timing job")
	}
	for id, value := range jobs {
		if id == "go" || id == "runner-route" || id == "tree-reuse" {
			continue
		}
		j := treeMap(value)
		if j["if"] != nil {
			t.Fatalf("%s must positively report reuse or test success, never be skipped", id)
		}
		needs := j["needs"]
		if id == "go-test" {
			if !reflect.DeepEqual(needs, []any{"runner-route", "tree-reuse"}) {
				t.Fatal("shards must retain the runner boundary and depend on tree proof")
			}
		} else if needs != "tree-reuse" {
			t.Fatalf("%s does not depend on proof", id)
		}
		steps, ok := j["steps"].([]any)
		if !ok || len(steps) < 2 {
			t.Fatalf("%s has no test fallback", id)
		}
		first := treeMap(steps[0])
		if first["if"] != "needs.tree-reuse.outputs.reuse == 'tree'" || !strings.Contains(first["run"].(string), "reuse=tree") {
			t.Fatalf("%s does not report verified reuse", id)
		}
		for _, value := range steps[1:] {
			condition, _ := treeMap(value)["if"].(string)
			if !strings.Contains(condition, "needs.tree-reuse.outputs.reuse != 'tree'") {
				t.Fatalf("%s action runs on reuse: %v", id, value)
			}
			if strings.Contains(condition, "== 'tree'") {
				t.Fatalf("%s fallback is contradictory: %s", id, condition)
			}
		}
		for _, service := range treeMap(j["services"]) {
			if treeMap(service)["image"] != "${{ needs.tree-reuse.outputs.reuse != 'tree' && 'pgvector/pgvector:pg18' || '' }}" {
				t.Fatalf("%s starts its database on reuse", id)
			}
		}
	}
}

func TestFailedTreeProofCannotSkipRequiredJobs(t *testing.T) {
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	proof := treeMap(jobs["tree-reuse"])
	if proof["continue-on-error"] != true {
		t.Fatal("failed, timed-out or cancelled proof must not fail a dependency and skip the suite")
	}
	if proof["if"] != nil || treeMap(proof["outputs"])["reuse"] != "${{ steps.proof.outputs.reuse }}" {
		t.Fatal("proof failure must leave reuse unset and the full-suite fallback eligible")
	}
	for _, id := range []string{"web", "release-check", "e2e", "migration-compat", "go-test", "go-static", "go-timing"} {
		if treeMap(jobs[id])["if"] != nil {
			t.Fatalf("%s can skip instead of running the full fallback after proof failure", id)
		}
	}
	if treeMap(jobs["go"])["if"] != "always()" {
		t.Fatal("go must report a result even after dependency failure")
	}
}

func TestTreeRecordIsTrustedCompletionWithLeastPrivilege(t *testing.T) {
	ci := treeWorkflow(t, "ci.yml")
	detector := treeMap(treeMap(ci["jobs"])["tree-reuse"])
	if !reflect.DeepEqual(treeMap(detector["permissions"]), map[string]any{"contents": "read", "actions": "read", "packages": "read"}) {
		t.Fatal("reuse lookup token must be read-only")
	}
	if timeout, ok := detector["timeout-minutes"].(int); detector["runs-on"] != "ubuntu-latest" || !ok || timeout < 5 || timeout > 10 {
		t.Fatal("proof must be a bounded hosted job")
	}
	checkout := treeMap(detector["steps"].([]any)[0])
	if checkout["name"] != "Checkout tested commit ${{ github.sha }}" || treeMap(checkout["with"])["ref"] != nil {
		t.Fatal("source run must snapshot the actual default checkout, never a live PR merge ref")
	}
	w := treeWorkflow(t, "ci-tree-record.yml")
	if !reflect.DeepEqual(treeMap(w["on"]), map[string]any{"workflow_run": map[string]any{"workflows": []any{"CI"}, "types": []any{"completed"}}}) {
		t.Fatal("publication must follow the completed source workflow")
	}
	if !reflect.DeepEqual(treeMap(w["permissions"]), map[string]any{"contents": "read"}) {
		t.Fatal("workflow permissions must stay read-only")
	}
	j := treeMap(treeMap(w["jobs"])["record"])
	if !reflect.DeepEqual(treeMap(j["permissions"]), map[string]any{"contents": "read", "actions": "read", "statuses": "write", "packages": "write"}) {
		t.Fatal("only record status/package writes are authorized")
	}
	condition, _ := j["if"].(string)
	for _, binding := range []string{"conclusion == 'success'", "event == 'push'", "event == 'pull_request'", "head_repository.full_name == github.repository", "vars.CI_TREE_REUSE != 'off'"} {
		if !strings.Contains(condition, binding) {
			t.Fatalf("publication lacks %s", binding)
		}
	}
	steps := j["steps"].([]any)
	if len(steps) != 2 || j["runs-on"] != "ubuntu-latest" {
		t.Fatal("publisher must execute only the trusted checkout and recorder")
	}
	checkout = treeMap(steps[0])
	if checkout["uses"] != "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1" || !reflect.DeepEqual(treeMap(checkout["with"]), map[string]any{"persist-credentials": false, "ref": "${{ github.sha }}"}) {
		t.Fatal("publisher must never check out source-run or PR code")
	}
	for _, value := range []map[string]any{treeMap(steps[1]), treeMap(treeMap(treeMap(ci["jobs"])["tree-reuse"])["steps"].([]any)[1])} {
		if treeMap(value["env"])["CI_TREE_REUSE"] != "${{ vars.CI_TREE_REUSE }}" {
			t.Fatal("kill switch must reach producer and consumer")
		}
	}
}

func TestTreeReuseFixtureProgram(t *testing.T) {
	cmd := exec.Command("node", "--test", "scripts/ci-tree-reuse.test.mjs")
	cmd.Dir = root(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tree reuse fixtures: %v\n%s", err, out)
	}
}

func TestTreeReuseRegressionsRejectOriginal(t *testing.T) {
	const original = "e18f107f1ac720696760302395ad4f26e6a92866"
	baseline := func(path string) []byte {
		t.Helper()
		cmd := exec.Command("git", "show", original+":"+path)
		cmd.Dir = root(t)
		out, err := cmd.Output()
		if err != nil {
			t.Skip("original commit unavailable in a shallow checkout; baseline comparison requires branch history")
		}
		return out
	}
	dir := t.TempDir()
	for _, path := range []string{".github/workflows/ci.yml", "scripts/ci-tree-reuse.mjs"} {
		body := baseline(path)
		target := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	packageDir := filepath.Join(dir, "scripts/releaseworkflow")
	if err := os.MkdirAll(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Re-run the current test binary against the original workflow fixture.
	cmd := exec.Command(os.Args[0], "-test.run=^TestFailedTreeProofCannotSkipRequiredJobs$")
	cmd.Dir = packageDir
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "failed, timed-out or cancelled proof") {
		t.Fatalf("original proof workflow must fail the intended regression: %v\n%s", err, out)
	}
	fixtures, err := os.ReadFile(filepath.Join(root(t), "scripts/ci-tree-reuse.test.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts/ci-tree-reuse.test.mjs"), fixtures, 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("node", "--test", "--test-name-pattern=behind-main PR", "scripts/ci-tree-reuse.test.mjs")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "PR merge tree differs from head") {
		t.Fatalf("original head-tree verifier must fail the intended regression: %v\n%s", err, out)
	}
}

func TestTreeReuseRecomputesRealImmutableMerge(t *testing.T) {
	dir := t.TempDir()
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--quiet", "--initial-branch=main")
	git("", "remote", "add", "origin", dir)
	commit := func(files map[string]string, parents ...string) (string, string) {
		t.Helper()
		var entries []string
		for name, content := range files {
			blob := git(content, "hash-object", "-w", "--stdin")
			entries = append(entries, "100644 blob "+blob+"\t"+name+"\n")
		}
		tree := git(strings.Join(entries, ""), "mktree")
		args := []string{"-c", "user.name=Tree proof fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-m", "Fixture"}
		for _, parent := range parents {
			args = append(args, "-p", parent)
		}
		return git("", args...), tree
	}
	ancestor, _ := commit(map[string]string{"shared": "original\n"})
	base, _ := commit(map[string]string{"shared": "original\n", "upstream": "main\n"}, ancestor)
	head, headTree := commit(map[string]string{"shared": "original\n", "feature": "PR\n"}, ancestor)
	checkout, tree := commit(map[string]string{"shared": "original\n", "upstream": "main\n", "feature": "PR\n"}, base, head)
	if tree == headTree {
		t.Fatal("fixture must preserve a behind-main merge that differs from the PR head")
	}
	conflict, _ := commit(map[string]string{"shared": "conflict\n"}, ancestor)
	otherConflict, _ := commit(map[string]string{"shared": "other conflict\n"}, ancestor)
	script := (&url.URL{Scheme: "file", Path: filepath.Join(root(t), "scripts/ci-tree-reuse.mjs")}).String()
	program := `import assert from 'node:assert/strict';
import { cleanMergeTree } from ` + strconv.Quote(script) + `;
const [base, head, checkout, tree, conflict, otherConflict] = process.argv.slice(1);
assert.equal(cleanMergeTree(base, head, checkout), tree);
assert.throws(() => cleanMergeTree(conflict, otherConflict, checkout),
  error => error.status === 1 && error.stdout.includes('CONFLICT'));`
	cmd := exec.Command("node", "--input-type=module", "-e", program, base, head, checkout, tree, conflict, otherConflict)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real clean/conflicting merge proof: %v\n%s", err, out)
	}
}
