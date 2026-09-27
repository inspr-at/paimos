// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_provenance_unsupported

package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func writeInstructionFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func linkInstructionFixture(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// Observe real opens and reads; hooks only schedule filesystem changes. No
// sleeps, process races, real private stores, or substituted safe syscall flags.
type instructionIOProbe struct {
	fds        map[int]bool
	reads      int
	bytes      int
	before     func(string)
	after      func(string)
	beforeRead func()
}

func (p *instructionIOProbe) hash(t *testing.T, path string) (ProvenanceItem, error) {
	t.Helper()
	p.fds = map[int]bool{}
	item, err := hashInstructionFileWithIO(path, func(abs string) (*os.File, error) {
		return openInstructionFileAt(abs, func(dir int, name string, flags int, perm uint32) (int, error) {
			if flags&unix.O_NOFOLLOW == 0 || flags&unix.O_CLOEXEC == 0 || flags&unix.O_NONBLOCK == 0 {
				t.Fatal("open lacks required descriptor safety flags")
			}
			if p.before != nil {
				p.before(name)
			}
			fd, err := unix.Openat(dir, name, flags, perm)
			if err == nil {
				p.fds[fd] = true
				if p.after != nil {
					p.after(name)
				}
			}
			return fd, err
		}, unix.Readlinkat)
	}, func(r io.Reader) ([]byte, error) {
		p.reads++
		if p.beforeRead != nil {
			p.beforeRead()
		}
		body, err := io.ReadAll(r)
		p.bytes += len(body)
		return body, err
	})
	for fd := range p.fds {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatal("instruction descriptor leaked")
		}
	}
	return item, err
}

func assertInstructionDenied(t *testing.T, p *instructionIOProbe, path string) {
	t.Helper()
	item, err := p.hash(t, path)
	if err == nil || item.ContentSHA256 != nil || strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatalf("expected path-free failure without digest: %v", err)
	}
	if p.reads != 0 || p.bytes != 0 {
		t.Fatalf("denied target reached content reader: calls=%d bytes=%d", p.reads, p.bytes)
	}
}

func assertInstructionDigest(t *testing.T, item ProvenanceItem, err error, body, logical string) {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	if err != nil || item.ContentSHA256 == nil || *item.ContentSHA256 != hex.EncodeToString(sum[:]) || item.ByteSize == nil || *item.ByteSize != int64(len(body)) || item.LogicalName != logical {
		t.Fatalf("wrong descriptor digest or logical identity: %v", err)
	}
}

func TestInstructionTraversalRejectsAncestorAndLeafLinks(t *testing.T) {
	for _, tc := range []struct{ name, store, suffix, target string }{
		{"parent", ".ssh", "AGENTS.md", ""},
		{"ancestor", "agent-transcripts", "nested/AGENTS.md", ""},
		{"skill", ".inspr", "demo/SKILL.md", ""},
		{"relative", "credentials", "CLAUDE.md", "credentials"},
		{"cleaned_target", ".ssh", "AGENTS.md", "unused/../.ssh"},
		// This strict policy intentionally rejects benign custom directory
		// links too. Its physical spelling remains usable below.
		{"benign", "physical", "AGENTS.md", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, tc.store)
			writeInstructionFixture(t, filepath.Join(target, tc.suffix), "synthetic forbidden target")
			if tc.name == "cleaned_target" {
				// Keep the target resolvable even without lexical cleaning, so
				// a pathname-following regression would actually reach the body.
				if err := os.Mkdir(filepath.Join(dir, "unused"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.target != "" {
				target = tc.target
			}
			linkInstructionFixture(t, target, filepath.Join(dir, "public"))
			assertInstructionDenied(t, &instructionIOProbe{}, filepath.Join(dir, "public", tc.suffix))
			if tc.name == "benign" {
				item, err := (&instructionIOProbe{}).hash(t, filepath.Join(dir, tc.store, tc.suffix))
				assertInstructionDigest(t, item, err, "synthetic forbidden target", "AGENTS.md")
			}
		})
	}
	t.Run("leaf", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, ".ssh", "AGENTS.md")
		writeInstructionFixture(t, target, "synthetic forbidden target")
		linkInstructionFixture(t, target, filepath.Join(dir, "AGENTS.md"))
		assertInstructionDenied(t, &instructionIOProbe{}, filepath.Join(dir, "AGENTS.md"))
	})
}

func TestInstructionTraversalNameSwaps(t *testing.T) {
	for _, tc := range []struct {
		name, component string
		after           bool
	}{
		{"leaf_before_open", "AGENTS.md", false},
		{"ancestor_before_open", "public", false},
		{"leaf_after_open", "AGENTS.md", true},
		{"ancestor_after_open", "public", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			public := filepath.Join(dir, "public")
			private := filepath.Join(dir, ".ssh")
			path := filepath.Join(public, "AGENTS.md")
			// Equal lengths would defeat the old Lstat/Open size comparison.
			writeInstructionFixture(t, path, "approved-body")
			writeInstructionFixture(t, filepath.Join(private, "AGENTS.md"), "forbidden-one")
			swapped := false
			swap := func(name string) {
				if name != tc.component || swapped {
					return
				}
				swapped = true
				from, target := path, filepath.Join(private, "AGENTS.md")
				if tc.component == "public" {
					from, target = public, private
				}
				if err := os.Rename(from, from+"-saved"); err != nil {
					t.Fatal(err)
				}
				linkInstructionFixture(t, target, from)
			}
			p := &instructionIOProbe{}
			if tc.after {
				p.after = swap
				item, err := p.hash(t, path)
				assertInstructionDigest(t, item, err, "approved-body", "AGENTS.md")
				if p.reads != 1 || p.bytes != len("approved-body") {
					t.Fatal("expected only the pinned approved descriptor to be read")
				}
			} else {
				p.before = swap
				assertInstructionDenied(t, p, path)
			}
			if !swapped {
				t.Fatal("test did not reach the scheduled name swap")
			}
		})
	}
}

func TestInstructionTraversalDeniesPathsBeforeOpening(t *testing.T) {
	dir := t.TempDir()
	for _, part := range []string{".ssh", ".inspr", ".config", "credentials", "agent-transcripts", "notes.env", "signing.key", "private.pem"} {
		t.Run(part, func(t *testing.T) {
			p := &instructionIOProbe{}
			assertInstructionDenied(t, p, filepath.Join(dir, part, "AGENTS.md"))
			if len(p.fds) != 0 {
				t.Fatal("denied lexical path opened a descriptor")
			}
		})
	}
}

func TestInstructionDescriptorChecksBeforeRead(t *testing.T) {
	for _, kind := range []string{"directory", "fifo", "oversize", "swapped_oversize"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "AGENTS.md")
			p := &instructionIOProbe{}
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				writeInstructionFixture(t, path, "approved")
				grow := func() {
					if err := os.Truncate(path, maxProvenanceBytes+1); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "oversize" {
					grow()
				} else {
					p.before = func(name string) {
						if name == "AGENTS.md" {
							if err := os.Rename(path, path+"-saved"); err != nil {
								t.Fatal(err)
							}
							writeInstructionFixture(t, path, "replacement")
							grow()
						}
					}
				}
			}
			assertInstructionDenied(t, p, path)
		})
	}
}

func TestInstructionDescriptorBoundsGrowthDuringRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	writeInstructionFixture(t, path, "approved")
	p := &instructionIOProbe{beforeRead: func() {
		if err := os.Truncate(path, maxProvenanceBytes*2); err != nil {
			t.Fatal(err)
		}
	}}
	item, err := p.hash(t, path)
	if err == nil || item.ContentSHA256 != nil || int64(p.bytes) != maxProvenanceBytes+1 {
		t.Fatalf("growing descriptor not bounded: bytes=%d err=%v", p.bytes, err)
	}
}

func TestInstructionDescriptorReadFailureIsRedactedAndClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	writeInstructionFixture(t, path, "approved")
	var opened *os.File
	item, err := hashInstructionFileWithIO(path, func(abs string) (*os.File, error) {
		var err error
		opened, err = openInstructionFile(abs)
		return opened, err
	}, func(io.Reader) ([]byte, error) {
		return nil, errors.New("synthetic read failure at " + path)
	})
	if err != errInstructionPath || item.ContentSHA256 != nil || opened == nil {
		t.Fatal("read failure leaked details or a digest")
	}
	if _, err := opened.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("descriptor was not closed after read failure")
	}
}

func TestInstructionDarwinAliasTargetBoundary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin system aliases only")
	}
	for _, tc := range []struct {
		name, target                     string
		swap, redirectedPrivate, allowed bool
	}{
		{name: "relative", target: "private/tmp", allowed: true},
		{name: "absolute", target: "/private/tmp", allowed: true},
		{name: "private_target", target: ".ssh"},
		{name: "cleaned_private_target", target: "private/tmp/../credentials"},
		{name: "alias_swap", target: "private/tmp", swap: true, allowed: true},
		{name: "private_prefix_link", target: "private/tmp", redirectedPrivate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Redirect only the initial root open into a synthetic tree. All
			// subsequent openat/readlinkat calls operate on real fixture fds.
			root := t.TempDir()
			writeInstructionFixture(t, filepath.Join(root, "private/tmp/AGENTS.md"), "approved")
			writeInstructionFixture(t, filepath.Join(root, "private/credentials/AGENTS.md"), "forbidden")
			writeInstructionFixture(t, filepath.Join(root, ".ssh/AGENTS.md"), "forbidden")
			writeInstructionFixture(t, filepath.Join(root, ".ssh/tmp/AGENTS.md"), "forbidden")
			linkInstructionFixture(t, tc.target, filepath.Join(root, "tmp"))
			if tc.redirectedPrivate {
				if err := os.Rename(filepath.Join(root, "private"), filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				linkInstructionFixture(t, ".ssh", filepath.Join(root, "private"))
			}
			reads := 0
			fds := map[int]bool{}
			item, err := hashInstructionFileWithIO("/tmp/AGENTS.md", func(abs string) (*os.File, error) {
				return openInstructionFileAt(abs, func(dir int, name string, flags int, perm uint32) (int, error) {
					if name == "/" {
						name = root
					} // fixture root, never a private store
					fd, err := unix.Openat(dir, name, flags, perm)
					if err == nil {
						fds[fd] = true
					}
					return fd, err
				}, func(dir int, name string, buf []byte) (int, error) {
					n, err := unix.Readlinkat(dir, name, buf)
					if tc.swap {
						if e := os.Rename(filepath.Join(root, "tmp"), filepath.Join(root, "saved-alias")); e != nil {
							t.Fatal(e)
						}
						linkInstructionFixture(t, ".ssh", filepath.Join(root, "tmp"))
					}
					return n, err
				})
			}, func(r io.Reader) ([]byte, error) { reads++; return io.ReadAll(r) })
			if tc.allowed {
				assertInstructionDigest(t, item, err, "approved", "AGENTS.md")
			} else if err != errInstructionPath || item.ContentSHA256 != nil || reads != 0 {
				t.Fatal("unexpected alias target reached content read")
			}
			for fd := range fds {
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
					t.Fatal("alias walk leaked descriptor")
				}
			}
		})
	}
}

func TestInstructionDarwinSystemAliases(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin system aliases only")
	}
	// Synthetic trees under the actual public temporary roots only. No private
	// store is opened. Both physical and system-alias spellings must work.
	for _, root := range []string{"/tmp", "/var/tmp"} {
		t.Run(root, func(t *testing.T) {
			t.Setenv("TMPDIR", root)
			dir := t.TempDir()
			path := filepath.Join(dir, "AGENTS.md")
			writeInstructionFixture(t, path, "approved alias fixture")
			item, err := (&instructionIOProbe{}).hash(t, path)
			assertInstructionDigest(t, item, err, "approved alias fixture", "AGENTS.md")
			item, err = (&instructionIOProbe{}).hash(t, "/private"+path)
			assertInstructionDigest(t, item, err, "approved alias fixture", "AGENTS.md")
		})
	}
}
