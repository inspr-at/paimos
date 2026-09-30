// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ClaudeDependencyInfo is safe to display. It contains no pairing authority or
// vendor identity, and versions come only from checked Node/package metadata.
type ClaudeDependencyInfo struct {
	NodePath     string `json:"node_path"`
	SDKPath      string `json:"sdk_path"`
	NodeResolved string `json:"node_resolved"`
	SDKResolved  string `json:"sdk_resolved"`
	NodeVersion  string `json:"node_version"`
	SDKVersion   string `json:"sdk_version"`
}

type ClaudeRepinPlan struct {
	Old          ClaudeDependencyInfo `json:"old"`
	New          ClaudeDependencyInfo `json:"new"`
	snapshotHash string
	runtimeHash  string
}

var dependencyVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.-]+)?$`)

func inspectClaudeDependencies(ctx context.Context, pins ClaudeDependencies, workspace string) (ClaudeDependencyInfo, error) {
	info := ClaudeDependencyInfo{NodePath: pins.NodePath, SDKPath: pins.SDKPath, NodeVersion: "unavailable", SDKVersion: "unavailable"}
	physical, err := ResolveClaudeRuntime(pins, workspace)
	if err != nil {
		return info, err
	}
	info.NodeResolved, info.SDKResolved = physical.NodePath, physical.SDKPath
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(op, physical.NodePath, "--version")
	cmd.WaitDelay = time.Second
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	output := &boundedBuffer{max: 256}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil || !dependencyVersion.MatchString(strings.TrimSpace(output.String())) {
		return info, errors.New("checked Node version unavailable")
	}
	info.NodeVersion = strings.TrimSpace(output.String())
	dir, err := claudePackageDir(pins.SDKPath, physical.SDKPath, workspace)
	if err != nil {
		return info, err
	}
	manifest, err := pinnedRegular(filepath.Join(dir, "package.json"), workspace, false)
	if err != nil {
		return info, err
	}
	// sdkEntry already enforces the same bounded manifest before this read.
	f, err := os.Open(manifest)
	if err != nil {
		return info, ErrUnsafePath
	}
	defer f.Close()
	var pkg struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&pkg) != nil || !dependencyVersion.MatchString(pkg.Version) {
		return info, errors.New("Claude SDK package version unavailable")
	}
	info.SDKVersion = pkg.Version
	return info, nil
}

func (e *Engine) repinState() (*snapshot, RuntimeConfig, string, string, error) {
	s, err := e.load()
	if err != nil {
		return nil, RuntimeConfig{}, "", "", err
	}
	if s.DisconnectAll || s.View.ComputerState != "connected" || s.Phase != "connected" && s.Phase != "verification_pending" {
		return nil, RuntimeConfig{}, "", "", errors.New("repin requires a connected pairing; finish or disconnect the pending enrollment first")
	}
	raw, err := e.Store.Read(RuntimeName, 128<<10)
	var c RuntimeConfig
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Schema != "aeon.agent-runtime.v1" || c.Origin != s.Origin || c.Workspace != s.Request.Workspace || c.TenantID != s.View.TenantID || c.PrincipalID != s.BoundPrincipal || c.ComputerID != s.BoundComputer || c.DaemonID != s.BoundDaemon {
		return nil, c, "", "", errors.New("repin runtime ownership does not match this pairing")
	}
	claude := false
	for _, a := range c.Accounts {
		if a.Harness != "claude" {
			continue
		}
		approved := false
		for _, enrollment := range s.View.Enrollments {
			approved = approved || enrollment.AccountID == a.AccountID && enrollment.AccountKey == a.Key && enrollment.Harness == "claude" && enrollment.State == "connected" && !s.Removed[a.AccountID]
		}
		if !approved {
			return nil, c, "", "", errors.New("repin cannot restore a removed Claude account")
		}
		claude = true
	}
	if !claude {
		return nil, c, "", "", errors.New("no connected Claude harness to repin; use add-harness first")
	}
	snapshotRaw, err := e.Store.Read(snapshotName, 1<<20)
	return s, c, Hash(snapshotRaw), Hash(raw), err
}

// PrepareClaudeRepin never writes or requires the old installation to exist.
// The hashes bind confirmation to exactly the pairing/config that was shown.
func (e *Engine) PrepareClaudeRepin(ctx context.Context, d Discovery, given ClaudeDependencies) (ClaudeRepinPlan, error) {
	if err := e.Store.Lock(); err != nil {
		return ClaudeRepinPlan{}, err
	}
	defer e.Store.Unlock()
	s, _, sh, rh, err := e.repinState()
	if err != nil {
		return ClaudeRepinPlan{}, err
	}
	pins, err := d.ResolveClaudeDependencies(given, s.Request.Workspace)
	if err != nil {
		return ClaudeRepinPlan{}, err
	}
	next, err := inspectClaudeDependencies(ctx, pins, s.Request.Workspace)
	if err != nil {
		return ClaudeRepinPlan{}, err
	}
	old, _ := inspectClaudeDependencies(ctx, ClaudeDependencies{NodePath: s.NodePath, SDKPath: s.ClaudeSDKPath}, s.Request.Workspace)
	if s.ClaudePinInfo != nil {
		old = *s.ClaudePinInfo
	}
	return ClaudeRepinPlan{Old: old, New: next, snapshotHash: sh, runtimeHash: rh}, nil
}

// ApplyClaudeRepin records an immutable local request before publishing runtime
// pins. No credential, account, approval, service definition or fence changes.
// If publishing is interrupted, rerunning repin repairs the runtime from a new
// preview; the earlier request remains in the audit trail.
func (e *Engine) ApplyClaudeRepin(ctx context.Context, plan ClaudeRepinPlan) (string, error) {
	if err := e.Store.Lock(); err != nil {
		return "", err
	}
	defer e.Store.Unlock()
	s, c, sh, rh, err := e.repinState()
	if err != nil {
		return "", err
	}
	if sh != plan.snapshotHash || rh != plan.runtimeHash {
		return "", errors.New("pairing changed after preview; rerun repin")
	}
	next, err := inspectClaudeDependencies(ctx, ClaudeDependencies{NodePath: plan.New.NodePath, SDKPath: plan.New.SDKPath}, s.Request.Workspace)
	if err != nil || next != plan.New {
		return "", errors.New("Claude dependencies changed after preview; rerun repin")
	}
	id, err := uuid()
	if err != nil {
		return "", err
	}
	event := struct {
		Kind string          `json:"kind"`
		ID   string          `json:"id"`
		At   time.Time       `json:"at"`
		Plan ClaudeRepinPlan `json:"changes"`
	}{"claude_dependencies_repin_requested", id, e.now(), plan}
	raw, _ := json.Marshal(event)
	if err := e.Store.Write("claude-repin-"+id+".json", raw, true); err != nil {
		return "", err
	}
	s.NodePath, s.ClaudeSDKPath, s.ClaudeRepinID, s.ClaudePinInfo = next.NodePath, next.SDKPath, id, &next
	if err := e.save(s, false); err != nil {
		return "", err
	}
	c.NodePath, c.ClaudeSDKPath, c.ClaudeRepinID = next.NodePath, next.SDKPath, id
	c.RecordAttachIdentities()
	raw, _ = json.Marshal(c)
	if err := e.Store.Write(RuntimeName, raw, false); err != nil {
		return "", errors.New("repin recorded but runtime update incomplete; rerun repin")
	}
	return id, nil
}

// AcknowledgeClaudeRepin is called only after the daemon has replaced its idle
// Claude adapter (or started with these pins). It never signals a process.
func AcknowledgeClaudeRepin(root string, c RuntimeConfig) error {
	if c.ClaudeRepinID == "" {
		return nil
	}
	if !uuidPattern.MatchString(c.ClaudeRepinID) {
		return ErrUnsafePath
	}
	s, err := OpenStore(root, false)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Lock(); err != nil {
		return err
	}
	raw, err := s.Read(RuntimeName, 128<<10)
	var current RuntimeConfig
	if err != nil || json.Unmarshal(raw, &current) != nil || current.ClaudeRepinID != c.ClaudeRepinID || current.DaemonID != c.DaemonID || current.NodePath != c.NodePath || current.ClaudeSDKPath != c.ClaudeSDKPath {
		return errors.New("repin acknowledgement is stale")
	}
	raw, _ = json.Marshal(struct {
		Kind     string `json:"kind"`
		ID       string `json:"id"`
		DaemonID string `json:"daemon_id"`
	}{"claude_dependencies_repin_applied", c.ClaudeRepinID, c.DaemonID})
	err = s.Write("claude-repin-"+c.ClaudeRepinID+"-applied.json", raw, true)
	if errors.Is(err, ErrCollision) {
		old, readErr := s.Read("claude-repin-"+c.ClaudeRepinID+"-applied.json", 4096)
		if readErr == nil && string(old) == string(raw) {
			return nil
		}
	}
	return err
}

func ClaudeRepinApplied(s *Store, id string) (bool, error) {
	if !uuidPattern.MatchString(id) {
		return false, ErrUnsafePath
	}
	raw, err := s.Read("claude-repin-"+id+"-applied.json", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	var event struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if err != nil || json.Unmarshal(raw, &event) != nil || event.Kind != "claude_dependencies_repin_applied" || event.ID != id {
		return false, errors.New("repin acknowledgement unavailable")
	}
	return true, nil
}
