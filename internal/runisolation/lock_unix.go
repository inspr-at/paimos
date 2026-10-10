// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package runisolation

import (
	"context"
	"errors"
	"os"
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
	f, err := root.OpenFile(name, flags|unix.O_NOFOLLOW, 0600)
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

func waitLock(ctx context.Context, root *os.Root, name string) (*os.File, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if err := bounded.Err(); err != nil {
			return nil, err
		}
		f, err := lockFile(root, name, true)
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
