// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"path/filepath"
	"strings"
)

// ServiceExecutable keeps the package manager's stable entry point without
// trusting PATH or executing brew. The link must resolve to this running binary.
// Nix executables remain physical so declarative ownership is still detected.
func ServiceExecutable(executable, home string) (string, error) {
	physical, err := filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(physical) {
		return "", ErrUnsafePath
	}
	if strings.HasPrefix(physical, "/nix/store/") {
		return physical, nil
	}
	stable := ""
	// Homebrew: <prefix>/Cellar/aeon-agentd/<version>/bin/aeon-agentd.
	packageDir := filepath.Dir(filepath.Dir(filepath.Dir(physical)))
	cellar := filepath.Dir(packageDir)
	if filepath.Base(physical) == "aeon-agentd" && filepath.Base(filepath.Dir(physical)) == "bin" && filepath.Base(packageDir) == "aeon-agentd" && filepath.Base(cellar) == "Cellar" {
		stable = filepath.Join(filepath.Dir(cellar), "bin", "aeon-agentd")
	} else if filepath.IsAbs(home) && within(filepath.Join(home, ".local", "lib", "aeon"), physical) {
		stable = filepath.Join(home, ".local", "bin", "aeon-agentd")
	}
	if stable == "" {
		return physical, nil
	}
	resolved, err := filepath.EvalSymlinks(stable)
	if err != nil || resolved != physical {
		return "", errors.New("stable aeon-agentd link is missing or points to another binary; repair the Homebrew or checksum installation before pairing")
	}
	return stable, nil
}

func (m ServiceManager) managedExecutable() bool {
	if !ManagedPath(m.Executable) {
		return false
	}
	stable, err := ServiceExecutable(m.Executable, m.Home)
	// Only the two verified installer links are exempt from the conservative
	// symlink rule. All other managed/symlink installations stay untouched.
	return err != nil || stable != m.Executable || strings.HasPrefix(stable, "/nix/store/")
}
