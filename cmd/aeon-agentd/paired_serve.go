// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func pairedAdapters(c agentsetup.RuntimeConfig) ([]agentd.EnrolledAccount, []agentd.Adapter, error) {
	codexHomes, emails, claudeHomes, cursorIDs := map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
	claudeEmails := map[string]string{}
	grokBindings := map[string]agentd.GrokBinding{}
	grokHomes, cursorHomes := map[string]string{}, map[string]string{}
	paths := map[string]string{}
	accounts := []agentd.EnrolledAccount{}
	for _, a := range c.Accounts {
		if old := paths[a.Harness]; old != "" && old != a.Path {
			return nil, nil, errors.New("harness executable binding changed")
		}
		paths[a.Harness] = a.Path
		accounts = append(accounts, agentd.EnrolledAccount{ID: a.AccountID, Key: a.Key, Harness: a.Harness})
		switch a.Harness {
		case agentd.Codex:
			codexHomes[a.Key] = a.Home
			emails[a.Key] = a.Identity
		case agentd.Claude:
			claudeHomes[a.Key] = a.Home
			claudeEmails[a.Key] = a.Identity
		case agentd.Cursor:
			cursorIDs[a.Key] = a.Identity
			if a.Home != "" {
				cursorHomes[a.Key] = a.Home
			}
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
		adapters = append(adapters, cursor)
	}
	if len(grokBindings) > 0 {
		grok := agentd.NewGrokAdapter(grokBindings)
		grok.Homes = grokHomes
		adapters = append(adapters, grok)
	}
	return accounts, adapters, nil
}

func servePaired(root string, capacityInterval time.Duration) error {
	c, err := agentsetup.ReadRuntimeConfig(root)
	if err != nil {
		return err
	}
	// The independent lifecycle proof must be usable before runtime Me. A
	// revoked key cannot prevent cold-start tombstone discovery/fencing.
	permitted, err := pairedPreflight(context.Background(), root, c.Origin, nil)
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	state := filepath.Join(root, "daemon")
	remote := agentd.NewRemote(c.Origin, string(key))
	s, err := agentd.NewSupervisor(ctx, agentd.Config{CapacityInterval: capacityInterval, API: remote, StateRoot: state, DaemonID: c.DaemonID, Workspace: c.Workspace, Accounts: accounts, Adapters: adapters, EstimatedUnits: map[string]int64{"requests": 1}})
	if err != nil {
		return err
	}
	defer s.Close(context.Background())
	if s.TenantID() != c.TenantID || s.PrincipalID() != c.PrincipalID {
		return errors.New("runtime identity differs from approved pairing")
	}
	// Generation-specific sockets avoid unlinking or adopting stale listeners.
	name := "agentd-" + s.Generation() + ".sock"
	watches, err := pairedAttach(root, c, remote)
	if err != nil {
		return err
	}
	defer func() {
		op, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		watches.Close(op)
	}()
	local, err := agentd.ServeLocal(s, filepath.Join(state, name), watches)
	if err != nil {
		return err
	}
	defer local.Close()
	store, err := agentsetup.OpenStore(state, false)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]string{"socket": name, "daemon_id": c.DaemonID, "generation": s.Generation()})
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
		watches.Sweep(op)
		// Lifecycle reconciliation is required before every fresh dispatch;
		// the independent tombstone proof still works after key revocation.
		err = syncPairing(op, root, c.Origin, s)
		if err == nil {
			next, _, readErr := agentsetup.ReadRuntime(root)
			if readErr == nil {
				// AEON-342: Claude repins and dependency repairs hold only
				// Claude; identity or binding changes still stop the daemon.
				c, readErr = pollPairedRuntime(op, s, root, c, next)
				if readErr != nil {
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
	unchanged := next
	unchanged.NodePath, unchanged.ClaudeSDKPath, unchanged.ClaudeRepinID = old.NodePath, old.ClaudeSDKPath, old.ClaudeRepinID
	if next.ClaudeRepinID == "" || !reflect.DeepEqual(old, unchanged) {
		return errors.New("repin changed more than Claude dependencies")
	}
	for _, adapter := range adapters {
		if claude, ok := adapter.(*agentd.ClaudeAdapter); ok {
			return s.RestartClaude(ctx, claude)
		}
	}
	return errors.New("repin has no approved Claude adapter")
}

type pairedRuntimeSupervisor interface {
	SetHarnessHold(string, string)
	RefreshAccounts([]agentd.EnrolledAccount, []agentd.Adapter) error
	RestartClaude(context.Context, *agentd.ClaudeAdapter) error
	PollOnce(context.Context) error
}

// pollPairedRuntime keeps recovery and other harnesses polling while Claude
// waits for a repin or a dependency repair. Identity changes still fail closed.
func pollPairedRuntime(ctx context.Context, s pairedRuntimeSupervisor, root string, c, next agentsetup.RuntimeConfig) (agentsetup.RuntimeConfig, error) {
	current, err := refreshPairedRuntime(ctx, s, root, c, next)
	if err == nil {
		_ = s.PollOnce(ctx)
	}
	return current, err
}

func refreshPairedRuntime(ctx context.Context, s pairedRuntimeSupervisor, root string, c, next agentsetup.RuntimeConfig) (agentsetup.RuntimeConfig, error) {
	if next.Origin != c.Origin || next.TenantID != c.TenantID || next.PrincipalID != c.PrincipalID || next.DaemonID != c.DaemonID || next.Workspace != c.Workspace || next.ComputerID != c.ComputerID {
		return c, errors.New("pairing configuration identity changed")
	}
	for _, nextAccount := range next.Accounts {
		for _, oldAccount := range c.Accounts {
			if oldAccount.AccountID == nextAccount.AccountID && oldAccount != nextAccount {
				return c, errors.New("approved account binding changed")
			}
		}
	}
	if !reflect.DeepEqual(c, next) {
		ac, ad, err := pairedAdapters(next)
		if err != nil {
			return c, err
		}
		if next.ClaudeRepinID != c.ClaudeRepinID {
			s.SetHarnessHold(agentd.Claude, "Claude repin pending: waiting for active Claude runs to exit")
			if err := agentsetup.ValidateRuntimeDependencies(next); err != nil {
				s.SetHarnessHold(agentd.Claude, err.Error())
				return c, nil
			}
			if err := restartPairedClaude(ctx, s, c, next, ad); err != nil {
				if !errors.Is(err, agentd.ErrDraining) {
					s.SetHarnessHold(agentd.Claude, "Claude repin failed: "+err.Error())
				}
				return c, nil
			}
		} else if err := s.RefreshAccounts(ac, ad); err != nil {
			return c, err
		}
		c = next
	}
	if err := agentsetup.ValidateRuntimeDependencies(c); err != nil {
		s.SetHarnessHold(agentd.Claude, err.Error())
		return c, nil
	}
	// c describes the adapter already installed above or at cold start. A
	// receipt retry must not replace it again or depend on historical runs.
	if err := acknowledgePairedClaude(root, c); err != nil {
		s.SetHarnessHold(agentd.Claude, "Claude repin pending: acknowledgement unavailable; retrying automatically")
		return c, nil
	}
	s.SetHarnessHold(agentd.Claude, "")
	return c, nil
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
