// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/piprobe"
)

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

type localPairing struct {
	root       string
	supervisor *agentd.Supervisor
}

func (l localPairing) client() (agentdwire.Client, error) {
	return agentdwire.OpenClient(filepath.Join(l.root, "daemon"))
}
func localStatus(s agentd.LifecycleStatus) agentsetup.LocalStatus {
	var blocked []agentsetup.BlockedAccount
	if len(s.BlockedAccounts) > 0 {
		blocked = append([]agentsetup.BlockedAccount(nil), s.BlockedAccounts...)
	}
	return agentsetup.LocalStatus{ProfilePermissions: s.ProfilePermissions, HarnessFailed: s.HarnessFailed, LoginRequired: s.LoginRequired, VerificationUnavailable: s.VerificationUnavailable, Ready: s.Ready, DaemonID: s.DaemonID, State: s.State, Active: s.ActiveRunIDs, Unconfirmed: s.UnconfirmedRunIDs, SettlementPending: s.SettlementPendingRunIDs, VerificationResults: s.VerificationResults, BlockedAccounts: blocked}
}
func (l localPairing) Status(ctx context.Context, account string) (agentsetup.LocalStatus, error) {
	if l.supervisor != nil {
		return localStatus(l.supervisor.Lifecycle(account)), nil
	}
	client, e := l.client()
	if e == nil {
		if status, err := client.Lifecycle(ctx, account); err == nil {
			return localStatus(status), nil
		}
	}
	c, err := agentsetup.ReadRuntimeConfig(l.root)
	if err != nil {
		return agentsetup.LocalStatus{}, err
	}
	status, err := agentd.OfflineLifecycle(filepath.Join(l.root, "daemon"), c.DaemonID, c.TenantID, c.PrincipalID, account)
	return localStatus(status), err
}
func (l localPairing) Fence(ctx context.Context, daemon, account string) (agentsetup.LocalStatus, error) {
	// This private per-pairing path is the only offline dispatch fence. It
	// cannot affect another daemon or vendor sign-in and survives a restart.
	state := filepath.Join(l.root, "daemon")
	store, err := agentsetup.OpenStore(state, true)
	if err != nil {
		return agentsetup.LocalStatus{}, err
	}
	store.Close()
	if err = agentd.PersistFence(state, daemon, account); err != nil {
		return agentsetup.LocalStatus{}, err
	}
	if l.supervisor != nil {
		s, e := l.supervisor.Drain(agentd.DrainRequest{DaemonID: daemon, AccountID: account})
		return localStatus(s), e
	}
	client, err := l.client()
	if err != nil {
		return agentsetup.LocalStatus{DaemonID: daemon, State: "unconfirmed"}, nil
	}
	s, err := client.Drain(ctx, agentd.DrainRequest{DaemonID: daemon, AccountID: account})
	if err != nil {
		return agentsetup.LocalStatus{DaemonID: daemon, State: "unconfirmed"}, nil
	}
	return localStatus(s), nil
}

func setupCommand(command string, args []string, out io.Writer) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var root, origin, tenantID, tenantSlug, workspace, computer, account, contextLabel, nodePath, sdkPath string
	var harnesses stringsFlag
	var jsonOutput, startService, once bool
	f.StringVar(&root, "state-root", "", "private pairing state directory")
	f.StringVar(&origin, "url", "", "HTTPS Aeon instance origin")
	f.StringVar(&tenantID, "tenant-id", "", "tenant UUID")
	f.StringVar(&tenantSlug, "tenant", "", "tenant slug (defaults to instance guide)")
	f.StringVar(&workspace, "workspace", "", "approved physical working folder")
	f.StringVar(&computer, "computer-name", "", "computer display name")
	f.Var(&harnesses, "harness", "selected harness; repeat for another harness")
	f.StringVar(&contextLabel, "account-context", "", "Expected account identity (pi: configured provider ID)")
	f.StringVar(&account, "account-id", "", "remove only this enrolled account")
	f.StringVar(&nodePath, "node-path", "", "pinned Node executable for npm harness launchers")
	f.StringVar(&sdkPath, "claude-sdk-path", "", "pinned Claude Agent SDK module")
	f.BoolVar(&jsonOutput, "json", false, "safe progress as JSON")
	f.BoolVar(&startService, "start-service", false, "request user service installation after authenticated Connect approval")
	f.BoolVar(&once, "once", false, "perform one resumable step")
	if f.Parse(args) != nil || len(f.Args()) != 0 {
		return errors.New("invalid setup arguments")
	}
	if !filepath.IsAbs(root) {
		return errors.New("--state-root must be an absolute private directory outside repositories")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("user home unavailable")
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return err
	}
	platform, err := agentsetup.CurrentPlatform()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	systemctl := ""
	if platform.OS == "linux" {
		if p, e := exec.LookPath("systemctl"); e == nil {
			systemctl, _ = filepath.EvalSymlinks(p)
		}
	}
	store, err := agentsetup.OpenStore(root, command == "setup")
	if err != nil {
		return err
	}
	defer store.Close()
	manager := &agentsetup.ServiceManager{Platform: platform, Home: home, UID: os.Getuid(), Executable: executable, Systemctl: systemctl}
	engine := &agentsetup.Engine{Store: store, Services: manager, Local: localPairing{root: root}}
	saved, savedErr := engine.SavedOptions()
	if savedErr == nil {
		if origin != "" && strings.TrimRight(origin, "/") != saved.Origin {
			return errors.New("instance differs from existing pairing")
		}
		origin = saved.Origin
		if workspace == "" {
			workspace = saved.Workspace
		}
	}
	if err = agentsetup.ValidateOrigin(origin); err != nil {
		return err
	}
	engine.API = agentsetup.HTTPClient{Origin: origin}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var candidates []agentsetup.Candidate
	if savedErr != nil || command == "add-harness" {
		d := agentsetup.Discovery{Home: home, NodePath: nodePath, Workspace: workspace}
		for _, h := range harnesses {
			c, e := d.Detect(ctx, h, contextLabel)
			if e != nil {
				stage := "login_required"
				if errors.Is(e, piprobe.ErrStart) || errors.Is(e, piprobe.ErrPrivateProfile) {
					stage = "blocked"
				}
				_ = printSetupProgress(out, jsonOutput, agentsetup.Progress{Schema: "aeon.agent-setup.v1", Stage: stage, Action: e.Error()})
				return e
			}
			candidates = append(candidates, c)
		}
		if command == "setup" || command == "add-harness" {
			for _, c := range candidates {
				if c.Harness == "claude" && (savedErr != nil || saved.NodePath == "" && saved.ClaudeSDKPath == "") {
					deps, e := d.ResolveClaudeDependencies(agentsetup.ClaudeDependencies{NodePath: nodePath, SDKPath: sdkPath}, workspace)
					if e != nil {
						_ = printSetupProgress(out, jsonOutput, agentsetup.Progress{Schema: "aeon.agent-setup.v1", Stage: "blocked", Action: e.Error()})
						return e
					}
					nodePath, sdkPath = deps.NodePath, deps.SDKPath
					break
				}
			}
		}
	}
	// The shared Node/SDK options belong to Claude. Other launchers retain a private
	// per-account interpreter binding even when --node-path was supplied.
	if nodePath, err = claudeNodeOption(nodePath, candidates, saved.Candidates); err != nil {
		return err
	}
	engine.ClaudeDependencies = agentsetup.ClaudeDependencies{NodePath: nodePath, SDKPath: sdkPath}
	if computer == "" {
		computer, _ = os.Hostname()
	}
	var p agentsetup.Progress
	switch command {
	case "setup":
		p, err = engine.Begin(ctx, agentsetup.Options{Origin: origin, TenantID: tenantID, TenantSlug: tenantSlug, Workspace: workspace, ComputerName: computer, Platform: platform, Candidates: candidates, StartService: startService, NodePath: nodePath, ClaudeSDKPath: sdkPath})
	case "status":
		p, err = engine.Status(ctx)
	case "disconnect":
		p, err = engine.Disconnect(ctx, account)
	case "add-harness":
		p, err = engine.AddHarness(ctx, candidates)
	default:
		return errors.New("unknown setup command")
	}
	store.Unlock()
	if e := printSetupProgress(out, jsonOutput, p); e != nil {
		return e
	}
	if err != nil {
		return err
	}
	for !once && (command == "setup" || command == "add-harness") && (p.Stage == "awaiting_approval" || p.Stage == "requesting" || p.Stage == "provisioning" || p.Stage == "verification_pending") {
		wait := 5 * time.Second
		if p.RetryAfterSeconds > 5 {
			wait = time.Duration(p.RetryAfterSeconds) * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("setup paused; rerun the same command to resume")
		case <-timer.C:
		}
		p, err = engine.Step(ctx)
		store.Unlock()
		if e := printSetupProgress(out, jsonOutput, p); e != nil {
			return e
		}
		if err != nil {
			return err
		}
	}
	if command == "setup" || command == "add-harness" {
		switch p.Stage {
		case "blocked", "login_required", "verification_failed":
			return errors.New("setup incomplete; follow the reported action before resuming")
		}
	}
	return nil
}

func claudeNodeOption(requested string, discovered, saved []agentsetup.Candidate) (string, error) {
	choices := discovered
	if len(choices) == 0 {
		choices = saved
	}
	for _, c := range choices {
		if c.Harness == "claude" {
			return requested, nil
		}
	}
	if len(choices) == 0 {
		return requested, nil
	}
	if len(discovered) == 0 && requested != "" {
		physical, err := filepath.EvalSymlinks(requested)
		matched := false
		for _, c := range choices {
			node := c.Interpreter()
			if node.Path == "" {
				continue
			}
			if err != nil || physical != node.Path {
				return "", errors.New("--node-path conflicts with saved interpreter; no enrollment was changed")
			}
			matched = true
		}
		if !matched {
			return "", errors.New("--node-path conflicts with saved interpreter; no enrollment was changed")
		}
	}
	return "", nil
}

func printSetupProgress(out io.Writer, jsonOutput bool, p agentsetup.Progress) error {
	if p.Schema == "" {
		p.Schema = "aeon.agent-setup.v1"
	}
	if jsonOutput {
		return json.NewEncoder(out).Encode(p)
	}
	if p.UserCode != "" {
		_, err := fmt.Fprintf(out, "Pairing code: %s\nApprove at: %s\n", p.UserCode, p.VerificationURI)
		if err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "%s: %s\n", p.Stage, p.Action)
	if err != nil {
		return err
	}
	for _, blocked := range p.BlockedAccounts {
		if _, err = fmt.Fprintf(out, "blocked account %s harness %s reason %s fix %s\n", blocked.AccountID, blocked.Harness, blocked.Reason, blocked.Fix); err != nil {
			return err
		}
	}
	return nil
}
