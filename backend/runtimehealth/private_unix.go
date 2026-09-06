//go:build darwin || linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func owned(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(s.Uid) == int64(os.Geteuid()) && (info.IsDir() || s.Nlink == 1)
}

// Every private directory component below the selected root must be real. The
// root's ancestors may be system directories (e.g. macOS /var -> /private/var).
func privateDir(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		parent := filepath.Dir(path)
		parentInfo, parentErr := os.Stat(parent)
		if errors.Is(parentErr, os.ErrNotExist) {
			if err = privateDir(parent, true); err != nil {
				return err
			}
		} else if parentErr != nil || !parentInfo.IsDir() || !trustedDefinitionOwner(parentInfo) || parentInfo.Mode().Perm()&0022 != 0 && parentInfo.Mode()&os.ModeSticky == 0 {
			return errors.New("private directory parent unsafe")
		}

		if err = os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return errors.New("private directory creation failed")
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return errors.New("private directory unavailable")
	}
	if !info.IsDir() || !owned(info) || info.Mode().Perm() != 0700 {
		return errors.New("private directory ownership, mode or type unsafe")
	}
	return nil
}
func safeFile(path string, socket bool) (os.FileInfo, error) {
	i, e := os.Lstat(path)
	if e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, errors.New("private state unavailable")
	}
	good := i.Mode().IsRegular()
	if socket {
		good = i.Mode()&os.ModeSocket != 0
	}
	if !good || !owned(i) || i.Mode().Perm() != 0600 {
		return nil, errors.New("private state ownership, mode or type unsafe")
	}
	return i, nil
}
func readPrivate(path string, max int64) ([]byte, error) {
	i, e := safeFile(path, false)
	if e != nil {
		return nil, e
	}
	if i.Size() > max {
		return nil, errors.New("private state exceeds bound")
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- fixed private runtime state; safeFile plus no-follow and matching descriptor ownership checks fence the read.
	if e != nil {
		return nil, errors.New("private state unavailable")
	}
	defer f.Close()
	actual, e := f.Stat()
	if e != nil || !os.SameFile(i, actual) || !owned(actual) {
		return nil, errors.New("private state changed")
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, errors.New("private state exceeds bound")
	}
	return b, nil
}
func writePrivate(path string, b []byte) error {
	if _, err := os.Lstat(path); err == nil {
		if _, err = safeFile(path, false); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("private state unavailable")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".runtime-*")
	if err != nil {
		return errors.New("private state write failed")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("private state write failed")
	}
	if os.Rename(f.Name(), path) != nil {
		return errors.New("private state commit failed")
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	d, e := os.Open(path) // #nosec G304 -- internal validated state/archive directory only, opened for directory sync.
	if e != nil {
		return errors.New("private directory sync failed")
	}
	defer d.Close()
	if d.Sync() != nil {
		return errors.New("private directory sync failed")
	}
	return nil
}

type stateLock struct{ f *os.File }

func lockState(dir, name string) (*stateLock, error) {
	if err := privateDir(dir, false); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, name)
	f, e := os.OpenFile(p, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600) // #nosec G304 -- fixed lock name under privateDir; no-follow and matching safeFile descriptor checks below.
	if e != nil {
		return nil, errors.New("runtime lock unavailable")
	}
	i, e := f.Stat()
	pinfo, pe := safeFile(p, false)
	if e != nil || pe != nil || !os.SameFile(i, pinfo) {
		f.Close()
		return nil, errors.New("runtime lock unsafe")
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("runtime lock held")
	}
	return &stateLock{f}, nil
}
func (l *stateLock) Close() { _ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN); _ = l.f.Close() }
func lockHeld(dir string) (bool, error) {
	p := filepath.Join(dir, "agentd.lock")
	if _, e := safeFile(p, false); errors.Is(e, os.ErrNotExist) {
		return false, nil
	} else if e != nil {
		return false, e
	}
	f, e := os.OpenFile(p, os.O_RDWR|syscall.O_NOFOLLOW, 0) // #nosec G304 -- fixed agentd.lock beneath the private instance directory; safeFile metadata checked above and symlinks refused.
	if e != nil {
		return false, errors.New("runtime lock unavailable")
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, nil
	} else if errors.Is(e, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, errors.New("runtime lock unknown")
}

func trustedDefinitionOwner(i os.FileInfo) bool {
	s, ok := i.Sys().(*syscall.Stat_t)
	return ok && (s.Uid == 0 || int64(s.Uid) == int64(os.Geteuid()))
}

func writableDirectory(path string) bool { return syscall.Access(path, 2) == nil }
