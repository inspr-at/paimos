// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/auth"
	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

const ownedLease = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func ownedDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type probeAdapter struct {
	label string
	keys  map[string]bool
}

func (probeAdapter) Name() string { return agentd.AdapterCodex }
func (probeAdapter) Capabilities() []agentd.Capability {
	return []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt, agentd.CapabilityStop}
}
func (a probeAdapter) AccountLabel(context.Context) string { return a.label }
func (a probeAdapter) HasAccount(key string) bool          { return a.keys[key] }
func (probeAdapter) Start(context.Context, agentd.StartRequest, func(agentd.AdapterEvent)) (agentd.Process, error) {
	return nil, errors.New("the readiness fixture never spawns a child")
}

type ownedDaemon struct {
	t                *testing.T
	project          int64
	service          *lifecycleintents.Service
	harness          *managedharness.Service
	reporter         auth.Principal
	supervisor       *agentd.Supervisor
	runtime          lifecycleintents.Runtime
	host             string
	workspace        string
	identity         string
	kernel           string
	loader           string
	accountKey       string
	profile          dispatchprofile.Profile
	hostKind         string
	generationDigest string
	platformInputs   agentd.ReadinessInputs
}

func newOwnedDaemon(t *testing.T, projectID, userID int64) *ownedDaemon {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hostKind, generationDigest, platformInputs := ownedPlatformFixture(t, root)
	workspace, kernel, loader := ownedWorkspaceFixture(t, root)
	identity := strings.Repeat("d", 64)
	supervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{
		Instance: "ppm-owned", Adapters: []agentd.Adapter{probeAdapter{label: "chatgpt", keys: map[string]bool{"coordinator": true}}},
		WorkspaceInspector: func(_ context.Context, path, mode string) (agentd.WorkspaceProvenance, error) {
			return agentd.WorkspaceProvenance{CanonicalPath: path, Identity: identity, Kind: "directory", Mode: mode}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	profile, err := dispatchprofile.Resolve("codex-sol-high", dispatchprofile.CatalogVersion, "codex")
	if err != nil {
		t.Fatal(err)
	}
	res, err := appdb.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'owned-runtime','not-a-credential','fixture','*')`, userID)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := res.LastInsertId()
	reporter, err := auth.NewAPIKeyPrincipal(keyID, userID, auth.ParseScopes("*"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := &ownedDaemon{
		t: t, project: projectID, service: lifecycleintents.NewService(appdb.DB), harness: managedharness.NewService(appdb.DB),
		reporter: reporter, supervisor: supervisor, host: "owned-fixture-host",
		workspace: workspace, identity: identity, kernel: kernel, loader: loader, accountKey: "coordinator", profile: profile,
		hostKind: hostKind, generationDigest: generationDigest, platformInputs: platformInputs,
	}
	daemon.register()
	return daemon
}

func ownedWorkspaceFixture(t *testing.T, root string) (workspace, kernel, loader string) {
	t.Helper()
	workspace = filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "doctrine", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	kernel = filepath.Join(workspace, "doctrine", "docs", "AGENTS-KERNEL.md")
	if err := os.WriteFile(kernel, []byte("# AGENTS — Kernel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader = filepath.Join(workspace, "CLAUDE.md")
	if err := os.WriteFile(loader, []byte("@./doctrine/docs/AGENTS-KERNEL.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace, kernel, loader
}

func ownedPlatformFixture(t *testing.T, root string) (hostKind, generationDigest string, inputs agentd.ReadinessInputs) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		store := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		current := filepath.Join(root, "home-manager")
		if err := os.Symlink(store, current); err != nil {
			t.Fatal(err)
		}
		return "macos-home-manager",
			ownedDigest([]byte("3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation")),
			agentd.ReadinessInputs{HomeManagerCurrent: current}
	}
	storeNix := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-nixos-system")
	if err := os.MkdirAll(storeNix, 0o755); err != nil {
		t.Fatal(err)
	}
	nixCurrent := filepath.Join(root, "nixos-system")
	if err := os.Symlink(storeNix, nixCurrent); err != nil {
		t.Fatal(err)
	}
	storeHM := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation")
	if err := os.MkdirAll(storeHM, 0o755); err != nil {
		t.Fatal(err)
	}
	hmCurrent := filepath.Join(root, "home-manager")
	if err := os.Symlink(storeHM, hmCurrent); err != nil {
		t.Fatal(err)
	}
	nixosMarker := filepath.Join(root, "etc", "nixos")
	if err := os.MkdirAll(nixosMarker, 0o755); err != nil {
		t.Fatal(err)
	}
	return "nixos-home-manager",
		ownedDigest([]byte("3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-nixos-system")),
		agentd.ReadinessInputs{
			NixOSMarker: nixosMarker, NixOSCurrent: nixCurrent, HomeManagerCurrent: hmCurrent,
		}
}

func (d *ownedDaemon) register() {
	d.t.Helper()
	runtime, err := d.service.RegisterRuntime(context.Background(), d.reporter, d.project, ownedLease, lifecycleintents.Registration{
		Generation:    uuid.NewString(),
		Host:          d.host,
		AccountLabel:  "chatgpt",
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2,
		Accounts:      []lifecycleintents.AccountChoice{{Key: d.accountKey, Label: "Coordinator"}},
		Profiles:      []lifecycleintents.Profile{{ID: d.profile.ID, Version: d.profile.Version}},
		Workspaces:    []lifecycleintents.Workspace{{Handle: uuid.NewString(), Identity: d.identity, Label: "Work"}},
	})
	if err != nil {
		d.t.Fatalf("owned runtime registration: %v", err)
	}
	d.runtime = runtime
}

func (d *ownedDaemon) worker() WorkerSelection {
	return WorkerSelection{
		WorkerName: "codex", RuntimeID: d.runtime.ID, RuntimeGeneration: d.runtime.Generation,
		AccountLabel: d.runtime.AccountLabel, AccountKey: d.accountKey,
		ProfileID: d.profile.ID, ProfileVersion: d.profile.Version,
		WorkspaceHandle: d.runtime.Workspaces[0].Handle,
	}
}

func (d *ownedDaemon) readinessSpec(intent lifecycleintents.Intent, mutate func(*agentd.ReadinessSpec)) agentd.ReadinessSpec {
	d.t.Helper()
	kernel, err := os.ReadFile(d.kernel)
	if err != nil {
		d.t.Fatal(err)
	}
	spec := agentd.ReadinessSpec{
		BaselineDigest: intent.Request.BaselineDigest,
		AccountLabel:   intent.Request.AccountLabel,
		AccountKey:     intent.Request.AccountKey,
		Profile:        d.profile,
		Expect: agentd.ReadinessExpectation{
			HostKind: d.hostKind, GenerationDigest: d.generationDigest,
			DoctrineKernelDigest: ownedDigest(kernel), Tools: []string{"git"},
		},
		Inputs: agentd.ReadinessInputs{
			WorkspaceRoot: d.workspace, WorkspaceIdentity: d.identity, WorkspaceMode: d.profile.WorkspaceMode,
			DoctrineKernel: d.kernel, DoctrineLoader: d.loader, Instance: "ppm-owned",
			HomeManagerCurrent:   d.platformInputs.HomeManagerCurrent,
			HomeManagerInstalled: d.platformInputs.HomeManagerInstalled,
			NixOSMarker:          d.platformInputs.NixOSMarker, NixOSCurrent: d.platformInputs.NixOSCurrent,
			NixOSInstalled: d.platformInputs.NixOSInstalled,
		},
		Doctor: func(context.Context) (agentd.ReadinessDoctorReport, error) {
			return agentd.ReadinessDoctorReport{Instance: "ppm-owned", Ready: true,
				Layers: []agentd.ReadinessDoctorLayer{{Name: "daemon_generation", State: "known", Code: "live_owned_generation"}}}, nil
		},
	}
	if mutate != nil {
		mutate(&spec)
	}
	return spec
}

func (d *ownedDaemon) step(mutate func(*agentd.ReadinessSpec)) *lifecycleintents.Intent {
	d.t.Helper()
	ctx := context.Background()
	intent, err := d.service.Claim(ctx, d.reporter, d.project, d.runtime.ID, ownedLease)
	if err != nil {
		d.t.Fatalf("claim: %v", err)
	}
	if intent == nil {
		return nil
	}
	executing, err := d.service.Transition(ctx, d.reporter, d.project, intent.ID, ownedLease, lifecycleintents.Transition{
		RuntimeID: d.runtime.ID, RuntimeGeneration: d.runtime.Generation, ExpectedRevision: intent.Revision, State: "executing",
	})
	if err != nil {
		d.t.Fatalf("executing transition for %s: %v", intent.Request.Operation, err)
	}
	switch intent.Request.Operation {
	case "readiness":
		observation, err := d.supervisor.ObserveReadiness(ctx, d.readinessSpec(executing, mutate))
		if err != nil {
			d.t.Fatalf("owned readiness probe: %v", err)
		}
		result := lifecycleclient.ReadinessResult(observation)
		done, err := d.service.Transition(ctx, d.reporter, d.project, intent.ID, ownedLease, lifecycleintents.Transition{
			RuntimeID: d.runtime.ID, RuntimeGeneration: d.runtime.Generation, ExpectedRevision: executing.Revision,
			State: "completed", Reason: "applied", Readiness: result.Readiness,
		})
		if err != nil {
			d.t.Fatalf("readiness completion: %v", err)
		}
		return &done
	case "start":
		session := d.spawnSession(executing)
		done, err := d.service.Transition(ctx, d.reporter, d.project, intent.ID, ownedLease, lifecycleintents.Transition{
			RuntimeID: d.runtime.ID, RuntimeGeneration: d.runtime.Generation, ExpectedRevision: executing.Revision,
			State: "completed", Reason: "applied", ResultSessionID: session,
		})
		if err != nil {
			d.t.Fatalf("start completion: %v", err)
		}
		return &done
	}
	d.t.Fatalf("unexpected claimed operation %q", intent.Request.Operation)
	return nil
}

func (d *ownedDaemon) spawnSession(intent lifecycleintents.Intent) string {
	d.t.Helper()
	ctx := context.Background()
	session, _, err := d.harness.Register(ctx, managedharness.RegisterInput{
		ProjectID: d.project, AgentName: intent.Request.AgentName, Harness: d.profile.Harness, Host: d.host,
		SessionRef: uuid.NewString(), WorkerLease: ownedLease, ManagementMode: managedharness.ManagementManaged,
		Role: intent.Request.Role, TicketID: intent.Request.TicketID, WorkShape: intent.Request.WorkShape,
		SteerMode: managedharness.SteerNone,
		Workspace: &models.HarnessWorkspaceProvenance{CanonicalPath: d.workspace, Identity: d.identity,
			Kind: "directory", Mode: d.profile.WorkspaceMode},
		DispatchProfileID: d.profile.ID, DispatchProfileVersion: d.profile.Version,
		AccountLabel: intent.Request.AccountLabel, AccountKey: intent.Request.AccountKey,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		d.t.Fatalf("owned session registration: %v", err)
	}
	if _, err := d.harness.HeartbeatWithActivity(ctx, session.ID, managedharness.PhaseWorking,
		managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
		d.t.Fatalf("owned session heartbeat: %v", err)
	}
	if err := d.service.RegisterSession(ctx, d.reporter, d.project, d.runtime.ID, ownedLease, ownedLease,
		lifecycleintents.SessionRegistration{SessionID: session.ID, Generation: intent.NewGeneration}); err != nil {
		d.t.Fatalf("owned session lifecycle registration: %v", err)
	}
	return session.ID
}
