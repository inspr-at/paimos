// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"path/filepath"
)

// DefaultStateRoot selects private pairing state, never the vendor's login home
// or the existing explicit-key daemon's state. OpenStore creates missing parents.
func DefaultStateRoot(goos, home, xdgStateHome string) (string, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
		return "", ErrUnsafePath
	}
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "aeon", "paired"), nil
	case "linux":
		if xdgStateHome == "" {
			xdgStateHome = filepath.Join(home, ".local", "state")
		}
		if !filepath.IsAbs(xdgStateHome) || filepath.Clean(xdgStateHome) != xdgStateHome {
			return "", errors.New("XDG_STATE_HOME must be an absolute physical directory")
		}
		return filepath.Join(xdgStateHome, "aeon", "paired"), nil
	default:
		return "", errors.New("pairing supports macOS and Linux")
	}
}

// ValidateStateLocation runs before creating directories or private state.
// OpenStore separately checks ownership, permissions and symlink ancestors.
func ValidateStateLocation(root, workspace string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || repositoryPath(root) {
		return errors.New("private setup state must be an absolute directory outside project repositories; use --state-root to choose a private folder there")
	}
	if workspace != "" && within(workspace, root) {
		return errors.New("the working folder contains the private pairing state; run pair from the intended project folder instead of your home folder, or pass --workspace with that folder; keep --state-root outside the working folder")
	}
	return nil
}
