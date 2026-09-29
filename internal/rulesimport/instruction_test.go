// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_rulesimport_unsupported

package rulesimport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstructionAllowlistRefusesSecretsAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, ".claude", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("SYNTHETIC_SECRET_251"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o600) })
	if _, _, _, err := ReadInstruction(secret); !errors.Is(err, ErrProhibitedPath) || strings.Contains(err.Error(), "SYNTHETIC_SECRET_251") {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, ".ssh", "AGENTS.md"),
		filepath.Join(dir, ".claude", "AGENTS.md"),
		filepath.Join(dir, ".codex", "CLAUDE.md"),
		filepath.Join(dir, ".claude", "nested", ".codex", "AGENTS.md"),
		dir + "/../AGENTS.md",
	} {
		if _, err := validateInstructionPath(path); !errors.Is(err, ErrProhibitedPath) {
			t.Fatalf("%s: %v", path, err)
		}
	}
	ok := filepath.Join(dir, ".claude", "CLAUDE.md")
	if _, err := validateInstructionPath(ok); err != nil {
		t.Fatal(err)
	}
	if _, err := validateDoctrinePath(ok); !errors.Is(err, ErrProhibitedPath) {
		t.Fatalf("doctrine reader accepted a harness file: %v", err)
	}
	missing := filepath.Join(dir, "AGENTS.md")
	if _, _, _, err := ReadInstruction(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.md")
	if err := os.WriteFile(target, []byte("SYNTHETIC_SECRET_251"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "CLAUDE.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadInstruction(link); !errors.Is(err, ErrSymlink) || strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "SYNTHETIC_SECRET_251") {
		t.Fatal(err)
	}
}

func TestReadContainedRefusesSecretsAndOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".env", filepath.Join(".ssh", "id_ed25519"), "id_rsa", "secrets.key", "credentials"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("SYNTHETIC_SECRET_251"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := ReadContained(path, []string{root}, 1024)
		if !errors.Is(err, ErrProhibitedPath) || strings.Contains(err.Error(), "SYNTHETIC_SECRET_251") || strings.Contains(err.Error(), root) {
			t.Fatal(err)
		}
	}
	outsideFile := filepath.Join(outside, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("SYNTHETIC_OUTSIDE_251"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadContained(outsideFile, []string{root}, 1024); !errors.Is(err, ErrProhibitedPath) || strings.Contains(err.Error(), "SYNTHETIC_OUTSIDE_251") {
		t.Fatal(err)
	}
	got, _, _, err := ReadContained(filepath.Join(root, "notes.md"), []string{root}, 1024)
	if err != nil || got != "safe\n" {
		t.Fatalf("%v %q", err, got)
	}
	target := filepath.Join(root, "secret-target.md")
	if err = os.WriteFile(target, []byte("SYNTHETIC_SECRET_251"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = ReadContained(filepath.Join(root, "linked.md"), []string{root}, 1024); !errors.Is(err, ErrSymlink) || strings.Contains(err.Error(), "SYNTHETIC_SECRET_251") {
		t.Fatal(err)
	}
}

func TestParseLoadedKeepsComparableFieldsOnly(t *testing.T) {
	body := "# Synthetic\n\n## Safety\n\n<!-- aeon-rule: safety -->\n- Keep the floor.\n  Why: it is the floor.\n"
	got, err := ParseLoaded("user/CLAUDE.local.md", body, strings.Repeat("ab", 32), len(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.Logical != "user/CLAUDE.local.md" || got.Unresolved != 0 || len(got.Rules) != 1 {
		t.Fatalf("%+v", got)
	}
	rule := got.Rules[0]
	if rule.ExplicitID != "safety" || rule.Text != "Keep the floor." || rule.Why != "it is the floor." || rule.Strength != StrengthNormal || !rule.Enabled {
		t.Fatalf("%+v", rule)
	}
	if strings.Contains(rule.Identity, "\n") || rule.Identity == "" {
		t.Fatal(rule.Identity)
	}
}
