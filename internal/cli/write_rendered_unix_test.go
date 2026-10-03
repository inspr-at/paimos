//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
