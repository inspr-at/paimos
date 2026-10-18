// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/inspr-at/paimos/internal/agentsecurity"
	"golang.org/x/sys/unix"
)

func vaultedName(name string) bool          { return name == snapshotName || name == "runtime.key" }
func (s *Store) vaultID(name string) string { return Hash([]byte(s.path)) + "/" + name }

// Read-only clients never compete for the migration lock. Keychain returns a
// complete item; legacy snapshots are read through an owned, read-only inode.
// An ACL denial never falls back to disk, and conflicting legacy state still
// fails closed. Migration/cleanup belongs exclusively to a writable opener.
func (s *Store) readVaultSnapshot(name string, max int64) ([]byte, error) {
	raw, vaultErr := s.vault.Read(s.vaultID(name))
	if vaultErr != nil && !errors.Is(vaultErr, os.ErrNotExist) {
		return nil, vaultErr
	}
	if int64(len(raw)) > max {
		return nil, ErrUnsafePath
	}
	f, err := s.openFile(name, unix.O_RDONLY, name == snapshotName)
	if errors.Is(err, os.ErrNotExist) {
		return raw, vaultErr
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	legacy, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(legacy)) > max {
		return nil, ErrUnsafePath
	}
	if vaultErr == nil {
		if !bytes.Equal(raw, legacy) && !allZero(legacy) {
			return nil, ErrCollision
		}
		return raw, nil
	}
	if !validVaultContent(name, legacy) {
		return nil, ErrUnsafePath
	}
	return legacy, nil
}

// readVault runs under Store.mu. A failed ACL check never falls back to disk.
// Migration is restart-safe: persist and verify the Keychain item first, then
// overwrite and unlink only the exact private inode whose bytes were imported.
// A pre-created Keychain item or a hardlink refuses migration and leaves the
// legacy 0600 file in place. That refusal does not rotate the secret or create
// an enclave key; rotating a legacy secret is a separate pairing choice.
func (s *Store) readVault(name string, max int64) (_ []byte, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("read Keychain-backed state %s: %w", filepath.Join(s.path, name), resultErr)
		}
	}()
	guard, err := s.lockNamed("keychain-migration.lock")
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	raw, err := s.vault.Read(s.vaultID(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, fileErr := s.open(name, unix.O_RDWR)
	if errors.Is(fileErr, os.ErrNotExist) {
		if err == nil && int64(len(raw)) > max {
			return nil, ErrUnsafePath
		}
		return raw, err
	}
	if fileErr != nil {
		return nil, fileErr
	}
	defer f.Close()
	legacy, readErr := io.ReadAll(io.LimitReader(f, max+1))
	if readErr != nil || int64(len(legacy)) > max {
		return nil, ErrUnsafePath
	}
	if err == nil {
		// A crash after overwriting leaves a zeroed inode to finish removing.
		if !bytes.Equal(raw, legacy) && !allZero(legacy) {
			return nil, ErrCollision
		}
	} else {
		if !validVaultContent(name, legacy) {
			return nil, ErrUnsafePath
		}
		if err = s.vault.Write(s.vaultID(name), legacy, true); err != nil {
			return nil, err
		}
		raw, err = s.vault.Read(s.vaultID(name))
		if err != nil || !bytes.Equal(raw, legacy) {
			return nil, errors.New("Keychain migration verification failed")
		}
	}
	if err = s.erasePrivateFile(name, f, len(legacy)); err != nil {
		return nil, err
	}
	return raw, nil
}

func allZero(raw []byte) bool {
	for _, b := range raw {
		if b != 0 {
			return false
		}
	}
	return true
}
func validVaultContent(name string, raw []byte) bool {
	if name == "runtime.key" {
		return regexp.MustCompile(`^aeon_[A-Za-z0-9_-]{1,64}_[0-9a-f]{64}$`).Match(raw)
	}
	var v snapshot
	return json.Unmarshal(raw, &v) == nil && v.Schema == "aeon.agent-setup.private.v1" && uuidPattern.MatchString(v.Request.RequestID) && hashPattern.MatchString(string(v.Lifecycle)) && (v.ComputerCleaned || hashPattern.MatchString(string(v.Device)) && hashPattern.MatchString(string(v.Runtime)))
}

// This is a best-effort logical overwrite; APFS snapshots/backups may retain
// older blocks. It does not follow symlinks and rejects observed inode swaps,
// including a hardlink (nlink != 1). The advisory guard serializes helpers,
// not hostile same-UID namespace edits. Refusal leaves the legacy file in place.
func (s *Store) erasePrivateFile(name string, f *os.File, size int) error {
	var held, named unix.Stat_t
	if unix.Fstat(int(f.Fd()), &held) != nil || !privateArtifact(&held, unix.S_IFREG) {
		return ErrUnsafePath
	}
	if unix.Fstatat(int(s.root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || held.Dev != named.Dev || held.Ino != named.Ino {
		return ErrCollision
	}
	if _, err := f.WriteAt(make([]byte, size), 0); err != nil {
		return errors.New("private state overwrite failed")
	}
	if err := f.Sync(); err != nil {
		return errors.New("private state overwrite failed")
	}
	if unix.Fstatat(int(s.root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || held.Dev != named.Dev || held.Ino != named.Ino {
		return ErrCollision
	}
	if unix.Unlinkat(int(s.root.Fd()), name, 0) != nil || unix.Fsync(int(s.root.Fd())) != nil {
		return errors.New("private state migration cleanup failed")
	}
	return nil
}

func (s *Store) writeVault(name string, raw []byte, first bool) error {
	if !validVaultContent(name, raw) {
		return ErrUnsafePath
	}
	// Import existing disk state before any update, so failures retain authority.
	if _, err := s.readVault(name, 1<<20); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := s.vault.Write(s.vaultID(name), raw, first)
	if errors.Is(err, agentsecurity.ErrExists) {
		return ErrCollision
	}
	return err
}
