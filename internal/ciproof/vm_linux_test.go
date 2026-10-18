// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package ciproof

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMutableHostInstallationRefusedBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "launcher")
	if err := os.WriteFile(name, []byte("not executable"), 0444); err != nil {
		t.Fatal(err)
	}
	if _, err := openPinned(FilePin{Path: name, Digest: RawDigest([]byte("not executable"))}); err == nil {
		t.Fatal("candidate-writable installation parent accepted")
	}
	link := filepath.Join(dir, "symlink")
	if err := os.Symlink(name, link); err != nil {
		t.Fatal(err)
	}
	if err := checkInstallation(link); err == nil {
		t.Fatal("installation symlink accepted")
	}
	if _, err := OpenAuthorityRepository(context.Background(), name, dir, FilePin{Path: name, Digest: RawDigest([]byte("not executable"))}); err == nil {
		t.Fatal("candidate-owned controller/mirror accepted")
	}
}
