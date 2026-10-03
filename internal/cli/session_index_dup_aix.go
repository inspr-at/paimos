//go:build aix

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// dupCloexec duplicates fd and sets FD_CLOEXEC.
// AIX has no F_DUPFD_CLOEXEC in x/sys (Go records the command as unsupported).
// ForkLock stops a Go exec from inheriting the copy between the two calls.
func dupCloexec(fd int) (int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	n, err := unix.Dup(fd)
	if err != nil {
		return -1, err
	}
	syscall.CloseOnExec(n)
	return n, nil
}
