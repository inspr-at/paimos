//go:build darwin && cgo

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func reexecDaemonName(t *testing.T, dir, file, mode string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destPath := filepath.Join(dir, file)
	dest, err := os.OpenFile(destPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dest.ReadFrom(source); err != nil {
		dest.Close()
		t.Fatal(err)
	}
	if err = dest.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(destPath, "-test.run", "^TestInstalledDaemonNamesMatchInstallerAndNixPackage$", "-test.count", "1", "-test.timeout", "30s")
	cmd.Env = append(os.Environ(), "AEON_DAEMON_NAME_CHECK="+mode)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s as %s: %v\n%s", file, mode, err, out)
	}
}

func TestInstalledDaemonNamesMatchInstallerAndNixPackage(t *testing.T) {
	if mode := os.Getenv("AEON_DAEMON_NAME_CHECK"); mode != "" {
		allowed := localAuthRunningExecutableAllowed()
		if allowed != (mode == "allow") {
			t.Fatalf("running executable allowed=%v mode=%s", allowed, mode)
		}
		return
	}
	// internal/agentpairing/install.go copies the release asset to paimos-agentd.
	// flake.nix packages.aeon-agentd installs bin/aeon-agentd.
	// scripts/build-release-binaries.sh keeps the architecture on the dist filename.
	for _, name := range []string{"paimos-agentd", "aeon-agentd"} {
		if !localAuthInstalledNameAllowed(name) {
			t.Fatalf("installed name %q rejected", name)
		}
	}
	for _, name := range []string{
		"",
		"paimos-agentd-darwin-arm64",
		"paimos-agentd-darwin-amd64",
		"aeon-agentd-darwin-arm64",
		"helper",
		"aeon",
		"paimos",
		"paimos-agentd ",
		"/tmp/paimos-agentd",
	} {
		if localAuthInstalledNameAllowed(name) {
			t.Fatalf("name %q accepted", name)
		}
	}
	if localAuthNilInstalledNameAllowed() {
		t.Fatal("nil name accepted")
	}
	if localAuthRunningExecutableAllowed() {
		t.Fatal("test executable qualified as an installed daemon")
	}
	dir := t.TempDir()
	reexecDaemonName(t, dir, "paimos-agentd", "allow")
	reexecDaemonName(t, dir, "aeon-agentd", "allow")
	reexecDaemonName(t, dir, "paimos-agentd-darwin-arm64", "reject")
}
