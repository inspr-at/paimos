// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
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
	"github.com/inspr-at/paimos/internal/version"
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
	return agentsetup.LocalStatus{HarnessErrors: s.HarnessErrors, ProfilePermissions: s.ProfilePermissions, HarnessFailed: s.HarnessFailed, LoginRequired: s.LoginRequired, VerificationUnavailable: s.VerificationUnavailable, Ready: s.Ready, DaemonID: s.DaemonID, State: s.State, Active: s.ActiveRunIDs, Unconfirmed: s.UnconfirmedRunIDs, SettlementPending: s.SettlementPendingRunIDs, VerificationResults: s.VerificationResults}
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
	return setupCommandInput(command, args, os.Stdin, out)
}

func setupCommandInput(command string, args []string, in io.Reader, out io.Writer) error {
	pair := command == "pair"
	if pair {
		command = "setup"
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var root, origin, tenantID, tenantSlug, workspace, computer, account, contextLabel, nodePath, sdkPath string
	var harnesses stringsFlag
	var jsonOutput, startService, once, yes bool
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
	f.BoolVar(&yes, "yes", false, "confirm Claude dependency repin without prompting")
	if f.Parse(args) != nil || len(f.Args()) != 0 {
		return errors.New("invalid setup arguments")
	}
	if command == "repin" {
		if len(harnesses) != 1 || harnesses[0] != "claude" {
			return errors.New("repin requires --harness claude")
		}
		valid := true
		f.Visit(func(v *flag.Flag) {
			switch v.Name {
			case "state-root", "harness", "node-path", "claude-sdk-path", "yes", "json":
			default:
				valid = false
			}
		})
		if !valid {
			return errors.New("repin accepts only --state-root, --harness claude, dependency paths, --yes and --json")
		}
	} else if yes {
		return errors.New("--yes is only supported for repin")
	}
	prompt := setupPrompt{in: bufio.NewReader(in), out: out, json: jsonOutput}
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
	if root == "" {
		root, err = agentsetup.DefaultStateRoot(platform.OS, home, os.Getenv("XDG_STATE_HOME"))
		if err != nil {
			return err
		}
	}
	if err = agentsetup.ValidateStateLocation(root, ""); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = setupExecutable(command, executable, home)
	if err != nil {
		return err
	}
	systemctl := ""
	if platform.OS == "linux" {
		if p, e := exec.LookPath("systemctl"); e == nil {
			systemctl, _ = filepath.EvalSymlinks(p)
		}
	}
	// Read existing options before defaulting the workspace, so rerunning pair
	// from another directory resumes the same approved request.
	store, err := agentsetup.OpenStore(root, false)
	if err != nil && !(command == "setup" && errors.Is(err, os.ErrNotExist)) {
		return err
	}
	manager := &agentsetup.ServiceManager{Platform: platform, Home: home, UID: os.Getuid(), Executable: executable, Systemctl: systemctl}
	engine := &agentsetup.Engine{Store: store, Services: manager, Local: localPairing{root: root}}
	var saved agentsetup.Options
	var savedErr error = os.ErrNotExist
	if store != nil {
		defer store.Close()
		saved, savedErr = engine.SavedOptions()
		if savedErr != nil && !errors.Is(savedErr, os.ErrNotExist) {
			return savedErr
		}
	}
	if savedErr == nil {
		if origin != "" && strings.TrimRight(origin, "/") != saved.Origin {
			return errors.New("instance differs from existing pairing")
		}
		origin = saved.Origin
		if workspace == "" {
			workspace = saved.Workspace
		}
	}
	if origin == "" && command == "setup" {
		origin, err = prompt.read("Aeon address from the pairing guide: ", "--url")
		if err != nil {
			return err
		}
	}
	if err = agentsetup.ValidateOrigin(origin); err != nil {
		return err
	}
	if workspace == "" && command == "setup" {
		workspace, err = os.Getwd()
		if err == nil {
			workspace, err = filepath.EvalSymlinks(workspace)
		}
		if err != nil {
			return errors.New("working folder unavailable; pass --workspace")
		}
		// Reject an impossible choice before asking the person to approve it.
		if err = agentsetup.ValidateStateLocation(root, workspace); err != nil {
			return err
		}
		yes, err := prompt.confirm(fmt.Sprintf("Use %q as the working folder?", workspace), "--workspace")
		if err != nil {
			return err
		}
		if !yes {
			return errors.New("working folder declined; rerun from the approved folder or pass --workspace")
		}
	}
	if err = agentsetup.ValidateStateLocation(root, workspace); err != nil {
		return err
	}
	engine.API = agentsetup.HTTPClient{Origin: origin}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "repin" {
		return repinClaude(ctx, engine, agentsetup.Discovery{Home: home}, agentsetup.ClaudeDependencies{NodePath: nodePath, SDKPath: sdkPath}, prompt, yes)
	}
	var candidates []agentsetup.Candidate
	if savedErr != nil || command == "add-harness" {
		d := agentsetup.Discovery{Home: home, NodePath: nodePath, Workspace: workspace}
		if len(harnesses) == 0 && (command == "setup" || command == "add-harness") {
			if jsonOutput {
				return errors.New("--harness is required with --json")
			}
			candidates, err = prompt.selectHarnesses(d.Available(ctx, contextLabel))
			if err != nil {
				return err
			}
		}
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
	if pair && savedErr != nil {
		explicitService := false
		f.Visit(func(v *flag.Flag) {
			if v.Name == "start-service" {
				explicitService = true
			}
		})
		if !explicitService {
			startService = true
			if errors.Is(manager.Preflight(ctx, root, nil), agentsetup.ErrDeclarative) {
				startService = false
				if err = printSetupProgress(out, jsonOutput, agentsetup.Progress{Stage: "managed_plan", Action: "Pairing preserves the Nix/Home Manager service. After browser approval, enable the reviewed declarative paired service for this state root."}); err != nil {
					return err
				}
			}
		}
	}
	if store == nil {
		store, err = agentsetup.OpenStore(root, true)
		if err != nil {
			return err
		}
		defer store.Close()
		engine.Store = store
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
	if command == "status" && err == nil {
		p = setupVersionStatus(ctx, engine.API, origin, p)
	}
	if e := printSetupProgress(out, jsonOutput, p); e != nil {
		return e
	}
	if err != nil {
		return err
	}
	p, err = pollSetupProgress(ctx, command, p, once, jsonOutput, out, engine, store.Unlock, waitSetupPoll)
	if err != nil {
		return err
	}
	if command == "setup" || command == "add-harness" {
		switch p.Stage {
		case "blocked", "login_required", "verification_failed":
			return errors.New("setup incomplete; follow the reported action before resuming")
		}
	}
	return nil
}

func setupExecutable(command, executable, home string) (string, error) {
	if command == "setup" || command == "pair" || command == "add-harness" {
		return agentsetup.ServiceExecutable(executable, home)
	}
	// Maintenance uses the running binary, even when an installer link was
	// removed or now targets a newer release. Unit ownership checks still apply.
	return filepath.EvalSymlinks(executable)
}

func setupVersionStatus(ctx context.Context, api agentsetup.PairingAPI, origin string, p agentsetup.Progress) agentsetup.Progress {
	p.VersionStatus = "unavailable"
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	g, err := api.Guide(ctx)
	if err != nil || g.Protocol != "pairing-v1" || strings.TrimRight(g.InstanceURL, "/") != strings.TrimRight(origin, "/") || g.Version == "" || g.Version == "dev" || version.Version == "dev" {
		return p
	}
	p.VersionStatus = "matching"
	if g.Version != version.Version {
		p.VersionStatus = "mismatch"
		p.Action = strings.TrimSpace(p.Action + " Installed helper and instance versions differ; check the published releases before upgrading.")
	}
	return p
}

type setupPoller interface {
	Status(context.Context) (agentsetup.Progress, error)
	Step(context.Context) (agentsetup.Progress, error)
}

func waitSetupPoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func pollSetupProgress(ctx context.Context, command string, p agentsetup.Progress, once, jsonOutput bool, out io.Writer, engine setupPoller, unlock func(), waitFor func(context.Context, time.Duration) error) (agentsetup.Progress, error) {
	for !once && setupNeedsPoll(command, p) {
		wait := 5 * time.Second
		if p.RetryAfterSeconds > 5 {
			wait = time.Duration(p.RetryAfterSeconds) * time.Second
		}
		if err := waitFor(ctx, wait); err != nil {
			return p, errors.New(command + " paused; rerun the same command to resume")
		}
		var err error
		if command == "disconnect" {
			p, err = engine.Status(ctx)
		} else {
			p, err = engine.Step(ctx)
		}
		unlock()
		if e := printSetupProgress(out, jsonOutput, p); e != nil {
			return p, e
		}
		if err != nil {
			return p, err
		}
	}
	return p, nil
}

func setupNeedsPoll(command string, p agentsetup.Progress) bool {
	if command == "disconnect" {
		return p.Stage == "draining"
	}
	return (command == "setup" || command == "add-harness") && (p.Stage == "awaiting_approval" || p.Stage == "requesting" || p.Stage == "provisioning" || p.Stage == "verification_pending")
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
	return err
}
