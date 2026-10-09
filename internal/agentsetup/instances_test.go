// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Risk: a stopped old service or a stale loaded definition must block shared
// mode without changing the installed definition, receipt or loaded service.
func TestSharedLedgerInstalledServicesIncludeStoppedAndLoadedDefinitions(t *testing.T) {
	for _, scenario := range []string{"capable-stopped", "old-stopped", "stale-loaded", "nix"} {
		t.Run(scenario, func(t *testing.T) {
			home := physicalTemp(t)
			dir := filepath.Join(home, "Library", "LaunchAgents")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			manager := ServiceManager{Platform: Platform{OS: "darwin", Arch: "arm64"}, Home: home, UID: 123, Executable: filepath.Join(home, "bin", "agentd"), Instance: "pma"}
			peer := manager
			peer.Instance = ""
			raw, err := peer.Definition(filepath.Join(home, "paired"))
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, "cm.aeon.agentd.plist")
			if scenario == "nix" {
				name = filepath.Join(dir, "at.inspr.aeon-agentd.plist")
			}
			if err = os.WriteFile(name, raw, 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			manager.Executor = executorFunc(func(_ context.Context, c Command) ([]byte, error) {
				calls++
				if c.Path == "/bin/launchctl" {
					if scenario == "stale-loaded" {
						return []byte("program = /old/daemon\n"), nil
					}
					return nil, &CommandError{ExitCode: 113}
				}
				if c.Path != peer.Executable || !strings.Contains(strings.Join(c.Args, " "), "--version") {
					t.Fatal("unexpected mutation command", c.Path, c.Args)
				}
				if scenario == "old-stopped" {
					return []byte("paimos-agentd legacy"), nil
				}
				return []byte("paimos-agentd dev ledger-v1"), nil
			})
			services, err := manager.InstalledLedgerServices(t.Context())
			if scenario == "capable-stopped" {
				if err != nil || len(services) != 1 || services[0].Root != filepath.Join(home, "paired") || calls != 2 {
					t.Fatal("stopped service overlooked", services, err, calls)
				}
			} else if err == nil {
				t.Fatal("unsafe installed service admitted")
			}
			after, e := os.ReadFile(name)
			if e != nil || !bytes.Equal(raw, after) {
				t.Fatal("preflight changed plist", e)
			}
		})
	}
}

// Risk: named pairings may accidentally adopt the default service or collide
// because workspaces differ. Instance naming is local and origin is canonical.
func TestSharedLedgerInstanceRootsLabelsLogsAndCanonicalOrigins(t *testing.T) {
	home := physicalTemp(t)
	root, err := InstanceStateRoot("darwin", home, "", "pma")
	if err != nil || !strings.HasSuffix(root, "/aeon/paired-pma") {
		t.Fatal(root, err)
	}
	manager := ServiceManager{Platform: Platform{OS: "darwin", Arch: "arm64"}, Home: home, Executable: filepath.Join(home, "bin", "agentd"), Instance: "pma"}
	raw, err := manager.Definition(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"cm.aeon.agentd.pma", "/aeon-agentd/pma/stdout.log", "/aeon-agentd/pma/stderr.log"} {
		if !bytes.Contains(raw, []byte(expected)) {
			t.Fatal("missing instance binding", expected)
		}
	}
	first, err := CanonicalLedgerOrigin("https://EXAMPLE.invalid:443/")
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalLedgerOrigin("https://example.invalid")
	if err != nil || first != second {
		t.Fatal("origin canonicalization", first, second, err)
	}
	for _, name := range []string{"../pma", "pma/name", "PMA", "pma\n", ""} {
		if name != "" && ValidateInstance(name) == nil {
			t.Fatal("invalid instance accepted", name)
		}
	}
	if ValidateInstance("") != nil {
		t.Fatal("default instance refused")
	}
	if _, err = InstanceStateRoot("darwin", home, "", "../pma"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatal("bad name failed for wrong reason", err)
	}
}

// Risk: a refused ledger handover can still install a service or write a
// receipt. The handover gate must finish before Engine.Install is invoked.
func TestSharedLedgerProvisioningGatePreservesServiceReceipts(t *testing.T) {
	e, api, _, opts, executor := engineFixture(t)
	opts.StartService = true
	gate := errors.New("fixture peer server has no ledger-v1")
	e.PrepareLedger = func(context.Context, RuntimeConfig) error { return gate }
	if _, err := e.Begin(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	api.approved = true
	now := e.Now()
	e.Now = func() time.Time { return now.Add(time.Minute) }
	if _, err := e.Step(t.Context()); !errors.Is(err, gate) {
		t.Fatal("handover gate bypassed", err)
	}
	dir, name, _ := e.Services.location()
	if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refusal changed installed service", err)
	}
	if _, err := e.Store.Read("service.json", 64<<10); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refusal changed service receipt", err)
	}
	if executor.active {
		t.Fatal("refusal activated service")
	}
}
