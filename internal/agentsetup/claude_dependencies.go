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
	look := d.LookPath
	if look == nil {
		look = exec.LookPath
	}
	node := given.NodePath
	if node == "" {
		var err error
		node, err = look("node")
		if err != nil {
			return ClaudeDependencies{}, errors.New(claudeDependencyAction)
		}
	}
	physicalNode, err := pinnedRegular(node, workspace, true)
	if err != nil || filepath.Base(physicalNode) != "node" {
		return ClaudeDependencies{}, errors.New("Claude Node executable is unsafe or unavailable; choose a user- or root-owned executable outside the workspace with --node-path")
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
			return ClaudeDependencies{}, errors.New(claudeDependencyAction)
		}
	}
	physicalSDK, err := pinnedRegular(sdk, workspace, false)
	if err != nil {
		return ClaudeDependencies{}, errors.New("Claude Agent SDK module is unsafe or unavailable; pass --claude-sdk-path to its installed module entry outside the workspace")
	}
	packageDir := filepath.Dir(physicalSDK)
	for packageDir != "/" && packageDir != "." && filepath.Base(packageDir) != "claude-agent-sdk" {
		packageDir = filepath.Dir(packageDir)
	}
	entry, err := sdkEntry(packageDir, workspace)
	if err != nil || entry != physicalSDK {
		return ClaudeDependencies{}, errors.New("Claude Agent SDK path must be the module entry declared by an installed @anthropic-ai/claude-agent-sdk package")
	}
	return ClaudeDependencies{NodePath: physicalNode, SDKPath: physicalSDK}, nil
}

func pinnedRegular(path, workspace string, executable bool) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrUnsafePath
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(physical) || repositoryPath(physical) || workspace != "" && within(workspace, physical) {
		return "", ErrUnsafePath
	}
	info, err := os.Stat(physical)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || executable && info.Mode().Perm()&0111 == 0 {
		return "", ErrUnsafePath
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 && int(owner.Uid) != os.Getuid() {
		return "", ErrUnsafePath
	}
	// A pinned file can still be replaced through a writable ancestor. Root-
	// or user-owned sticky shared directories protect the owned child below.
	for parent := filepath.Dir(physical); ; parent = filepath.Dir(parent) {
		dir, err := os.Stat(parent)
		if err != nil || !dir.IsDir() {
			return "", ErrUnsafePath
		}
		owner, ok := dir.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 && int(owner.Uid) != os.Getuid() || dir.Mode().Perm()&0022 != 0 && dir.Mode()&os.ModeSticky == 0 {
			return "", ErrUnsafePath
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return physical, nil
}

func sdkEntry(dir, workspace string) (string, error) {
	if filepath.Base(dir) != "claude-agent-sdk" || filepath.Base(filepath.Dir(dir)) != "@anthropic-ai" {
		return "", ErrUnsafePath
	}
	physicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil || repositoryPath(physicalDir) || workspace != "" && within(workspace, physicalDir) {
		return "", ErrUnsafePath
	}
	for _, path := range []string{physicalDir, filepath.Dir(physicalDir)} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return "", ErrUnsafePath
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 && int(owner.Uid) != os.Getuid() {
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
	physicalEntry, err := pinnedRegular(filepath.Join(physicalDir, entry), workspace, false)
	if err != nil || !within(physicalDir, physicalEntry) {
		return "", ErrUnsafePath
	}
	return physicalEntry, nil
}
