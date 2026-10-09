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
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func ledgerConfig(root string, c agentsetup.RuntimeConfig) (agentd.LedgerConfig, error) {
	// A recorded binding survives custom state roots and a removed service.
	store, err := agentsetup.OpenStore(filepath.Join(root, "daemon"), false)
	if err == nil {
		raw, e := store.Read("ledger-member.json", 8192)
		store.Close()
		if e == nil {
			var binding struct {
				Config agentd.LedgerConfig `json:"config"`
			}
			if json.Unmarshal(raw, &binding) != nil || binding.Config.Root != root {
				return agentd.LedgerConfig{}, agentd.ErrScope
			}
			return binding.Config, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return agentd.LedgerConfig{}, e
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return agentd.LedgerConfig{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return agentd.LedgerConfig{}, err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return agentd.LedgerConfig{}, err
	}
	platform, err := agentsetup.CurrentPlatform()
	if err != nil {
		return agentd.LedgerConfig{}, err
	}
	path, err := agentsetup.DefaultLedgerRoot(platform.OS, home, os.Getenv("XDG_STATE_HOME"))
	if err != nil {
		return agentd.LedgerConfig{}, err
	}
	label, err := agentsetup.RecordedServiceLabel(root)
	if err != nil {
		return agentd.LedgerConfig{}, err
	}
	return agentd.LedgerConfig{Path: path, Label: label, Root: root, Origin: c.Origin}, nil
}
func serverLedgerView(ctx context.Context, root string) (agentsetup.RuntimeConfig, agentsetup.View, error) {
	c, key, err := agentsetup.ReadRuntime(root)
	if err != nil {
		return c, agentsetup.View{}, err
	}
	remote := agentd.NewRemote(c.Origin, string(key))
	view, _, err := remote.LedgerView(ctx)
	if err == nil {
		err = agentsetup.RequireLedgerServer(view)
	}
	return c, view, err
}
func checkSharedPairing(ctx context.Context, manager *agentsetup.ServiceManager, root, origin, tenant string) error {
	op, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	services, err := manager.InstalledLedgerServices(op)
	if err != nil {
		return err
	}
	guide, err := (agentsetup.HTTPClient{Origin: origin}).Guide(op)
	if err != nil {
		return err
	}
	if err = agentsetup.RequireLedgerServer(agentsetup.View{ServerCapabilities: guide.ServerCapabilities}); err != nil {
		return err
	}
	canonical, err := agentsetup.CanonicalLedgerOrigin(origin)
	if err != nil {
		return err
	}
	for _, service := range services {
		c, _, err := serverLedgerView(op, service.Root)
		if err != nil {
			return err
		}
		peerOrigin, e := agentsetup.CanonicalLedgerOrigin(c.Origin)
		if e != nil {
			return e
		}
		if service.Root != root && peerOrigin == canonical && tenant != "" && c.TenantID == tenant {
			return errors.New("this origin and tenant already have a pairing")
		}
	}
	// Every running peer proves its own PID by a fenced import. This preserves
	// the existing unrecorded-process refusal while allowing verified peers.
	path, err := agentsetup.DefaultLedgerRoot(manager.Platform.OS, manager.Home, os.Getenv("XDG_STATE_HOME"))
	if err != nil {
		return err
	}
	manager.LedgerPeerPIDs = map[int]bool{}
	importedRoots := map[string]bool{}
	for _, service := range services {
		if service.Root == root {
			continue
		}
		peer, err := agentdwire.OpenClient(filepath.Join(service.Root, "daemon"))
		if err != nil {
			return errors.New("installed member must run its own ledger handover before shared pairing")
		}
		peerConfig, _, err := serverLedgerView(op, service.Root)
		if err != nil {
			return err
		}
		if err = peer.ImportLedger(op, agentd.LedgerConfig{Path: path, Label: service.Label, Root: service.Root, Origin: peerConfig.Origin}); err != nil {
			return err
		}
		importedRoots[service.Root] = true
	}
	if len(services) > 0 {
		ledger, err := agentsetup.OpenSharedLedger(path, false)
		if err != nil {
			return err
		}
		data, members, err := ledger.Snapshot()
		ledger.Close()
		if err != nil {
			return err
		}
		for _, member := range members {
			if importedRoots[member.Root] {
				instance := data.Instances[member.ID]
				if !instance.Imported || !instance.Enrolled || instance.PID < 1 {
					return agentsetup.ErrLedgerUnavailable
				}
				manager.LedgerPeerPIDs[instance.PID] = true
			}
		}
	}
	receipt, err := agentsetup.ReadServiceReceipt(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return manager.Preflight(op, root, receipt)
}
func prepareSharedPairing(ctx context.Context, manager agentsetup.ServiceManager, root string, c agentsetup.RuntimeConfig) error {
	if err := checkSharedPairing(ctx, &manager, root, c.Origin, c.TenantID); err != nil {
		return err
	}
	services, err := manager.InstalledLedgerServices(ctx)
	if err != nil {
		return err
	}
	ledgerPath, err := agentsetup.DefaultLedgerRoot(manager.Platform.OS, manager.Home, os.Getenv("XDG_STATE_HOME"))
	if err != nil {
		return err
	}
	// Each owner imports over its authenticated control socket. A stopped owner
	// is never impersonated by a peer. Reinstall/start that owner's service first.
	for _, service := range services {
		if service.Root == root {
			continue
		}
		peer, err := agentdwire.OpenClient(filepath.Join(service.Root, "daemon"))
		if err != nil {
			return errors.New("installed member must run its own ledger handover before shared pairing")
		}
		peerConfig, _, err := serverLedgerView(ctx, service.Root)
		if err != nil {
			return err
		}
		config := agentd.LedgerConfig{Path: ledgerPath, Label: service.Label, Root: service.Root, Origin: peerConfig.Origin}
		if err = peer.ImportLedger(ctx, config); err != nil {
			return err
		}
	}
	label, err := agentsetup.InstanceLabel(manager.Instance)
	if err != nil {
		return err
	}
	if current, err := agentdwire.OpenClient(filepath.Join(root, "daemon")); err == nil {
		return current.ImportLedger(ctx, agentd.LedgerConfig{Path: ledgerPath, Label: label, Root: root, Origin: c.Origin})
	}
	accounts, adapters, err := pairedAdapters(c)
	if err != nil {
		return err
	}
	_, key, err := agentsetup.ReadRuntime(root)
	if err != nil {
		return err
	}
	remote := agentd.NewRemote(c.Origin, string(key))
	// Provisioning imports this new owner's journal before service installation.
	// The instance lock refuses an already-running owner; it must use its socket.
	owner, err := agentd.NewSupervisor(ctx, agentd.Config{API: remote, StateRoot: filepath.Join(root, "daemon"), DaemonID: c.DaemonID, Workspace: c.Workspace, Accounts: accounts, Adapters: adapters, EstimatedUnits: map[string]int64{"requests": 1}})
	if err != nil {
		return err
	}
	defer owner.Close(context.Background())
	return owner.EnableLedger(ctx, agentd.LedgerConfig{Path: ledgerPath, Label: label, Root: root, Origin: c.Origin})
}
func leaveSharedPairing(ctx context.Context, root string) error {
	state := filepath.Join(root, "daemon")
	store, err := agentsetup.OpenStore(state, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = store.Read("ledger-member.json", 8192)
	store.Close()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	client, err := agentdwire.OpenClient(state)
	if err == nil {
		return client.LeaveLedger(ctx)
	}
	// The independent lifecycle response already confirmed revocation before
	// this hook. Offline owner reconciliation needs no revoked runtime key.
	c, err := agentsetup.ReadRuntimeConfig(root)
	if err != nil {
		return err
	}
	return agentd.OfflineLeaveLedger(state, c.DaemonID, c.TenantID, c.PrincipalID)
}
func ledgerCommand(args []string, out io.Writer) error {
	if len(args) < 1 || (args[0] != "status" && args[0] != "rebuild") {
		return errors.New("usage: aeon-agentd ledger status|rebuild [--ledger-root PATH]")
	}
	f := flag.NewFlagSet("ledger", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	path := ""
	f.StringVar(&path, "ledger-root", "", "private shared ledger directory")
	if f.Parse(args[1:]) != nil || len(f.Args()) != 0 {
		return errors.New("invalid ledger arguments")
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		home, err = filepath.EvalSymlinks(home)
		if err != nil {
			return err
		}
		platform, err := agentsetup.CurrentPlatform()
		if err != nil {
			return err
		}
		path, err = agentsetup.DefaultLedgerRoot(platform.OS, home, os.Getenv("XDG_STATE_HOME"))
		if err != nil {
			return err
		}
	}
	ledger, err := agentsetup.OpenSharedLedger(path, false)
	if err != nil {
		return err
	}
	defer ledger.Close()
	if args[0] == "rebuild" {
		generation, err := ledger.Rebuild()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Ledger rebuilding; every member must import generation "+generation)
		return err
	}
	data, members, err := ledger.Snapshot()
	if err != nil {
		return err
	}
	type memberStatus struct {
		Instance, Label, State string
		Groups                 int
	}
	rows := []memberStatus{}
	for _, member := range members {
		state := "awaiting import"
		v := data.Instances[member.ID]
		if v.Imported && v.Enrolled {
			state = "enrolled"
		}
		// The receipt location is discovery only. It cannot release a tombstone.
		root, e := agentsetup.OpenStoreReadOnly(member.Root)
		if e != nil {
			state = "member without service"
		} else {
			raw, e := root.Read("service.json", 64<<10)
			root.Close()
			var receipt agentsetup.ServiceReceipt
			if e != nil || json.Unmarshal(raw, &receipt) != nil {
				state = "member without service"
			} else if _, e := os.Lstat(receipt.Path); e != nil {
				state = "member without service"
			}
		}
		count := 0
		for _, group := range data.Groups {
			if group.Instance == member.ID {
				count++
			}
		}
		rows = append(rows, memberStatus{Instance: member.ID, Label: member.Label, State: state, Groups: count})
	}
	// No run IDs, account IDs, origins, tenant IDs or credentials are displayed.
	return json.NewEncoder(out).Encode(struct {
		Generation string         `json:"generation"`
		Rebuilding bool           `json:"rebuilding"`
		Members    []memberStatus `json:"members"`
	}{data.Generation, data.Rebuilding, rows})
}
