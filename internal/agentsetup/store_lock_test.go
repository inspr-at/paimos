// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStoreLockRetriesEINTRAndClassifiesErrors(t *testing.T) {
	s := testStore(t)
	calls := 0
	lock, err := s.lockNamedWithFlock("retry.lock", func(fd, operation int) error {
		calls++
		if calls < 3 {
			return unix.EINTR
		}
		return unix.Flock(fd, operation)
	})
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if calls != 3 {
		t.Fatalf("EINTR attempts: %d", calls)
	}
	for _, errno := range []error{unix.EWOULDBLOCK, unix.EAGAIN, unix.EIO, unix.EBADF} {
		t.Run(errno.Error(), func(t *testing.T) {
			lock, err := s.lockNamedWithFlock("error.lock", func(int, int) error { return errno })
			if lock != nil {
				lock.Close()
				t.Fatal("lock returned on failure")
			}
			busy := errors.Is(errno, unix.EWOULDBLOCK) || errors.Is(errno, unix.EAGAIN)
			if errors.Is(err, ErrBusy) != busy {
				t.Fatalf("wrong busy classification: %v", err)
			}
			if !busy && (!errors.Is(err, errno) || !strings.Contains(err.Error(), "flock error.lock")) {
				t.Fatalf("missing syscall context: %v", err)
			}
		})
	}
}

func TestStoreLockRetriesReplacedInode(t *testing.T) {
	for _, mutation := range []string{"unlink", "replace", "always-replace"} {
		t.Run(mutation, func(t *testing.T) {
			s := testStore(t)
			path := filepath.Join(s.Path(), "test.lock")
			calls := 0
			lock, err := s.lockNamedWithFlock("test.lock", func(fd, operation int) error {
				calls++
				if err := unix.Flock(fd, operation); err != nil {
					return err
				}
				if calls > 1 && mutation != "always-replace" {
					return nil
				}
				if mutation == "unlink" {
					return os.Remove(path) // simulate external removal, never production cleanup
				}
				if err := os.WriteFile(path+".replacement", nil, 0600); err != nil {
					return err
				}
				return os.Rename(path+".replacement", path)
			})
			if mutation == "always-replace" {
				if !errors.Is(err, ErrCollision) || lock != nil || calls != 4 {
					t.Fatalf("replacement retry bound: calls=%d lock=%v err=%v", calls, lock, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if calls != 2 {
				t.Fatalf("replacement attempts: %d", calls)
			}
			if err := s.verifyLock("test.lock", lock); err != nil {
				t.Fatal(err)
			}
			if other, err := s.LockNamed("test.lock"); !errors.Is(err, ErrBusy) {
				if other != nil {
					other.Close()
				}
				t.Fatalf("replacement not locked: %v", err)
			}
		})
	}
}

func TestStoreLockMissingDirectoryIsNotBusy(t *testing.T) {
	s := testStore(t)
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	lock, err := s.LockNamed("missing.lock")
	if lock != nil {
		lock.Close()
		t.Fatal("created a lock in a removed directory")
	}
	if err == nil || errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "open lock missing.lock") {
		t.Fatalf("missing directory error lost context: %v", err)
	}
}

func TestStoreLockNotInheritedByHarness(t *testing.T) {
	s := testStore(t)
	lock, err := s.LockNamed("child.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	flags, err := unix.FcntlInt(lock.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("lock is not close-on-exec: flags=%d err=%v", flags, err)
	}
	child := exec.Command("/bin/cat")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stdout, child.Stderr = io.Discard, io.Discard
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); child.Wait() }()
	lock.Close()
	next, err := s.LockNamed("child.lock")
	if err != nil {
		t.Fatalf("harness child retained lock: %v", err)
	}
	next.Close()
}
