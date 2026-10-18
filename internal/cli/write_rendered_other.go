//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import "fmt"

func writeRenderedRelative(workspace, rel, body string) error {
	return fmt.Errorf("workspace-contained renders require a unix host")
}
