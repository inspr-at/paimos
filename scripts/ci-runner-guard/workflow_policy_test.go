// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMainPushConcurrencyIsolation(t *testing.T) {
	workflows := readPolicyWorkflows(t)
	var ci map[string]any
	if err := yaml.Unmarshal(workflows["ci.yml"], &ci); err != nil {
		t.Fatal(err)
	}
	policy := mapping(ci["concurrency"])
	group, ok := policy["group"].(string)
	if !ok {
		t.Fatal("CI needs an expression-based concurrency group")
	}
	cancel, ok := policy["cancel-in-progress"].(string)
	if !ok {
		t.Fatal("CI cancellation must be event-guarded")
	}
	for _, tc := range []struct {
		name, event, ref, pr, run, wantGroup string
		wantCancel                           bool
	}{
		{"main-older", "push", "refs/heads/main", "", "100", "ci-push-refs/heads/main", true},
		{"main-newer", "push", "refs/heads/main", "", "101", "ci-push-refs/heads/main", true},
		{"pr-older", "pull_request", "refs/pull/42/merge", "42", "102", "ci-pull_request-42", true},
		{"pr-newer", "pull_request", "refs/pull/42/merge", "42", "103", "ci-pull_request-42", true},
		{"other-pr", "pull_request", "refs/pull/43/merge", "43", "104", "ci-pull_request-43", true},
		{"queue-older", "merge_group", "refs/heads/gh-readonly-queue/main/pr-42", "", "105", "ci-merge_group-105", false},
		{"queue-newer", "merge_group", "refs/heads/gh-readonly-queue/main/pr-42", "", "106", "ci-merge_group-106", false},
		{"manual-main-older", "workflow_dispatch", "refs/heads/main", "", "107", "ci-workflow_dispatch-107", false},
		{"manual-main-newer", "workflow_dispatch", "refs/heads/main", "", "108", "ci-workflow_dispatch-108", false},
		{"other-branch", "push", "refs/heads/work/example", "", "109", "ci-push-109", false},
		{"tag", "push", "refs/tags/v260930120000.0.0", "", "110", "ci-push-110", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			context := map[string]string{
				"github.event_name": tc.event, "github.ref": tc.ref,
				"github.event.pull_request.number": tc.pr, "github.run_id": tc.run,
			}
			gotGroup := expandConcurrency(t, group, context)
			gotCancel := expandConcurrency(t, cancel, context)
			if gotGroup != tc.wantGroup || gotCancel != fmt.Sprint(tc.wantCancel) {
				t.Fatalf("group/cancel = %s/%s, want %s/%t", gotGroup, gotCancel, tc.wantGroup, tc.wantCancel)
			}
		})
	}

	// Discover every workflow subscribed to main pushes, including path-filtered
	// rehearsal runs. Their groups must cancel main runs without sharing CI's group.
	var mainWorkflows []string
	for name, body := range workflows {
		var w map[string]any
		if err := yaml.Unmarshal(body, &w); err != nil {
			t.Fatal(err)
		}
		push := mapping(mapping(w["on"])["push"])
		if !hasNeed(push["branches"], "main") {
			continue
		}
		mainWorkflows = append(mainWorkflows, name)
		concurrency := mapping(w["concurrency"])
		if name == "ci.yml" {
			continue // The event matrix above covers CI's guarded expressions.
		}
		otherGroup, ok := concurrency["group"].(string)
		if !ok || concurrency["cancel-in-progress"] != true {
			t.Fatalf("%s must cancel superseded main runs", name)
		}
		context := map[string]string{"github.ref": "refs/heads/main"}
		if got := expandConcurrency(t, otherGroup, context); got != "release-image-check-refs/heads/main" {
			t.Fatalf("release rehearsal must keep its independent main group: %s", got)
		}
	}
	sort.Strings(mainWorkflows)
	if strings.Join(mainWorkflows, ",") != "ci.yml,release-image-check.yml" {
		t.Fatalf("main push workflow inventory changed; review its concurrency: %v", mainWorkflows)
	}
	for _, name := range []string{"go", "web", "release-check", "e2e"} {
		job := mapping(mapping(ci["jobs"])[name])
		if job == nil || (job["name"] != nil && job["name"] != name) {
			t.Fatalf("required check %q must keep its identity", name)
		}
	}
}

// Evaluate only the reviewed concurrency expressions' small grammar: string
// contexts/literals, ==, && and ||. Unknown atoms fail instead of guessing.
// Read expressions from YAML so the event matrix exercises the actual policy.
func expandConcurrency(t *testing.T, template string, context map[string]string) string {
	t.Helper()
	atom := func(text string) any {
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "'") && strings.HasSuffix(text, "'") {
			return strings.Trim(text, "'")
		}
		if value, ok := context[text]; ok {
			return value
		}
		t.Fatalf("unsupported concurrency atom %q", text)
		return nil
	}
	truthy := func(value any) bool { return value != nil && value != false && value != "" }
	expressions := regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
	return expressions.ReplaceAllStringFunc(template, func(expression string) string {
		var result any
		for _, disjunction := range strings.Split(expressions.FindStringSubmatch(expression)[1], "||") {
			for _, conjunction := range strings.Split(disjunction, "&&") {
				if left, right, equal := strings.Cut(conjunction, "=="); equal {
					result = atom(left) == atom(right)
				} else {
					result = atom(conjunction)
				}
				if !truthy(result) {
					break
				}
			}
			if truthy(result) {
				break
			}
		}
		return fmt.Sprint(result)
	})
}

// Mutate complete directory copies so every regression exercises the same
// repository-wide entry point used by release-check and the command-line guard.
func TestWorkflowPolicyMutations(t *testing.T) {
	workflows := readPolicyWorkflows(t)
	type mutation struct {
		name, file, want string
		edit             func(map[string]any)
		raw              func([]byte) []byte
	}
	var cases []mutation
	add := func(name, file, want string, edit func(map[string]any)) {
		cases = append(cases, mutation{name: name, file: file, want: want, edit: edit})
	}
	parse := func(body []byte) map[string]any {
		var w map[string]any
		if err := yaml.Unmarshal(body, &w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	ciGroup := mapping(parse(workflows["ci.yml"])["concurrency"])["group"]
	shared := map[string]any{"group": "shared", "cancel-in-progress": true}
	var files []string
	for file := range workflows {
		files = append(files, file)
	}
	sort.Strings(files)
	for _, file := range files {
		jobs := mapping(parse(workflows[file])["jobs"])
		var ids []string
		for id := range jobs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			for index, concurrency := range []any{shared, "shared", nil} {
				add(fmt.Sprintf("job-concurrency/%s/%s/%d", file, id, index), file, "must not override workflow-level concurrency", func(w map[string]any) {
					mapping(mapping(w["jobs"])[id])["concurrency"] = concurrency
				})
			}
		}
	}

	for _, id := range []string{"go-test", "go-timing", "go-static", "web-setup", "web-unit", "web-shard", "release-check-run", "e2e-run", "go", "web", "release-check", "e2e", "tier-measurements"} {
		add("raw-lane-consumer/"+id, "ci.yml", "effective tier-plan lane", func(w map[string]any) {
			job := mapping(mapping(w["jobs"])[id])
			if condition, ok := job["if"].(string); ok && strings.Contains(condition, "needs.tier-plan.outputs.lane") {
				job["if"] = strings.ReplaceAll(condition, "needs.tier-plan.outputs.lane", "needs.ci-plan.outputs.lane")
				return
			}
			for _, value := range job["steps"].([]any) {
				env := mapping(mapping(value)["env"])
				if env["CI_LANE"] == "${{ needs.tier-plan.outputs.lane }}" {
					env["CI_LANE"] = "${{ needs.ci-plan.outputs.lane }}"
					return
				}
			}
			t.Fatalf("mutation fixture %s has no effective lane consumer", id)
		})
	}
	add("effective-lane-publisher-missing", "ci.yml", "publish the effective lane", func(w map[string]any) {
		delete(mapping(mapping(mapping(w["jobs"])["tier-plan"])["outputs"]), "lane")
	})
	// All contexts and supporting job identities are reserved, even on a
	// path-filtered workflow, and even when a different id sets a reserved name.
	for _, id := range []string{"go", "web", "release-check", "e2e", "go-test", "go-static", "go-timing", "runner-route", "ci-plan", "web-setup", "web-unit", "web-shard", "e2e-run", "release-check-run", "cross-family", "gate/cross-family"} {
		for _, identity := range []string{id, strings.ToUpper(id)} {
			add("reserved-id/"+identity, "extra.yaml", "reserved to ci.yml", func(w map[string]any) {
				w["jobs"] = map[string]any{identity: map[string]any{"runs-on": "ubuntu-latest"}}
			})
			add("reserved-name/"+identity, "extra.yaml", "reserved to ci.yml", func(w map[string]any) {
				mapping(mapping(w["jobs"])["tests"])["name"] = identity
			})
		}
	}
	for index, event := range []any{
		"pull_request", []any{"pull_request", "workflow_dispatch"},
		map[string]any{"pull_request": nil},
		map[string]any{"pull_request": map[string]any{"types": []any{"opened"}}},
		map[string]any{"pull_request": map[string]any{"paths": []any{}}},
		map[string]any{"pull_request": map[string]any{"paths-ignore": nil}},
		map[string]any{"pull_request": map[string]any{"paths": "src/**"}},
		map[string]any{"pull_request": map[string]any{"paths": []any{nil}}},
		map[string]any{"pull_request": map[string]any{"paths": []any{""}}},
		map[string]any{"pull_request": map[string]any{"paths": []any{"src/**"}, "paths-ignore": []any{"docs/**"}}},
	} {
		add(fmt.Sprintf("unfiltered-pr/%d", index), "extra.yaml", "paths or paths-ignore", func(w map[string]any) { w["on"] = event })
	}
	for _, file := range []string{"pairing-platform.yml", "release-image-check.yml"} {
		add("removed-pr-paths/"+file, file, "paths or paths-ignore", func(w map[string]any) {
			delete(mapping(mapping(w["on"])["pull_request"]), "paths")
		})
	}
	for _, file := range []string{"extra.yaml", "test-runner-route.yml", "release-image-check.yml", "release.yml", "homebrew-tap.yml", "verify-live.yml"} {
		add("copied-ci-group/"+file, file, "must not copy ci.yml", func(w map[string]any) {
			w["concurrency"] = map[string]any{"group": ciGroup, "cancel-in-progress": true}
		})
	}
	for index, concurrency := range []any{shared, "shared", nil} {
		add(fmt.Sprintf("unknown-workflow-concurrency/%d", index), "extra.yaml", "workflow-level concurrency is not allowlisted", func(w map[string]any) { w["concurrency"] = concurrency })
		add(fmt.Sprintf("reusable-workflow-concurrency/%d", index), "test-runner-route.yml", "reusable workflows must not define concurrency", func(w map[string]any) { w["concurrency"] = concurrency })
	}
	for _, file := range []string{"ci.yml", "release.yml", "homebrew-tap.yml", "release-image-check.yml", "verify-live.yml"} {
		add("allowlisted-file-made-reusable/"+file, file, "reusable workflows must not define concurrency", func(w map[string]any) {
			mapping(w["on"])["workflow_call"] = nil
		})
		add("changed-group/"+file, file, "reviewed concurrency policy", func(w map[string]any) { mapping(w["concurrency"])["group"] = "shared" })
		add("changed-cancel/"+file, file, "reviewed concurrency policy", func(w map[string]any) { mapping(w["concurrency"])["cancel-in-progress"] = "${{ true }}" })
		add("extra-concurrency-key/"+file, file, "reviewed concurrency policy", func(w map[string]any) { mapping(w["concurrency"])["extra"] = true })
	}
	add("second-workflow-uses-job-concurrency", "extra.yaml", "must not override workflow-level concurrency", func(w map[string]any) {
		w["jobs"] = map[string]any{"runner-route": map[string]any{"uses": "./.github/workflows/test-runner-route.yml", "concurrency": map[string]any{"group": ciGroup, "cancel-in-progress": true}}}
	})
	add("second-unfiltered-ci-copy", "extra.yaml", "must not copy ci.yml", func(w map[string]any) {
		w["on"] = map[string]any{"pull_request": nil}
		w["concurrency"] = map[string]any{"group": ciGroup, "cancel-in-progress": true}
		w["jobs"] = map[string]any{}
		for _, id := range []string{"go", "web", "release-check", "e2e"} {
			mapping(w["jobs"])[id] = map[string]any{"runs-on": "ubuntu-latest"}
		}
	})
	for name, edit := range map[string]func(map[string]any){
		"unbound-attempt": func(job map[string]any) {
			job["runs-on"] = strings.Replace(job["runs-on"].(string), "needs.smoke-route.outputs.run_attempt == github.run_attempt && ", "", 1)
		},
		"unbound-main": func(job map[string]any) {
			job["runs-on"] = strings.Replace(job["runs-on"].(string), "github.ref == 'refs/heads/main' && ", "", 1)
		},
		"ci-route-reference": func(job map[string]any) {
			job["runs-on"] = strings.ReplaceAll(job["runs-on"].(string), "needs.smoke-route.", "needs.runner-route.")
		},
		"wrong-route-dependency": func(job map[string]any) { job["needs"] = "wrong-route" },
	} {
		want := "runner selection is not proven"
		if name == "wrong-route-dependency" {
			want = "must depend on the hosted"
		}
		add("smoke-route/"+name, "test-runner-smoke.yml", want, func(w map[string]any) { edit(mapping(mapping(w["jobs"])["smoke"])) })
	}

	// The original review's CI trigger, gate, dependency and runner mutations.
	for name, push := range map[string]any{
		"unfiltered-push": nil,
		"all-branches":    map[string]any{"branches": []any{"**"}},
		"extra-branch":    map[string]any{"branches": []any{"main", "work/**"}},
		"tag-push":        map[string]any{"branches": []any{"main"}, "tags": []any{"*"}},
	} {
		add(name, "ci.yml", "must not duplicate PR CI", func(w map[string]any) { mapping(w["on"])["push"] = push })
	}
	add("bare-event-list", "ci.yml", "CI must cover", func(w map[string]any) { w["on"] = []any{"push", "pull_request", "merge_group", "workflow_dispatch"} })
	add("pr-activity-filter", "ci.yml", "without path or activity filters", func(w map[string]any) { mapping(w["on"])["pull_request"] = map[string]any{"types": []any{"opened"}} })
	add("pr-path-filter", "ci.yml", "without path or activity filters", func(w map[string]any) { mapping(w["on"])["pull_request"] = map[string]any{"paths": []any{"src/**"}} })
	add("missing-merge-group", "ci.yml", "CI must cover", func(w map[string]any) { delete(mapping(w["on"]), "merge_group") })
	add("missing-merge-group-type", "ci.yml", "merge queue check requests", func(w map[string]any) { mapping(w["on"])["merge_group"] = nil })
	add("wrong-merge-group-type", "ci.yml", "merge queue check requests", func(w map[string]any) { mapping(w["on"])["merge_group"] = map[string]any{"types": []any{"wrong"}} })
	add("missing-ci-concurrency", "ci.yml", "reviewed concurrency policy", func(w map[string]any) { delete(w, "concurrency") })
	add("scalar-ci-concurrency", "ci.yml", "reviewed concurrency policy", func(w map[string]any) { w["concurrency"] = "shared" })
	add("missing-cancel-expression", "ci.yml", "reviewed concurrency policy", func(w map[string]any) { delete(mapping(w["concurrency"]), "cancel-in-progress") })
	add("cancel-queue", "ci.yml", "reviewed concurrency policy", func(w map[string]any) {
		mapping(w["concurrency"])["cancel-in-progress"] = "${{ contains(fromJSON('[\"pull_request\",\"push\",\"merge_group\"]'), github.event_name) }}"
	})
	add("main-does-not-cancel", "ci.yml", "reviewed concurrency policy", func(w map[string]any) {
		mapping(w["concurrency"])["cancel-in-progress"] = "${{ github.event_name == 'pull_request' }}"
	})
	add("main-group-still-unique", "ci.yml", "reviewed concurrency policy", func(w map[string]any) {
		mapping(w["concurrency"])["group"] = "ci-${{ github.event_name }}-${{ github.event.pull_request.number || github.run_id }}"
	})
	add("push-cancellation-without-main-guard", "ci.yml", "reviewed concurrency policy", func(w map[string]any) {
		mapping(w["concurrency"])["cancel-in-progress"] = "${{ github.event_name == 'pull_request' || github.event_name == 'push' }}"
	})
	add("non-pr-shared-group", "ci.yml", "reviewed concurrency policy", func(w map[string]any) {
		mapping(w["concurrency"])["group"] = "ci-${{ github.event_name }}-${{ github.event.pull_request.number || github.ref }}"
	})
	for _, id := range []string{"go", "web", "release-check", "e2e", "go-test", "go-static", "go-timing", "runner-route", "ci-plan", "web-setup", "web-unit", "web-shard", "e2e-run", "release-check-run", "migration-compat"} {
		for _, rename := range []bool{false, true} {
			add(fmt.Sprintf("missing-or-renamed/%s/%t", id, rename), "ci.yml", "is missing", func(w map[string]any) {
				jobs := mapping(w["jobs"])
				if _, exists := jobs[id]; !exists {
					t.Fatalf("required-job mutation target %q is absent", id)
				}
				if rename {
					jobs[id+"-renamed"] = jobs[id]
				}
				delete(jobs, id)
			})
		}
		if id == "runner-route" || id == "ci-plan" || id == "migration-compat" {
			for _, condition := range []any{false, "success()", nil, "needs.tier-plan.outputs.lane == 'full'"} {
				add(fmt.Sprintf("conditional/%s/%v", id, condition), "ci.yml", "must run without an if condition", func(w map[string]any) { mapping(mapping(w["jobs"])[id])["if"] = condition })
			}
			continue
		}
		for _, condition := range []any{false, "success()", nil} {
			add(fmt.Sprintf("conditional/%s/%v", id, condition), "ci.yml", "must", func(w map[string]any) { mapping(mapping(w["jobs"])[id])["if"] = condition })
		}
	}
	add("migration-classification-dependency", "ci.yml", "migration-compat must run independently", func(w map[string]any) { mapping(mapping(w["jobs"])["migration-compat"])["needs"] = "ci-plan" })
	for _, id := range []string{"go", "web", "release-check", "e2e"} {
		add("renamed-check-context/"+id, "ci.yml", "renamed to", func(w map[string]any) { mapping(mapping(w["jobs"])[id])["name"] = "other" })
	}
	add("missing-go-dependency", "ci.yml", "effective lane publisher", func(w map[string]any) { mapping(mapping(w["jobs"])["go"])["needs"] = []any{"go-test", "go-timing"} })
	add("missing-web-dependency", "ci.yml", "effective lane publisher", func(w map[string]any) { mapping(mapping(w["jobs"])["web"])["needs"] = []any{"web-setup"} })
	add("web-skipped-on-failure", "ci.yml", "web must report failures", func(w map[string]any) { delete(mapping(mapping(w["jobs"])["web"]), "if") })
	add("web-full-shards-omitted", "ci.yml", "web shard matrix must retain", func(w map[string]any) {
		mapping(mapping(mapping(mapping(w["jobs"])["web-shard"])["strategy"])["matrix"])["shard"] = []any{1}
	})
	add("web-units-omitted-from-aggregate", "ci.yml", "web must gate setup, every unit shard", func(w map[string]any) {
		mapping(mapping(w["jobs"])["web"])["needs"] = []any{"ci-plan", "web-setup", "web-shard", "tree-reuse", "cache-prime", "tier-plan"}
	})
	for _, mutate := range []string{"matrix", "fail-fast", "continue-on-error"} {
		add("web-unit-weakened/"+mutate, "ci.yml", "four blocking shards", func(w map[string]any) {
			unit := mapping(mapping(w["jobs"])["web-unit"])
			switch mutate {
			case "matrix":
				mapping(mapping(unit["strategy"])["matrix"])["shard"] = []any{1}
			case "fail-fast":
				mapping(unit["strategy"])["fail-fast"] = true
			case "continue-on-error":
				unit["continue-on-error"] = true
			}
		})
	}
	add("collapsed-shards", "ci.yml", "routed shard count", func(w map[string]any) {
		mapping(mapping(mapping(mapping(w["jobs"])["go-test"])["strategy"])["matrix"])["shard"] = []any{1}
	})
	add("write-permissions", "ci.yml", "read-only token permissions", func(w map[string]any) { w["permissions"] = map[string]any{"contents": "write"} })
	for name, raw := range map[string]func([]byte) []byte{
		"invalid-yaml-tab": func(body []byte) []byte { return []byte(strings.Replace(string(body), "  push:", "\tpush:", 1)) },
		"comment-only-push-filter": func(body []byte) []byte {
			return []byte(strings.Replace(string(body), "    branches: [main]", "    # branches: [main]", 1))
		},
		"duplicate-cancel-key": func(body []byte) []byte {
			return []byte(strings.Replace(string(body), "  cancel-in-progress:", "  cancel-in-progress: true\n  cancel-in-progress:", 1))
		},
		"duplicate-concurrency-key": func(body []byte) []byte {
			return append(append([]byte(nil), body...), []byte("\nconcurrency: {group: shared, cancel-in-progress: true}\n")...)
		},
	} {
		want := "already defined"
		if name == "invalid-yaml-tab" {
			want = "yaml:"
		} else if name == "comment-only-push-filter" {
			want = "must not duplicate PR CI"
		}
		cases = append(cases, mutation{name: name, file: "ci.yml", want: want, raw: raw})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyPolicyWorkflows(t, workflows)
			body, exists := workflows[tc.file]
			if !exists {
				body = []byte("on:\n  pull_request:\n    paths: ['src/**']\njobs:\n  tests:\n    runs-on: ubuntu-latest\n")
			}
			if tc.raw != nil {
				body = tc.raw(body)
			} else {
				workflow := parse(body)
				tc.edit(workflow)
				var err error
				body, err = yaml.Marshal(workflow)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, tc.file), body, 0600); err != nil {
				t.Fatal(err)
			}
			problems, err := checkDirectory(dir)
			got := strings.Join(problems, "\n")
			if err != nil {
				got += err.Error()
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("mutation must fail with %q; got %q", tc.want, got)
			}
		})
	}
}

func readPolicyWorkflows(t *testing.T) map[string][]byte {
	t.Helper()
	dir := "../../.github/workflows"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	workflows := map[string][]byte{}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		workflows[entry.Name()] = body
	}
	return workflows
}

func copyPolicyWorkflows(t *testing.T, workflows map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range workflows {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWorkflowPolicyAllowsReviewedWorkflowsAndFilteredPRs(t *testing.T) {
	workflows := readPolicyWorkflows(t)
	for _, filter := range []string{"paths", "paths-ignore"} {
		t.Run(filter, func(t *testing.T) {
			dir := copyPolicyWorkflows(t, workflows)
			body := "on:\n  pull_request:\n    " + filter + ": ['docs/**']\njobs:\n  documentation:\n    runs-on: ubuntu-latest\n"
			if err := os.WriteFile(filepath.Join(dir, "extra.yaml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			problems, err := checkDirectory(dir)
			if err != nil || len(problems) != 0 {
				t.Fatalf("reviewed directory and filtered PR rejected: %v %v", problems, err)
			}
		})
	}
}

// Execute the actual YAML gate commands. A fabricated green/skipped fixture
// must not conceal a failed, cancelled or unexpectedly omitted dependency.
func TestCIClassifiedAggregateResults(t *testing.T) {
	var workflow map[string]any
	if err := yaml.Unmarshal(readPolicyWorkflows(t)["ci.yml"], &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := mapping(workflow["jobs"])
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"go", "web", "release-check", "e2e"} {
		steps := mapping(jobs[id])["steps"].([]any)
		step := mapping(steps[len(steps)-1])
		run, ok := step["run"].(string)
		if !ok {
			t.Fatalf("%s has no executable gate", id)
		}
		for _, lane := range []string{"docs-only", "spec-only", "full"} {
			t.Run(id+"/"+lane, func(t *testing.T) {
				values := map[string]string{"CI_LANE": lane, "CI_PLAN": "success", "REUSE": "none", "REUSE_PROOF": "skipped", "SOURCE_RUN": "", "CACHE_PRIME": "skipped"}
				for name := range mapping(step["env"]) {
					if name == "CI_LANE" || name == "CI_PLAN" || name == "REUSE" || name == "REUSE_PROOF" || name == "SOURCE_RUN" || name == "CACHE_PRIME" {
						continue
					}
					if name == "TIER_PLAN" {
						values[name] = "success"
						continue
					}
					if name == "TIER_LAYOUT" {
						values[name] = "full"
						continue
					}
					values[name] = "skipped"
					if lane == "full" || lane == "spec-only" && (name == "WEB_SETUP" || name == "WEB_UNIT" || name == "WEB_SHARD") {
						values[name] = "success"
					}
				}
				execute := func(changed, result string) error {
					t.Helper()
					cmd := exec.Command("bash", "-c", run)
					cmd.Dir = root
					cmd.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+filepath.Join(t.TempDir(), "summary"))
					for name, value := range values {
						if name == changed {
							value = result
						}
						cmd.Env = append(cmd.Env, name+"="+value)
					}
					return cmd.Run()
				}
				if err := execute("", ""); err != nil {
					t.Fatalf("legitimate %s gate rejected: %v", lane, err)
				}
				for name, expected := range values {
					if name == "CI_LANE" || name == "REUSE" || name == "REUSE_PROOF" || name == "SOURCE_RUN" || name == "CACHE_PRIME" || name == "TIER_LAYOUT" {
						continue
					}
					wrong := "success"
					if expected == "success" {
						wrong = "skipped"
					}
					for _, result := range []string{"failure", "cancelled", wrong} {
						if err := execute(name, result); err == nil {
							t.Fatalf("gate accepted %s=%s (expected %s)", name, result, expected)
						}
					}
				}
				if err := execute("CI_LANE", "unknown"); err == nil {
					t.Fatal("unknown classification accepted")
				}
			})
		}
	}
}

func TestCIClassifiedWebShardMatrix(t *testing.T) {
	var workflow map[string]any
	if err := yaml.Unmarshal(readPolicyWorkflows(t)["ci.yml"], &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := mapping(workflow["jobs"])
	shard := mapping(jobs["web-shard"])
	expression := mapping(mapping(shard["strategy"])["matrix"])["shard"].(string)
	expression = strings.TrimPrefix(expression, "${{ fromJSON(")
	expression = strings.TrimSuffix(expression, ") }}")
	for _, lane := range []string{"full", "spec-only"} {
		for _, event := range []string{"pull_request", "merge_group", "push", "workflow_dispatch"} {
			for _, mode := range []string{"full", "essential"} {
				const premerge = `contains(fromJSON('["pull_request","merge_group"]'), github.event_name)`
				got := expandConcurrency(t, "${{ "+expression+" }}", map[string]string{"needs.tier-plan.outputs.lane": lane, "needs.tier-plan.outputs.mode": mode, premerge: map[bool]string{true: "yes", false: ""}[event == "pull_request" || event == "merge_group"]})
				want := "[1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12]"
				if lane == "spec-only" {
					want = "[1]"
				} else if mode == "essential" && (event == "pull_request" || event == "merge_group") {
					want = "[1, 2]"
				}
				if got != want {
					t.Fatalf("%s/%s/%s matrix = %s, want %s", lane, event, mode, got, want)
				}
			}
		}
	}
	// Full history is needed only for the lightweight PR classifier; retain
	// the existing previous-release checkout used by migration compatibility.
	planSteps := mapping(jobs["ci-plan"])["steps"].([]any)
	checkout := mapping(mapping(planSteps[0])["with"])
	if checkout["fetch-depth"] != "${{ github.event_name == 'pull_request' && '0' || '1' }}" || checkout["persist-credentials"] != false {
		t.Fatal("classifier checkout must keep PR-only history and no persisted credentials")
	}
	for _, id := range []string{"go-test", "go-static", "go-timing", "web-setup", "web-unit", "web-shard", "release-check-run", "e2e-run"} {
		steps := mapping(jobs[id])["steps"].([]any)
		if depth := mapping(mapping(steps[0])["with"])["fetch-depth"]; depth != nil {
			t.Fatalf("%s heavy checkout must retain shallow history; got %v", id, depth)
		}
	}
}

func TestCIMigrationCompatibilityIndependentOfClassification(t *testing.T) {
	var workflow map[string]any
	if err := yaml.Unmarshal(readPolicyWorkflows(t)["ci.yml"], &workflow); err != nil {
		t.Fatal(err)
	}
	job := mapping(mapping(workflow["jobs"])["migration-compat"])
	if job == nil {
		t.Fatal("migration compatibility job is missing")
	}
	for _, key := range []string{"if", "needs"} {
		if _, exists := job[key]; exists {
			t.Fatalf("migration compatibility must run independently of classification; found %s", key)
		}
	}
}

func TestCIPlanUsesTrustedBaseClassifier(t *testing.T) {
	var workflow map[string]any
	if err := yaml.Unmarshal(readPolicyWorkflows(t)["ci.yml"], &workflow); err != nil {
		t.Fatal(err)
	}
	steps := mapping(mapping(workflow["jobs"])["ci-plan"])["steps"].([]any)
	for _, value := range steps {
		step := mapping(value)
		if step["id"] != "plan" {
			continue
		}
		if mapping(step["env"])["PR_BASE_SHA"] != "${{ github.event.pull_request.base.sha }}" {
			t.Fatal("classifier must bind its trusted source to the PR base commit")
		}
		run, _ := step["run"].(string)
		for _, required := range []string{`git show "$PR_BASE_SHA:scripts/ci-pr-plan.mjs"`, `node "$RUNNER_TEMP/ci-pr-plan.mjs"`} {
			if !strings.Contains(run, required) {
				t.Fatalf("classifier must load and execute the base copy: missing %s", required)
			}
		}
		if strings.Contains(run, "node scripts/ci-pr-plan.mjs") {
			t.Fatal("classifier must never execute the PR checkout's script")
		}
		return
	}
	t.Fatal("classifier step is missing")
}

// OPS-257 L4 phase 2: the static layout is a PR-only feedback lane. Its
// aggregates accept exactly the skipped shard/timing/smoke jobs and still
// require static checks, setup and units; any other layout value is rejected.
func TestCIStaticLayoutAggregatesAndJobGates(t *testing.T) {
	var workflow map[string]any
	if err := yaml.Unmarshal(readPolicyWorkflows(t)["ci.yml"], &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := mapping(workflow["jobs"])
	if mapping(mapping(jobs["tier-plan"])["outputs"])["layout"] != "${{ steps.tiers.outputs.layout }}" {
		t.Fatal("tier-plan must publish the layout output")
	}
	for id, static := range map[string]bool{"go-test": true, "go-timing": true, "web-shard": true, "e2e-run": true, "go-static": false, "web-setup": false, "web-unit": false, "release-check-run": false} {
		condition, _ := mapping(jobs[id])["if"].(string)
		if strings.Contains(condition, "needs.tier-plan.outputs.layout != 'static'") != static {
			t.Fatalf("%s static-layout gate = %v, want %v", id, !static, static)
		}
	}
	if _, exists := mapping(jobs["migration-compat"])["if"]; exists {
		t.Fatal("migration-compat must stay unconditional in the static layout")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	type gate struct {
		accepted map[string]string
		rejected []map[string]string
	}
	cases := map[string]gate{
		"go": {
			accepted: map[string]string{"GO_TEST": "skipped", "GO_TIMING": "skipped", "GO_STATIC": "success"},
			rejected: []map[string]string{{"GO_TEST": "success"}, {"GO_TEST": "failure"}, {"GO_TIMING": "success"}, {"GO_STATIC": "skipped"}, {"GO_STATIC": "failure"}, {"GITHUB_EVENT_NAME": "merge_group"}, {"GITHUB_EVENT_NAME": "push"}, {"CI_LANE": "spec-only"}, {"TIER_LAYOUT": "bogus"}, {"TIER_LAYOUT": ""}, {"TIER_PLAN": "failure"}},
		},
		"web": {
			accepted: map[string]string{"WEB_SETUP": "success", "WEB_UNIT": "success", "WEB_SHARD": "skipped"},
			rejected: []map[string]string{{"WEB_SHARD": "success"}, {"WEB_SHARD": "failure"}, {"WEB_UNIT": "skipped"}, {"WEB_UNIT": "failure"}, {"WEB_SETUP": "skipped"}, {"GITHUB_EVENT_NAME": "merge_group"}, {"TIER_LAYOUT": "bogus"}, {"TIER_LAYOUT": ""}},
		},
		"e2e": {
			accepted: map[string]string{"CHECK_RESULT": "skipped"},
			rejected: []map[string]string{{"CHECK_RESULT": "success"}, {"CHECK_RESULT": "failure"}, {"GITHUB_EVENT_NAME": "merge_group"}, {"TIER_LAYOUT": "bogus"}, {"TIER_LAYOUT": ""}},
		},
	}
	for id, c := range cases {
		steps := mapping(jobs[id])["steps"].([]any)
		step := mapping(steps[len(steps)-1])
		run := step["run"].(string)
		if _, exists := mapping(step["env"])["TIER_LAYOUT"]; !exists {
			t.Fatalf("%s must read the tier layout", id)
		}
		execute := func(overrides map[string]string) error {
			values := map[string]string{"CI_LANE": "full", "CI_PLAN": "success", "TIER_PLAN": "success", "TIER_LAYOUT": "static", "REUSE": "none", "REUSE_PROOF": "skipped", "SOURCE_RUN": "", "CACHE_PRIME": "skipped", "GITHUB_EVENT_NAME": "pull_request"}
			for name, value := range c.accepted {
				values[name] = value
			}
			for name, value := range overrides {
				values[name] = value
			}
			cmd := exec.Command("bash", "-c", run)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+filepath.Join(t.TempDir(), "summary"))
			for name, value := range values {
				cmd.Env = append(cmd.Env, name+"="+value)
			}
			return cmd.Run()
		}
		if err := execute(nil); err != nil {
			t.Fatalf("%s rejected the legitimate static layout: %v", id, err)
		}
		for _, overrides := range c.rejected {
			if err := execute(overrides); err == nil {
				t.Fatalf("%s accepted %v in the static layout", id, overrides)
			}
		}
	}
	// release-check has no layout input: the static lane keeps release checks.
	if _, exists := mapping(mapping(mapping(jobs["release-check"])["steps"].([]any)[0])["env"])["TIER_LAYOUT"]; exists {
		t.Fatal("release checks must not vary with the tier layout")
	}
}
