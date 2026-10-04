// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
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

type coverageEntry struct{ Step, SHA256, Mode, Counterpart string }

// The step fingerprint covers the complete YAML object, including conditions,
// permissions inputs, env and action SHA. Additions and edits need a reviewed
// counterpart in the inventory, not just another name in a job allowlist.
func coverageProblems(data, inventory []byte, dry workflow, tests string) []string {
	var source struct {
		Jobs map[string]struct{ Steps []map[string]any }
	}
	var coverage map[string][]coverageEntry
	if yaml.Unmarshal(data, &source) != nil || json.Unmarshal(inventory, &coverage) != nil {
		return []string{"invalid coverage input"}
	}
	var problems []string
	if len(source.Jobs) != len(coverage) {
		problems = append(problems, "release job inventory differs")
	}
	for id, job := range source.Jobs {
		entries := coverage[id]
		if len(job.Steps) != len(entries) {
			problems = append(problems, id+": release step inventory differs")
			continue
		}
		for i, sourceStep := range job.Steps {
			entry := entries[i]
			name, _ := sourceStep["name"].(string)
			if name == "" {
				name, _ = sourceStep["uses"].(string)
			}
			body, _ := json.Marshal(sourceStep)
			if entry.Step != name || entry.SHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) {
				problems = append(problems, id+"/"+name+": counterpart needs review")
			}
			found := false
			switch entry.Mode {
			case "step":
				jobID, stepName, ok := strings.Cut(entry.Counterpart, "/")
				if ok {
					for _, s := range dry.Jobs[jobID].Steps {
						found = found || s.Name == stepName
					}
				}
			case "action":
				for _, job := range dry.Jobs {
					for _, s := range job.Steps {
						found = found || s.Uses == entry.Counterpart
					}
				}
			case "test":
				found = strings.Contains(tests, "func "+entry.Counterpart+"(t *testing.T)")
			case "disabled":
				found = entry.Counterpart != "" && (name == "Sign and notarize paimos-agentd" || name == "Remove signing keychain and temp files" || name == "Attest pushed image" || strings.HasPrefix(name, "docker/login-action@"))
			}
			if !found {
				problems = append(problems, id+"/"+name+": no executable counterpart or approved disabled operation")
			}
		}
	}
	return problems
}

func TestEveryReleaseStepHasReviewedRehearsalCounterpart(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := os.ReadFile(filepath.Join(root(t), "scripts/release-rehearsal-coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	var tests strings.Builder
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		tests.Write(b)
	}
	dry := readWorkflow(t, "release-image-check.yml")
	if problems := coverageProblems(data, inventory, dry, tests.String()); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	// Negative fixtures prove the inventory actually detects additions/changes.
	for _, mutation := range []string{
		strings.Replace(string(data), "jobs:\n", "jobs:\n  extra:\n    runs-on: ubuntu-latest\n    steps: [{run: 'echo untested'}]\n", 1),
		strings.Replace(string(data), "      - name: Release checks\n", "      - name: New release step\n        run: echo untested\n      - name: Release checks\n", 1),
		strings.Replace(string(data), "--verify-tag", "--verify-tag --new-unrehearsed-flag", 1),
	} {
		if len(coverageProblems([]byte(mutation), inventory, dry, tests.String())) == 0 {
			t.Fatal("release drift silently passed")
		}
	}
	delete(dry.Jobs, "assets-rehearsal")
	if len(coverageProblems(data, inventory, dry, tests.String())) == 0 {
		t.Fatal("missing rehearsal counterpart silently passed")
	}
}

func TestRehearsalGateBeforeSideEffects(t *testing.T) {
	w := readWorkflow(t, "release.yml")
	for id, boundaries := range map[string][]string{
		"agentd-darwin":  {"Build darwin paimos-agentd with LocalAuthentication", "Sign and notarize paimos-agentd"},
		"image-platform": {"Release checks", "Build and push"},
	} {
		gateAt, gate := named(t, w.Jobs[id], "Require exact-SHA release rehearsal")
		for _, boundary := range boundaries {
			boundaryAt, _ := named(t, w.Jobs[id], boundary)
			if gateAt >= boundaryAt {
				t.Fatalf("%s can run %s before the rehearsal gate", id, boundary)
			}
		}
		if gate.If != "" || gate.ContinueOnError || !strings.HasPrefix(gate.Run, `node scripts/check-release-rehearsal.mjs --sha "`) || gate.Env["GH_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" || w.Jobs[id].Permissions["actions"] != "read" {
			t.Fatalf("%s can bypass exact-SHA receipt", id)
		}
	}
	gateAt, _ := named(t, w.Jobs["image-platform"], "Require exact-SHA release rehearsal")
	loginFound := false
	for i, s := range w.Jobs["image-platform"].Steps {
		if strings.HasPrefix(s.Uses, "docker/login-action@") {
			loginFound = true
			if gateAt >= i {
				t.Fatal("image-platform can log in before the rehearsal gate")
			}
		}
	}
	if !loginFound {
		t.Fatal("missing image-platform registry login")
	}
}

func TestTagMatchesVersionFailsClosed(t *testing.T) {
	_, guard := named(t, readWorkflow(t, "release.yml").Jobs["image-platform"], "Tag matches version.json")
	for _, version := range []string{"261002004358.0.0", "261001205522.0.0"} {
		dir := t.TempDir()
		stub := "#!/bin/bash\nif [ \"$1\" = scripts/verify-release.mjs ]; then printf '%s' '{\"version\":\"" + version + "\"}'; else exec \"$REAL_NODE\" \"$@\"; fi\n"
		if err := os.WriteFile(filepath.Join(dir, "node"), []byte(stub), 0700); err != nil {
			t.Fatal(err)
		}
		node, err := exec.LookPath("node")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", guard.Run)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "REAL_NODE="+node, "VERSION=261002004358.0.0")
		if err := cmd.Run(); (err == nil) != (version == "261002004358.0.0") {
			t.Fatalf("version match guard: %v", err)
		}
	}
}

func TestRehearsalReleaseTagValidation(t *testing.T) {
	for _, tag := range []string{"v261002004358.0.0", "v260229120000.0.0", "261002004358.0.0", "v1.2.3"} {
		cmd := exec.Command("node", filepath.Join(root(t), "scripts/release-tag.mjs"), tag)
		if err := cmd.Run(); (err == nil) != (tag == "v261002004358.0.0") {
			t.Fatalf("tag %q: %v", tag, err)
		}
	}
}

// Always exercise the system bash (3.2 on macOS), even inside nix develop.
// Also exercise the PATH bash when it differs, covering the Nix/CI shell.
func digestTestShells(t *testing.T) []string {
	t.Helper()
	pathBash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	shells := []string{"/bin/bash"}
	if pathBash != shells[0] {
		shells = append(shells, pathBash)
	}
	return shells
}

func TestRehearsalPlatformDigestHandoff(t *testing.T) {
	_, source := named(t, readWorkflow(t, "release.yml").Jobs["image-platform"], "Record platform digest")
	for _, bash := range digestTestShells(t) {
		t.Run(bash, func(t *testing.T) {
			for _, tc := range []struct {
				name, digest string
				valid        bool
			}{
				{"valid", "sha256:" + strings.Repeat("a", 64), true},
				{"empty", "", false},
				{"bad", "bad", false},
				{"short", "sha256:" + strings.Repeat("a", 63), false},
				{"long", "sha256:" + strings.Repeat("a", 65), false},
				{"uppercase", "sha256:" + strings.Repeat("A", 64), false},
				{"non-hex", "sha256:" + strings.Repeat("g", 64), false},
				{"wrong-algorithm", "sha512:" + strings.Repeat("a", 64), false},
				{"trailing-newline", "sha256:" + strings.Repeat("a", 64) + "\n", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					dir := t.TempDir()
					cmd := exec.Command(bash, "-c", source.Run)
					cmd.Env = append(os.Environ(), "DIGEST="+tc.digest, "ARCH=arm64", "RUNNER_TEMP="+dir, "GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary"))
					output, err := cmd.CombinedOutput()
					if tc.valid {
						if err != nil {
							t.Fatalf("digest handoff: %v: %s", err, output)
						}
						body, err := os.ReadFile(filepath.Join(dir, "image-digests/arm64.txt"))
						if err != nil || string(body) != tc.digest+"\n" {
							t.Fatal("lost platform digest")
						}
						body, err = os.ReadFile(filepath.Join(dir, "summary"))
						if err != nil || !strings.Contains(string(body), "ghcr.io/inspr-at/aeon@"+tc.digest) {
							t.Fatal("lost verified image summary")
						}
						return
					}
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "Invalid platform image digest") {
						t.Fatalf("expected digest rejection, got %v: %s", err, output)
					}
					for _, path := range []string{"image-digests", "summary"} {
						if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
							t.Fatalf("invalid digest produced %s: %v", path, err)
						}
					}
				})
			}
		})
	}
}

func TestRehearsalIndexDigestValidation(t *testing.T) {
	_, source := named(t, readWorkflow(t, "release.yml").Jobs["image"], "Publish multi-arch index")
	// Exercise the exact post-publish body. The earlier mapfile/build commands
	// require Bash 4 and Docker; neither is part of digest validation.
	const start = `digest="$(jq `
	_, suffix, ok := strings.Cut(source.Run, start)
	if !ok {
		t.Fatal("missing index metadata digest extraction")
	}
	for _, bash := range digestTestShells(t) {
		t.Run(bash, func(t *testing.T) {
			for _, digest := range []string{"sha256:" + strings.Repeat("a", 64), "bad", ""} {
				t.Run(digest, func(t *testing.T) {
					dir := t.TempDir()
					// Functions keep all registry and verifier calls offline and observable.
					stubs := `jq() { printf '%s\n' "$TEST_DIGEST"; }
docker() { printf '%s\n' "$*" >> "$RUNNER_TEMP/inspect"; printf '{}'; }
node() { printf '%s\n' "$*" >> "$RUNNER_TEMP/verify"; }
`
					cmd := exec.Command(bash, "-c", "set -euo pipefail\n"+stubs+start+suffix)
					cmd.Env = append(os.Environ(), "TEST_DIGEST="+digest, "RUNNER_TEMP="+dir, "GITHUB_OUTPUT="+filepath.Join(dir, "output"))
					output, err := cmd.CombinedOutput()
					if digest != "bad" && digest != "" {
						if err != nil {
							t.Fatalf("index digest: %v: %s", err, output)
						}
						for path, want := range map[string]string{
							"inspect": "buildx imagetools inspect --raw ghcr.io/inspr-at/aeon@" + digest + "\n",
							"verify":  "scripts/release-image-index.mjs verify " + filepath.Join(dir, "published-index.json") + "\n",
							"output":  "digest=" + digest + "\n",
						} {
							body, err := os.ReadFile(filepath.Join(dir, path))
							if err != nil || string(body) != want {
								t.Fatalf("%s: got %q, want %q: %v", path, body, want, err)
							}
						}
						return
					}
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "Invalid multi-arch image digest") {
						t.Fatalf("expected digest rejection, got %v: %s", err, output)
					}
					for _, path := range []string{"inspect", "verify", "output", "published-index.json"} {
						if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
							t.Fatalf("invalid digest produced %s: %v", path, err)
						}
					}
				})
			}
		})
	}
}

func TestRehearsalBuildsAndReceiptCannotSkip(t *testing.T) {
	w := readWorkflow(t, "release-image-check.yml")
	r := readWorkflow(t, "release.yml")
	native := w.Jobs["agentd-rehearsal"]
	if !reflect.DeepEqual(native.Strategy, r.Jobs["agentd-darwin"].Strategy) || native.Environment != "" {
		t.Fatal("native rehearsal differs from production targets or requests signing")
	}
	_, export := named(t, w.Jobs["image-dry-run"], "Export production image with provenance locally")
	_, push := named(t, r.Jobs["image-platform"], "Build and push")
	for _, key := range []string{"context", "platforms", "provenance", "cache-from", "build-args"} {
		if export.With[key] != push.With[key] {
			t.Fatalf("export differs: %s", key)
		}
	}
	if export.With["push"] != "false" || !strings.HasPrefix(export.With["outputs"], "type=oci,dest=") || export.With["cache-to"] != "" {
		t.Fatal("rehearsal exports outside the local runner")
	}
	aggregate := w.Jobs["release-rehearsal"]
	if aggregate.If != "always()" || !reflect.DeepEqual(aggregate.Needs, []string{"image-dry-run", "agentd-rehearsal", "assets-rehearsal"}) {
		t.Fatal("receipt can skip a job")
	}
	_, receipt := named(t, aggregate, "Require every rehearsal job and record the exact SHA")
	for _, fragment := range []string{`test "$IMAGE" = success`, `test "$NATIVE" = success`, `test "$ASSETS" = success`, `test "$(git rev-parse HEAD)" = "`} {
		if !strings.Contains(receipt.Run, fragment) {
			t.Fatalf("receipt missing %s", fragment)
		}
	}
	web := readWorkflow(t, "docker-web-check.yml")
	_, build := named(t, web.Jobs["docker-web-stage"], "Build the production Docker web stage")
	if build.With["context"] != "." || build.With["target"] != "web" || build.With["push"] != "false" {
		t.Fatal("PR misses real Docker web context")
	}
}
