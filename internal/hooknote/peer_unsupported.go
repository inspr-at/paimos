// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package hooknote

import "net"

// snapshot and observe fail closed where the kernel peer calls this feature
// requires are not implemented. A missing check is not a successful match.
func snapshot(net.Conn, bool) (Process, error) { return Process{}, ErrPeer }

func observe(int, bool) (Process, error) { return Process{}, ErrPeer }
