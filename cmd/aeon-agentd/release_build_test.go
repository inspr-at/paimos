// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseBuildsSplitDarwinCGO(t *testing.T) {
	workflow := readRepo(t, ".github/workflows/release.yml")
	script := readRepo(t, "scripts/build-release-binaries.sh")
	flake := readRepo(t, "flake.nix")
	for _, needle := range []string{"macos-15", "macos-15-intel", "scripts/build-release-binaries.sh darwin-agentd", "scripts/build-release-binaries.sh linux-agentd", "scripts/build-release-binaries.sh verify-darwin"} {
		if !strings.Contains(workflow, needle) {
			t.Fatalf("workflow missing %s", needle)
		}
	}
	if strings.Contains(workflow, "CGO_ENABLED=0 GOOS=") {
		t.Fatal("release workflow still builds every target with CGO disabled")
	}
	for _, needle := range []string{"CGO_ENABLED=1 GOOS=darwin", "CGO_ENABLED=0", "LocalAuthentication.framework", "statically linked", "must run on"} {
		if !strings.Contains(script, needle) {
			t.Fatalf("build script missing %s", needle)
		}
	}
	if !strings.Contains(flake, "stdenv.isDarwin") || !strings.Contains(flake, "CGO_ENABLED = 1") || !strings.Contains(flake, "CGO_ENABLED = 0") {
		t.Fatal("nix package does not keep linux CGO off and enable it for darwin agentd")
	}
}

func TestReleaseSignsDarwinAgentdBeforeChecksums(t *testing.T) {
	workflow := readRepo(t, ".github/workflows/release.yml")
	script := readRepo(t, "scripts/build-release-binaries.sh")
	darwin, rest, ok := strings.Cut(workflow, "\n  release:\n")
	if !ok {
		t.Fatal("release workflow has no release job after agentd-darwin")
	}
	for _, needle := range []string{"environment: release-signing", "AEON_DEVELOPER_ID_TEAM: P66J39QV6V", "bash scripts/sign-notarize.sh", "secrets.APPLE_CERTIFICATE"} {
		if !strings.Contains(darwin, needle) {
			t.Fatalf("agentd-darwin job missing %s", needle)
		}
	}
	build := strings.Index(darwin, "build-release-binaries.sh darwin-agentd")
	sign := strings.Index(darwin, "bash scripts/sign-notarize.sh")
	upload := strings.Index(darwin, "actions/upload-artifact@")
	if !(build < sign && sign < upload) {
		t.Fatal("darwin agentd must be signed after the build and before upload")
	}
	if strings.Contains(rest, "secrets.APPLE_") || strings.Contains(rest, "release-signing") {
		t.Fatal("signing secrets leak beyond the agentd-darwin job")
	}
	if !strings.Contains(rest, "SHA256SUMS") {
		t.Fatal("checksums must be computed in the release job, after signing")
	}
	var wf releaseWorkflow
	if err := yaml.Unmarshal([]byte(workflow), &wf); err != nil {
		t.Fatalf("release.yml: %v", err)
	}
	if want := map[string]any{"push": map[string]any{"tags": []any{"v*"}}}; !reflect.DeepEqual(wf.On, want) {
		t.Fatalf("release workflow must trigger on v* tags only, got %#v", wf.On)
	}
	steps := wf.Jobs["agentd-darwin"].Steps
	signAt := -1
	for i, st := range steps {
		if st.Name == "Sign and notarize paimos-agentd" {
			signAt = i
		}
	}
	if signAt < 0 || signAt+1 >= len(steps) {
		t.Fatal("agentd-darwin has no step after signing")
	}
	cleanup := steps[signAt+1]
	if cleanup.Name != "Remove signing keychain and temp files" || cleanup.If != "always()" {
		t.Fatalf("the step right after signing must be the always() keychain cleanup, got %q if %q", cleanup.Name, cleanup.If)
	}
	if len(cleanup.Env) != 0 {
		t.Fatal("the cleanup step must not receive any env (no secrets)")
	}
	for _, needle := range []string{`: "${RUNNER_TEMP:?`, `[ "$rt" -ef / ]`, "set +e", "security delete-keychain", `rm -rf "$rt"/sign.*`, `[ "$failed" = 0 ] || exit 1`} {
		if !strings.Contains(cleanup.Run, needle) {
			t.Fatalf("cleanup step missing %s", needle)
		}
	}
	del, rm := strings.Index(cleanup.Run, "security delete-keychain"), strings.Index(cleanup.Run, "rm -rf")
	for _, guard := range []string{"RUNNER_TEMP:?", `[ "$rt" -ef / ]`} {
		if at := strings.Index(cleanup.Run, guard); at < 0 || at > del || at > rm {
			t.Fatalf("cleanup must check %s before any filesystem operation", guard)
		}
	}
	for _, needle := range []string{"could not read the keychain search list", "could not verify the keychain search list"} {
		if !strings.Contains(cleanup.Run, needle) {
			t.Fatalf("cleanup must fail when a keychain search-list query fails (%s)", needle)
		}
	}
	entries, err := os.ReadDir("../../.github/workflows")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "release.yml" || !(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			continue
		}
		other := readRepo(t, ".github/workflows/"+name)
		if strings.Contains(other, "APPLE_") || strings.Contains(other, "release-signing") || strings.Contains(other, "sign-notarize") {
			t.Fatalf("%s must never reference signing secrets", name)
		}
	}
	if !strings.Contains(script, "agentd.expectedTeamID=${team}") || !strings.Contains(script, "require_team") {
		t.Fatal("build script does not embed and check the expected Developer ID team")
	}
}

type releaseWorkflow struct {
	On   any `yaml:"on"`
	Jobs map[string]struct {
		Steps []struct {
			Name string            `yaml:"name"`
			If   string            `yaml:"if"`
			Env  map[string]string `yaml:"env"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + rel)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
