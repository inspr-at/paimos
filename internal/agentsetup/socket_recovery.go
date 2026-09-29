// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type recoveryInode struct {
	device uint64
	inode  uint64
}

func privateRecoveryInode(st *unix.Stat_t, kind uint32) (recoveryInode, error) {
	if uint32(st.Mode) != kind|0600 || int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		return recoveryInode{}, ErrUnsafePath
	}
	return recoveryInode{uint64(st.Dev), uint64(st.Ino)}, nil
}

func (s *Store) recoveryInode(name string, kind uint32) (recoveryInode, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(int(s.root.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return recoveryInode{}, err
	}
	return privateRecoveryInode(&st, kind)
}

func (s *Store) recoveryAbsent(name string) bool {
	var st unix.Stat_t
	return errors.Is(unix.Fstatat(int(s.root.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW), unix.ENOENT)
}

// quarantineRecovery uses an exclusive rename so even an aside-name collision
// cannot overwrite state. Keep the basename shorter than agentd.sock: a valid
// socket near sun_path's limit must remain connectable after it is renamed.
func (s *Store) quarantineRecovery(name string) (string, error) {
	for range 8 {
		id, err := randomSecret()
		if err != nil {
			return "", err
		}
		aside := ".s" + string(id[:8])
		err = renameExclusive(int(s.root.Fd()), name, aside)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		return aside, err
	}
	return "", ErrCollision
}

func probeRecoverySocket(path string) error {
	conn, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if conn != nil {
		conn.Close()
	}
	return err
}

// RecoverUnrecordedSocket requires the socket's lifetime lock to be held. It
// recovers only private, owned artifacts with no owner record and a refused
// connection to the quarantined socket. All filesystem operations are relative
// to the private store descriptor; a new listener at name is never unlinked.
func (s *Store) RecoverUnrecordedSocket(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoverUnrecordedSocket(name, probeRecoverySocket)
}

// The probe seam lets tests bind a real replacement at the exact cleanup gap.
func (s *Store) recoverUnrecordedSocket(name string, probe func(string) error) (result error) {
	if s.root == nil || !validName(name) || !validName(name+".owner.json") {
		return ErrUnsafePath
	}
	ownerName, tokenName := name+".owner.json", name+".token"
	if !s.recoveryAbsent(ownerName) {
		return ErrCollision
	}
	want, err := s.recoveryInode(name, unix.S_IFSOCK)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	token, tokenErr := s.recoveryInode(tokenName, unix.S_IFREG)
	if tokenErr != nil && !errors.Is(tokenErr, unix.ENOENT) {
		return tokenErr
	}
	aside, err := s.quarantineRecovery(name)
	if err != nil {
		return err
	}
	restore := func(from, to string) {
		if err := renameExclusive(int(s.root.Fd()), from, to); err != nil {
			// In particular, EEXIST keeps BOTH inodes. Never overwrite a
			// listener that bound the original name while it was absent.
			result = errors.Join(result, fmt.Errorf("recovery artifact retained at %q: %w", filepath.Join(s.path, from), ErrCollision), err)
		}
	}
	defer func() {
		if aside != "" {
			restore(aside, name)
		}
	}()
	if got, err := s.recoveryInode(aside, unix.S_IFSOCK); err != nil || got != want {
		return ErrCollision
	}
	// Probing the original path before rename leaves an unlink race. Probe
	// the moved inode itself, including a live listener moved in before rename.
	if !errors.Is(probe(filepath.Join(s.path, aside)), unix.ECONNREFUSED) {
		return ErrCollision
	}
	if !s.recoveryAbsent(ownerName) {
		return ErrCollision
	}
	got, err := s.recoveryInode(tokenName, unix.S_IFREG)
	if tokenErr == nil {
		if err != nil || got != token {
			return ErrCollision
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return ErrCollision
	}
	// Isolate the token too: never unlink a newly published token by its
	// original name. On refusal, restore each artifact without clobbering.
	tokenAside := ""
	if tokenErr == nil {
		tokenAside, err = s.quarantineRecovery(tokenName)
		if err != nil {
			return err
		}
		defer func() {
			if tokenAside != "" {
				restore(tokenAside, tokenName)
			}
		}()
		if got, err := s.recoveryInode(tokenAside, unix.S_IFREG); err != nil || got != token {
			return ErrCollision
		}
	}
	if got, err := s.recoveryInode(aside, unix.S_IFSOCK); err != nil || got != want || !s.recoveryAbsent(ownerName) {
		return ErrCollision
	}
	// Remove the token first so a second crash cannot strand an unrecorded
	// token with no socket. Only quarantined names are ever removed.
	if tokenAside != "" {
		if err := unix.Unlinkat(int(s.root.Fd()), tokenAside, 0); err != nil {
			return err
		}
		tokenAside = ""
	}
	if err := unix.Unlinkat(int(s.root.Fd()), aside, 0); err != nil {
		return err
	}
	aside = ""
	if !s.recoveryAbsent(name) {
		return ErrCollision
	}
	return nil
}
