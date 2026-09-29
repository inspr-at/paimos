//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openHeartbeatHold(path string) (heartbeatHold, error) {
	dir, err := openPrivateHeartbeatDir(path)
	if err != nil {
		return heartbeatHold{}, err
	}
	hold := heartbeatHold{dir: dir}
	lock, err := hold.openLock()
	if err != nil {
		_ = dir.Close()
		return heartbeatHold{}, err
	}
	hold.lock = lock
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		hold.release()
		return heartbeatHold{}, errHeartbeatBusy
	}
	return hold, nil
}

// openPrivateHeartbeatDir opens the state directory without following a final
// symlink. An existing directory must already be owned by this user and mode
// 0700; creation is the only path that sets those bits.
func openPrivateHeartbeatDir(path string) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	if abs == string(os.PathSeparator) {
		return nil, errHeartbeatState
	}
	info, lerr := os.Lstat(abs)
	created := false
	switch {
	case lerr == nil && info.Mode()&os.ModeSymlink != 0:
		return nil, errHeartbeatState
	case errors.Is(lerr, os.ErrNotExist):
		if err := os.Mkdir(abs, 0o700); err != nil {
			return nil, err
		}
		created = true
	case lerr != nil:
		return nil, lerr
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, filepath.Base(abs), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errHeartbeatState
	}
	if created && unix.Fchmod(fd, 0o700) != nil {
		unix.Close(fd)
		return nil, errHeartbeatState
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Getuid()) || st.Mode&0o777 != 0o700 {
		unix.Close(fd)
		return nil, errHeartbeatState
	}
	return os.NewFile(uintptr(fd), abs), nil
}

func (h *heartbeatHold) release() {
	if h == nil {
		return
	}
	if h.lock != nil {
		_ = h.lock.Close()
		h.lock = nil
	}
	if h.dir != nil {
		_ = h.dir.Close()
		h.dir = nil
	}
}

func (h *heartbeatHold) openLock() (*os.File, error) {
	if h == nil || h.dir == nil {
		return nil, errHeartbeatState
	}
	fd, err := unix.Openat(int(h.dir.Fd()), "heartbeat.lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(h.dir.Fd()), "heartbeat.lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, err
	}
	if created && unix.Fchmod(fd, 0o600) != nil {
		unix.Close(fd)
		return nil, errHeartbeatState
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || st.Mode&0o777 != 0o600 {
		unix.Close(fd)
		return nil, errHeartbeatState
	}
	return os.NewFile(uintptr(fd), "heartbeat.lock"), nil
}

func (h *heartbeatHold) readFile(name string, max int) ([]byte, error) {
	if h == nil || h.dir == nil || !validStateName(name) || max <= 0 {
		return nil, errHeartbeatState
	}
	fd, err := unix.Openat(int(h.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, errHeartbeatState
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || st.Mode&0o777 != 0o600 || st.Size < 0 || st.Size > int64(max) {
		return nil, errHeartbeatState
	}
	buf := make([]byte, st.Size)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (h *heartbeatHold) writeFile(name string, raw []byte) error {
	if h == nil || h.dir == nil || !validStateName(name) || len(raw) > 1<<20 {
		return errHeartbeatState
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".tmp-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(h.dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, werr := f.Write(raw)
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = unix.Unlinkat(int(h.dir.Fd()), tmp, 0)
		return errors.New("heartbeat state write failed")
	}
	if err := unix.Renameat(int(h.dir.Fd()), tmp, int(h.dir.Fd()), name); err != nil {
		_ = unix.Unlinkat(int(h.dir.Fd()), tmp, 0)
		return err
	}
	_ = unix.Fsync(int(h.dir.Fd()))
	return nil
}

func (h *heartbeatHold) remove(name string) error {
	if h == nil || h.dir == nil || !validStateName(name) {
		return errHeartbeatState
	}
	err := unix.Unlinkat(int(h.dir.Fd()), name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

var errRefusedFile = errors.New("refusing an unowned, linked or non-regular file")

// openNoFollow opens a regular file owned by this user without following any
// path component. macOS publishes /var, /tmp and /etc as symlinks; only that
// system prefix is rewritten to the link's direct target. Every later
// component is opened with O_NOFOLLOW, so a symlinked parent inside the
// usage tree is refused. O_NONBLOCK rejects a FIFO without waiting.
func openNoFollow(path string) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	abs = rewriteSystemSymlinkPrefix(filepath.Clean(abs))
	if abs == string(os.PathSeparator) {
		return nil, errRefusedFile
	}
	parent, base := filepath.Split(abs)
	parent = filepath.Clean(parent)
	base = strings.TrimSuffix(base, string(os.PathSeparator))
	if base == "" || base == "." || base == ".." {
		return nil, errRefusedFile
	}
	dirfd, err := openNoFollowDir(parent)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirfd)
	fd, err := unix.Openat(dirfd, base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) {
		unix.Close(fd)
		return nil, errRefusedFile
	}
	return os.NewFile(uintptr(fd), abs), nil
}

func openNoFollowDir(dir string) (int, error) {
	fd, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if dir == string(os.PathSeparator) {
		return fd, nil
	}
	rel := strings.TrimPrefix(filepath.Clean(dir), string(os.PathSeparator))
	for _, name := range strings.Split(rel, string(os.PathSeparator)) {
		if name == "" || name == "." || name == ".." {
			unix.Close(fd)
			return -1, errRefusedFile
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if errors.Is(err, unix.ENOENT) {
			return -1, os.ErrNotExist
		}
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// rewriteSystemSymlinkPrefix replaces a root-level system symlink (/var,
// /tmp, /etc) with the direct target of that symlink. Later components are
// not rewritten and are not followed.
func rewriteSystemSymlinkPrefix(abs string) string {
	for _, name := range []string{"var", "tmp", "etc"} {
		prefix := string(os.PathSeparator) + name
		if abs != prefix && !strings.HasPrefix(abs, prefix+string(os.PathSeparator)) {
			continue
		}
		info, err := os.Lstat(prefix)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return abs
		}
		target, err := os.Readlink(prefix)
		if err != nil || target == "" || strings.Contains(target, "..") || strings.Contains(target, "\x00") {
			return abs
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(string(os.PathSeparator), target)
		}
		target = filepath.Clean(target)
		if !filepath.IsAbs(target) || target == prefix || strings.HasPrefix(target, prefix+string(os.PathSeparator)) {
			return abs
		}
		rest := strings.TrimPrefix(abs, prefix)
		return filepath.Clean(target + rest)
	}
	return abs
}
