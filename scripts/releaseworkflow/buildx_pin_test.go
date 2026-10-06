// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"crypto/sha256"
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
	"AEON_BUILDX_VERSION":      "v0.37.1",
	"AEON_BUILDX_SHA256_AMD64": "9447199cdb435f25880548343c128a4b6650e8891ee598905d8d29d39a8e359b",
	"AEON_BUILDX_SHA256_ARM64": "e5cc9fe3bbff5cbc91230981f7860e06076110730a2db997082652199042a1f2",
	"AEON_BUILDKIT_VERSION":    "v0.33.1",
	"AEON_BUILDKIT_IMAGE":      "moby/buildkit:v0.33.1@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea",
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
				want := map[string]string{"version": "${{ env.AEON_BUILDX_VERSION }}", "cache-binary": "false", "driver": "docker-container", "driver-opts": "image=${{ env.AEON_BUILDKIT_IMAGE }}"}
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
				// The checksum comparison must precede the first plugin invocation.
				checksum := strings.Index(guard.Run, `if [[ "$actual_sha" != "$expected_sha" ]]`)
				command := strings.Index(guard.Run, "docker buildx ")
				if !strings.Contains(guard.Run, `sha256sum -- "$plugin"`) || checksum < 0 || command < 0 || checksum > command {
					problems = append(problems, label+": checksum assertion missing or late")
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
		for _, mutation := range []string{"version", "image", "action", "driver", "assertion", "skip", "ignore", "override", "extra", "removed", "timeout", "workflow pin", "cache missing", "cache true", "checksum missing", "checksum short", "checksum long", "arm checksum missing", "arm checksum short", "arm checksum long", "checksum removed", "checksum moved", "guard removed", "guard moved"} {
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
				case "cache missing":
					delete(j.Steps[at].With, "cache-binary")
				case "cache true":
					j.Steps[at].With["cache-binary"] = "true"
				case "checksum missing", "checksum short", "checksum long", "arm checksum missing", "arm checksum short", "arm checksum long":
					key := "AEON_BUILDX_SHA256_AMD64"
					if strings.HasPrefix(mutation, "arm ") {
						key = "AEON_BUILDX_SHA256_ARM64"
					}
					mutation = strings.TrimPrefix(mutation, "arm ")
					if mutation == "checksum missing" {
						delete(w.Env, key)
					} else if mutation == "checksum short" {
						w.Env[key] = strings.Repeat("a", 63)
					} else {
						w.Env[key] = strings.Repeat("a", 65)
					}
					wantProblem = "wrong " + key
				case "checksum removed", "checksum moved":
					run := j.Steps[at+1].Run
					start, end := strings.Index(run, "# Resolve Docker CLI"), strings.Index(run, `if ! buildx=`)
					if start < 0 || end <= start {
						t.Fatal("fixture has no checksum block")
					}
					j.Steps[at+1].Run = run[:start] + run[end:]
					if mutation == "checksum moved" {
						j.Steps[at+1].Run += run[start:end]
					}
					wantProblem = "checksum assertion missing or late"
				case "guard removed":
					j.Steps = append(j.Steps[:at+1], j.Steps[at+2:]...)
					wantProblem = "missing immediate version assertion"
				case "guard moved":
					j.Steps[at+1], j.Steps[at+2] = step{Run: "docker buildx version"}, j.Steps[at+1]
					wantProblem = "missing immediate version assertion"
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
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			plugin := filepath.Join(dir, ".docker", "cli-plugins", "docker-buildx")
			if err := os.MkdirAll(filepath.Dir(plugin), 0700); err != nil {
				t.Fatal(err)
			}
			data := []byte("fixture buildx binary")
			if err := os.WriteFile(plugin, data, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "uname"), []byte("#!/bin/bash\nprintf 'x86_64\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			// This stub proves the workflow assertion without invoking Docker.
			stub := fmt.Sprintf("#!/bin/bash\ncase \"$*\" in\n 'buildx version') printf '%%s\\n' \"$FIXTURE_BUILDX\"; exit %d;;\n 'buildx inspect --bootstrap') printf '%%s\\n' \"$FIXTURE_INSPECT\"; exit %d;;\n *) exit 99;;\nesac\n", tc.versionExit, tc.inspectExit)
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", guard.Run)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURE_BUILDX="+tc.buildx, "FIXTURE_INSPECT="+tc.inspect, "HOME="+dir, "DOCKER_CONFIG="+filepath.Join(dir, ".docker"))
			for key, value := range builderPins {
				if key == "AEON_BUILDX_SHA256_AMD64" {
					value = fmt.Sprintf("%x", sha256.Sum256(data))
				}
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

func TestBuilderChecksumAndPluginPathFailClosed(t *testing.T) {
	_, guard := named(t, readWorkflow(t, "release.yml").Jobs["image-platform"], builderAssertion)
	for _, tc := range []struct {
		name, arch, failure string
	}{
		{"amd64", "x86_64", ""},
		{"arm64", "aarch64", ""},
		{"unsupported arch", "arm64", "unsupported Buildx runner architecture"},
		{"tampered", "x86_64", "expected SHA256"},
		{"wrong arm checksum", "aarch64", "expected SHA256"},
		{"missing checksum", "x86_64", "invalid pinned Buildx SHA256"},
		{"short checksum", "x86_64", "invalid pinned Buildx SHA256"},
		{"long checksum", "aarch64", "invalid pinned Buildx SHA256"},
		{"missing plugin", "x86_64", "Buildx plugin path assertion failed"},
		{"symlink plugin", "x86_64", "Buildx plugin path assertion failed"},
		{"symlink directory", "x86_64", "Buildx plugin path assertion failed"},
		{"nonexecutable plugin", "x86_64", "Buildx plugin path assertion failed"},
		{"extra dir shadow", "x86_64", "Buildx plugin path assertion failed"},
		{"broken symlink shadow", "x86_64", "Buildx plugin path assertion failed"},
		{"unused extra dir", "x86_64", ""},
		{"alternate config shadow", "x86_64", "Buildx plugin path assertion failed"},
		{"alternate config missing", "x86_64", "Buildx plugin path assertion failed"},
		{"malformed config", "x86_64", "Buildx plugin path assertion failed"},
		{"invalid extra dirs", "x86_64", "Buildx plugin path assertion failed"},
		{"checksum command fails", "x86_64", "actual unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			write := func(name, data string, mode os.FileMode) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte(data), mode); err != nil {
					t.Fatal(err)
				}
			}
			link := func(target, name string) {
				t.Helper()
				if err := os.Symlink(target, name); err != nil {
					t.Fatal(err)
				}
			}
			plugin := filepath.Join(dir, ".docker", "cli-plugins", "docker-buildx")
			configDir := filepath.Join(dir, ".docker")
			amd, arm := "fixture amd64 binary", "fixture arm64 binary"
			amdHash, armHash := fmt.Sprintf("%x", sha256.Sum256([]byte(amd))), fmt.Sprintf("%x", sha256.Sum256([]byte(arm)))
			data := amd
			if tc.arch == "aarch64" {
				data = arm
			}
			if tc.name != "missing plugin" && tc.name != "symlink plugin" && tc.name != "symlink directory" {
				write(plugin, data, 0700)
			}
			switch tc.name {
			case "tampered":
				write(plugin, "tampered binary", 0700)
			case "wrong arm checksum":
				armHash = amdHash
			case "missing checksum":
				amdHash = ""
			case "short checksum":
				amdHash = strings.Repeat("a", 63)
			case "long checksum":
				armHash = strings.Repeat("a", 65)
			case "symlink plugin":
				other := filepath.Join(dir, "other-buildx")
				write(other, data, 0700)
				if err := os.MkdirAll(filepath.Dir(plugin), 0700); err != nil {
					t.Fatal(err)
				}
				link(other, plugin)
			case "symlink directory":
				other := filepath.Join(dir, "plugins")
				write(filepath.Join(other, "docker-buildx"), data, 0700)
				if err := os.MkdirAll(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				link(other, filepath.Dir(plugin))
			case "nonexecutable plugin":
				if err := os.Chmod(plugin, 0600); err != nil {
					t.Fatal(err)
				}
			case "extra dir shadow", "unused extra dir", "broken symlink shadow":
				extra := filepath.Join(dir, "extra")
				if err := os.MkdirAll(extra, 0700); err != nil {
					t.Fatal(err)
				}
				if tc.name == "extra dir shadow" {
					write(filepath.Join(extra, "docker-buildx"), data, 0700)
				}
				if tc.name == "broken symlink shadow" {
					link(filepath.Join(extra, "missing"), filepath.Join(extra, "docker-buildx"))
				}
				write(filepath.Join(configDir, "config.json"), fmt.Sprintf(`{"cliPluginsExtraDirs":[%q]}`, extra), 0600)
			case "alternate config shadow", "alternate config missing":
				configDir = filepath.Join(dir, "alternate")
				if tc.name == "alternate config shadow" {
					write(filepath.Join(configDir, "cli-plugins", "docker-buildx"), data, 0700)
				}
			case "malformed config":
				write(filepath.Join(configDir, "config.json"), "{invalid", 0600)
			case "invalid extra dirs":
				write(filepath.Join(configDir, "config.json"), `{"cliPluginsExtraDirs":"bad"}`, 0600)
			case "checksum command fails":
				write(filepath.Join(dir, "sha256sum"), "#!/bin/bash\nexit 1\n", 0700)
			}
			write(filepath.Join(dir, "uname"), "#!/bin/bash\nprintf '%s\\n' \"$FIXTURE_ARCH\"\n", 0700)
			// A command log proves invalid bytes/paths never reach any Docker command.
			log := filepath.Join(dir, "docker-calls")
			write(filepath.Join(dir, "docker"), `#!/bin/bash
printf '%s\n' "$*" >> "$FIXTURE_CALLS"
case "$*" in
  'buildx version') printf 'github.com/docker/buildx v0.37.1 fixture\n';;
  'buildx inspect --bootstrap') printf 'BuildKit version: v0.33.1\n';;
  *) exit 99;;
esac
`, 0700)
			cmd := exec.Command("bash", "-c", guard.Run)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "HOME="+dir, "DOCKER_CONFIG="+configDir,
				"FIXTURE_ARCH="+tc.arch, "FIXTURE_CALLS="+log)
			for key, value := range builderPins {
				if key == "AEON_BUILDX_SHA256_AMD64" {
					value = amdHash
				}
				if key == "AEON_BUILDX_SHA256_ARM64" {
					value = armHash
				}
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if tc.failure != "" {
				if err == nil || !strings.Contains(string(output), tc.failure) {
					t.Fatalf("expected %q, got %v: %s", tc.failure, err, output)
				}
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("Docker invoked before checksum/path rejection: %v", err)
				}
				if tc.failure == "expected SHA256" && !strings.Contains(string(output), ", actual ") {
					t.Fatalf("missing actual checksum: %s", output)
				}
			} else {
				if err != nil {
					t.Fatalf("valid plugin rejected: %v: %s", err, output)
				}
				calls, err := os.ReadFile(log)
				if err != nil || string(calls) != "buildx version\nbuildx inspect --bootstrap\n" {
					t.Fatalf("unexpected Docker calls: %q: %v", calls, err)
				}
			}
		})
	}
}
