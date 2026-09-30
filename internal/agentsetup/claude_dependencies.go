// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"encoding/json"
	"errors"
	"fmt"
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
	if err == nil && filepath.Base(physicalNode) != "node" {
		err = fmt.Errorf("%w: %q is not named node", ErrUnsafePath, physicalNode)
	}
	if err != nil {
		return logical, physical, fmt.Errorf("Claude Node executable is unsafe or unavailable; %w; choose a user- or root-owned executable outside the workspace with --node-path", err)
	}
	sdk := given.SDKPath
	if sdk == "" {
		var installedErr error
		roots := []string{filepath.Join(filepath.Dir(filepath.Dir(node)), "lib", "node_modules"), filepath.Join(filepath.Dir(filepath.Dir(physicalNode)), "lib", "node_modules")}
		if filepath.IsAbs(d.Home) {
			roots = append(roots, filepath.Join(d.Home, ".local", "lib", "node_modules"), filepath.Join(d.Home, ".npm-global", "lib", "node_modules"))
		}
		for _, root := range roots {
			pkg := filepath.Join(root, "@anthropic-ai", "claude-agent-sdk")
			entry, e := sdkEntry(pkg, workspace)
			if e == nil {
				sdk = entry
				break
			}
			if _, statErr := os.Lstat(pkg); statErr == nil && installedErr == nil {
				installedErr = e
			}
		}
		if sdk == "" {
			if installedErr != nil {
				return logical, physical, fmt.Errorf("Claude Agent SDK module is unsafe or unavailable; %w; check the installed package or pass --claude-sdk-path outside the workspace", installedErr)
			}
			return logical, physical, errors.New(claudeDependencyAction)
		}
	}
	physicalSDK, err := pinnedRegular(sdk, workspace, false)
	if err != nil {
		return logical, physical, fmt.Errorf("Claude Agent SDK module is unsafe or unavailable; %w; pass --claude-sdk-path to its installed module entry outside the workspace", err)
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
	return installedPathPolicy().pinnedRegular(path, workspace, executable)
}

// ResolveClaudeExecutable preserves the CLI's existing physical executable
// policy. The stricter stable-link policy belongs to the Node/SDK pins only;
// applying it to the approved CLI would reject existing Homebrew installations.
func ResolveClaudeExecutable(path, _ string) (string, error) {
	physical, err := filepath.EvalSymlinks(path)
	if err == nil && filepath.IsAbs(path) && physical == path {
		info, err := os.Stat(physical)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return physical, nil
		}
	}
	return "", &HarnessIssue{Reason: "cli_unavailable", Err: errors.New("Claude CLI executable changed or unavailable; restore the approved physical executable, then retry")}
}

func trustedClaudeOwner(info os.FileInfo) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && (owner.Uid == 0 || int(owner.Uid) == os.Getuid())
}

func resolveOwnedPath(path, workspace string) (string, os.FileInfo, error) {
	return installedPathPolicy().resolve(path, workspace)
}

func sdkEntry(dir, workspace string) (string, error) {
	if filepath.Base(dir) != "claude-agent-sdk" || filepath.Base(filepath.Dir(dir)) != "@anthropic-ai" {
		return "", ErrUnsafePath
	}
	physicalDir, info, err := resolveOwnedPath(dir, workspace)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", ErrUnsafePath
	}
	for _, path := range []string{physicalDir, filepath.Dir(physicalDir)} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", ErrUnsafePath
		}
		if err := ownedComponentError(path, info, false); err != nil {
			return "", err
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
	if err != nil {
		return "", err
	}
	if !within(physicalDir, physicalEntry) {
		return "", ErrUnsafePath
	}
	// Keep the package link, so an activation can move it to a new version.
	return filepath.Join(dir, entry), nil
}
