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
			original := `{"permissions":{"allow":["Bash(true)"]},"env":{"EXAMPLE":"preserve-without-printing & <keep>"},"hooks":{"PostToolUse":[{"matcher":"Read","custom":"keep","hooks":[{"type":"command","command":"existing-hook && echo <keep>","timeout":8}]}],"SessionStart":[{"hooks":[{"type":"command","command":"startup-hook"}]}]}}`
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
			backups, _ := filepath.Glob(path + ".backup-*")
			if len(backups) != 0 {
				t.Fatal("dry run made a backup")
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
			if strings.Contains(string(installed), `\u0026`) || strings.Contains(string(installed), `\u003c`) {
				t.Fatal("unrelated values were HTML escaped")
			}
			backups, _ = filepath.Glob(path + ".backup-*")
			if len(backups) != 1 {
				t.Fatal("missing backup")
			}
			saved, _ := os.ReadFile(backups[0])
			backupInfo, _ := os.Stat(backups[0])
			if string(saved) != original || backupInfo.Mode().Perm() != 0600 {
				t.Fatal("backup changed bytes or exposes private settings")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != originalInfo.Mode().Perm() {
				t.Fatal("permissions changed")
			}
			out.Reset()
			if err := rt.mergeInboxHooks(path, command, harness, false, false); err != nil {
				t.Fatal(err)
			}
			backups, _ = filepath.Glob(path + ".backup-*")
			if len(backups) != 1 {
				t.Fatal("idempotent install made a backup")
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
			backups, _ = filepath.Glob(path + ".backup-*")
			if len(backups) != 2 {
				t.Fatal("uninstall did not back up settings")
			}
			saved, _ = os.ReadFile(backups[1])
			if !bytes.Equal(saved, installed) {
				t.Fatal("uninstall backup is not exact")
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
	if err := os.Mkdir(filepath.Join(dir, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(shared, []byte(`{"shared":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		raw, _ := os.ReadFile(shared)
		if string(raw) != `{"shared":true}` {
			t.Error("project install modified shared settings")
		}
	})
	for _, harness := range []string{"claude", "codex"} {
		code, _, stderr := runCLI([]string{"aeon", "hook", "install", "--harness", harness, "--scope", "project"}, "")
		if code != 0 {
			t.Fatal(stderr)
		}
		name := "settings.local.json"
		if harness == "codex" {
			name = "hooks.json"
		}
		if _, err := os.Stat(filepath.Join(dir, "."+harness, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStableHookExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	profile := filepath.Join(dir, "aeon")
	if err := os.Symlink(executable, profile); err != nil {
		t.Fatal(err)
	}
	// Ignore direct store and relative PATH entries; retain the profile symlink.
	path, err := stableHookExecutable(executable, "/nix/store/old-aeon/bin:.:"+dir)
	if err != nil || path != profile {
		t.Fatalf("stable path = %q, %v", path, err)
	}
	if resolved, _ := filepath.EvalSymlinks(path); resolved == path {
		t.Fatal("test needs a symlink")
	}
	path, err = stableHookExecutable(executable, "")
	if err != nil || path != executable {
		t.Fatalf("non-Nix executable fallback: %q, %v", path, err)
	}
}

func TestHookInstallBackupFailurePreservesSettings(t *testing.T) {
	// The original filename fits NAME_MAX; adding the backup suffix does not.
	path := filepath.Join(t.TempDir(), strings.Repeat("s", 220)+".json")
	before := []byte(`{"keep":"literal & <value>"}`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rt := &runtime{stdout: &out}
	if err := rt.mergeInboxHooks(path, "aeon hook claude ", "claude", false, false); err == nil {
		t.Fatal("installed without required backup")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("backup failure replaced settings")
	}
}
