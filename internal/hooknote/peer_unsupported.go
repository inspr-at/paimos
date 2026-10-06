// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package hooknote

import "net"

// Snapshot and Observe fail closed where the kernel peer calls this feature
// requires are not implemented. A missing check is not a successful match.
func Snapshot(net.Conn) (Process, error) { return Process{}, ErrPeer }

func Observe(int) (Process, error) { return Process{}, ErrPeer }
