// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package hooknote

import (
	"errors"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func withFD(c net.Conn, fn func(fd int) error) error {
	sc, ok := c.(syscall.Conn)
	if !ok || c == nil {
		return ErrPeer
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return ErrPeer
	}
	var out error
	if err = raw.Control(func(fd uintptr) { out = fn(int(fd)) }); err != nil {
		return ErrPeer
	}
	return out
}

// setCloexec keeps the socket out of a child after fork or exec.
// A cleared flag is descriptor inheritance and the peer check fails closed.
func setCloexec(fd int) error {
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return errors.New("socket is inheritable")
	}
	return nil
}
