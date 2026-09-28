// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_rulesimport_unsupported

package rulesimport

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Changes are scheduled exactly at syscall boundaries on synthetic fixtures.
// The real openat flags and descriptor checks run; no timing sleeps are needed.
func TestDoctrineAncestorAndLeafSwaps(t *testing.T) {
	for _, mode := range []string{"ancestor before", "ancestor after", "leaf before", "leaf after", "fifo before", "directory before", "oversize before"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := writeDoc(t, root, "safe/AGENTS.md", "- Approved synthetic instruction.\n")
			forbidden := writeDoc(t, root, "forbidden/AGENTS.md", "- FORBIDDEN-SYNTHETIC-CONTENT\n")
			reads := 0
			swapped := false
			fds := map[int]bool{}
			swap := func() {
				if swapped {
					return
				}
				swapped = true
				if strings.HasPrefix(mode, "ancestor") {
					if err := os.Rename(filepath.Join(root, "safe"), filepath.Join(root, "saved")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Dir(forbidden), filepath.Join(root, "safe")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Rename(path, path+".saved"); err != nil {
						t.Fatal(err)
					}
					switch mode {
					case "fifo before":
						if err := unix.Mkfifo(path, 0600); err != nil {
							t.Fatal(err)
						}
					case "directory before":
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
					case "oversize before":
						f, err := os.Create(path)
						if err != nil {
							t.Fatal(err)
						}
						if err = f.Truncate(MaxFileBytes + 1); err != nil {
							t.Fatal(err)
						}
						f.Close()
					default:
						if err := os.Symlink(forbidden, path); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			f, err := openDoctrineAt(path, func(dir int, name string, flags int, perm uint32) (int, error) {
				required := unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
				if flags&required != required {
					t.Fatal("unsafe open flags")
				}
				boundary := (strings.HasPrefix(mode, "ancestor") && name == "safe") || (!strings.HasPrefix(mode, "ancestor") && name == "AGENTS.md")
				if boundary && strings.HasSuffix(mode, "before") {
					swap()
				}
				fd, e := unix.Openat(dir, name, flags, perm)
				if e == nil {
					fds[fd] = true
				}
				if boundary && e == nil && strings.HasSuffix(mode, "after") {
					swap()
				}
				return fd, e
			}, unix.Readlinkat)
			var body string
			if err == nil {
				body, _, _, err = readOpenedDoctrineWith(f, func(r io.Reader) ([]byte, error) { reads++; return io.ReadAll(r) })
				f.Close()
			}
			if !swapped {
				t.Fatal("test missed swap boundary")
			}
			if strings.HasSuffix(mode, "after") {
				if err != nil || body != "- Approved synthetic instruction.\n" || reads != 1 {
					t.Fatalf("pinned descriptor changed: %v", err)
				}
			} else if err == nil || reads != 0 || body != "" {
				t.Fatal("unsafe substitution reached content reader")
			}
			for fd := range fds {
				if _, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(e, unix.EBADF) {
					t.Fatal("descriptor leaked")
				}
			}
		})
	}
}

func TestDoctrinePreflightOpensNothingForbidden(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{".ssh/AGENTS.md", ".inspr/AGENTS.md", "Secrets/AGENTS.md", ".config/AGENTS.md", "transcripts/AGENTS.md", "credentials/AGENTS.md", "data.age/AGENTS.md", ".env", "data.gpg", "ar1-api.json", "notes.txt", "AGENTS.md.key", "unknown.md"} {
		t.Run(relative, func(t *testing.T) {
			path := writeDoc(t, root, relative, "FORBIDDEN-SYNTHETIC-CONTENT")
			opens := 0
			f, err := openDoctrineAt(path, func(int, string, int, uint32) (int, error) { opens++; return -1, unix.EACCES }, unix.Readlinkat)
			if err == nil || f != nil || opens != 0 || strings.Contains(err.Error(), "FORBIDDEN-SYNTHETIC-CONTENT") {
				t.Fatal("forbidden input was opened")
			}
		})
	}
	// Check lexical private-store segments before path.Clean can erase them.
	path := root + "/Secrets/../AGENTS.md"
	_, err := validateDoctrinePath(path)
	if !errors.Is(err, ErrProhibitedPath) {
		t.Fatal("private segment escaped through cleaning")
	}
}

func TestDoctrineBoundedReadAndDescriptorChecks(t *testing.T) {
	for _, size := range []int{0, MaxFileBytes, MaxFileBytes + 1} {
		path := writeDoc(t, t.TempDir(), "AGENTS.md", strings.Repeat("x", size))
		f, err := openNoFollow(path)
		if err != nil {
			t.Fatal(err)
		}
		reads := 0
		body, _, n, err := readOpenedDoctrineWith(f, func(r io.Reader) ([]byte, error) { reads++; return io.ReadAll(r) })
		f.Close()
		if size > MaxFileBytes {
			if !errors.Is(err, ErrByteBound) || reads != 0 {
				t.Fatal("oversize file reached reader")
			}
		} else if err != nil || n != size || len(body) != size {
			t.Fatal("exact bound rejected")
		}
	}
	// Append after fstat: the bounded reader still refuses, without truncating.
	path := writeDoc(t, t.TempDir(), "AGENTS.md", "small")
	f, err := openNoFollow(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _, _, err = readOpenedDoctrineWith(f, func(r io.Reader) ([]byte, error) {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxFileBytes+200)), 0600); err != nil {
			t.Fatal(err)
		}
		b, e := io.ReadAll(r)
		if len(b) != MaxFileBytes+1 {
			t.Fatal("read was not bounded")
		}
		return b, e
	})
	if !errors.Is(err, ErrByteBound) {
		t.Fatal("growing file was truncated or accepted")
	}
	for _, raw := range []string{"nul\x00data", "invalid\xffdata"} {
		_, _, _, err := readDoctrine(writeDoc(t, t.TempDir(), "AGENTS.md", raw))
		if !errors.Is(err, ErrNotText) {
			t.Fatal("nontext accepted")
		}
	}
}

func TestDoctrineDarwinAliases(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin aliases only")
	}
	for _, tc := range []struct {
		name, target               string
		swap, privateLink, allowed bool
	}{
		{"relative", "private/tmp", false, false, true},
		{"absolute", "/private/tmp", false, false, true},
		{"other target", "forbidden", false, false, false},
		{"unclean target", "private/tmp/../forbidden", false, false, false},
		{"alias swap", "private/tmp", true, false, true},
		{"private symlink", "private/tmp", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeDoc(t, root, "private/tmp/AGENTS.md", "approved")
			writeDoc(t, root, "forbidden/tmp/AGENTS.md", "forbidden")
			if err := os.Symlink(tc.target, filepath.Join(root, "tmp")); err != nil {
				t.Fatal(err)
			}
			if tc.privateLink {
				if err := os.Rename(filepath.Join(root, "private"), filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("forbidden", filepath.Join(root, "private")); err != nil {
					t.Fatal(err)
				}
			}
			f, err := openDoctrineAt("/tmp/AGENTS.md", func(dir int, name string, flags int, perm uint32) (int, error) {
				if name == "/" {
					name = root
				} // Only root is redirected into a synthetic tree.
				return unix.Openat(dir, name, flags, perm)
			}, func(dir int, name string, b []byte) (int, error) {
				n, e := unix.Readlinkat(dir, name, b)
				if tc.swap {
					if err := os.Rename(filepath.Join(root, "tmp"), filepath.Join(root, "saved-alias")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("forbidden", filepath.Join(root, "tmp")); err != nil {
						t.Fatal(err)
					}
				}
				return n, e
			})
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
				body, _, _, e := readOpenedDoctrine(f)
				f.Close()
				if e != nil || body != "approved" {
					t.Fatal("alias read changed")
				}
			} else if err == nil {
				f.Close()
				t.Fatal("unsafe alias accepted")
			}
		})
	}
}
