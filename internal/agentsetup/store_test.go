// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func physicalTemp(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestStoreExclusiveCommitInterruptedAfterRename(t *testing.T) {
	s := testStore(t)
	if e := s.Write(".write-fixture", []byte("durable identity"), true); e != nil {
		t.Fatal(e)
	}
	if e := renameExclusive(int(s.root.Fd()), ".write-fixture", "identity"); e != nil {
		t.Fatal(e)
	}
	if e := unix.Fsync(int(s.root.Fd())); e != nil {
		t.Fatal(e)
	}
	// Simulate process loss before deferred temp cleanup: exclusive rename has
	// no two-link intermediate state and needs no hardlink exception to resume.
	other, e := OpenStore(s.Path(), false)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if b, e := other.Read("identity", 128); e != nil || string(b) != "durable identity" {
		t.Fatal("interrupted identity commit did not resume")
	}
	if e := other.Write("identity", []byte("duplicate"), true); !errors.Is(e, ErrCollision) {
		t.Fatal("resumption replaced identity")
	}
}
func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(filepath.Join(physicalTemp(t), "state"), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStorePrivateAtomicResumeAndCollision(t *testing.T) {
	s := testStore(t)
	if e := s.Lock(); e != nil {
		t.Fatal(e)
	}
	if e := s.Write("identity.json", []byte("initial"), true); e != nil {
		t.Fatal(e)
	}
	if e := s.Write("identity.json", []byte("replacement"), true); !errors.Is(e, ErrCollision) {
		t.Fatal("identity collision accepted")
	}
	if e := s.Write("identity.json", []byte("resumed"), false); e != nil {
		t.Fatal(e)
	}
	info, _ := os.Stat(filepath.Join(s.Path(), "identity.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("private mode lost")
	}
	raw, e := s.Read("identity.json", 128)
	if e != nil || string(raw) != "resumed" {
		t.Fatal("resume failed")
	}
	second, e := OpenStore(s.Path(), false)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	if e = second.Lock(); !errors.Is(e, ErrBusy) {
		t.Fatal("concurrent setup accepted")
	}
}

func TestStoreRejectsLinksTraversalAndPublicState(t *testing.T) {
	root := physicalTemp(t)
	s := testStore(t)
	foreign := filepath.Join(root, "vendor-auth.fixture")
	if e := os.WriteFile(foreign, []byte("preserve unrelated bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"symlink", "hardlink"} {
		path := filepath.Join(s.Path(), name)
		var e error
		if name == "symlink" {
			e = os.Symlink(foreign, path)
		} else {
			e = os.Link(foreign, path)
		}
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Read(name, 128); !errors.Is(e, ErrUnsafePath) {
			t.Fatal("link read accepted")
		}
		if e = s.Write(name, []byte("changed"), false); !errors.Is(e, ErrUnsafePath) {
			t.Fatal("link write accepted")
		}
		if e = s.RemoveExact(name, Hash([]byte("preserve unrelated bytes"))); !errors.Is(e, ErrUnsafePath) {
			t.Fatal("link cleanup accepted")
		}
	}
	for _, name := range []string{"../other", "/absolute", "a/b", ".", ".."} {
		if e := s.Write(name, nil, false); !errors.Is(e, ErrUnsafePath) {
			t.Fatal("traversal accepted")
		}
	}
	alias := filepath.Join(root, "alias")
	if e := os.Symlink(s.Path(), alias); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenStore(alias, false); !errors.Is(e, ErrUnsafePath) {
		t.Fatal("symlink directory accepted")
	}
	public := filepath.Join(root, "public")
	if e := os.Mkdir(public, 0755); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenStore(public, true); !errors.Is(e, ErrUnsafePath) {
		t.Fatal("public root adopted")
	}
	b, _ := os.ReadFile(foreign)
	if string(b) != "preserve unrelated bytes" {
		t.Fatal("foreign bytes changed")
	}
}

// Risk: competing creators can fail a safe shared repository fence, while an
// EEXIST retry must never adopt a public directory, link or non-directory.
// The injected mkdir runs only after openat observed ENOENT; the competing
// artifact is installed before the real mkdirat, proving that exact race.
func TestStoreConcurrentDirectoryCreationKeepsPrivateBoundary(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, kind := range []string{"private", "public", "symlink", "file", "denied"} {
			t.Run(fmt.Sprintf("nested=%t/%s", nested, kind), func(t *testing.T) {
				root := physicalTemp(t)
				component := filepath.Join(root, "state")
				path := component
				if nested {
					path = filepath.Join(component, "child")
				}
				var winner *Store
				calls := 0
				store, err := openDirectoryWithMkdir(path, true, true, func(fd int, name string, mode uint32) error {
					calls++
					if calls != 1 || name != "state" || mode != 0700 {
						t.Fatalf("unexpected creation: calls=%d name=%q mode=%o", calls, name, mode)
					}
					switch kind {
					case "private":
						var err error
						winner, err = OpenStore(path, true)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { winner.Close() })
						if err := winner.Lock(); err != nil {
							t.Fatal(err)
						}
					case "public":
						if err := os.Mkdir(component, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.Chmod(component, 0777); err != nil {
							t.Fatal(err)
						}
					case "symlink":
						if err := os.Symlink(physicalTemp(t), component); err != nil {
							t.Fatal(err)
						}
					case "file":
						if err := os.WriteFile(component, []byte("preserve competing file"), 0600); err != nil {
							t.Fatal(err)
						}
					case "denied":
						return unix.EACCES
					}
					return unix.Mkdirat(fd, name, mode)
				})
				if store != nil {
					t.Cleanup(func() { store.Close() })
				}
				if calls != 1 {
					t.Fatalf("creation interleaving did not occur: %d", calls)
				}
				if kind == "private" {
					if err != nil || store == nil {
						t.Fatalf("private competing directory rejected: %v", err)
					}
					if err := store.Lock(); !errors.Is(err, ErrBusy) {
						t.Fatalf("competing store's fence was lost: %v", err)
					}
					if err := store.Write("shared", []byte("same directory"), true); err != nil {
						t.Fatal(err)
					}
					if raw, err := winner.Read("shared", 128); err != nil || string(raw) != "same directory" {
						t.Fatalf("stores do not share the validated directory: %q %v", raw, err)
					}
					return
				}
				context := "open directory " + component
				if kind == "public" {
					context = "validate directory " + component
				} else if kind == "denied" {
					context = "create directory " + component
				}
				if store != nil || !errors.Is(err, ErrUnsafePath) || errors.Is(err, unix.EEXIST) || !strings.Contains(err.Error(), context) {
					t.Fatalf("wrong unsafe competitor rejection: %v", err)
				}
				if kind == "denied" && !errors.Is(err, unix.EACCES) {
					t.Fatalf("creation error was lost: %v", err)
				}
				if kind == "public" {
					info, err := os.Lstat(component)
					if err != nil || info.Mode().Perm() != 0777 {
						t.Fatalf("public competitor was repaired: %v", err)
					}
				}
				if kind == "file" {
					if raw, err := os.ReadFile(component); err != nil || string(raw) != "preserve competing file" {
						t.Fatalf("competing file was changed: %q %v", raw, err)
					}
				}
			})
		}
	}
}

func TestStoreCleanupRequiresRecordedDigest(t *testing.T) {
	s := testStore(t)
	if e := s.Write("owned", []byte("owned data"), true); e != nil {
		t.Fatal(e)
	}
	if e := s.RemoveExact("owned", Hash([]byte("other"))); !errors.Is(e, ErrCollision) {
		t.Fatal("unrecognized file removed")
	}
	if e := s.RemoveExact("owned", Hash([]byte("owned data"))); e != nil {
		t.Fatal(e)
	}
	if e := s.RemoveExact("owned", Hash([]byte("owned data"))); e != nil {
		t.Fatal(e)
	}
}
