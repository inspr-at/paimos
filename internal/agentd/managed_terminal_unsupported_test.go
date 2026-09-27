// SPDX-License-Identifier: AGPL-3.0-only
//go:build aeon_test_unsupported || !darwin

package agentd

import (
	"runtime"
	"strings"
	"testing"
)

func TestTerminalUnsupportedHostRejectsBeforeLaunch(t *testing.T) {
	// No toolchain is present. Capability rejection must happen before even
	// looking up git, including the branch probe used by mutating commands.
	t.Setenv("PATH", t.TempDir())
	want := "bounded terminal requires the macOS sandbox"
	if runtime.GOOS == "darwin" {
		want = "safe child lifetime observation unsupported"
	}
	_, err := runTerminal(t.Context(), t.TempDir(), "aeon/test", terminalArgs{Command: "git", Args: []string{"add", "--", "safe.go"}})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("unsupported terminal launch error=%v; want %q", err, want)
	}
}
