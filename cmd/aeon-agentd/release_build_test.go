// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"os"
	"strings"
	"testing"
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
	sign = strings.Index(darwin, "- name: Sign and notarize paimos-agentd")
	cleanup := strings.Index(darwin, "- name: Remove signing keychain and temp files")
	if cleanup < sign || !strings.Contains(darwin[cleanup:], "if: always()") ||
		!strings.Contains(darwin[cleanup:], "security delete-keychain") ||
		!strings.Contains(darwin[cleanup:], `rm -rf "$RUNNER_TEMP"/sign.*`) {
		t.Fatal("agentd-darwin needs an always() step after signing that deletes the keychain and signing temp files")
	}
	if on := topLevelBlock(workflow, "on"); on != "  push:\n    tags:\n      - \"v*\"\n" {
		t.Fatalf("release workflow must trigger on v* tags only, got:\n%s", on)
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

// topLevelBlock returns the indented body of a top-level YAML key.
func topLevelBlock(doc, key string) string {
	_, after, ok := strings.Cut(doc, "\n"+key+":\n")
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(after, "\n") {
		if line != "\n" && !strings.HasPrefix(line, " ") {
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + rel)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
