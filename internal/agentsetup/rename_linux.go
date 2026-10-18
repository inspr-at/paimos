// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import "golang.org/x/sys/unix"

func renameExclusive(fd int, from, to string) error {
	return unix.Renameat2(fd, from, fd, to, unix.RENAME_NOREPLACE)
}

func exchangeHookSettings(fd int, from, to string) error {
	return unix.Renameat2(fd, from, fd, to, unix.RENAME_EXCHANGE)
}
