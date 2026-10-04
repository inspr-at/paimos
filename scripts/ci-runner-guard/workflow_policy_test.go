// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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

	// All contexts and supporting job identities are reserved, even on a
	// path-filtered workflow, and even when a different id sets a reserved name.
	for _, id := range []string{"go", "web", "release-check", "e2e", "go-test", "go-static", "go-timing", "runner-route", "cross-family", "gate/cross-family"} {
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
	for _, file := range []string{"extra.yaml", "test-runner-route.yml", "release-image-check.yml", "release.yml", "homebrew-tap.yml"} {
		add("copied-ci-group/"+file, file, "must not copy ci.yml", func(w map[string]any) {
			w["concurrency"] = map[string]any{"group": ciGroup, "cancel-in-progress": true}
		})
	}
	for index, concurrency := range []any{shared, "shared", nil} {
		add(fmt.Sprintf("unknown-workflow-concurrency/%d", index), "extra.yaml", "workflow-level concurrency is not allowlisted", func(w map[string]any) { w["concurrency"] = concurrency })
		add(fmt.Sprintf("reusable-workflow-concurrency/%d", index), "test-runner-route.yml", "reusable workflows must not define concurrency", func(w map[string]any) { w["concurrency"] = concurrency })
	}
	for _, file := range []string{"ci.yml", "release.yml", "homebrew-tap.yml", "release-image-check.yml"} {
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
	add("cancel-main-and-queue", "ci.yml", "reviewed concurrency policy", func(w map[string]any) {
		mapping(w["concurrency"])["cancel-in-progress"] = "${{ contains(fromJSON('[\"pull_request\",\"push\",\"merge_group\"]'), github.event_name) }}"
	})
	add("non-pr-shared-group", "ci.yml", "reviewed concurrency policy", func(w map[string]any) {
		mapping(w["concurrency"])["group"] = "ci-${{ github.event_name }}-${{ github.event.pull_request.number || github.ref }}"
	})
	for _, id := range []string{"go", "web", "release-check", "e2e", "go-test", "go-static", "go-timing", "runner-route"} {
		for _, rename := range []bool{false, true} {
			add(fmt.Sprintf("missing-or-renamed/%s/%t", id, rename), "ci.yml", "is missing", func(w map[string]any) {
				jobs := mapping(w["jobs"])
				if rename {
					jobs[id+"-renamed"] = jobs[id]
				}
				delete(jobs, id)
			})
		}
		if id == "runner-route" || id == "go-test" {
			continue
		}
		for _, condition := range []any{false, "success()", nil} {
			add(fmt.Sprintf("conditional/%s/%v", id, condition), "ci.yml", "must", func(w map[string]any) { mapping(mapping(w["jobs"])[id])["if"] = condition })
		}
	}
	for _, id := range []string{"go", "web", "release-check", "e2e"} {
		add("renamed-check-context/"+id, "ci.yml", "renamed to", func(w map[string]any) { mapping(mapping(w["jobs"])[id])["name"] = "other" })
	}
	add("missing-go-dependency", "ci.yml", "go must gate every", func(w map[string]any) { mapping(mapping(w["jobs"])["go"])["needs"] = []any{"go-test", "go-timing"} })
	add("missing-web-dependency", "ci.yml", "web must gate setup and every UI shard", func(w map[string]any) { mapping(mapping(w["jobs"])["web"])["needs"] = []any{"web-setup"} })
	add("web-skipped-on-failure", "ci.yml", "web must report failures", func(w map[string]any) { delete(mapping(mapping(w["jobs"])["web"]), "if") })
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
