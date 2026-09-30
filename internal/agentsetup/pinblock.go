// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

// BlockedAccount is one enrollment the paired daemon must not launch.
// Reason and Fix use the same vocabulary and repair mapping as HarnessDetail.
type BlockedAccount struct {
	AccountID string     `json:"account_id"`
	Harness   string     `json:"harness"`
	Reason    string     `json:"reason"`
	Fix       HarnessFix `json:"fix"`
}

type pinHit struct {
	BlockedAccount
	// err is the local diagnostic. It never leaves this computer.
	err error
}

// AccountPinBlocks reports every per-account pin problem. A hit means that
// account stays enrolled and unlaunchable; it is not a daemon-wide failure.
func AccountPinBlocks(c RuntimeConfig) []BlockedAccount {
	hits := accountPinHits(c)
	if len(hits) == 0 {
		return nil
	}
	out := make([]BlockedAccount, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.BlockedAccount)
	}
	return out
}

// ValidateRuntimeDependencies gates execution, never access to recovery metadata
// or the credential needed to revoke this computer. One bad pin fails the whole
// config here. The paired daemon uses AccountPinBlocks and keeps the other accounts.
// The typed reason is the first block's; the error keeps its local diagnostic.
func ValidateRuntimeDependencies(c RuntimeConfig) error {
	hits := accountPinHits(c)
	if len(hits) == 0 {
		return nil
	}
	return &HarnessIssue{Reason: hits[0].Reason, Err: hits[0].err}
}

func accountPinHits(c RuntimeConfig) []pinHit {
	var hits []pinHit
	var shared *string
	for _, a := range c.Accounts {
		if a.Harness == "grok" {
			continue
		}
		var reason string
		var diagnostic error
		if a.Harness == "claude" {
			// Claude launches with the shared Node/SDK pins that repin replaces,
			// following their links like the adapter does. A per-account Node
			// recorded at discovery is never executed, so it cannot block Claude.
			if _, err := ResolveClaudeExecutable(a.Path, c.Workspace); err != nil {
				reason, diagnostic = HarnessFailureReason(err), err
			} else {
				if shared == nil {
					value := claudeSharedReason(c)
					shared = &value
				}
				reason = *shared
				diagnostic = errors.New("Claude dependencies changed: run aeon-agentd repin --harness claude; saved dependencies are " + strings.ReplaceAll(strings.TrimPrefix(reason, "pin_"), "_", " "))
			}
		} else {
			node := a.Node
			if a.Harness == "pi" {
				node = a.PiNode
			}
			reason = nodePinReason(a.Path, c.Workspace, node)
			diagnostic = fmt.Errorf("%w; %s interpreter pin is %s: run %s", harnesslaunch.ErrStart, a.Harness, strings.TrimPrefix(reason, "pin_"), RecoveryFix(a.Harness, reason).Command)
		}
		if reason == "" {
			continue
		}
		hits = append(hits, pinHit{BlockedAccount: BlockedAccount{
			AccountID: a.AccountID, Harness: a.Harness, Reason: reason, Fix: RecoveryFix(a.Harness, reason),
		}, err: diagnostic})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].AccountID == hits[j].AccountID {
			return hits[i].Harness < hits[j].Harness
		}
		return hits[i].AccountID < hits[j].AccountID
	})
	return hits
}

func nodePinReason(launcher, workspace string, node harnesslaunch.Node) string {
	needed, err := harnesslaunch.NeedsNode(launcher)
	if err != nil {
		return PinInvalid
	}
	if node.Path == "" && node.Version == "" {
		if needed {
			return PinMissing
		}
		return ""
	}
	if node.Path == "" || node.Version == "" {
		return PinPartial
	}
	if err = validateNode(launcher, workspace, node); err != nil {
		if pathUnsafe(node.Path, workspace, true) {
			return PinUnsafe
		}
		return PinInvalid
	}
	raw, err := (OSExecutor{}).Run(context.Background(), Command{Path: node.Path, Args: []string{"--version"}, Env: harnesslaunch.Environment(nil, node.Path), Dir: commandDir(workspace, userHome())})
	match := safeVersion.FindSubmatch(raw)
	if err != nil || len(match) != 2 || string(match[1]) != node.Version {
		return PinDrifted
	}
	return ""
}

func claudeSharedReason(c RuntimeConfig) string {
	if c.NodePath == "" || c.ClaudeSDKPath == "" {
		return PinMissing
	}
	valid, err := (Discovery{}).ResolveClaudeDependencies(ClaudeDependencies{NodePath: c.NodePath, SDKPath: c.ClaudeSDKPath}, c.Workspace)
	if err == nil && valid.NodePath == c.NodePath && valid.SDKPath == c.ClaudeSDKPath {
		return ""
	}
	if pathUnsafe(c.NodePath, c.Workspace, true) || pathUnsafe(c.ClaudeSDKPath, c.Workspace, false) {
		return PinUnsafe
	}
	return PinInvalid
}

// pathUnsafe reports the workspace, ownership, and writability failures inside
// pinnedRegular. A missing or malformed path is invalid, not unsafe.
func pathUnsafe(path, workspace string, executable bool) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(physical) {
		return false
	}
	if repositoryPath(physical) || workspace != "" && within(workspace, physical) {
		return true
	}
	info, err := os.Stat(physical)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if info.Mode().Perm()&0022 != 0 {
		return true
	}
	if executable && info.Mode().Perm()&0111 == 0 {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 && int(owner.Uid) != os.Getuid() {
		return true
	}
	for parent := filepath.Dir(physical); ; parent = filepath.Dir(parent) {
		dir, err := os.Stat(parent)
		if err != nil || !dir.IsDir() {
			return false
		}
		owner, ok := dir.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 && int(owner.Uid) != os.Getuid() || dir.Mode().Perm()&0022 != 0 && dir.Mode()&os.ModeSticky == 0 {
			return true
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return false
}
