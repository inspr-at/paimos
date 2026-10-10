//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Risk: an operator's symlinked checkout must support renders and index writes
// without allowing a symlink below that checkout to redirect the output.
func TestWriteRenderedAcceptsSymlinkWorkspaceRoot(t *testing.T) {
	physical := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(physical, workspace); err != nil {
		t.Fatal(err)
	}
	entry := renderedSkillEntry{Project: "AEON", Agent: "ops", Harness: "codex", Path: ".agents/skills/ops/SKILL.md"}
	t.Run("render", func(t *testing.T) {
		path, err := resolveSkillPath("", workspace, filepath.FromSlash(entry.Path))
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{"first", "replacement"} {
			if err := writeRenderedInWorkspace(workspace, path, body); err != nil {
				t.Fatalf("render through symlinked workspace: %v", err)
			}
			assertRenderedFile(t, filepath.Join(physical, filepath.FromSlash(entry.Path)), body)
			assertNoRenderedTemps(t, filepath.Dir(path))
		}
	})
	t.Run("index", func(t *testing.T) {
		for _, path := range []string{entry.Path, ".agents/skills/ops/replacement.md"} {
			entry.Path = path
			if err := upsertRenderedSkill(workspace, entry); err != nil {
				t.Fatalf("update index through symlinked workspace: %v", err)
			}
			idx, err := readSkillIndex(skillIndexPath(physical))
			if err != nil || idx.SchemaVersion != "1" || len(idx.Entries) != 1 || idx.Entries[0] != entry {
				t.Fatalf("physical index = %+v, error = %v, want entry %+v", idx, err, entry)
			}
			raw, err := os.ReadFile(skillIndexPath(physical))
			if err != nil {
				t.Fatal(err)
			}
			assertRenderedFile(t, skillIndexPath(physical), string(raw))
			assertNoRenderedTemps(t, filepath.Dir(skillIndexPath(physical)))
		}
	})
	t.Run("refuse-descendant-symlink", func(t *testing.T) {
		outside := t.TempDir()
		victim := filepath.Join(outside, "rendered")
		if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(physical, "linked-output")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(workspace, "linked-output", "rendered")
		if err := writeRenderedInWorkspace(workspace, path, "replacement"); err == nil || !strings.Contains(err.Error(), "without following symlinks") {
			t.Fatalf("expected descendant symlink refusal, got %v", err)
		}
		assertRenderedFile(t, victim, "original")
		assertNoRenderedTemps(t, outside)
		if got, err := os.Readlink(link); err != nil || got != outside {
			t.Fatalf("descendant symlink changed: %q, %v", got, err)
		}
	})
	if got, err := os.Readlink(workspace); err != nil || got != physical {
		t.Fatalf("workspace symlink changed: %q, %v", got, err)
	}
}

func TestWriteRenderedHoldsDirectoryAcrossAncestorSwap(t *testing.T) {
	for _, failRename := range []bool{false, true} {
		name := "publish"
		if failRename {
			name = "cleanup"
		}
		t.Run(name, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			rel := filepath.Join(".agents", "skills", "ops")
			dir, err := openRenderedDir(workspace, rel)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			if failRename {
				if err := os.Mkdir(filepath.Join(workspace, rel, "rendered"), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			outsideDir := filepath.Join(outside, "skills", "ops")
			if err := os.MkdirAll(outsideDir, 0o750); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(outsideDir, "rendered")
			if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			// Swap the ancestor after opening the destination directory. This
			// proves descriptor anchoring without a timing-dependent race.
			parent := filepath.Join(workspace, ".agents")
			moved := filepath.Join(workspace, "held")
			if err := os.Rename(parent, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, parent); err != nil {
				t.Fatal(err)
			}
			err = writeRenderedAt(dir, "rendered", "replacement")
			if (err != nil) != failRename || failRename && !strings.Contains(err.Error(), "rename") {
				t.Fatalf("render error = %v, want failure = %t", err, failRename)
			}
			assertRenderedFile(t, victim, "original")
			heldDir := filepath.Join(moved, "skills", "ops")
			if !failRename {
				assertRenderedFile(t, filepath.Join(heldDir, "rendered"), "replacement")
			} else if info, err := os.Stat(filepath.Join(heldDir, "rendered")); err != nil || !info.IsDir() {
				t.Fatalf("destination directory changed: %v", err)
			}
			assertNoRenderedTemps(t, heldDir)
			assertNoRenderedTemps(t, outsideDir)
		})
	}
}
