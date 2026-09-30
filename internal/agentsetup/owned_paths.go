// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// The filesystem/prefix hooks let tests exercise real temporary installations
// without modifying a machine's package manager or requiring chown privileges.
type ownedPathPolicy struct {
	lstat          func(string) (os.FileInfo, error)
	homebrewPrefix func(string) bool
}

func installedPathPolicy() ownedPathPolicy {
	return ownedPathPolicy{os.Lstat, knownHomebrewPrefix}
}

func knownHomebrewPrefix(path string) bool {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	switch path {
	case "/opt/homebrew", "/usr/local/Homebrew", "/home/linuxbrew/.linuxbrew":
		return true
	}
	return false
}

func ownedComponentError(path string, info os.FileInfo, sticky bool) error {
	if !trustedClaudeOwner(info) {
		return fmt.Errorf("%w: %q is not owned by this user or root", ErrUnsafePath, path)
	}
	// Symlink permission bits are not access controls; check their owners and
	// every parent and target component instead.
	if info.Mode()&os.ModeSymlink != 0 || sticky && info.IsDir() && info.Mode()&os.ModeSticky != 0 {
		return nil
	}
	if info.Mode().Perm()&0002 != 0 {
		return fmt.Errorf("%w: %q is world-writable; remove write access for others", ErrUnsafePath, path)
	}
	if info.Mode().Perm()&0020 != 0 {
		stat := info.Sys().(*syscall.Stat_t) // trustedClaudeOwner checked the type.
		// macOS admin members can already become root through sudo. Accept
		// their writable directories (including Homebrew bin/Cellar), never files.
		if runtime.GOOS == "darwin" && info.IsDir() && stat.Gid == 80 {
			return nil
		}
		group := strconv.FormatUint(uint64(stat.Gid), 10)
		if entry, err := user.LookupGroupId(group); err == nil {
			group = entry.Name
		}
		return fmt.Errorf("%w: %q is writable by group %q; remove group write access or choose a trusted installation", ErrUnsafePath, path, group)
	}
	return nil
}

func (p ownedPathPolicy) repositoryError(path string) error {
	for dir := path; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		git := filepath.Join(dir, ".git")
		info, err := p.lstat(git)
		if err != nil {
			continue
		}
		if !p.homebrewPrefix(dir) {
			return fmt.Errorf("%w: %q is a repository; choose an installation outside repositories", ErrUnsafePath, dir)
		}
		// Package-manager prefixes are not agent workspaces. Only these fixed
		// Homebrew repositories qualify, with a trusted prefix AND .git entry.
		prefix, err := p.lstat(dir)
		if err != nil || !prefix.IsDir() {
			return fmt.Errorf("%w: %q is not an available Homebrew directory", ErrUnsafePath, dir)
		}
		if err := ownedComponentError(dir, prefix, false); err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %q is not a regular repository entry", ErrUnsafePath, git)
		}
		if err := ownedComponentError(git, info, false); err != nil {
			return err
		}
	}
	return nil
}

func (p ownedPathPolicy) locationError(path, workspace string) error {
	if workspace != "" && within(workspace, path) {
		return fmt.Errorf("%w: %q is inside the workspace; choose an installation outside it", ErrUnsafePath, path)
	}
	return p.repositoryError(path)
}

func (p ownedPathPolicy) pinnedRegular(path, workspace string, executable bool) (string, error) {
	physical, info, err := p.resolve(path, workspace)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", ErrUnsafePath, physical)
	}
	if executable && info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("%w: %q is not executable; restore its executable permission", ErrUnsafePath, physical)
	}
	return physical, nil
}

// Check every traversed component, including intermediate links that disappear
// from EvalSymlinks' result. Sticky shared ancestors (e.g. /tmp) protect owned
// children; the admin-group directory exception applies only on macOS.
func (p ownedPathPolicy) resolve(path, workspace string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nil, fmt.Errorf("%w: %q must be a clean absolute path", ErrUnsafePath, path)
	}
	if err := p.locationError(path, workspace); err != nil {
		return "", nil, err
	}
	pending, physical, links := strings.Split(path, string(filepath.Separator)), string(filepath.Separator), 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			physical = filepath.Dir(physical)
			continue
		}
		next := filepath.Join(physical, part)
		if err := p.locationError(next, workspace); err != nil {
			return "", nil, err
		}
		info, err := p.lstat(next)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %q is unavailable; check that the installation exists and is accessible", ErrUnsafePath, next)
		}
		if err := ownedComponentError(next, info, true); err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", nil, fmt.Errorf("%w: %q exceeds the symlink limit; repair the link chain", ErrUnsafePath, next)
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", nil, fmt.Errorf("%w: %q is an unreadable symlink", ErrUnsafePath, next)
			}
			if filepath.IsAbs(target) {
				physical = string(filepath.Separator)
			}
			pending = append(strings.Split(target, string(filepath.Separator)), pending...)
			continue
		}
		if len(pending) > 0 && !info.IsDir() {
			return "", nil, fmt.Errorf("%w: %q is not a directory", ErrUnsafePath, next)
		}
		physical = next
	}
	// A link target can end in '.' or '..', so inspect the final physical path.
	info, err := p.lstat(physical)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("%w: %q is unavailable or changed during resolution", ErrUnsafePath, physical)
	}
	if err := ownedComponentError(physical, info, true); err != nil {
		return "", nil, err
	}
	return physical, info, nil
}
