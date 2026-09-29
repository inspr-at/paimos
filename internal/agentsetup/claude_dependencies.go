// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// ClaudeDependencies are local runtime pins, never vendor credentials.
type ClaudeDependencies struct {
	NodePath string
	SDKPath  string
}

const claudeDependencyAction = "Claude dependencies required: install Node.js and @anthropic-ai/claude-agent-sdk in a global npm prefix outside the workspace, or pass absolute --node-path and --claude-sdk-path paths to the installed Node executable and SDK module entry; then resume setup."

// ResolveClaudeDependencies uses executable lookup and package metadata only.
// In particular it never invokes npm, Node module resolution, or project code.
func (d Discovery) ResolveClaudeDependencies(given ClaudeDependencies, workspace string) (ClaudeDependencies, error) {
	logical, _, err := d.resolveClaudeDependencies(given, workspace)
	return logical, err
}

// ResolveClaudeRuntime resolves the saved links for this launch only. Callers
// execute these physical paths, never the links that were checked earlier.
func ResolveClaudeRuntime(given ClaudeDependencies, workspace string) (ClaudeDependencies, error) {
	if given.NodePath == "" || given.SDKPath == "" {
		return ClaudeDependencies{}, errors.New(claudeDependencyAction)
	}
	_, physical, err := (Discovery{}).resolveClaudeDependencies(given, workspace)
	return physical, err
}

func (d Discovery) resolveClaudeDependencies(given ClaudeDependencies, workspace string) (logical, physical ClaudeDependencies, err error) {
	look := d.LookPath
	if look == nil {
		look = exec.LookPath
	}
	node := given.NodePath
	if node == "" {
		var err error
		node, err = look("node")
		if err != nil {
			return logical, physical, errors.New(claudeDependencyAction)
		}
	}
	physicalNode, err := pinnedRegular(node, workspace, true)
	if err != nil || filepath.Base(physicalNode) != "node" {
		return logical, physical, errors.New("Claude Node executable is unsafe or unavailable; choose a user- or root-owned executable outside the workspace with --node-path")
	}
	sdk := given.SDKPath
	if sdk == "" {
		roots := []string{filepath.Join(filepath.Dir(filepath.Dir(node)), "lib", "node_modules"), filepath.Join(filepath.Dir(filepath.Dir(physicalNode)), "lib", "node_modules")}
		if filepath.IsAbs(d.Home) {
			roots = append(roots, filepath.Join(d.Home, ".local", "lib", "node_modules"), filepath.Join(d.Home, ".npm-global", "lib", "node_modules"))
		}
		for _, root := range roots {
			entry, e := sdkEntry(filepath.Join(root, "@anthropic-ai", "claude-agent-sdk"), workspace)
			if e == nil {
				sdk = entry
				break
			}
		}
		if sdk == "" {
			return logical, physical, errors.New(claudeDependencyAction)
		}
	}
	physicalSDK, err := pinnedRegular(sdk, workspace, false)
	if err != nil {
		return logical, physical, errors.New("Claude Agent SDK module is unsafe or unavailable; pass --claude-sdk-path to its installed module entry outside the workspace")
	}
	if _, err := claudePackageDir(sdk, physicalSDK, workspace); err != nil {
		return logical, physical, errors.New("Claude Agent SDK path must be the module entry declared by an installed @anthropic-ai/claude-agent-sdk package")
	}
	return ClaudeDependencies{NodePath: node, SDKPath: sdk}, ClaudeDependencies{NodePath: physicalNode, SDKPath: physicalSDK}, nil
}

func claudePackageDir(logical, physical, workspace string) (string, error) {
	for _, path := range []string{logical, physical} {
		for dir := filepath.Dir(path); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			if filepath.Base(dir) != "claude-agent-sdk" {
				continue
			}
			entry, err := sdkEntry(dir, workspace)
			if err == nil {
				resolved, err := pinnedRegular(entry, workspace, false)
				if err == nil && resolved == physical {
					return dir, nil
				}
			}
		}
	}
	return "", ErrUnsafePath
}

func pinnedRegular(path, workspace string, executable bool) (string, error) {
	physical, info, err := resolveOwnedPath(path, workspace)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || executable && info.Mode().Perm()&0111 == 0 {
		return "", ErrUnsafePath
	}
	return physical, nil
}

// ResolveClaudeExecutable applies the same link-chain checks to the CLI before
// its account probe or launch. It does not change any other harness's policy.
func ResolveClaudeExecutable(path, workspace string) (string, error) {
	return pinnedRegular(path, workspace, true)
}

func trustedClaudeOwner(info os.FileInfo) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && (owner.Uid == 0 || int(owner.Uid) == os.Getuid())
}

// Check every traversed component, including intermediate links that disappear
// from EvalSymlinks' result. Sticky shared ancestors (e.g. /tmp) protect owned
// children; ordinary group/world-writable directories never do.
func resolveOwnedPath(path, workspace string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nil, ErrUnsafePath
	}
	if repositoryPath(path) || workspace != "" && within(workspace, path) {
		return "", nil, ErrUnsafePath
	}
	pending, physical, links := strings.Split(path, string(filepath.Separator)), string(filepath.Separator), 0
	var info os.FileInfo
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
		if repositoryPath(next) || workspace != "" && within(workspace, next) {
			return "", nil, ErrUnsafePath
		}
		var err error
		info, err = os.Lstat(next)
		if err != nil {
			return "", nil, ErrUnsafePath
		}
		if !trustedClaudeOwner(info) {
			return "", nil, ErrUnsafePath
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", nil, ErrUnsafePath
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", nil, ErrUnsafePath
			}
			if filepath.IsAbs(target) {
				physical = string(filepath.Separator)
			}
			pending = append(strings.Split(target, string(filepath.Separator)), pending...)
			continue
		}
		if info.Mode().Perm()&0022 != 0 && !(info.IsDir() && info.Mode()&os.ModeSticky != 0) || len(pending) > 0 && !info.IsDir() {
			return "", nil, ErrUnsafePath
		}
		physical = next
	}
	if info == nil {
		return "", nil, ErrUnsafePath
	}
	return physical, info, nil
}

func sdkEntry(dir, workspace string) (string, error) {
	if filepath.Base(dir) != "claude-agent-sdk" || filepath.Base(filepath.Dir(dir)) != "@anthropic-ai" {
		return "", ErrUnsafePath
	}
	physicalDir, info, err := resolveOwnedPath(dir, workspace)
	if err != nil || !info.IsDir() {
		return "", ErrUnsafePath
	}
	for _, path := range []string{physicalDir, filepath.Dir(physicalDir)} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return "", ErrUnsafePath
		}
		if !trustedClaudeOwner(info) {
			return "", ErrUnsafePath
		}
	}
	manifest, err := pinnedRegular(filepath.Join(physicalDir, "package.json"), workspace, false)
	if err != nil {
		return "", err
	}
	f, err := os.Open(manifest)
	if err != nil {
		return "", ErrUnsafePath
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return "", ErrUnsafePath
	}
	var pkg struct {
		Name    string          `json:"name"`
		Main    string          `json:"main"`
		Exports json.RawMessage `json:"exports"`
	}
	if json.Unmarshal(raw, &pkg) != nil || pkg.Name != "@anthropic-ai/claude-agent-sdk" {
		return "", ErrUnsafePath
	}
	entry := pkg.Main
	if len(pkg.Exports) != 0 {
		entry = ""
		if json.Unmarshal(pkg.Exports, &entry) != nil {
			var exports map[string]json.RawMessage
			if json.Unmarshal(pkg.Exports, &exports) != nil {
				return "", ErrUnsafePath
			}
			var direct string
			if json.Unmarshal(exports["."], &direct) == nil {
				entry = direct
			} else {
				var conditions map[string]string
				if json.Unmarshal(exports["."], &conditions) == nil {
					for _, key := range []string{"import", "default"} {
						if conditions[key] != "" {
							entry = conditions[key]
							break
						}
					}
				}
			}
		}
	}
	if entry == "" || filepath.IsAbs(entry) || strings.HasPrefix(entry, "../") || entry == ".." || strings.Contains(entry, "\\") {
		return "", ErrUnsafePath
	}
	for _, part := range strings.Split(entry, "/") {
		if part == ".." {
			return "", ErrUnsafePath
		}
	}
	physicalEntry, err := pinnedRegular(filepath.Join(physicalDir, entry), workspace, false)
	if err != nil || !within(physicalDir, physicalEntry) {
		return "", ErrUnsafePath
	}
	// Keep the package link, so an activation can move it to a new version.
	return filepath.Join(dir, entry), nil
}
