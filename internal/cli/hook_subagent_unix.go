//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func openSubagentParent(path string) (*os.File, error) {
	path = canonicalPrivatePath(path)
	if path == "" {
		return nil, errHeartbeatState
	}
	fd, err := openNoFollowDir(path)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Getuid()) || st.Mode&0777 != 0700 {
		unix.Close(fd)
		return nil, errHeartbeatState
	}
	return os.NewFile(uintptr(fd), path), nil
}

// Child paths are opened relative to validated directory descriptors. No hook
// payload can choose a path, follow a symlink, or loosen an existing mode.
func openSubagentDir(parent *os.File, name string, create bool) (*os.File, error) {
	if parent == nil || (name != "subagents" && !subagentDirName(name)) {
		return nil, errHeartbeatState
	}
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Getuid()) || st.Mode&0777 != 0700 {
		unix.Close(fd)
		return nil, errHeartbeatState
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), nil
}

func lockSubagentDir(dir *os.File) (heartbeatHold, error) {
	hold := heartbeatHold{dir: dir}
	lock, err := hold.openLock()
	if err != nil {
		return heartbeatHold{}, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return heartbeatHold{}, errHeartbeatBusy
	}
	hold.lock = lock
	return hold, nil
}

// This short lock covers lifecycle publication and the local reset only.
// Never hold it over a network request or while acquiring the child lock.
func lockSubagentLifecycle(ctx context.Context, dir *os.File) (*os.File, error) {
	hold := heartbeatHold{dir: dir}
	lock, err := hold.openNamedLock("subagent.lifecycle.lock")
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			lock.Close()
			return nil, err
		}
		err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			lock.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
