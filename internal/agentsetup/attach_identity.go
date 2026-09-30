// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// AttachIdentity is a local fallback for unsigned installations. It is never
// pairing authority and never sent to the server. Wildcards match one path
// component, so a version slot cannot escape into a sibling installation.
type AttachIdentity struct {
	Exact       bool   `json:"exact_file,omitempty"`
	InstallRoot string `json:"install_root"`
	Owner       int    `json:"owner"`
}

// TrustedAttachExecutable applies the same owner, ancestor, symlink, workspace
// and macOS admin-directory rules as the dependency pins (AEON-399).
func TrustedAttachExecutable(path, workspace string) (string, os.FileInfo, error) {
	physical, info, err := resolveOwnedPath(path, workspace)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", nil, ErrUnsafePath
	}
	return physical, info, nil
}

// RecordAttachIdentity derives a narrow vendor package root from the approved
// physical entrypoint. It does not interpret or execute shell wrappers. Unknown
// layouts retain their exact file pin rather than trusting a broad bin folder.
func RecordAttachIdentity(harness, path, workspace string) *AttachIdentity {
	if harness != "claude" && harness != "codex" && harness != "cursor" {
		return nil
	}
	physical, info, err := TrustedAttachExecutable(path, workspace)
	if err != nil {
		return nil
	}
	parts := strings.Split(physical, string(filepath.Separator))
	root := physical
	// Native installers keep only vendor versions beneath this fixed subtree.
	// Trust that vendor subtree, never ~/.local/bin or the entire share folder.
	for i := 0; i+4 < len(parts); i++ {
		if parts[i] == ".local" && parts[i+1] == "share" && parts[i+3] == "versions" &&
			((harness == "claude" && parts[i+2] == "claude") || (harness == "cursor" && parts[i+2] == "cursor-agent")) {
			root = strings.Join(parts[:i+4], string(filepath.Separator))
			break
		}
	}
	for i := 0; i+2 < len(parts); i++ {
		pkg := parts[i+1]
		end := i + 2
		if strings.HasPrefix(pkg, "@") && end < len(parts) {
			pkg += "/" + parts[end]
			end++
		}
		if parts[i] != "node_modules" || !attachPackage(harness, pkg) {
			continue
		}
		rootParts := append([]string(nil), parts[:end]...)
		// Claude's native npm updater retains its package subtree under a
		// changing package-<version> directory, not the entire npm prefix.
		for j := 0; j+1 < len(rootParts); j++ {
			if rootParts[j] == ".ai-cli-updates" && strings.HasPrefix(rootParts[j+1], "package-") {
				rootParts[j+1] = "package-*"
			}
		}
		root = strings.Join(rootParts, string(filepath.Separator))
		break
	}
	return &AttachIdentity{InstallRoot: root, Owner: int(info.Sys().(*syscall.Stat_t).Uid), Exact: root == physical}
}

func attachPackage(harness, pkg string) bool {
	switch harness {
	case "claude":
		return pkg == "@anthropic-ai/claude-code"
	case "codex":
		return pkg == "@openai/codex"
	case "cursor":
		return pkg == "@cursor/agent" || pkg == "@cursor/cli"
	}
	return false
}

func (identity AttachIdentity) Matches(path string, info os.FileInfo) bool {
	if info == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || identity.Owner < 0 || int(owner.Uid) != identity.Owner || !filepath.IsAbs(identity.InstallRoot) || filepath.Clean(identity.InstallRoot) != identity.InstallRoot || identity.InstallRoot == "/" {
		return false
	}
	if identity.Exact {
		return path == identity.InstallRoot
	}
	root, target := strings.Split(identity.InstallRoot, "/"), strings.Split(path, "/")
	if len(target) < len(root) {
		return false
	}
	for i, component := range root {
		// Recorded roots are literals except the single updater version slot.
		if component == "package-*" && i > 0 && root[i-1] == ".ai-cli-updates" {
			if !strings.HasPrefix(target[i], "package-") || target[i] == "package-" {
				return false
			}
		} else if component != target[i] {
			return false
		}
	}
	return true
}

// RecordAttachIdentities runs only while publishing an approved pairing or
// repairing its local pins. Daemon startup never invents a fallback identity.
func (c *RuntimeConfig) RecordAttachIdentities() {
	if c.AttachIdentities == nil {
		c.AttachIdentities = make(map[string]AttachIdentity)
	}
	for _, a := range c.Accounts {
		if identity := RecordAttachIdentity(a.Harness, a.Path, c.Workspace); identity != nil {
			c.AttachIdentities[a.Harness] = *identity
		}
	}
}

// Preserve recorded roots when an old auto-update version no longer exists.
// Only unchanged account bindings in this same pairing may retain a fallback.
func (c *RuntimeConfig) preserveAttachIdentities(previous RuntimeConfig) {
	if c.Schema != previous.Schema || c.Origin != previous.Origin || c.TenantID != previous.TenantID || c.PrincipalID != previous.PrincipalID || c.ComputerID != previous.ComputerID || c.DaemonID != previous.DaemonID || c.Workspace != previous.Workspace {
		return
	}
	if c.AttachIdentities == nil {
		c.AttachIdentities = make(map[string]AttachIdentity)
	}
	for _, account := range c.Accounts {
		for _, old := range previous.Accounts {
			if account.Harness == old.Harness && account.AccountID == old.AccountID && account.Key == old.Key && account.Home == old.Home && account.Identity == old.Identity && account.Path == old.Path {
				if identity, exists := previous.AttachIdentities[account.Harness]; exists {
					c.AttachIdentities[account.Harness] = identity
				}
			}
		}
	}
}
