// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const readinessWorkspaceIdentity = "b1946ac92492d2347c6235b4d2611184b1946ac92492d2347c6235b4d2611184"

// readinessHost builds a real on-disk host fixture: an activated generation
// symlink into a store-shaped directory, a wired doctrine loader and kernel, and
// a workspace. The probe reads all of them for real using the host kind that
// observeHostKind can actually observe on this GOOS.
type readinessHost struct {
	root              string
	workspace         string
	kernel            string
	loader            string
	hostKind          string
	generationDigest  string
	platformInputs    ReadinessInputs
}

func newReadinessHost(t *testing.T) readinessHost {
	t.Helper()
	if runtime.GOOS == "darwin" {
		return newDarwinHomeManagerReadinessHost(t)
	}
	return newLinuxNixOSHomeManagerReadinessHost(t)
}

func newDarwinHomeManagerReadinessHost(t *testing.T) readinessHost {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation")
	if err = os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "home-manager")
	if err = os.Symlink(store, current); err != nil {
		t.Fatal(err)
	}
	workspace, kernel, loader := readinessWorkspaceFixture(t, root)
	return readinessHost{
		root: root, workspace: workspace, kernel: kernel, loader: loader,
		hostKind:         "macos-home-manager",
		generationDigest: digestText("3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation"),
		platformInputs:   ReadinessInputs{HomeManagerCurrent: current},
	}
}

func newLinuxNixOSHomeManagerReadinessHost(t *testing.T) readinessHost {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storeNix := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-nixos-system")
	if err = os.MkdirAll(storeNix, 0o755); err != nil {
		t.Fatal(err)
	}
	nixCurrent := filepath.Join(root, "nixos-system")
	if err = os.Symlink(storeNix, nixCurrent); err != nil {
		t.Fatal(err)
	}
	storeHM := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation")
	if err = os.MkdirAll(storeHM, 0o755); err != nil {
		t.Fatal(err)
	}
	hmCurrent := filepath.Join(root, "home-manager")
	if err = os.Symlink(storeHM, hmCurrent); err != nil {
		t.Fatal(err)
	}
	nixosMarker := filepath.Join(root, "etc", "nixos")
	if err = os.MkdirAll(nixosMarker, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace, kernel, loader := readinessWorkspaceFixture(t, root)
	return readinessHost{
		root: root, workspace: workspace, kernel: kernel, loader: loader,
		hostKind:         "nixos-home-manager",
		generationDigest: digestText("3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-nixos-system"),
		platformInputs: ReadinessInputs{
			NixOSMarker:        nixosMarker,
			NixOSCurrent:       nixCurrent,
			HomeManagerCurrent: hmCurrent,
		},
	}
}

func readinessWorkspaceFixture(t *testing.T, root string) (workspace, kernel, loader string) {
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

func mismatchedReadinessHostKind() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return "macos-home-manager"
}

func (h readinessHost) spec(t *testing.T) ReadinessSpec {
	t.Helper()
	profile, err := dispatchprofile.Resolve("codex-sol-high", dispatchprofile.CatalogVersion, AdapterCodex)
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := os.ReadFile(h.kernel)
	if err != nil {
		t.Fatal(err)
	}
	return ReadinessSpec{
		BaselineDigest: "sha256:" + strings.Repeat("a", 64),
		AccountLabel:   "chatgpt",
		AccountKey:     "coordinator",
		Profile:        profile,
		Expect: ReadinessExpectation{
			HostKind:             h.hostKind,
			GenerationDigest:     h.generationDigest,
			DoctrineKernelDigest: digestBytes(kernel),
			Tools:                []string{"git"},
		},
		Inputs: ReadinessInputs{
			WorkspaceRoot:        h.workspace,
			WorkspaceIdentity:    readinessWorkspaceIdentity,
			WorkspaceMode:        WorkspaceExclusive,
			DoctrineKernel:       h.kernel,
			DoctrineLoader:       h.loader,
			HomeManagerCurrent:   h.platformInputs.HomeManagerCurrent,
			HomeManagerInstalled: h.platformInputs.HomeManagerInstalled,
			NixOSMarker:          h.platformInputs.NixOSMarker,
			NixOSCurrent:         h.platformInputs.NixOSCurrent,
			NixOSInstalled:       h.platformInputs.NixOSInstalled,
			Instance:             "ppm-readiness",
		},
		Doctor: func(context.Context) (ReadinessDoctorReport, error) {
			return ReadinessDoctorReport{Instance: "ppm-readiness", Ready: true,
				Layers: []ReadinessDoctorLayer{{Name: "daemon_generation", State: "known", Code: "live_owned_generation"}}}, nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) },
	}
}

func newReadinessSupervisor(t *testing.T, identity string) *Supervisor {
	t.Helper()
	adapter := &keyedDispatchAdapter{dispatchAdapter: dispatchAdapter{label: "chatgpt"}, keys: map[string]bool{"coordinator": true}}
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-readiness", Adapters: []Adapter{adapter},
		WorkspaceInspector: func(_ context.Context, path, mode string) (WorkspaceProvenance, error) {
			return WorkspaceProvenance{CanonicalPath: path, Identity: identity, Kind: WorkspaceDirectory, Mode: mode}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	return supervisor
}

func checkByID(t *testing.T, observation ReadinessObservation, id string) ReadinessCheckResult {
	t.Helper()
	for _, check := range observation.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("check %s missing from %+v", id, observation.Checks)
	return ReadinessCheckResult{}
}

func TestObserveReadinessProvesRealHostState(t *testing.T) {
	host := newReadinessHost(t)
	supervisor := newReadinessSupervisor(t, readinessWorkspaceIdentity)
	observation, err := supervisor.ObserveReadiness(context.Background(), host.spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != "ready" {
		t.Fatalf("status=%s next=%s checks=%+v", observation.Status, observation.NextAction, observation.Checks)
	}
	if observation.ContractVersion != ReadinessContractVersion || observation.TTLSeconds != ReadinessTTLSeconds {
		t.Fatalf("contract=%s ttl=%d", observation.ContractVersion, observation.TTLSeconds)
	}
	if !observation.ObservedAt.Equal(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("observed_at=%s is not the daemon's own observation time", observation.ObservedAt)
	}
	if observation.WorkspaceIdentity != readinessWorkspaceIdentity || observation.AccountKey != "coordinator" {
		t.Fatalf("observation is not bound to the workspace and named account: %+v", observation)
	}
	for _, required := range []string{"host_kind", "activated_generation", "doctrine_loader", "workspace_isolation",
		"tool_prerequisites", "paimos_runtime_doctor", "paimos_account"} {
		if got := checkByID(t, observation, required); got.Status != "pass" {
			t.Fatalf("required check %s = %+v", required, got)
		}
	}
	// No path, executable or environment value may leave the host.
	for _, check := range observation.Checks {
		if strings.Contains(check.Reason, "/") || strings.Contains(check.Reason, host.root) {
			t.Fatalf("check reason leaks host detail: %+v", check)
		}
	}
}

func TestObserveReadinessBlocksOnRealHostDefects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ReadinessSpec)
		check  string
		status string
		reason string
	}{
		{"inactive generation", func(spec *ReadinessSpec) {
			spec.Expect.GenerationDigest = digestText("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-other-generation")
		}, "activated_generation", "fail", "generation_digest_mismatch"},
		{"unwired doctrine loader", func(spec *ReadinessSpec) {
			if err := os.WriteFile(spec.Inputs.DoctrineLoader, []byte("# no kernel reference\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "doctrine_loader", "fail", "doctrine_loader_unwired"},
		{"legacy autoload", func(spec *ReadinessSpec) {
			if err := os.WriteFile(spec.Inputs.DoctrineLoader,
				[]byte("@./doctrine/docs/AGENTS-KERNEL.md\n@./doctrine/docs/AGENTS-CORE.md\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "doctrine_loader", "fail", "doctrine_loader_legacy"},
		{"changed kernel", func(spec *ReadinessSpec) {
			if err := os.WriteFile(spec.Inputs.DoctrineKernel, []byte("# tampered\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "doctrine_loader", "fail", "doctrine_hash_mismatch"},
		{"undeclared tool", func(spec *ReadinessSpec) {
			spec.Expect.Tools = []string{"paimos-tool-that-is-not-installed"}
		}, "tool_prerequisites", "fail", "declared_tool_missing"},
		{"runtime doctor unavailable", func(spec *ReadinessSpec) { spec.Doctor = nil },
			"paimos_runtime_doctor", "unsupported", "missing_integration_paimos_runtime_doctor"},
		{"runtime doctor layer unready", func(spec *ReadinessSpec) {
			spec.Doctor = func(context.Context) (ReadinessDoctorReport, error) {
				return ReadinessDoctorReport{Instance: "ppm-readiness", Ready: false,
					Layers: []ReadinessDoctorLayer{{Name: "journal", State: "action_required", Code: "journal_corrupt_or_unsafe"}}}, nil
			}
		}, "paimos_runtime_doctor", "fail", "layer_journal"},
		{"account label mismatch", func(spec *ReadinessSpec) { spec.AccountLabel = "claude_ai_max" },
			"paimos_account", "fail", "account_label_mismatch"},
		{"named account unavailable", func(spec *ReadinessSpec) { spec.AccountKey = "not-registered" },
			"paimos_account", "fail", "named_account_unavailable"},
		{"host kind mismatch", func(spec *ReadinessSpec) { spec.Expect.HostKind = mismatchedReadinessHostKind() },
			"host_kind", "fail", "host_kind_mismatch"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			host := newReadinessHost(t)
			supervisor := newReadinessSupervisor(t, readinessWorkspaceIdentity)
			spec := host.spec(t)
			testCase.mutate(&spec)
			observation, err := supervisor.ObserveReadiness(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Status == "ready" {
				t.Fatalf("defective host reported ready: %+v", observation.Checks)
			}
			got := checkByID(t, observation, testCase.check)
			if got.Status != testCase.status || got.Reason != testCase.reason {
				t.Fatalf("check=%+v want status=%s reason=%s", got, testCase.status, testCase.reason)
			}
			if observation.NextAction == "none" {
				t.Fatal("blocked observation carries no next action")
			}
		})
	}
}

func TestObserveReadinessRefusesForeignWorkspaceIdentity(t *testing.T) {
	host := newReadinessHost(t)
	supervisor := newReadinessSupervisor(t, strings.Repeat("c", 64))
	observation, err := supervisor.ObserveReadiness(context.Background(), host.spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := checkByID(t, observation, "workspace_isolation"); got.Status != "fail" || got.Reason != "workspace_identity_mismatch" {
		t.Fatalf("workspace check=%+v", got)
	}
	if observation.Status == "ready" {
		t.Fatal("observation ready with a foreign workspace")
	}
}

func TestObserveReadinessRequiresBoundWorkspaceAndBaseline(t *testing.T) {
	host := newReadinessHost(t)
	supervisor := newReadinessSupervisor(t, readinessWorkspaceIdentity)
	spec := host.spec(t)
	spec.BaselineDigest = ""
	if _, err := supervisor.ObserveReadiness(context.Background(), spec); err == nil {
		t.Fatal("unbound observation produced")
	}
}

type piReadinessAdapter struct {
	keyedDispatchAdapter
}

func (*piReadinessAdapter) Name() string { return AdapterPi }

func (*piReadinessAdapter) AccountLabel(context.Context) string { return "unknown" }

func TestObserveReadinessPiNamedContextIsNotVerifiedIdentity(t *testing.T) {
	host := newReadinessHost(t)
	adapter := &piReadinessAdapter{keyedDispatchAdapter: keyedDispatchAdapter{
		dispatchAdapter: dispatchAdapter{label: "unknown"},
		keys:            map[string]bool{"operator-pi": true},
	}}
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-readiness", Adapters: []Adapter{adapter},
		WorkspaceInspector: func(_ context.Context, path, mode string) (WorkspaceProvenance, error) {
			return WorkspaceProvenance{CanonicalPath: path, Identity: readinessWorkspaceIdentity, Kind: WorkspaceDirectory, Mode: mode}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	spec := host.spec(t)
	profile, err := dispatchprofile.Resolve("pi-anthropic-sonnet-high", dispatchprofile.CatalogVersion, AdapterPi)
	if err != nil {
		t.Fatal(err)
	}
	spec.Profile = profile
	spec.AccountLabel = AccountPiContext
	spec.AccountKey = "operator-pi"
	observation, err := supervisor.ObserveReadiness(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	got := checkByID(t, observation, "paimos_account")
	if got.Status != "pass" || got.Reason != "named_context_selected" {
		t.Fatalf("pi account check=%+v", got)
	}
	if got.Reason == "account_verified" {
		t.Fatal("selected Pi context was mislabeled as verified provider identity")
	}
}
