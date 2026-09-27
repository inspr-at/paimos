// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import "golang.org/x/sys/unix"

func renameExclusive(fd int, from, to string) error {
	return unix.RenameatxNp(fd, from, fd, to, unix.RENAME_EXCL)
}
