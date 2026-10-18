// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultStateRoot(t *testing.T) {
	for _, tc := range []struct{ os, home, xdg, want string }{
		{"darwin", "/Users/test", "/ignored", "/Users/test/Library/Application Support/aeon/paired"},
		{"linux", "/home/test", "", "/home/test/.local/state/aeon/paired"},
		{"linux", "/home/test", "/custom/state", "/custom/state/aeon/paired"},
	} {
		got, err := DefaultStateRoot(tc.os, tc.home, tc.xdg)
		if err != nil || got != tc.want {
			t.Fatalf("%s default = %q: %v", tc.os, got, err)
		}
	}
	for _, tc := range []struct{ os, home, xdg string }{
		{"linux", "/home/test", "relative"}, {"linux", "/home/test", "/a/../b"},
		{"darwin", "relative", ""}, {"windows", "/home/test", ""},
	} {
		if _, err := DefaultStateRoot(tc.os, tc.home, tc.xdg); err == nil {
			t.Fatal("unsafe default accepted")
		}
	}
}

func TestDefaultStoreCreatesAllPrivateParentsAndResumes(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home := physicalTemp(t)
			root, err := DefaultStateRoot(goos, home, "")
			if err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(root, true)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			for path := root; path != home; path = filepath.Dir(path) {
				info, err := os.Lstat(path)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatalf("parent mode: %v %v", info, err)
				}
			}
			store, err = OpenStore(root, true)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
		})
	}
}

func TestStateLocationRejectsRepositoryBeforeCreatingParents(t *testing.T) {
	repo := physicalTemp(t)
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(repo, "new", "paired"), "relative", "/", filepath.Join(repo, "work", "paired")} {
		if err := ValidateStateLocation(root, filepath.Join(repo, "work")); err == nil {
			t.Fatal("unsafe state location accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "new")); !os.IsNotExist(err) {
		t.Fatal("validation created state")
	}
	if err := ValidateStateLocation(filepath.Join(physicalTemp(t), "state"), physicalTemp(t)); err != nil {
		t.Fatal(err)
	}
}

func TestBeginRejectsPrivateStoreInsideHomebrewShapedRepository(t *testing.T) {
	e, api, _, opts, exec := engineFixture(t)
	prefix := filepath.Join(physicalTemp(t), "homebrew")
	if err := os.MkdirAll(filepath.Join(prefix, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(prefix, "aeon-state"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e.Store = store
	if _, err := e.Begin(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "private setup state must be outside project repositories") {
		t.Fatal("private store inside Homebrew-shaped repository accepted", err)
	}
	if api.createCount != 0 || len(exec.calls) != 0 {
		t.Fatal("unsafe private store reached pairing or service preflight")
	}
	if _, err := store.Read(snapshotName, 1<<20); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe private store persisted pairing state", err)
	}
}
