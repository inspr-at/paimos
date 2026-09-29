// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func hookSettings(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestHookInstallRoundTrip(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			original := `{"permissions":{"allow":["Bash(true)"]},"env":{"EXAMPLE":"preserve-without-printing"},"hooks":{"PostToolUse":[{"matcher":"Read","custom":"keep","hooks":[{"type":"command","command":"existing-hook","timeout":8}]}],"SessionStart":[{"hooks":[{"type":"command","command":"startup-hook"}]}]}}`
			if err := os.WriteFile(path, []byte(original), 0640); err != nil {
				t.Fatal(err)
			}
			originalInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			before := hookSettings(t, path)
			var out bytes.Buffer
			rt := &runtime{stdout: &out}
			command := "'/tool path/aeon' --instance ppm hook " + harness + " "
			if err := rt.mergeInboxHooks(path, command, harness, false, true); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, hookSettings(t, path)) || !strings.Contains(out.String(), "+ Stop") || strings.Contains(out.String(), "preserve-without-printing") || strings.Contains(out.String(), "existing-hook") {
				t.Fatal("dry run changed or exposed unrelated settings")
			}
			out.Reset()
			if err := rt.mergeInboxHooks(path, command, harness, false, false); err != nil {
				t.Fatal(err)
			}
			installed, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(installed), inboxHookMarker) != 3 || !strings.Contains(string(installed), "startup-hook") {
				t.Fatal("incorrect merge")
			}
			if harness == "codex" && strings.Count(string(installed), `"additionalContextLimit": 0`) != 2 {
				t.Fatal("Codex context must not be silently shortened")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != originalInfo.Mode().Perm() {
				t.Fatal("permissions changed")
			}
			out.Reset()
			if err := rt.mergeInboxHooks(path, command, harness, false, false); err != nil {
				t.Fatal(err)
			}
			again, _ := os.ReadFile(path)
			if !bytes.Equal(installed, again) || !strings.Contains(out.String(), "unchanged") {
				t.Fatal("installation is not idempotent")
			}
			if err := rt.mergeInboxHooks(path, command, harness, true, false); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, hookSettings(t, path)) {
				t.Fatal("uninstall changed unrelated settings")
			}
			uninstalled, _ := os.ReadFile(path)
			if err := rt.mergeInboxHooks(path, command, harness, true, false); err != nil {
				t.Fatal(err)
			}
			again, _ = os.ReadFile(path)
			if !bytes.Equal(uninstalled, again) {
				t.Fatal("uninstall not idempotent")
			}
		})
	}
}

func TestHookInstallPreservesSharedGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"hooks":{"Stop":[{"matcher":"x","custom":true,"hooks":[{"type":"command","command":"old hook` + inboxHookMarker + `"},{"type":"command","command":"keep-me"}]}]}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rt := &runtime{stdout: &out}
	if err := rt.mergeInboxHooks(path, "aeon hook claude ", "claude", true, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "keep-me") || !strings.Contains(string(raw), `"custom": true`) || strings.Contains(string(raw), inboxHookMarker) {
		t.Fatal("shared group was damaged")
	}
}

func TestHookInstallRejectsInvalidSettingsAndSymlinks(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{`, `{"hooks":null}`, `{"hooks":{"Stop":{}}}`, `{"hooks":{"Stop":[{}]}}`} {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		rt := &runtime{stdout: &out}
		if rt.mergeInboxHooks(path, "aeon hook claude ", "claude", false, false) == nil {
			t.Fatalf("accepted invalid settings %s", raw)
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("invalid settings overwritten")
		}
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if (&runtime{stdout: &out}).mergeInboxHooks(link, "aeon hook claude ", "claude", false, false) == nil {
		t.Fatal("followed symlink")
	}
}

func TestHookInstallCLIUserScope(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			setupHookTest(t)
			dir := t.TempDir()
			variable, file := "CLAUDE_CONFIG_DIR", "settings.json"
			if harness == "codex" {
				variable, file = "CODEX_HOME", "hooks.json"
			}
			t.Setenv(variable, dir)
			config := filepath.Join(t.TempDir(), "config with ' quote.yaml")
			args := []string{"aeon", "--config", config, "--instance", "ppm", "hook", "install", "--harness", harness}
			code, out, stderr := runCLI(append(args, "--dry-run"), "")
			if code != 0 || stderr != "" || !strings.Contains(out, "+ Stop") {
				t.Fatal("CLI dry run failed", stderr)
			}
			if _, err := os.Stat(filepath.Join(dir, file)); !os.IsNotExist(err) {
				t.Fatal("dry run wrote settings")
			}
			code, _, stderr = runCLI(args, "")
			if code != 0 {
				t.Fatal(stderr)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, file))
			var data map[string]any
			_ = json.Unmarshal(raw, &data)
			group := data["hooks"].(map[string]any)["Stop"].([]any)[0].(map[string]any)
			command := group["hooks"].([]any)[0].(map[string]any)["command"].(string)
			if !strings.Contains(command, shellHookQuote(config)) || !strings.Contains(command, "--instance 'ppm'") {
				t.Fatal("install dropped instance or quoting")
			}
			code, _, stderr = runCLI([]string{"aeon", "hook", "uninstall", "--harness", harness}, "")
			if code != 0 {
				t.Fatal(stderr)
			}
		})
	}
}

func TestHookInstallProjectScope(t *testing.T) {
	setupHookTest(t)
	dir := t.TempDir()
	t.Chdir(dir)
	for _, harness := range []string{"claude", "codex"} {
		code, _, stderr := runCLI([]string{"aeon", "hook", "install", "--harness", harness, "--scope", "project"}, "")
		if code != 0 {
			t.Fatal(stderr)
		}
		name := "settings.json"
		if harness == "codex" {
			name = "hooks.json"
		}
		if _, err := os.Stat(filepath.Join(dir, "."+harness, name)); err != nil {
			t.Fatal(err)
		}
	}
}
