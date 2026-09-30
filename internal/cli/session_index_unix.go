//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func sessionIndexRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errRefusedFile
	}
	root := canonicalPrivatePath(filepath.Join(home, ".aeon", "sessions", "index"))
	if root == "" {
		return "", errRefusedFile
	}
	return root, nil
}

func lookupSessionIndex(vendor string) (string, string, sessionIndexResult) {
	source := strings.ToLower(strings.TrimSpace(vendor))
	if !validUUID(source) {
		return "", "", sessionIndexAbsent
	}
	root, err := sessionIndexRoot()
	if err != nil {
		return "", "", sessionIndexAbsent
	}
	raw, err := readOwnerFile(filepath.Join(root, source), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", sessionIndexAbsent
	}
	if err != nil {
		return "", "", sessionIndexRejected
	}
	stateDir := canonicalPrivatePath(strings.TrimSpace(string(raw)))
	if stateDir == "" || strings.ContainsAny(string(raw), "\x00") || strings.Contains(strings.TrimSpace(string(raw)), "\n") {
		return "", "", sessionIndexRejected
	}
	var st unix.Stat_t
	if unix.Lstat(stateDir, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Getuid()) || st.Mode&0o777 != 0o700 {
		return "", "", sessionIndexRejected
	}
	idRaw, err := readOwnerFile(filepath.Join(stateDir, "session.id"), 256)
	if err != nil {
		return "", "", sessionIndexRejected
	}
	id := strings.ToLower(strings.TrimSpace(string(idRaw)))
	if !validUUID(id) {
		return "", "", sessionIndexRejected
	}
	stateRaw, err := readOwnerFile(filepath.Join(stateDir, "state.json"), 1<<20)
	if err != nil {
		return "", "", sessionIndexRejected
	}
	var disk heartbeatDisk
	if json.Unmarshal(stateRaw, &disk) != nil || disk.Schema != heartbeatSchema || !strings.EqualFold(disk.SessionID, id) {
		return "", "", sessionIndexRejected
	}
	if disk.Closed || disk.Terminal {
		return "", "", sessionIndexRejected
	}
	return id, indexLabel(disk, stateDir), sessionIndexBound
}

func indexLabel(disk heartbeatDisk, stateDir string) string {
	if label := heartbeatText(disk.SentLabel, 128); label != "" {
		return label
	}
	if label := heartbeatText(filepath.Base(stateDir), 64); label != "" {
		return label
	}
	return "session"
}

func writeSessionIndex(source, stateDir string) error {
	source = strings.ToLower(strings.TrimSpace(source))
	if !validUUID(source) {
		return errRefusedFile
	}
	abs := canonicalPrivatePath(stateDir)
	if abs == "" {
		return errRefusedFile
	}
	var st unix.Stat_t
	if unix.Lstat(abs, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Getuid()) || st.Mode&0o777 != 0o700 {
		return errRefusedFile
	}
	root, err := sessionIndexRoot()
	if err != nil {
		return err
	}
	if err := mkdirPrivate(root); err != nil {
		return err
	}
	dirfd, err := openNoFollowDir(root)
	if err != nil {
		return errRefusedFile
	}
	defer unix.Close(dirfd)
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".tmp-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(dirfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return errRefusedFile
	}
	f := os.NewFile(uintptr(fd), tmp)
	payload := abs + "\n"
	_, werr := f.Write([]byte(payload))
	if werr == nil {
		werr = unix.Fchmod(int(f.Fd()), 0o600)
	}
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = unix.Unlinkat(dirfd, tmp, 0)
		return errRefusedFile
	}
	if err := unix.Renameat(dirfd, tmp, dirfd, source); err != nil {
		_ = unix.Unlinkat(dirfd, tmp, 0)
		return errRefusedFile
	}
	_ = unix.Fsync(dirfd)
	dropOtherSessionIndexEntries(dirfd, root, source, abs)
	return nil
}

func removeSessionIndexForState(stateDir string) {
	abs := canonicalPrivatePath(stateDir)
	if abs == "" {
		return
	}
	root, err := sessionIndexRoot()
	if err != nil {
		return
	}
	dirfd, err := openNoFollowDir(root)
	if err != nil {
		return
	}
	defer unix.Close(dirfd)
	dropOtherSessionIndexEntries(dirfd, root, "", abs)
}

// dropOtherSessionIndexEntries unlinks index files whose text is stateDir.
// keep is left in place; an empty keep drops every match. Callers pass the
// directory fd so the final name is not followed.
func dropOtherSessionIndexEntries(dirfd int, root, keep, stateDir string) {
	dup, err := unix.Dup(dirfd)
	if err != nil {
		return
	}
	dir := os.NewFile(uintptr(dup), root)
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil || closeErr != nil {
		return
	}
	if len(entries) > 4096 {
		entries = entries[:4096]
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == keep || !validUUID(name) || name != strings.ToLower(name) {
			continue
		}
		raw, err := readOwnerFile(filepath.Join(root, name), 4096)
		if err != nil {
			continue
		}
		if canonicalPrivatePath(strings.TrimSpace(string(raw))) != stateDir {
			continue
		}
		_ = unix.Unlinkat(dirfd, name, 0)
	}
}

func readOwnerFile(path string, max int) ([]byte, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Mode().Perm() != 0o600 || st.Size() < 0 || st.Size() > int64(max) {
		return nil, errRefusedFile
	}
	buf := make([]byte, st.Size())
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func canonicalPrivatePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\r\n\x00") {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = rewriteSystemSymlinkPrefix(filepath.Clean(abs))
	if !filepath.IsAbs(abs) || abs == string(os.PathSeparator) {
		return ""
	}
	return abs
}

// mkdirPrivate creates abs without following a symlink at any component.
// Directories from .aeon downward are owner-only. Ancestors such as /Users
// stay untouched.
func mkdirPrivate(abs string) error {
	abs = canonicalPrivatePath(abs)
	if abs == "" {
		return errRefusedFile
	}
	fd, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return errRefusedFile
	}
	owned := false
	for _, name := range strings.Split(strings.TrimPrefix(abs, string(os.PathSeparator)), string(os.PathSeparator)) {
		if name == "" || name == "." || name == ".." {
			unix.Close(fd)
			return errRefusedFile
		}
		created := false
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			if mk := unix.Mkdirat(fd, name, 0o700); mk != nil {
				unix.Close(fd)
				return errRefusedFile
			}
			created = true
			next, err = unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if err != nil {
			return errRefusedFile
		}
		var st unix.Stat_t
		if unix.Fstat(next, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			unix.Close(next)
			return errRefusedFile
		}
		// ~/.aeon may already exist for config. Require ownership from there
		// down, and force 0700 on the index directories without relabeling an
		// existing config directory.
		if name == ".aeon" || owned {
			if st.Uid != uint32(os.Getuid()) {
				unix.Close(next)
				return errRefusedFile
			}
		}
		if name == ".aeon" {
			owned = true
		}
		if created || (owned && name != ".aeon") {
			if st.Mode&0o777 != 0o700 && unix.Fchmod(next, 0o700) != nil {
				unix.Close(next)
				return errRefusedFile
			}
		}
		fd = next
	}
	unix.Close(fd)
	return nil
}
