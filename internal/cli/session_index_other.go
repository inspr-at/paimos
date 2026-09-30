//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

func lookupSessionIndex(string) (string, string, sessionIndexResult) {
	return "", "", sessionIndexAbsent
}

func writeSessionIndex(string, string, int, string) error { return nil }

func removeSessionIndexForState(string) {}

func stateDirSentLabel(string) string { return "" }
