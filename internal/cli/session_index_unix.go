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
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const sessionIndexLockFile = ".lock"

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
	entry, ok := parseSessionIndexEntry(raw)
	if !ok || !ownerAlive(entry.OwnerPID, entry.OwnerStart) {
		return "", "", sessionIndexRejected
	}
	id, label, bound := readBoundGeneration(entry.StateDir)
	if !bound {
		return "", "", sessionIndexRejected
	}
	return id, label, sessionIndexBound
}

type sessionIndexEntry struct {
	StateDir   string
	OwnerPID   int
	OwnerStart string
}

func parseSessionIndexEntry(raw []byte) (sessionIndexEntry, bool) {
	if len(raw) == 0 || len(raw) > 4096 || strings.ContainsRune(string(raw), 0) {
		return sessionIndexEntry{}, false
	}
	text := strings.TrimSuffix(string(raw), "\n")
	lines := strings.Split(text, "\n")
	if len(lines) != 3 {
		return sessionIndexEntry{}, false
	}
	dir := canonicalPrivatePath(lines[0])
	if dir == "" || lines[0] != dir {
		return sessionIndexEntry{}, false
	}
	pid, err := strconv.Atoi(lines[1])
	if err != nil || pid <= 0 || strconv.Itoa(pid) != lines[1] {
		return sessionIndexEntry{}, false
	}
	if !validOwnerStart(lines[2]) {
		return sessionIndexEntry{}, false
	}
	return sessionIndexEntry{StateDir: dir, OwnerPID: pid, OwnerStart: lines[2]}, true
}

func validOwnerStart(start string) bool {
	if start == "" || len(start) > 64 || start[0] == '.' || start[len(start)-1] == '.' {
		return false
	}
	dots := 0
	for _, c := range start {
		switch {
		case c >= '0' && c <= '9':
		case c == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// entryStateDir reads a current three-line entry or a legacy path line.
// Removal still understands the legacy line so an old binding can be withdrawn.
func entryStateDir(raw []byte) (string, bool) {
	if entry, ok := parseSessionIndexEntry(raw); ok {
		return entry.StateDir, true
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" || strings.ContainsAny(text, "\r\n\x00") || strings.TrimSpace(text) != text {
		return "", false
	}
	dir := canonicalPrivatePath(text)
	if dir == "" || dir != text {
		return "", false
	}
	return dir, true
}

func readBoundGeneration(stateDir string) (string, string, bool) {
	var st unix.Stat_t
	if unix.Lstat(stateDir, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Getuid()) || st.Mode&0o777 != 0o700 {
		return "", "", false
	}
	idRaw, err := readOwnerFile(filepath.Join(stateDir, "session.id"), 256)
	if err != nil {
		return "", "", false
	}
	id := strings.ToLower(strings.TrimSpace(string(idRaw)))
	if !validUUID(id) {
		return "", "", false
	}
	stateRaw, err := readOwnerFile(filepath.Join(stateDir, "state.json"), 1<<20)
	if err != nil {
		return "", "", false
	}
	var disk heartbeatDisk
	if json.Unmarshal(stateRaw, &disk) != nil || disk.Schema != heartbeatSchema || !strings.EqualFold(disk.SessionID, id) {
		return "", "", false
	}
	if disk.Closed || disk.Terminal {
		return "", "", false
	}
	return id, indexLabel(disk, stateDir), true
}

func stateDirSentLabel(dir string) string {
	abs := canonicalPrivatePath(dir)
	if abs == "" {
		return ""
	}
	var st unix.Stat_t
	if unix.Lstat(abs, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Getuid()) || st.Mode&0o777 != 0o700 {
		return ""
	}
	idRaw, err := readOwnerFile(filepath.Join(abs, "session.id"), 256)
	if err != nil {
		return ""
	}
	id := strings.ToLower(strings.TrimSpace(string(idRaw)))
	if !validUUID(id) {
		return ""
	}
	stateRaw, err := readOwnerFile(filepath.Join(abs, "state.json"), 1<<20)
	if err != nil {
		return ""
	}
	var disk heartbeatDisk
	if json.Unmarshal(stateRaw, &disk) != nil || !strings.EqualFold(disk.SessionID, id) {
		return ""
	}
	return indexLabel(disk, abs)
}

// sessionIndexLive reports whether raw is a binding lookup would still honor.
func sessionIndexLive(raw []byte) (string, bool) {
	entry, ok := parseSessionIndexEntry(raw)
	if !ok || !ownerAlive(entry.OwnerPID, entry.OwnerStart) {
		if ok {
			return entry.StateDir, false
		}
		return "", false
	}
	if _, _, bound := readBoundGeneration(entry.StateDir); !bound {
		return entry.StateDir, false
	}
	return entry.StateDir, true
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

func writeSessionIndex(source, stateDir string, ownerPID int, ownerStart string) error {
	source = strings.ToLower(strings.TrimSpace(source))
	if !validUUID(source) || ownerPID <= 0 || !validOwnerStart(ownerStart) {
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
	return withSessionIndexLock(true, func(dirfd int, root string) error {
		existing, err := readOwnerFile(filepath.Join(root, source), 4096)
		if err == nil {
			dir, live := sessionIndexLive(existing)
			if live && dir != abs {
				return sessionIndexConflictError(source)
			}
		}
		if err := storeSessionIndexEntry(dirfd, source, abs, ownerPID, ownerStart); err != nil {
			return err
		}
		dropOtherSessionIndexEntries(dirfd, root, source, abs)
		return nil
	})
}

func removeSessionIndexForState(stateDir string) {
	abs := canonicalPrivatePath(stateDir)
	if abs == "" {
		return
	}
	_ = withSessionIndexLock(false, func(dirfd int, root string) error {
		dropOtherSessionIndexEntries(dirfd, root, "", abs)
		return nil
	})
}

// withSessionIndexLock serializes publish and removal. create is false for
// removal so a missing index directory is left absent.
func withSessionIndexLock(create bool, fn func(dirfd int, root string) error) error {
	root, err := sessionIndexRoot()
	if err != nil {
		return err
	}
	if create {
		if err := mkdirPrivate(root); err != nil {
			return err
		}
	}
	dirfd, err := openNoFollowDir(root)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	lockfd, err := unix.Openat(dirfd, sessionIndexLockFile, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return errRefusedFile
	}
	defer unix.Close(lockfd)
	var st unix.Stat_t
	if unix.Fstat(lockfd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) {
		return errRefusedFile
	}
	if st.Mode&0o777 != 0o600 && unix.Fchmod(lockfd, 0o600) != nil {
		return errRefusedFile
	}
	if err := unix.Flock(lockfd, unix.LOCK_EX); err != nil {
		return errRefusedFile
	}
	defer unix.Flock(lockfd, unix.LOCK_UN)
	return fn(dirfd, root)
}

func storeSessionIndexEntry(dirfd int, source, stateDir string, ownerPID int, ownerStart string) error {
	payload := stateDir + "\n" + strconv.Itoa(ownerPID) + "\n" + ownerStart + "\n"
	if len(payload) > 4096 {
		return errRefusedFile
	}
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
	return nil
}

// dropOtherSessionIndexEntries unlinks index files whose stored state directory
// is still stateDir. The compare runs while the index lock is held. keep is
// left in place; an empty keep drops every match.
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
		if name == keep || name == sessionIndexLockFile || !validUUID(name) || name != strings.ToLower(name) {
			continue
		}
		raw, err := readOwnerFile(filepath.Join(root, name), 4096)
		if err != nil {
			continue
		}
		dir, ok := entryStateDir(raw)
		if !ok || dir != stateDir {
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
