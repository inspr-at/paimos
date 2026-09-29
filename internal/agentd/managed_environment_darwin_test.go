// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin && !aeon_test_unsupported

package agentd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// All procargs probes target a fresh fixture parent with an explicitly synthetic
// environment, never the real daemon, test runner, or any other existing PID.
func TestTerminalProcargsFixture(t *testing.T) {
	args := os.Args
	if len(args) < 3 {
		return
	}
	mode, arg := args[len(args)-2], args[len(args)-1]
	switch mode {
	case "aeon-env-child", "aeon-env-baseline":
		for _, name := range []string{"AEON_RUNTIME_KEY", "AEON_DATABASE_URL", "AEON_TEST_DATABASE_URL", "DATABASE_URL"} {
			if os.Getenv(name) != "" {
				t.Fatal("inherited fixture daemon setting")
			}
		}
		pid, err := strconv.Atoi(arg)
		if err != nil || pid != os.Getppid() {
			t.Fatal("target is not fixture parent")
		}
		// kern.procargs2 via numeric MIB: no name-resolution denial can hide an
		// otherwise unfiltered read. Neither buffer nor environment is printed.
		mib := [3]int32{1, 49, int32(pid)}
		data := make([]byte, 1<<20)
		size := uintptr(len(data))
		_, _, errno := syscall.Syscall6(unix.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 3, uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
		if mode == "aeon-env-baseline" {
			if errno != 0 || !strings.Contains(string(data), "fixture-parent-value") {
				t.Fatal("fixture did not reproduce unsandboxed parent environment access")
			}
			return
		}
		if errno != syscall.EPERM && errno != syscall.EACCES {
			t.Fatalf("numeric procargs query was not denied: errno=%d size=%d fixture_present=%t", errno, size, strings.Contains(string(data), "fixture-parent-value"))
		}
		if strings.Contains(string(data), "fixture-parent-value") || strings.Contains(string(data), "AEON_") || strings.Contains(string(data), "DATABASE_URL=") {
			t.Fatal("numeric procargs query returned fixture environment bytes")
		}
		named, err := unix.SysctlRaw("kern.procargs2", pid)
		if err == nil || strings.Contains(string(named), "fixture-parent-value") {
			t.Fatal("named procargs query was allowed")
		}
		// Process-list interfaces are denied too. Output stays in memory even if
		// the assertion fails, so only synthetic status appears in test output.
		ps := exec.Command("/bin/ps", "eww", "-p", arg)
		raw, _ := ps.Output()
		if strings.Contains(string(raw), "fixture-parent-value") || strings.Contains(string(raw), "AEON_") || strings.Contains(string(raw), "DATABASE_URL=") {
			t.Fatal("ps exposed fixture environment")
		}
	case "aeon-env-parent":
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		exe, err = filepath.EvalSymlinks(exe)
		if err != nil {
			t.Fatal(err)
		}
		baseline := exec.CommandContext(t.Context(), exe, "-test.run=^TestTerminalProcargsFixture$", "--", "aeon-env-baseline", strconv.Itoa(os.Getpid()))
		baseline.Env = terminalEnvironment(arg, "off", "", exe)
		if out, err := baseline.CombinedOutput(); err != nil {
			t.Fatalf("baseline: %v %s", err, out)
		}
		profile := terminalSandboxProfile(arg, arg, nil, []string{exe, "/bin/ps"})
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		out, err := runSandboxedTerminal(ctx, arg, profile, exe, terminalEnvironment(arg, "off", "", exe), []string{"-test.run=^TestTerminalProcargsFixture$", "--", "aeon-env-child", strconv.Itoa(os.Getpid())})
		if err != nil {
			t.Fatalf("sandbox fixture: %v %s", err, out)
		}
	}
}

func TestTerminalCannotReadFixtureParentEnvironment(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestTerminalProcargsFixture$", "--", "aeon-env-parent", root)
	cmd.Env = append(terminalEnvironment(root, "off", "", exe), "AEON_RUNTIME_KEY=fixture-parent-value", "AEON_DATABASE_URL=fixture-parent-value", "AEON_TEST_DATABASE_URL=fixture-parent-value", "DATABASE_URL=fixture-parent-value")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture isolation: %v %s", err, out)
	}
}
