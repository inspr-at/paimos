// SPDX-License-Identifier: AGPL-3.0-only
//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package agentd

import "errors"

type LocalServer struct{}

func ServeLocal(*Supervisor, string, ...*AttachManager) (*LocalServer, error) {
	return nil, errors.New("local agentd transport unsupported on this platform")
}
func (*LocalServer) Close() error { return nil }
