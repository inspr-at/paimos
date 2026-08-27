//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

package harness

import "os/exec"

// configureProcessGroup is a no-op where process groups are unavailable; the
// timeout then kills only the direct child.
func configureProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
