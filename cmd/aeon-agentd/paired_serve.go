// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/piprobe"
)

func pairedAdapters(c agentsetup.RuntimeConfig) ([]agentd.EnrolledAccount, []agentd.Adapter, error) {
	// Any pin problem stays enrolled and blocked. It must not veto startup
	// or polling for every other account on this computer.
	blocked := map[string]agentsetup.BlockedAccount{}
	for _, block := range agentsetup.AccountPinBlocks(c) {
		if _, seen := blocked[block.AccountID]; !seen {
			blocked[block.AccountID] = block
		}
	}
	codexHomes, emails, claudeHomes, cursorIDs := map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
	claudeEmails := map[string]string{}
	piHomes, piProviders := map[string]string{}, map[string]string{}
	piNodes := map[string]piprobe.Node{}
	codexNodes, cursorNodes := map[string]harnesslaunch.Node{}, map[string]harnesslaunch.Node{}
	grokBindings := map[string]agentd.GrokBinding{}
	grokHomes, cursorHomes := map[string]string{}, map[string]string{}
	paths := map[string]string{}
	accounts := []agentd.EnrolledAccount{}
	for _, a := range c.Accounts {
		block, isBlocked := blocked[a.AccountID]
		accounts = append(accounts, agentd.EnrolledAccount{ID: a.AccountID, Key: a.Key, Harness: a.Harness, DependencyBlocked: isBlocked, PinReason: block.Reason, PinFix: block.Fix.Kind})
		if isBlocked {
			continue
		}
		if old := paths[a.Harness]; old != "" && old != a.Path {
			return nil, nil, errors.New("harness executable binding changed")
		}
		paths[a.Harness] = a.Path
		switch a.Harness {
		case agentd.Codex:
			codexHomes[a.Key] = a.Home
			codexNodes[a.Key] = a.Node
			emails[a.Key] = a.Identity
		case agentd.Claude:
			claudeHomes[a.Key] = a.Home
			claudeEmails[a.Key] = a.Identity
		case agentd.Cursor:
			cursorIDs[a.Key] = a.Identity
			if a.Home != "" {
				cursorHomes[a.Key] = a.Home
			}
			cursorNodes[a.Key] = a.Node
		case agentd.Pi:
			if !piprobe.ValidProvider(a.Identity) || !filepath.IsAbs(a.Home) {
				return nil, nil, errors.New("pi private provider binding unavailable")
			}
			piHomes[a.Key], piProviders[a.Key] = a.Home, a.Identity
			piNodes[a.Key] = a.PiNode
		case agentd.Grok:
			if a.Home != "" {
				grokHomes[a.Key] = a.Home
			}
			if a.Grok.BinaryPath != a.Path || a.Grok.PrincipalSHA256 != a.Identity || a.Grok.AuthPath == "" || a.Grok.ScratchRoot == "" {
				return nil, nil, errors.New("native Grok private binding unavailable")
			}
			grokBindings[a.Key] = a.Grok
		default:
			return nil, nil, errors.New("guided adapter unavailable")
		}
	}
	adapters := []agentd.Adapter{}
	if p := paths[agentd.Codex]; p != "" {
		a := agentd.NewCodexAdapter(p, codexHomes)
		a.SetExpectedEmails(emails)
		a.Nodes = codexNodes
		adapters = append(adapters, a)
	}
	if p := paths[agentd.Claude]; p != "" {
		a := agentd.NewClaudeAdapter(c.NodePath, c.ClaudeSDKPath, p, claudeHomes)
		a.Workspace = c.Workspace
		a.SetExpectedEmails(claudeEmails)
		adapters = append(adapters, a)
	}
	if p := paths[agentd.Cursor]; p != "" {
		cursor := agentd.NewCursorAdapter(p, cursorIDs)
		if len(cursorHomes) > 0 {
			cursor.Homes = cursorHomes
		}
		cursor.Nodes = cursorNodes
		adapters = append(adapters, cursor)
	}
	if p := paths[agentd.Pi]; p != "" {
		a := agentd.NewPiAdapter(p, piHomes)
		a.SetExpectedProviders(piProviders)
		a.Nodes = piNodes
		adapters = append(adapters, a)
	}
	if len(grokBindings) > 0 {
		grok := agentd.NewGrokAdapter(grokBindings)
		grok.Homes = grokHomes
		adapters = append(adapters, grok)
	}
	return accounts, adapters, nil
}

func servePaired(root string, capacityInterval time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return servePairedContext(ctx, root, capacityInterval)
}

func servePairedContext(ctx context.Context, root string, capacityInterval time.Duration) error {
	state := filepath.Join(root, "daemon")
	socket, err := agentsetup.ResolveSocketPath(state, nil)
	if err != nil {
		var tooLong *agentsetup.SocketPathLengthError
		if errors.As(err, &tooLong) {
			return fmt.Errorf("%w Use a shorter --setup-root.", err)
		}
		return err
	}
	c, err := agentsetup.ReadRuntimeConfig(root)
	if err != nil {
		return err
	}
	// The independent lifecycle proof must be usable before runtime Me. A
	// revoked key cannot prevent cold-start tombstone discovery/fencing.
	permitted, err := pairedPreflight(ctx, root, c.Origin, nil)
	if err != nil {
		return err
	}
	if !permitted {
		return nil
	} // successful intentional exit; helper owns cleanup
	c, key, err := agentsetup.ReadRuntime(root)
	if err != nil {
		return err
	}
	accounts, adapters, err := pairedAdapters(c)
	if err != nil {
		return err
	}
	if err := agentsetup.PrepareSocketDirectory(socket); err != nil {
		return err
	}
	remote := agentd.NewRemote(c.Origin, string(key))
	s, err := agentd.NewSupervisor(ctx, agentd.Config{CapacityInterval: capacityInterval, API: remote, StateRoot: state, DaemonID: c.DaemonID, Workspace: c.Workspace, Accounts: accounts, Adapters: adapters, EstimatedUnits: map[string]int64{"requests": 1},
		PollDiagnostic: func(reason string) { slog.Warn("agentd polling diagnostic", "reason", reason) }})
	if err != nil {
		return err
	}
	defer s.Close(context.Background())
	if s.TenantID() != c.TenantID || s.PrincipalID() != c.PrincipalID {
		return errors.New("runtime identity differs from approved pairing")
	}
	watches, err := pairedAttach(root, c, remote)
	if err != nil {
		slog.Warn("attach disabled; restart agentd after updating agentd or Aeon to retry", "error", err)
	}
	if watches != nil {
		defer func() {
			op, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			watches.Close(op)
		}()
	}
	local, err := agentd.ServePairedLocal(s, socket, watches)
	if err != nil {
		return err
	}
	defer local.Close()
	store, err := agentsetup.OpenStore(state, false)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(agentsetup.ControlReference{Socket: socket, DaemonID: c.DaemonID, Generation: s.Generation()})
	err = store.Write("control.json", raw, false)
	store.Close()
	if err != nil {
		return err
	}
	captureCtx, stopCapture := context.WithCancel(ctx)
	captureDone := make(chan struct{})
	go func() { defer close(captureDone); s.RunCapacityCaptures(captureCtx) }()
	defer func() { stopCapture(); <-captureDone }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	stopping := false
	for {
		if stopping || ctx.Err() != nil {
			stopping = true
			stopCapture()
			<-captureDone
			op, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = s.Close(op)
			cancel()
			if err == nil {
				return nil
			}
			awaitDrainRetry()
			continue
		}
		op, cancel := context.WithTimeout(ctx, 20*time.Second)
		if watches != nil {
			watches.Sweep(op)
		}
		// Lifecycle reconciliation is required before every fresh dispatch;
		// the independent tombstone proof still works after key revocation.
		err = syncPairing(op, root, c.Origin, s)
		if err == nil {
			next, _, readErr := agentsetup.ReadRuntime(root)
			if readErr == nil {
				// Claude repins and dependency repairs hold only Claude;
				// identity or binding changes still stop the daemon.
				c, readErr = pollPairedRuntime(op, s, root, c, next)
				if errors.Is(readErr, errStopDaemon) {
					stopping = true
				}
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			stopping = true
		case <-ticker.C:
		}
	}
}

func restartPairedClaude(ctx context.Context, s pairedRuntimeSupervisor, old, next agentsetup.RuntimeConfig, adapters []agentd.Adapter) error {
	// Repin is not an account-enrollment or authority-change mechanism.
	// Another harness may renew its own interpreter pin in the same config
	// when that pin passes its identity, launcher and interpreter checks.
	if next.ClaudeRepinID == "" || !claudeRepinPreservesOtherBindings(old, next) {
		return errors.New("repin changed more than Claude dependencies")
	}
	for _, adapter := range adapters {
		if claude, ok := adapter.(*agentd.ClaudeAdapter); ok {
			return s.RestartClaude(ctx, claude)
		}
	}
	return errors.New("repin has no approved Claude adapter")
}

// claudeRepinPreservesOtherBindings accepts Claude's shared pin replacement
// plus a per-account interpreter pin that was validated on its own. Identity,
// launcher, home and every other fence still have to match.
func claudeRepinPreservesOtherBindings(old, next agentsetup.RuntimeConfig) bool {
	rolled := next
	rolled.Accounts = append([]agentsetup.RuntimeAccount(nil), next.Accounts...)
	rolled.NodePath, rolled.ClaudeSDKPath, rolled.ClaudeRepinID = old.NodePath, old.ClaudeSDKPath, old.ClaudeRepinID
	if len(rolled.Accounts) != len(old.Accounts) {
		return false
	}
	used := map[int]bool{}
	for i := range rolled.Accounts {
		match := -1
		for j := range old.Accounts {
			if !used[j] && old.Accounts[j].AccountID == rolled.Accounts[i].AccountID {
				match = j
				break
			}
		}
		if match < 0 {
			return false
		}
		used[match] = true
		if acceptedAccountPin(old.Accounts[match], rolled.Accounts[i], next.Workspace) {
			rolled.Accounts[i].Node = old.Accounts[match].Node
			rolled.Accounts[i].PiNode = old.Accounts[match].PiNode
		}
	}
	return reflect.DeepEqual(old, rolled)
}

// acceptedAccountPin reports a no-op or a pin swap whose new interpreter
// passes AccountPinBlocks on its own. Claude and Grok pins are not renewals.
// An invalid new pin returns false so it cannot ride a Claude repin.
func acceptedAccountPin(old, next agentsetup.RuntimeAccount, workspace string) bool {
	if next.Harness == agentd.Claude || next.Harness == agentd.Grok || old.Harness == agentd.Claude || old.Harness == agentd.Grok {
		return false
	}
	if old.Harness != next.Harness || old.Key != next.Key || old.AccountID != next.AccountID || old.Path != next.Path || old.Home != next.Home || old.Identity != next.Identity || old.Grok != next.Grok {
		return false
	}
	if old.Node == next.Node && old.PiNode == next.PiNode {
		return true
	}
	blocks := agentsetup.AccountPinBlocks(agentsetup.RuntimeConfig{Workspace: workspace, Accounts: []agentsetup.RuntimeAccount{next}})
	return len(blocks) == 0
}

func adoptAcceptedPins(current, next agentsetup.RuntimeConfig) agentsetup.RuntimeConfig {
	out := current
	accounts := append([]agentsetup.RuntimeAccount(nil), current.Accounts...)
	for i := range accounts {
		for _, candidate := range next.Accounts {
			if candidate.AccountID == accounts[i].AccountID && acceptedAccountPin(accounts[i], candidate, next.Workspace) {
				accounts[i].Node = candidate.Node
				accounts[i].PiNode = candidate.PiNode
			}
		}
	}
	out.Accounts = accounts
	return out
}

func accountsExcept(accounts []agentd.EnrolledAccount, harness string) []agentd.EnrolledAccount {
	kept := make([]agentd.EnrolledAccount, 0, len(accounts))
	for _, account := range accounts {
		if account.Harness != harness {
			kept = append(kept, account)
		}
	}
	return kept
}

func adaptersExceptClaude(adapters []agentd.Adapter) []agentd.Adapter {
	kept := make([]agentd.Adapter, 0, len(adapters))
	for _, adapter := range adapters {
		if _, ok := adapter.(*agentd.ClaudeAdapter); ok {
			continue
		}
		kept = append(kept, adapter)
	}
	return kept
}

type pairedRuntimeSupervisor interface {
	SetHarnessHoldWithReason(string, string, string)
	RefreshAccounts([]agentd.EnrolledAccount, []agentd.Adapter) error
	PinHealthMatches([]agentd.EnrolledAccount) bool
	RestartClaude(context.Context, *agentd.ClaudeAdapter) error
	PollOnce(context.Context) error
}

// errStopDaemon marks a refresh failure that must end this daemon generation.
var errStopDaemon = errors.New("paired daemon must stop")

// pollPairedRuntime keeps recovery and other harnesses polling while Claude
// waits for a repin or a dependency repair. Identity changes fail closed: no
// poll, while fence sync continues; an approved binding change stops the daemon.
func pollPairedRuntime(ctx context.Context, s pairedRuntimeSupervisor, root string, c, next agentsetup.RuntimeConfig) (agentsetup.RuntimeConfig, error) {
	current, err := refreshPairedRuntime(ctx, s, root, c, next)
	if err == nil {
		_ = s.PollOnce(ctx)
	}
	return current, err
}

func refreshPairedRuntime(ctx context.Context, s pairedRuntimeSupervisor, root string, c, next agentsetup.RuntimeConfig) (agentsetup.RuntimeConfig, error) {
	_, ac, ad, stop, _, err := runtimeRefresh(c, next)
	if err != nil {
		if stop {
			return c, fmt.Errorf("%w: %w", errStopDaemon, err)
		}
		return c, err
	}
	if !reflect.DeepEqual(c, next) || !s.PinHealthMatches(ac) {
		if next.ClaudeRepinID != c.ClaudeRepinID {
			s.SetHarnessHoldWithReason(agentd.Claude, "repin_pending", "Claude repin pending: waiting for active Claude runs to exit")
			if err := validatePairedClaude(next); err != nil {
				s.SetHarnessHoldWithReason(agentd.Claude, agentsetup.HarnessFailureReason(err), err.Error())
				return c, nil
			}
			if err := restartPairedClaude(ctx, s, c, next, ad); err != nil {
				if errors.Is(err, agentd.ErrDraining) {
					// Apply the other harness's accepted pin now. Leave Claude's
					// shared pins and repin id unchanged so the next tick retries
					// the adapter swap after the live Claude process exits.
					// Omit Claude accounts: their adapter is still the previous one,
					// and RefreshAccounts rejects an unblocked account with no adapter.
					if err := s.RefreshAccounts(accountsExcept(ac, agentd.Claude), adaptersExceptClaude(ad)); err != nil {
						return c, err
					}
					return adoptAcceptedPins(c, next), nil
				}
				s.SetHarnessHoldWithReason(agentd.Claude, agentsetup.HarnessFailureReason(err), "Claude repin failed: "+err.Error())
				return c, nil
			}
		}
		if err := s.RefreshAccounts(ac, ad); err != nil {
			return c, err
		}
		c = next
	}
	if err := validatePairedClaude(c); err != nil {
		s.SetHarnessHoldWithReason(agentd.Claude, agentsetup.HarnessFailureReason(err), err.Error())
		return c, nil
	}
	// c describes the adapter already installed above or at cold start. A
	// receipt retry must not replace it again or depend on historical runs.
	if err := acknowledgePairedClaude(root, c); err != nil {
		s.SetHarnessHoldWithReason(agentd.Claude, "repin_pending", "Claude repin pending: acknowledgement unavailable; retrying automatically")
		return c, nil
	}
	s.SetHarnessHoldWithReason(agentd.Claude, "dependency_invalid", "")
	return c, nil
}

// Claude acknowledgement never depends on another harness's pin health.
func validatePairedClaude(c agentsetup.RuntimeConfig) error {
	accounts := []agentsetup.RuntimeAccount{}
	for _, a := range c.Accounts {
		if a.Harness == agentd.Claude {
			accounts = append(accounts, a)
		}
	}
	c.Accounts = accounts
	return agentsetup.ValidateRuntimeDependencies(c)
}

func acknowledgePairedClaude(root string, c agentsetup.RuntimeConfig) error {
	if c.ClaudeRepinID == "" {
		return nil
	}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		return err
	}
	applied, err := agentsetup.ClaudeRepinApplied(store, c.ClaudeRepinID)
	store.Close()
	if err != nil || applied {
		return err
	}
	return agentsetup.AcknowledgeClaudeRepin(root, c)
}

// runtimeRefresh classifies one poll tick. Pin problems are account blocks
// inside the adapter set: they never stop the daemon or suppress polling.
// A changed pairing identity suppresses polling. A non-pin binding change stops the daemon.
func runtimeRefresh(current, next agentsetup.RuntimeConfig) (updated agentsetup.RuntimeConfig, accounts []agentd.EnrolledAccount, adapters []agentd.Adapter, stop, poll bool, err error) {
	if next.Origin != current.Origin || next.TenantID != current.TenantID || next.PrincipalID != current.PrincipalID || next.DaemonID != current.DaemonID || next.Workspace != current.Workspace || next.ComputerID != current.ComputerID {
		return current, nil, nil, false, false, errors.New("pairing configuration identity changed")
	}
	if nonPinBindingChanged(current, next) {
		return current, nil, nil, true, false, errors.New("approved account binding changed")
	}
	accounts, adapters, err = pairedAdapters(next)
	if err != nil {
		return current, nil, nil, false, false, err
	}
	return next, accounts, adapters, false, true, nil
}

func nonPinBindingChanged(current, next agentsetup.RuntimeConfig) bool {
	for _, nextAccount := range next.Accounts {
		for _, oldAccount := range current.Accounts {
			if oldAccount.AccountID == nextAccount.AccountID && pinFree(oldAccount) != pinFree(nextAccount) {
				return true
			}
		}
	}
	return false
}

func pinFree(account agentsetup.RuntimeAccount) agentsetup.RuntimeAccount {
	account.Node = harnesslaunch.Node{}
	account.PiNode = piprobe.Node{}
	return account
}

func syncPairing(ctx context.Context, root, origin string, s *agentd.Supervisor) error {
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		return err
	}
	defer store.Close()
	e := agentsetup.Engine{Store: store, API: agentsetup.HTTPClient{Origin: origin}, Local: localPairing{root: root, supervisor: s}}
	return e.SyncFences(ctx)
}

// pairedPreflight injects the public lifecycle transport in cold-start tests.
func pairedPreflight(ctx context.Context, root, origin string, api agentsetup.PairingAPI) (bool, error) {
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		return false, err
	}
	defer store.Close()
	if api == nil {
		api = agentsetup.HTTPClient{Origin: origin}
	}
	e := agentsetup.Engine{Store: store, API: api, Local: localPairing{root: root}}
	if err = e.SyncFences(ctx); err != nil {
		return false, err
	}
	return e.DispatchPermitted()
}
