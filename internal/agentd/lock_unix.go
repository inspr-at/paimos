// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"os"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func acquireInstanceLock(root, id string) (*os.File, error) {
	s, err := agentsetup.OpenStore(root, false)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.LockNamed("aeon-agentd-" + id + ".lock")
}
