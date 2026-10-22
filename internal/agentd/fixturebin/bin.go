// SPDX-License-Identifier: AGPL-3.0-only

// Package fixturebin resolves POSIX commands that test fixtures exec.
// NixOS runners have no /bin/true, /bin/sleep, or /bin/cat; those commands
// are on PATH. Call Require once per test and reuse the path.
package fixturebin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Require returns an executable for name. It tries exec.LookPath, which
// searches PATH, then the conventional absolute path /bin/<name> when that
// file is executable. The test skips only when both are absent.
func Require(t *testing.T, name string) string {
	t.Helper()
	if !singleCommand(name) {
		t.Fatalf("posix fixture name %q is not a single command", name)
	}
	path, err := resolve(name)
	if err != nil {
		t.Skip(err.Error())
	}
	return path
}

// ShellExec is a /bin/sh fragment that execs the resolved tool with args.
// Resolve it once per test and splice the fragment into the fixture script.
func ShellExec(t *testing.T, name string, args ...string) string {
	t.Helper()
	parts := []string{"exec", Quote(Require(t, name))}
	for _, arg := range args {
		parts = append(parts, Quote(arg))
	}
	return strings.Join(parts, " ")
}

// Quote wraps a path as one POSIX sh single-quoted word.
func Quote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'"'"'`) + "'"
}

func resolve(name string) (string, error) {
	path, lookErr := exec.LookPath(name)
	if lookErr == nil {
		return path, nil
	}
	fallback := filepath.Join("/bin", name)
	info, statErr := os.Stat(fallback)
	if statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return fallback, nil
	}
	return "", fmt.Errorf("posix fixture %q is not on PATH (%v) and %s is missing or not executable", name, lookErr, fallback)
}

func singleCommand(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}
