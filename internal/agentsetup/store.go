// SPDX-License-Identifier: AGPL-3.0-only

// Package agentsetup implements the local, resumable computer enrollment
// boundary. It never reads vendor credential files or invokes a shell.
package agentsetup

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

var (
	ErrUnsafePath = errors.New("setup path is not a private, owned physical path")
	ErrCollision  = errors.New("setup file already exists; refusing to overwrite unrelated state")
	ErrBusy       = errors.New("another setup operation owns this computer state")
)

// Store anchors every access at an opened directory descriptor. No pathname
// lookup follows symlinks, including ancestors; no regular file may be linked.
// The advisory lock serializes cooperating helpers, not hostile same-UID code.
type Store struct {
	mu   sync.Mutex
	root *os.File
	lock *os.File
	path string
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 180 && !strings.ContainsAny(name, "/\\\x00\r\n")
}

// OpenStore creates private missing directories only when create is true. It
// never chmods, repairs, adopts or replaces an existing nonprivate state root.
func OpenStore(path string, create bool) (*Store, error) {
	return openDirectory(path, create, true)
}

// ReadPrivateFile protects legacy explicit file flags too, without requiring
// a formerly public ancestor to be changed or adopted by guided setup.
func ReadPrivateFile(path string, max int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafePath
	}
	s, err := openDirectory(filepath.Dir(path), false, false)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Read(filepath.Base(path), max)
}

// openDirectory also anchors user-service directories, which may conventionally
// be 0755. Their ancestors still cannot be writable by any other user.
func openDirectory(path string, create, private bool) (*Store, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrUnsafePath
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnsafePath
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		if !validName(part) {
			unix.Close(fd)
			return nil, ErrUnsafePath
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) && create {
			if e = unix.Mkdirat(fd, part, 0700); e == nil {
				e = unix.Fsync(fd)
			}
			if e != nil {
				unix.Close(fd)
				return nil, ErrUnsafePath
			}
			next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if e != nil {
			if errors.Is(e, unix.ENOENT) {
				return nil, os.ErrNotExist
			}
			return nil, ErrUnsafePath
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil {
			unix.Close(fd)
			return nil, ErrUnsafePath
		}
		owned := int(st.Uid) == os.Getuid()
		trustedParent := (owned || st.Uid == 0) && (st.Mode&0022 == 0 || st.Uid == 0 && st.Mode&unix.S_ISVTX != 0)
		if !trustedParent || i == len(parts)-1 && (!owned || private && st.Mode&0777 != 0700) {
			unix.Close(fd)
			return nil, ErrUnsafePath
		}
	}
	return &Store{root: os.NewFile(uintptr(fd), "private-state"), path: path}, nil
}

func (s *Store) Path() string { return s.path }

// Lock is held until Close. A crash releases it; the lock inode is retained.
func (s *Store) Lock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		return nil
	}
	f, err := s.lockNamed("setup.lock")
	if err != nil {
		return err
	}
	s.lock = f
	return nil
}

// LockNamed returns an owned descriptor; closing it releases only this lock.
func (s *Store) LockNamed(name string) (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lockNamed(name)
}
func (s *Store) lockNamed(name string) (*os.File, error) {
	f, err := s.open(name, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return nil, ErrBusy
	}
	return f, nil
}

func (s *Store) open(name string, flags int) (*os.File, error) {
	if !validName(name) || s.root == nil {
		return nil, ErrUnsafePath
	}
	fd, err := unix.Openat(int(s.root.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, ErrUnsafePath
	}
	f := os.NewFile(uintptr(fd), "private-file")
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		f.Close()
		return nil, ErrUnsafePath
	}
	return f, nil
}

func (s *Store) Read(name string, max int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.open(name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, errors.New("private state exceeds bound or is unreadable")
	}
	return b, nil
}

// Write commits a complete file durably. createOnly uses the platform's atomic
// exclusive rename; interruption never leaves a second hardlink to an identity.
func (s *Store) Write(name string, raw []byte, createOnly bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validName(name) || len(raw) > 1<<20 {
		return ErrUnsafePath
	}
	if f, err := s.open(name, unix.O_RDONLY); err == nil {
		f.Close()
		if createOnly {
			return ErrCollision
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := randomSecret()
	if err != nil {
		return err
	}
	tmp := ".write-" + string(id[:16])
	f, err := s.open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(s.root.Fd()), tmp, 0) // only our exclusively created temporary inode
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("private state write failed")
	}
	if createOnly {
		err = renameExclusive(int(s.root.Fd()), tmp, name)
	} else {
		err = unix.Renameat(int(s.root.Fd()), tmp, int(s.root.Fd()), name)
	}
	if errors.Is(err, unix.EEXIST) {
		return ErrCollision
	}
	if err != nil || unix.Fsync(int(s.root.Fd())) != nil {
		return errors.New("private state commit failed")
	}
	return nil
}

// RemoveExact is only for pairing-owned artifacts with recorded content
// identity. Vendor homes, workspaces, foreign state and unknown files never fit.
func (s *Store) RemoveExact(name, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.open(name, unix.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	f.Close()
	if err != nil || len(b) > 1<<20 || Hash(b) != digest {
		return ErrCollision
	}
	if unix.Unlinkat(int(s.root.Fd()), name, 0) != nil || unix.Fsync(int(s.root.Fd())) != nil {
		return errors.New("owned state cleanup failed")
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
	if s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	return err
}

func (s *Store) Unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
}

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// secret redacts generic diagnostic formatting; only the private snapshot and
// capability request encoders serialize it. Callers must not log request bodies.
type secret string

func (secret) String() string               { return "[private]" }
func (secret) GoString() string             { return "[private]" }
func (s secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "[private]") }
func randomSecret() (secret, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.New("secure identity generation failed")
	}
	return secret(hex.EncodeToString(raw[:])), nil
}
