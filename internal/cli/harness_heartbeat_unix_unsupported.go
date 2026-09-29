//go:build aix || dragonfly || freebsd || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

// readOwnerStamp fails closed where the helper cannot prove a process start
// identity. A bare PID check would keep a reused or zombie owner "alive".
func readOwnerStamp(int) (ownerStamp, error) {
	return ownerStamp{}, errOwnerGone
}
