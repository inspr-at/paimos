// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	return nodePinReasonWithExecutor(context.Background(), launcher, workspace, node, OSExecutor{})
}

func nodePinReasonWithExecutor(ctx context.Context, launcher, workspace string, node harnesslaunch.Node, executor Executor) string {
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
	raw, err := executor.Run(ctx, Command{Path: node.Path, Args: []string{"--version"}, Env: harnesslaunch.Environment(nil, node.Path), Dir: commandDir(workspace, userHome())})
	match := safeVersion.FindSubmatch(raw)
	if err != nil || len(match) != 2 || string(match[1]) != node.Version {
		return PinDrifted
	}
	return ""
}

// Hash only a checked physical launcher, with an explicit read bound. This is
// local approval metadata; neither the hash nor its path enters telemetry.
func codexLauncherHash(path, workspace string) string {
	physical, info, err := TrustedAttachExecutable(path, workspace)
	const maximum = 32 << 20
	if err != nil || physical != path || info.Size() > maximum {
		return ""
	}
	f, err := os.Open(physical)
	if err != nil {
		return ""
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || len(raw) > maximum {
		return ""
	}
	return Hash(raw)
}

func recordCodexLauncher(s *snapshot, c Candidate) {
	if c.Harness != "codex" {
		return
	}
	if hash := codexLauncherHash(c.Path, s.Request.Workspace); hash != "" {
		if s.CodexPins.LauncherHashes == nil {
			s.CodexPins.LauncherHashes = map[string]string{}
		}
		s.CodexPins.LauncherHashes[c.Key] = hash
	}
}

// Unknown installations have no automatic upgrade authority. Homebrew keeps
// its prefix and formula fence; Nix keeps its nodejs package class.
var nodeFormula = regexp.MustCompile(`^node(?:@[0-9]+)?$`)

func nodeLocationClass(path string) string {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ""
	}
	for _, prefix := range []string{"/opt/homebrew", "/usr/local", "/usr/local/Homebrew", "/home/linuxbrew/.linuxbrew"} {
		parts := strings.Split(strings.TrimPrefix(path, prefix+"/Cellar/"), "/")
		if strings.HasPrefix(path, prefix+"/Cellar/") && len(parts) == 4 && parts[1] != "" && parts[2] == "bin" && parts[3] == "node" && nodeFormula.MatchString(parts[0]) {
			return prefix + "/Cellar/" + parts[0]
		}
	}
	parts := strings.Split(strings.TrimPrefix(path, "/nix/store/"), "/")
	if strings.HasPrefix(path, "/nix/store/") && len(parts) == 3 && strings.Contains(parts[0], "-nodejs-") && parts[1] == "bin" && parts[2] == "node" {
		return "/nix/store/nodejs"
	}
	return ""
}

func (d Discovery) locationClass(path string) string {
	if d.nodeClass != nil {
		return d.nodeClass(path)
	}
	return nodeLocationClass(path)
}

// autoRenewCodexPins runs after current lifecycle fences, under setup.lock.
// A failed check leaves the original block and its existing repair intact.
func (e *Engine) autoRenewCodexPins(ctx context.Context, s *snapshot) error {
	if len(s.CodexPins.LauncherHashes) == 0 || s.DisconnectAll || s.ComputerCleaned || s.View.ComputerState != "connected" || s.Phase != "connected" && s.Phase != "verification_pending" {
		return nil
	}
	d := Discovery{Home: userHome(), Workspace: s.Request.Workspace}
	if e.PinDiscovery != nil {
		d = *e.PinDiscovery
		d.Workspace = s.Request.Workspace
	}
	if d.Executor == nil {
		d.Executor = OSExecutor{}
	}
	raw, err := e.Store.Read(RuntimeName, 128<<10)
	var config RuntimeConfig
	if err != nil || json.Unmarshal(raw, &config) != nil || config.Schema != "aeon.agent-runtime.v1" || config.Origin != s.Origin || config.Workspace != s.Request.Workspace || config.TenantID != s.View.TenantID || config.PrincipalID != s.BoundPrincipal || config.ComputerID != s.BoundComputer || config.DaemonID != s.BoundDaemon {
		return errors.New("automatic pin renewal runtime unavailable")
	}
	for _, enrolled := range s.View.Enrollments {
		if ctx.Err() != nil {
			return nil
		}
		if enrolled.State != "connected" || enrolled.Harness != "codex" || s.Removed[enrolled.AccountID] {
			continue
		}
		for _, current := range config.Accounts {
			if current.AccountID != enrolled.AccountID || current.Key != enrolled.AccountKey || current.Harness != "codex" || current.Node.Path == "" || current.Node.Version == "" || current.Identity == CodexChatGPTLogin {
				continue
			}
			reason := nodePinReasonWithExecutor(ctx, current.Path, config.Workspace, current.Node, d.Executor)
			if reason == "" || reason == PinUnsafe {
				continue
			}
			oldClass := d.locationClass(current.Node.Path)
			hash := s.CodexPins.LauncherHashes[current.Key]
			if oldClass == "" || hash == "" || codexLauncherHash(current.Path, config.Workspace) != hash {
				continue
			}
			approved := false
			for _, local := range s.Candidates {
				approved = approved || local.Candidate.Key == current.Key && local.Candidate.Harness == "codex" && local.Path == current.Path && local.Home == current.Home && local.Identity == current.Identity && local.Candidate.Node == current.Node
			}
			if !approved || current.Home != filepath.Join(d.Home, ".codex") {
				continue
			}
			profile, err := openDirectory(current.Home, false, true)
			if err != nil {
				continue
			}
			profile.Close()
			retry := d
			retry.NodePath = ""
			if d.LookPath == nil && strings.Contains(oldClass, "/Cellar/") {
				prefix, formula, _ := strings.Cut(oldClass, "/Cellar/")
				retry.NodePath = filepath.Join(prefix, "opt", formula, "bin", "node")
			} else if d.LookPath == nil && oldClass == "/nix/store/nodejs" {
				retry.NodePath = filepath.Join(d.Home, ".nix-profile", "bin", "node")
			}
			if retry.NodePath == "" {
				lookup := d.LookPath
				if lookup == nil {
					lookup = exec.LookPath
				}
				retry.NodePath, err = lookup("node")
				if err != nil {
					continue
				}
			}
			// Check the candidate's location before executing even --version.
			nodePath, err := pinnedRegular(retry.NodePath, config.Workspace, true)
			if err != nil || d.locationClass(nodePath) != oldClass {
				continue
			}
			retry.LookPath = func(name string) (string, error) {
				if name == "codex" {
					return current.Path, nil
				}
				return "", os.ErrNotExist
			}
			candidate, err := retry.Detect(ctx, "codex", current.Identity)
			if err != nil || candidate.Node.Path != nodePath || candidate.Path != current.Path || candidate.Home != current.Home || candidate.Identity != current.Identity || codexLauncherHash(current.Path, config.Workspace) != hash {
				continue
			}
			profile, err = openDirectory(current.Home, false, true)
			if err != nil {
				continue
			}
			profile.Close()
			candidate.Key, candidate.Label = current.Key, enrolled.Label
			if _, err := e.renewPin(ctx, s, enrolled, candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

// The event is advisory and never changes readiness or a recovery command.
// Blocked accounts retain their existing pin reason in attention_accounts
// until the daemon's fresh probe. Ready reports continue to omit reason_detail.
func pinRenewalDetails(s *snapshot, local LocalStatus) LocalStatus {
	if len(s.CodexPins.Renewals) == 0 {
		return local
	}
	local.AccountStatuses = maps.Clone(local.AccountStatuses)
	local.HarnessDetails = maps.Clone(local.HarnessDetails)
	for _, enrolled := range s.View.Enrollments {
		if enrolled.Harness != "codex" || enrolled.State != "connected" || s.Removed[enrolled.AccountID] {
			continue
		}
		renewed, found := s.CodexPins.Renewals[enrolled.AccountKey]
		if !found || renewed.Path == "" {
			continue
		}
		account := local.AccountStatuses[enrolled.AccountID]
		if renewedPinDetail(account) {
			account.Reason = PinRenewed
			local.AccountStatuses[enrolled.AccountID] = account.WithProbeDetail("codex", ProbePinRenewed)
		}
		if detail, ok := local.HarnessDetails["codex"]; ok {
			if renewedPinDetail(detail) && account.Reason == PinRenewed {
				detail.Reason = PinRenewed
				detail = detail.WithProbeDetail("codex", ProbePinRenewed)
			}
			detail.Attention = append([]AccountAttention(nil), detail.Attention...)
			for i := range detail.Attention {
				if detail.Attention[i].AccountID == enrolled.AccountID && SafeProbeDetail(detail.Attention[i].Reason, ProbePinRenewed) != "" {
					detail.Attention[i].Reason = PinRenewed
					detail.Attention[i].ReasonDetail = ProbePinRenewed
				}
			}
			local.HarnessDetails["codex"] = detail
		}
	}
	return local
}

func renewedPinDetail(detail HarnessDetail) bool {
	return detail.State == "blocked" && SafeProbeDetail(detail.Reason, ProbePinRenewed) != ""
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
	if installedPathPolicy().repositoryError(physical) != nil || workspace != "" && within(workspace, physical) {
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
		if ownedComponentError(parent, dir, true) != nil {
			return true
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return false
}
