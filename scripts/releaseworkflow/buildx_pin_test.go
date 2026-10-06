// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// One reviewed expectation for every tag and rehearsal builder.
var builderPins = map[string]string{
	"AEON_BUILDX_VERSION":   "v0.37.1",
	"AEON_BUILDKIT_VERSION": "v0.33.1",
	"AEON_BUILDKIT_IMAGE":   "moby/buildkit:v0.33.1@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea",
}

const builderAction = "docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069"
const builderAssertion = "Assert pinned Buildx and BuildKit versions"

func builderPinProblems(release, dry workflow) []string {
	var problems []string
	var reference *step
	for _, pair := range []struct {
		name string
		w    workflow
		jobs map[string]int
	}{
		{"release", release, map[string]int{"image-platform": 1, "image": 1}},
		{"rehearsal", dry, map[string]int{"image-dry-run": 1}},
	} {
		for key, value := range builderPins {
			if pair.w.Env[key] != value {
				problems = append(problems, pair.name+": wrong "+key)
			}
		}
		counts := map[string]int{}
		for id, j := range pair.w.Jobs {
			for key := range builderPins {
				if _, ok := j.Env[key]; ok {
					problems = append(problems, pair.name+"/"+id+": job overrides "+key)
				}
			}
			for i, s := range j.Steps {
				for key := range builderPins {
					if _, ok := s.Env[key]; ok {
						problems = append(problems, pair.name+"/"+id+": step overrides "+key)
					}
				}
				if !strings.HasPrefix(s.Uses, "docker/setup-buildx-action@") {
					continue
				}
				counts[id]++
				label := pair.name + "/" + id
				want := map[string]string{"version": "${{ env.AEON_BUILDX_VERSION }}", "driver": "docker-container", "driver-opts": "image=${{ env.AEON_BUILDKIT_IMAGE }}"}
				if s.Uses != builderAction || !reflect.DeepEqual(s.With, want) || s.If != "" || s.ContinueOnError {
					problems = append(problems, label+": builder setup is not pinned and mandatory")
				}
				if i+1 >= len(j.Steps) || j.Steps[i+1].Name != builderAssertion {
					problems = append(problems, label+": missing immediate version assertion")
					continue
				}
				guard := j.Steps[i+1]
				if guard.If != "" || guard.ContinueOnError || guard.Uses != "" || len(guard.Env) != 0 || guard.Run == "" {
					problems = append(problems, label+": version assertion can be bypassed")
				}
				if reference == nil {
					reference = &guard
				} else if !reflect.DeepEqual(*reference, guard) {
					problems = append(problems, label+": version assertion differs")
				}
			}
		}
		if !reflect.DeepEqual(counts, pair.jobs) {
			problems = append(problems, pair.name+": unexpected builder setup inventory")
		}
	}
	if release.Jobs["image-platform"].TimeoutMinutes != 45 || dry.Jobs["image-dry-run"].TimeoutMinutes != 45 {
		problems = append(problems, "platform tag and rehearsal budgets must both be 45 minutes")
	}
	return problems
}

func TestRehearsedBuilderPins(t *testing.T) {
	if problems := builderPinProblems(readWorkflow(t, "release.yml"), readWorkflow(t, "release-image-check.yml")); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	for _, target := range []struct{ file, job string }{{"release.yml", "image-platform"}, {"release.yml", "image"}, {"release-image-check.yml", "image-dry-run"}} {
		for _, mutation := range []string{"version", "image", "action", "driver", "assertion", "skip", "ignore", "override", "extra", "removed", "timeout", "workflow pin"} {
			t.Run(target.job+"/"+mutation, func(t *testing.T) {
				release, dry := readWorkflow(t, "release.yml"), readWorkflow(t, "release-image-check.yml")
				w := &release
				if target.file == "release-image-check.yml" {
					w = &dry
				}
				j := w.Jobs[target.job]
				at := -1
				for i, s := range j.Steps {
					if s.Uses == builderAction {
						at = i
						break
					}
				}
				if at < 0 {
					t.Fatal("fixture has no builder setup")
				}
				wantProblem := "builder setup is not pinned and mandatory"
				switch mutation {
				case "version":
					delete(j.Steps[at].With, "version")
				case "image":
					j.Steps[at].With["driver-opts"] = "image=moby/buildkit:buildx-stable-1"
				case "action":
					j.Steps[at].Uses = "docker/setup-buildx-action@v4"
				case "driver":
					j.Steps[at].With["driver"] = "docker"
				case "assertion":
					j.Steps[at+1].Run = "true"
					wantProblem = "version assertion differs"
				case "skip":
					j.Steps[at+1].If = "false"
					wantProblem = "version assertion can be bypassed"
				case "ignore":
					j.Steps[at+1].ContinueOnError = true
					wantProblem = "version assertion can be bypassed"
				case "override":
					j.Env = map[string]string{"AEON_BUILDX_VERSION": "latest"}
					wantProblem = "job overrides AEON_BUILDX_VERSION"
				case "extra":
					j.Steps = append(j.Steps, j.Steps[at])
					wantProblem = "unexpected builder setup inventory"
				case "removed":
					j.Steps = append(j.Steps[:at], j.Steps[at+1:]...)
					wantProblem = "unexpected builder setup inventory"
				case "timeout":
					if target.job == "image" {
						return
					} // Only platform jobs share the budget.
					j.TimeoutMinutes = 30
					wantProblem = "budgets must both be 45 minutes"
				case "workflow pin":
					w.Env["AEON_BUILDKIT_IMAGE"] = "moby/buildkit:buildx-stable-1"
					wantProblem = "wrong AEON_BUILDKIT_IMAGE"
				}
				w.Jobs[target.job] = j
				if problems := builderPinProblems(release, dry); !strings.Contains(strings.Join(problems, "\n"), wantProblem) {
					t.Fatalf("expected %q, got %v", wantProblem, problems)
				}
			})
		}
	}
}

func TestBuilderVersionAssertionFailsClosed(t *testing.T) {
	_, guard := named(t, readWorkflow(t, "release.yml").Jobs["image-platform"], builderAssertion)
	for _, tc := range []struct {
		name, buildx, inspect    string
		versionExit, inspectExit int
		ok                       bool
	}{
		{"pinned", "github.com/docker/buildx v0.37.1 0b265a9", "BuildKit version: v0.33.1", 0, 0, true},
		{"all nodes pinned", "github.com/docker/buildx v0.37.1 hash", "BuildKit version: v0.33.1\nBuildKit version: v0.33.1", 0, 0, true},
		{"wrong Buildx", "github.com/docker/buildx v0.37.2 hash", "BuildKit version: v0.33.1", 0, 0, false},
		{"wrong BuildKit", "github.com/docker/buildx v0.37.1 hash", "BuildKit version: v0.33.2", 0, 0, false},
		{"mixed nodes", "github.com/docker/buildx v0.37.1 hash", "BuildKit version: v0.33.1\nBuildKit version: v0.33.2", 0, 0, false},
		{"no BuildKit", "github.com/docker/buildx v0.37.1 hash", "Name: builder", 0, 0, false},
		{"no Buildx", "", "BuildKit version: v0.33.1", 0, 0, false},
		{"suffix", "github.com/docker/buildx v0.37.1 hash", "BuildKit version: v0.33.1-dev", 0, 0, false},
		{"version command fails", "github.com/docker/buildx v0.37.1 hash", "BuildKit version: v0.33.1", 1, 0, false},
		{"bootstrap fails", "github.com/docker/buildx v0.37.1 hash", "BuildKit version: v0.33.1", 0, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// This stub proves the workflow assertion without invoking Docker.
			stub := fmt.Sprintf("#!/bin/bash\ncase \"$*\" in\n 'buildx version') printf '%%s\\n' \"$FIXTURE_BUILDX\"; exit %d;;\n 'buildx inspect --bootstrap') printf '%%s\\n' \"$FIXTURE_INSPECT\"; exit %d;;\n *) exit 99;;\nesac\n", tc.versionExit, tc.inspectExit)
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", guard.Run)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURE_BUILDX="+tc.buildx, "FIXTURE_INSPECT="+tc.inspect)
			for key, value := range builderPins {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("assertion result: %v: %s", err, output)
			}
			if !tc.ok && !strings.Contains(string(output), "Expected Buildx v0.37.1 and BuildKit v0.33.1") {
				t.Fatalf("missing expected-version error: %s", output)
			}
		})
	}
}
