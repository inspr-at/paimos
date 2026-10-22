// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathProofCommandsRunInShells(t *testing.T) {
	const origin = "https://aeon.example"
	commands := []struct {
		name, line string
		want       []string
	}{
		{"homebrew", homebrewCommand(origin), []string{"pair", "--url", origin}},
		{"nix", nixPairCommand(origin), []string{"pair", "--url", origin}},
		{"homebrew-add-harness", homebrewAddHarnessCommand(), []string{"add-harness"}},
		{"nix-add-harness", nixAddHarnessCommand(), []string{"add-harness"}},
	}
	for _, command := range commands {
		if !strings.Contains(command.line, "env ") {
			t.Fatalf("%s is not prefixed with env: %s", command.name, command.line)
		}
		for _, shell := range []string{"fish", "zsh", "bash"} {
			t.Run(command.name+"/"+shell, func(t *testing.T) {
				got := runPathProof(t, requireShell(t, shell), command.line)
				want := strings.Join(command.want, "\n") + "\n"
				if string(got) != want {
					t.Fatalf("stub args %q, want %q", got, want)
				}
			})
		}
	}
}

func requireShell(t *testing.T, name string) string {
	t.Helper()
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	fallbacks := []string{"/bin/" + name}
	if name == "fish" {
		fallbacks = append(fallbacks, "/Users/markus/.nix-profile/bin/fish")
	}
	for _, fallback := range fallbacks {
		if info, err := os.Stat(fallback); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return fallback
		}
	}
	t.Skipf("%s is not installed: not found on PATH or at %s", name, strings.Join(fallbacks, ", "))
	return ""
}

func runPathProof(t *testing.T, shell, line string) []byte {
	t.Helper()
	root := t.TempDir()
	prefix := filepath.Join(root, "prefix")
	bin := filepath.Join(prefix, "bin")
	home := filepath.Join(root, "home")
	nixBin := filepath.Join(home, ".nix-profile", "bin")
	pathBin := filepath.Join(root, "path")
	for _, dir := range []string{bin, nixBin, pathBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const stub = "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$AEON_STUB_ARGV\"\n"
	for _, path := range []string{filepath.Join(bin, "aeon-agentd"), filepath.Join(nixBin, "aeon-agentd")} {
		if err := os.WriteFile(path, []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const brew = "#!/bin/sh\ncase \"$1\" in\n--prefix) printf '%s\\n' \"$AEON_BREW_PREFIX\" ;;\ninstall) exit 0 ;;\n*) printf 'unexpected brew\\n' >&2; exit 2 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(pathBin, "brew"), []byte(brew), 0o755); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(root, "argv")
	cmd := exec.Command(shell, "-c", line)
	cmd.Env = []string{
		"PATH=" + pathBin + ":/usr/bin:/bin",
		"HOME=" + home,
		"AEON_STUB_ARGV=" + argv,
		"AEON_BREW_PREFIX=" + prefix,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\n%s\n%s", err, line, out)
	}
	got, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("stub was not invoked: %v\n%s", err, out)
	}
	return got
}
