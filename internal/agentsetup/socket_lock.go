// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// LockSocket acquires the listener's lifetime lock before inspecting or removing
// any socket artifacts. The caller must retain the returned descriptor until
// the listener is closed and its socket/token cleanup is complete. Clients do
// not take this lock. As with Store.Lock, it serializes cooperating daemons,
// not hostile code running as the same uid. Never unlink the lock file.
func (s *Store) LockSocket(name string) (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validName(name) || !validName(name+".owner.json") {
		return nil, ErrUnsafePath
	}
	lock, err := s.lockNamed(name + ".lock")
	if errors.Is(err, ErrBusy) {
		return nil, fmt.Errorf("agentd is already running for this state root: %w", err)
	}
	if err != nil {
		return nil, err
	}
	if err := s.cleanSocketArtifacts(name, lock, nil); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

// CleanupSocket removes only this listener's recorded socket/token inodes,
// after closing the listener and while retaining its verified lifetime lock.
// Lost lock identity leaves residue for a later, verified owner to recover.
func (s *Store) CleanupSocket(name string, lock *os.File, socketInfo, tokenInfo os.FileInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanSocketArtifacts(name, lock, map[string]os.FileInfo{
		name: socketInfo, name + ".token": tokenInfo,
	})
}

type socketArtifact struct {
	name string
	kind uint32
	dev  uint64
	ino  uint64
}

func privateArtifact(st *unix.Stat_t, kind uint32) bool {
	return uint32(st.Mode) == kind|0600 && int(st.Uid) == os.Getuid() && st.Nlink == 1
}

// Old recovery generated exactly .s plus eight lowercase hex digits, for both
// sockets and tokens. Do not adopt unrelated dotfiles sharing the .s prefix.
func legacySocketAside(name string) bool {
	return len(name) == 10 && strings.HasPrefix(name, ".s") && strings.Trim(name[2:], "0123456789abcdef") == ""
}

// cleanSocketArtifacts runs only with a verified lifetime lock. Never remove a
// socket that accepts a connection, even if somebody replaced the lock file.
// Only ECONNREFUSED proves a socket stale; ambiguous dial errors fail closed.
// Cooperating daemons never replace lock files. Hostile same-uid mutation of
// paths between validation and unlink is outside this advisory-lock boundary.
func (s *Store) cleanSocketArtifacts(name string, lock *os.File, owned map[string]os.FileInfo) error {
	if err := s.verifyLock(name+".lock", lock); err != nil {
		return err
	}
	items := []socketArtifact{
		{name: name, kind: unix.S_IFSOCK},
		{name: name + ".token", kind: unix.S_IFREG},
	}
	if owned == nil {
		items = append(items, socketArtifact{name: name + ".owner.json", kind: unix.S_IFREG})
		// Open a fresh directory cursor relative to the pinned fd, not the path.
		fd, err := unix.Openat(int(s.root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		dir := os.NewFile(uintptr(fd), "socket-artifacts")
		names, err := dir.Readdirnames(-1)
		dir.Close()
		if err != nil {
			return err
		}
		for _, entry := range names {
			if legacySocketAside(entry) {
				items = append(items, socketArtifact{name: entry})
			}
		}
	}
	// Validate the complete set before removing anything, so an unsafe token,
	// old owner record or aside cannot cause partial cleanup of safe artifacts.
	present := make([]socketArtifact, 0, len(items))
	for _, item := range items {
		if owned != nil && owned[item.name] == nil {
			continue
		}
		var st unix.Stat_t
		err := unix.Fstatat(int(s.root.Fd()), item.name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return err
		}
		if item.kind == 0 {
			item.kind = uint32(st.Mode) & unix.S_IFMT
		}
		if (item.kind != unix.S_IFSOCK && item.kind != unix.S_IFREG) || !privateArtifact(&st, item.kind) {
			return ErrUnsafePath
		}
		if owned != nil {
			info, err := os.Lstat(filepath.Join(s.path, item.name))
			if err != nil || !os.SameFile(owned[item.name], info) {
				return ErrCollision
			}
		}
		item.dev, item.ino = uint64(st.Dev), uint64(st.Ino)
		present = append(present, item)
	}
	// Check every socket before deleting any artifact, including stranded legacy
	// sockets. A successful connect preserves the socket AND its token unchanged.
	for _, item := range present {
		if item.kind == unix.S_IFSOCK {
			if err := socketInactive(filepath.Join(s.path, item.name)); err != nil {
				return err
			}
		}
	}
	for _, item := range present {
		if err := s.verifyLock(name+".lock", lock); err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstatat(int(s.root.Fd()), item.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
			!privateArtifact(&st, item.kind) || uint64(st.Dev) != item.dev || uint64(st.Ino) != item.ino {
			return ErrCollision
		}
		if err := unix.Unlinkat(int(s.root.Fd()), item.name, 0); err != nil {
			return err
		}
	}
	return unix.Fsync(int(s.root.Fd()))
}

func socketInactive(path string) error {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		conn.Close()
		return fmt.Errorf("agentd is already running for this state root: %w", ErrBusy)
	}
	if errors.Is(err, unix.ECONNREFUSED) {
		return nil
	}
	return fmt.Errorf("cannot establish stale socket %s: %w", path, err)
}
