// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
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
