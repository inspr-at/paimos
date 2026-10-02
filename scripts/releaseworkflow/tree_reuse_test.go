// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
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
	for _, id := range []string{"go", "web", "release-check", "e2e"} {
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

func TestTreeRecordIsTrustedCompletionWithLeastPrivilege(t *testing.T) {
	ci := treeWorkflow(t, "ci.yml")
	detector := treeMap(treeMap(ci["jobs"])["tree-reuse"])
	if !reflect.DeepEqual(treeMap(detector["permissions"]), map[string]any{"contents": "read", "actions": "read", "packages": "read"}) {
		t.Fatal("reuse lookup token must be read-only")
	}
	if detector["runs-on"] != "ubuntu-latest" || detector["timeout-minutes"] != 1 {
		t.Fatal("proof must be a bounded hosted job")
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
	checkout := treeMap(steps[0])
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
