// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_checkpoint_unsupported

package sessionusage

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Hooks schedule real name swaps around real openat/fstat/read operations. The
// forbidden targets contain VALID synthetic checkpoints, so JSON rejection
// cannot mask an accidental read of the replacement descriptor.
type checkpointIOProbe struct {
	before, after func(string)
	beforeRead    func()
	reads, bytes  int
	fds           map[int]bool
}

func (p *checkpointIOProbe) read(t *testing.T, path string) (*Checkpoint, error) {
	t.Helper()
	p.fds = map[int]bool{}
	c, err := readCheckpointWithIO(path, func(abs string) (*os.File, error) {
		return openCheckpointFileAt(abs, func(dir int, name string, flags int, mode uint32) (int, error) {
			if flags&(unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC) != unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC {
				t.Fatal("unsafe flags")
			}
			if p.before != nil {
				p.before(name)
			}
			fd, err := unix.Openat(dir, name, flags, mode)
			if err == nil {
				p.fds[fd] = true
				if p.after != nil {
					p.after(name)
				}
			}
			return fd, err
		})
	}, func(r io.Reader) ([]byte, error) {
		p.reads++
		if p.beforeRead != nil {
			p.beforeRead()
		}
		b, err := io.ReadAll(r)
		p.bytes += len(b)
		return b, err
	})
	for fd := range p.fds {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatal("checkpoint fd leaked")
		}
	}
	if err != nil && strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatal("path leaked")
	}
	return c, err
}
func checkpointRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func writeCheckpoint(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, checkpointJSON(t, n), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestCheckpointDescriptorNameSwaps(t *testing.T) {
	for _, component := range []string{"public", "checkpoint.json"} {
		for _, after := range []bool{false, true} {
			name := component + "_before"
			if after {
				name = component + "_after"
			}
			t.Run(name, func(t *testing.T) {
				root := checkpointRoot(t)
				public := filepath.Join(root, "public")
				private := filepath.Join(root, ".codex")
				path := filepath.Join(public, "checkpoint.json")
				writeCheckpoint(t, path, 1)
				writeCheckpoint(t, filepath.Join(private, "checkpoint.json"), 2)
				swapped := false
				swap := func(part string) {
					if part != component || swapped {
						return
					}
					swapped = true
					from, to := public, private
					if component == "checkpoint.json" {
						from, to = path, filepath.Join(private, "checkpoint.json")
					}
					if err := os.Rename(from, from+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(to, from); err != nil {
						t.Fatal(err)
					}
				}
				p := &checkpointIOProbe{}
				if after {
					p.after = swap
				} else {
					p.before = swap
				}
				c, err := p.read(t, path)
				if !swapped {
					t.Fatal("swap did not run")
				}
				if after {
					if err != nil || c.Bytes != 1 || p.reads != 1 {
						t.Fatalf("pinned descriptor lost: %v", err)
					}
				} else if err == nil || p.reads != 0 {
					t.Fatal("symlink replacement read")
				}
			})
		}
	}
}
func TestCheckpointFIFOAndOpenedDescriptorBounds(t *testing.T) {
	for _, mode := range []string{"fifo", "directory", "oversize", "grow_before_stat", "grow_after_stat", "truncate_after_stat", "1024_bytes"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(checkpointRoot(t), "checkpoint.json")
			writeCheckpoint(t, path, 1)
			p := &checkpointIOProbe{}
			switch mode {
			case "fifo", "directory":
				p.before = func(name string) {
					if name != "checkpoint.json" {
						return
					}
					if err := os.Rename(path, path+"-saved"); err != nil {
						t.Fatal(err)
					}
					var err error
					if mode == "fifo" {
						err = unix.Mkfifo(path, 0600)
					} else {
						err = os.Mkdir(path, 0700)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			case "oversize":
				if err := os.WriteFile(path, append(checkpointJSON(t, 1), []byte(strings.Repeat(" ", 1025))...), 0600); err != nil {
					t.Fatal(err)
				}
			case "grow_before_stat":
				p.after = func(name string) {
					if name == "checkpoint.json" {
						if err := os.Truncate(path, 1025); err != nil {
							t.Fatal(err)
						}
					}
				}
			case "grow_after_stat":
				p.beforeRead = func() {
					if err := os.Truncate(path, 2048); err != nil {
						t.Fatal(err)
					}
				}
			case "truncate_after_stat":
				// Still valid JSON after truncation; size mismatch must catch the mutation.
				if err := os.WriteFile(path, append(checkpointJSON(t, 1), ' '), 0600); err != nil {
					t.Fatal(err)
				}
				p.beforeRead = func() {
					if err := os.Truncate(path, int64(len(checkpointJSON(t, 1)))); err != nil {
						t.Fatal(err)
					}
				}
			case "1024_bytes":
				b := checkpointJSON(t, 1)
				b = append(b, []byte(strings.Repeat(" ", 1024-len(b)))...)
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now()
			c, err := p.read(t, path)
			if time.Since(started) > time.Second {
				t.Fatal("special-file open blocked")
			}
			if mode == "1024_bytes" {
				if err != nil || c.Bytes != 1 || p.bytes != 1024 {
					t.Fatalf("valid limit failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe mutation accepted")
			}
			if mode == "grow_after_stat" {
				if p.bytes != 1025 {
					t.Fatal("bounded read did not stop at 1025")
				}
			} else if mode != "truncate_after_stat" && p.reads != 0 {
				t.Fatal("content read before type/size check")
			}
		})
	}
}
