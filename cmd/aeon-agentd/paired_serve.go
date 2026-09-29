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
	if err := agentsetup.ValidateRuntimeDependencies(c); err != nil {
		return nil, nil, err
	}
	codexHomes, emails, claudeHomes, cursorIDs := map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
	claudeEmails := map[string]string{}
	grokBindings := map[string]agentd.GrokBinding{}
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
		case agentd.Grok:
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
		a.SetExpectedEmails(claudeEmails)
		adapters = append(adapters, a)
	}
	if p := paths[agentd.Cursor]; p != "" {
		adapters = append(adapters, agentd.NewCursorAdapter(p, cursorIDs))
	}
	if len(grokBindings) > 0 {
		adapters = append(adapters, agentd.NewGrokAdapter(grokBindings))
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
		if stopping {
			stopCapture()
			<-captureDone
			op, cancel := context.WithTimeout(context.Background(), time.Second)
			err = s.Close(op)
			cancel()
			if err == nil {
				return nil
			}
		}
		op, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		watches.Sweep(op)
		if !stopping {
			// Lifecycle reconciliation is required before every fresh dispatch;
			// the independent tombstone proof still works after key revocation.
			err = syncPairing(op, root, c.Origin, s)
			if err == nil {
				next, _, readErr := agentsetup.ReadRuntime(root)
				if readErr == nil {
					readErr = agentsetup.ValidateRuntimeDependencies(next)
				}
				if readErr == nil && !reflect.DeepEqual(c, next) {
					if next.Origin != c.Origin || next.TenantID != c.TenantID || next.PrincipalID != c.PrincipalID || next.DaemonID != c.DaemonID || next.Workspace != c.Workspace || next.ComputerID != c.ComputerID {
						readErr = errors.New("pairing configuration identity changed")
					} else {
						for _, nextAccount := range next.Accounts {
							for _, oldAccount := range c.Accounts {
								if oldAccount.AccountID == nextAccount.AccountID && oldAccount != nextAccount {
									readErr = errors.New("approved account binding changed")
								}
							}
						}
						if readErr != nil {
							stopping = true
							cancel()
							continue
						}
						var ac []agentd.EnrolledAccount
						var ad []agentd.Adapter
						ac, ad, readErr = pairedAdapters(next)
						if readErr == nil {
							readErr = s.RefreshAccounts(ac, ad)
							if readErr == nil {
								c = next
							}
						}
					}
				}
				if readErr == nil {
					_ = s.PollOnce(op)
				}
			}
		} else {
			_ = s.PollOnce(op)
		}
		cancel()
		if stopping {
			<-ticker.C
			continue
		}
		select {
		case <-ctx.Done():
			stopping = true
		case <-ticker.C:
		}
	}
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
