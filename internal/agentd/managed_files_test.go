// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func managedFileFixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "file"), []byte("inside fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return work, outside
}

func TestManagedFilesRejectSymlinksHardlinksAndTraversal(t *testing.T) {
	for _, kind := range []string{"external_link", "internal_link", "parent_link", "hard_link", "traversal", "absolute"} {
		t.Run(kind, func(t *testing.T) {
			work, outside := managedFileFixture(t)
			path := "attack"
			var err error
			switch kind {
			case "external_link":
				err = os.Symlink(outside, filepath.Join(work, path))
			case "internal_link":
				err = os.Symlink(filepath.Join(work, "file"), filepath.Join(work, path))
			case "parent_link":
				err = os.Symlink(filepath.Dir(outside), filepath.Join(work, path))
				path += "/outside"
			case "hard_link":
				err = os.Link(outside, filepath.Join(work, path))
			case "traversal":
				path = "../outside"
			case "absolute":
				path = outside
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readManagedFile(work, path); err == nil {
				t.Fatal("unsafe read allowed")
			}
			if err := writeManagedFile(work, path, "modified", nil); err == nil {
				t.Fatal("unsafe write allowed")
			}
			if err := writeManagedFile(work, path, "", &fileEditArgs{Old: "fixture", New: "modified"}); err == nil {
				t.Fatal("unsafe edit allowed")
			}
			if kind != "traversal" && kind != "absolute" {
				for _, grep := range []bool{false, true} {
					pattern := "*"
					if grep {
						pattern = "fixture"
					}
					if _, err := searchManagedFiles(t.Context(), work, fileSearchArgs{Pattern: pattern}, grep); err == nil {
						t.Fatal("unsafe search allowed")
					}
				}
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "outside fixture" {
				t.Fatal("outside fixture changed")
			}
		})
	}
}

func TestManagedFileDescriptorSurvivesPathRetarget(t *testing.T) {
	for _, write := range []bool{false, true} {
		work, outside := managedFileFixture(t)
		f, err := openManagedFile(work, "file", write, false)
		if err != nil {
			t.Fatal(err)
		}
		// Deterministic former hook/open interleaving: retarget after open, before IO.
		if err := os.Rename(filepath.Join(work, "file"), filepath.Join(work, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(work, "file")); err != nil {
			t.Fatal(err)
		}
		data, err := managedReadFD(f)
		if err != nil || data != "inside fixture" {
			t.Fatal("descriptor followed retargeted link")
		}
		if write {
			if err := writeManagedFD(f, "edited", nil); err != nil {
				t.Fatal(err)
			}
		}
		f.Close()
		if data, err := os.ReadFile(outside); err != nil || string(data) != "outside fixture" {
			t.Fatal("retarget changed outside")
		}
		if _, err := readManagedFile(work, "file"); err == nil {
			t.Fatal("later open followed symlink")
		}
	}
}

func TestManagedDirectoryDescriptorSurvivesParentRetarget(t *testing.T) {
	work, outside := managedFileFixture(t)
	if err := os.Mkdir(filepath.Join(work, "parent"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "parent", "outside"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := openManagedDirectory(work, "parent")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.Rename(filepath.Join(work, "parent"), filepath.Join(work, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(work, "parent")); err != nil {
		t.Fatal(err)
	}
	f, _, err := openManagedChild(dir, "outside")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if data, err := managedReadFD(f); err != nil || data != "inside" {
		t.Fatal("parent retarget escaped descriptor")
	}
}

func TestManagedFileRechecksLinkCountOnOpenDescriptor(t *testing.T) {
	work, _ := managedFileFixture(t)
	f, err := openManagedFile(work, "file", true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Link(filepath.Join(work, "file"), filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := checkManagedRegular(f); err == nil {
		t.Fatal("opened descriptor did not observe added hard link")
	}
	if _, err := managedReadFD(f); err == nil {
		t.Fatal("read accepted newly hardlinked descriptor")
	}
	if err := writeManagedFD(f, "changed", nil); err == nil {
		t.Fatal("write accepted newly hardlinked descriptor")
	}
	if err := writeManagedFD(f, "", &fileEditArgs{Old: "fixture", New: "changed"}); err == nil {
		t.Fatal("edit accepted newly hardlinked descriptor")
	}
}

func TestManagedFilesReadWriteEditAndSearch(t *testing.T) {
	work, _ := managedFileFixture(t)
	if err := writeManagedFile(work, "nested/deep/file", "new directory", nil); err != nil {
		t.Fatal(err)
	}
	if data, err := readManagedFile(work, "nested/deep/file"); err != nil || data != "new directory" {
		t.Fatal("nested file creation failed")
	}
	if err := writeManagedFile(work, "new", "hello world\n", nil); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedFile(work, "new", "", &fileEditArgs{Old: "world", New: "workspace"}); err != nil {
		t.Fatal(err)
	}
	if got, err := readManagedFile(work, "new"); err != nil || got != "hello workspace\n" {
		t.Fatalf("read: %q %v", got, err)
	}
	if got, err := searchManagedFiles(t.Context(), work, fileSearchArgs{Pattern: "new"}, false); err != nil || got != "new\n" {
		t.Fatalf("glob: %q %v", got, err)
	}
	if got, err := searchManagedFiles(t.Context(), work, fileSearchArgs{Pattern: "workspace"}, true); err != nil || got != "new:1:hello workspace\n" {
		t.Fatalf("grep: %q %v", got, err)
	}
	if err := writeManagedFile(work, "new", "", &fileEditArgs{Old: "missing", New: "x"}); err == nil {
		t.Fatal("ambiguous edit succeeded")
	}
	if err := writeManagedFile(work, "new", strings.Repeat("x", managedFileLimit+1), nil); err == nil {
		t.Fatal("oversized write succeeded")
	}
}

func TestManagedEditBoundsExpansionBeforeAllocation(t *testing.T) {
	work, _ := managedFileFixture(t)
	original := strings.Repeat("x", managedFileLimit)
	if err := os.WriteFile(filepath.Join(work, "large"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedFile(work, "large", "", &fileEditArgs{Old: "x", New: strings.Repeat("y", 4096), All: true}); err == nil {
		t.Fatal("unbounded replacement allowed")
	}
	data, err := os.ReadFile(filepath.Join(work, "large"))
	if err != nil || string(data) != original {
		t.Fatal("failed edit modified file")
	}
}
