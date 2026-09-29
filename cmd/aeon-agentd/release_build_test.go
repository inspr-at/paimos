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

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + rel)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
