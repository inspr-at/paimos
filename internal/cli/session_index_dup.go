//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import "golang.org/x/sys/unix"

// dupCloexec duplicates fd and sets FD_CLOEXEC in the same syscall.
func dupCloexec(fd int) (int, error) {
	return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
}
