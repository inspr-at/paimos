// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package runisolation

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

func openRecord(root *os.Root) (*os.File, error) {
	return root.OpenFile("record.json", os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func lockFile(root *os.Root, name string, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := openLock(root, name, flags, create)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err == nil {
		a, e1 := f.Stat()
		b, e2 := root.Lstat(name)
		if e1 != nil || e2 != nil || !a.Mode().IsRegular() || !os.SameFile(a, b) {
			err = errors.New("isolation lock identity changed")
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return f, nil
}

func openLock(root *os.Root, name string, flags int, create bool) (*os.File, error) {
	if create && runtime.GOOS == "darwin" {
		// Darwin's simultaneous O_CREAT|O_NOFOLLOW opens can return ENOENT
		// for a present directory. A fresh directory descriptor serializes
		// creation only, following agentsetup.Store's established guard.
		guard, err := root.Open(".")
		if err != nil {
			return nil, err
		}
		defer guard.Close()
		for {
			err = unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		if err != nil {
			return nil, err
		}
	}
	return root.OpenFile(name, flags|unix.O_NOFOLLOW, 0600)
}

func waitLock(ctx context.Context, root *os.Root, name string, create bool) (*os.File, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if err := bounded.Err(); err != nil {
			return nil, err
		}
		f, err := lockFile(root, name, create)
		if !errors.Is(err, ErrBusy) {
			return f, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-bounded.Done():
			timer.Stop()
			return nil, bounded.Err()
		case <-timer.C:
		}
	}
}
