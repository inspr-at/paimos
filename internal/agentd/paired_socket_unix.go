// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import "errors"

// ServePairedLocal uses the same lifetime lock as explicit local startup.
func ServePairedLocal(s *Supervisor, socket string, attachments ...*AttachManager) (*LocalServer, error) {
	if s == nil || s.state == nil {
		return nil, errors.New("invalid paired local socket")
	}
	return ServeLocal(s, socket, attachments...)
}
