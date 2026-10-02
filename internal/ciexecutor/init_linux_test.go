// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package ciexecutor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/ciproof"
)

func TestArtifactReadCannotFollowCandidateLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "output"), []byte("bounded output"), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := collectArtifacts(root, []string{"output"})
	if err != nil || len(a) != 1 || a[0].Digest != ciproof.RawDigest([]byte("bounded output")) {
		t.Fatalf("artifact read failed: %v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "other"), []byte("outside data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/self/fd/1", filepath.Join(root, "magic")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link/other", "../other", "magic", "missing"} {
		if _, err := collectArtifacts(root, []string{name}); err == nil {
			t.Fatalf("candidate path accepted: %s", name)
		}
	}
	if _, err := collectArtifacts(root, []string{"output", "output"}); err == nil {
		t.Fatal("duplicate artifact accepted")
	}
}
