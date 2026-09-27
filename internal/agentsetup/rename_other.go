// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package agentsetup

import "errors"

func renameExclusive(int, string, string) error {
	return errors.New("atomic identity creation unsupported on this platform")
}
