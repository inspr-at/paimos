// SPDX-License-Identifier: AGPL-3.0-only
//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package agentd

func confirmedLedgerExit(Record) bool { return false }
