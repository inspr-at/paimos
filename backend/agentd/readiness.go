// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

// ReadinessContractVersion is the evidence contract this probe emits. The
// server refuses any other version.
const ReadinessContractVersion = "inspr.readiness.v1"

// ReadinessTTLSeconds is the contract's observation lifetime. The authority
// clamps it further to the runtime registration it was observed under.
const ReadinessTTLSeconds = 300

const maxReadinessFileBytes = 256 << 10

var storeGeneration = regexp.MustCompile(`^[0-9a-z]{32}-[A-Za-z0-9+._?=-]+$`)
var legacyAutoload = regexp.MustCompile(`@\./(?:[A-Za-z0-9._-]+/)*AGENTS-(?:CORE|PROFILE-[A-Z0-9][A-Z0-9_-]*)\.md`)

// ReadinessExpectation is operator-local declared state: what this host is
// supposed to be running. It comes from the daemon's own protected
// configuration, never from a browser request or an imported handover.
type ReadinessExpectation struct {
	HostKind             string   `json:"host_kind"`
	GenerationDigest     string   `json:"generation_digest"`
	DoctrineKernelDigest string   `json:"doctrine_kernel_digest"`
	DoctrineLoaderRef    string   `json:"doctrine_loader_ref"`
	Tools                []string `json:"tools"`
}

// ReadinessInputs are the exact paths this daemon owns. They are never
// reported: only closed check codes and digests leave the host.
type ReadinessInputs struct {
	WorkspaceRoot        string
	WorkspaceIdentity    string
	WorkspaceMode        string
	DoctrineKernel       string
	DoctrineLoader       string
	HomeManagerCurrent   string
	HomeManagerInstalled string
	NixOSMarker          string
	NixOSCurrent         string
	NixOSInstalled       string
	Instance             string
}

// ReadinessDoctorLayer mirrors one runtimehealth layer without importing that
// package, which depends on this one.
type ReadinessDoctorLayer struct {
	Name  string
	State string
	Code  string
}

type ReadinessDoctorReport struct {
	Instance string
	Ready    bool
	Layers   []ReadinessDoctorLayer
}

// ReadinessDoctor is the existing local runtime diagnosis, injected so the
// daemon can pass its own in-process runtimehealth report and a test can pass a
// deterministic one. A nil doctor is reported as an unsupported integration —
// never as a pass.
type ReadinessDoctor func(context.Context) (ReadinessDoctorReport, error)

type ReadinessSpec struct {
	BaselineDigest string
	AccountLabel   string
	AccountKey     string
	Expect         ReadinessExpectation
	Inputs         ReadinessInputs
	Profile        dispatchprofile.Profile
	Doctor         ReadinessDoctor
	Now            func() time.Time
}

type ReadinessCheckResult struct {
	ID     string
	Status string
	Reason string
	Digest string
}

type ReadinessObservation struct {
	ContractVersion   string
	Status            string
	ObservedAt        time.Time
	TTLSeconds        int
	HostKind          string
	NextAction        string
	WorkspaceIdentity string
	AccountKey        string
	BaselineDigest    string
	Checks            []ReadinessCheckResult
}

var readinessNextAction = map[string]string{
	"host_kind":             "adopt_nix_home_manager",
	"activated_generation":  "activate_home_manager",
	"doctrine_loader":       "repair_doctrine_loader",
	"workspace_isolation":   "select_declared_workspace",
	"tool_prerequisites":    "install_declared_tools",
	"paimos_runtime_doctor": "run_paimos_runtime_setup",
	"paimos_account":        "verify_named_account",
}

// ObserveReadiness runs the required host checks with this daemon's own
// context and returns one bounded observation. Every check is a real local
// probe: no cached file, no operator assertion and no client-supplied value can
// turn an unobserved host into a ready one.
func (s *Supervisor) ObserveReadiness(ctx context.Context, spec ReadinessSpec) (ReadinessObservation, error) {
	if spec.Now == nil {
		spec.Now = time.Now
	}
	if spec.Inputs.WorkspaceIdentity == "" || spec.BaselineDigest == "" {
		return ReadinessObservation{}, errors.New("readiness observation requires a bound workspace and baseline")
	}
	observed := observeHostKind(spec.Inputs)
	out := ReadinessObservation{
		ContractVersion:   ReadinessContractVersion,
		ObservedAt:        spec.Now().UTC(),
		TTLSeconds:        ReadinessTTLSeconds,
		HostKind:          spec.Expect.HostKind,
		WorkspaceIdentity: spec.Inputs.WorkspaceIdentity,
		AccountKey:        spec.AccountKey,
		BaselineDigest:    spec.BaselineDigest,
		NextAction:        "none",
	}
	out.Checks = []ReadinessCheckResult{
		checkHostKind(spec, observed),
		checkActivatedGeneration(spec),
		checkDoctrineLoader(spec),
		s.checkWorkspaceIsolation(ctx, spec),
		checkToolPrerequisites(spec),
		checkRuntimeDoctor(ctx, spec),
		s.checkAccount(ctx, spec),
		checkDispatchProfile(spec),
	}
	out.Status = "ready"
	for _, check := range out.Checks {
		if check.ID == "dispatch_profile" {
			continue
		}
		if check.Status == "pass" {
			continue
		}
		if out.Status == "ready" {
			out.NextAction = readinessNextAction[check.ID]
			out.Status = "needs_setup"
		}
		if check.Status != "fail" {
			out.Status = "unavailable"
		}
	}
	if out.NextAction == "" {
		out.NextAction = "none"
	}
	return out, nil
}

func checkHostKind(spec ReadinessSpec, observed string) ReadinessCheckResult {
	switch {
	case observed == "unsupported":
		return ReadinessCheckResult{"host_kind", "unsupported", "non_nix_onboarding_unavailable", ""}
	case spec.Expect.HostKind == "nixos-home-manager" && runtime.GOOS == "darwin":
		return ReadinessCheckResult{"host_kind", "unsupported", "nixos_unavailable_on_macos", ""}
	case observed != spec.Expect.HostKind:
		return ReadinessCheckResult{"host_kind", "fail", "host_kind_mismatch", ""}
	}
	return ReadinessCheckResult{"host_kind", "pass", "host_kind_supported", digestText(observed)}
}

// observeHostKind reads the actual host: a NixOS marker or a resolvable Home
// Manager profile. An unmanaged host is "unsupported", not "assumed fine".
func observeHostKind(inputs ReadinessInputs) string {
	managed := resolveGeneration(inputs.HomeManagerCurrent) != "" || resolveGeneration(inputs.HomeManagerInstalled) != ""
	if inputs.NixOSMarker != "" && exists(inputs.NixOSMarker) {
		if managed {
			return "nixos-home-manager"
		}
		return "nixos"
	}
	if runtime.GOOS == "darwin" {
		if managed {
			return "macos-home-manager"
		}
		return "macos"
	}
	return "unsupported"
}

func checkActivatedGeneration(spec ReadinessSpec) ReadinessCheckResult {
	activated, installed := spec.Inputs.HomeManagerCurrent, spec.Inputs.HomeManagerInstalled
	inactive, missing := "generation_not_activated", "home_manager_generation_missing"
	if spec.Expect.HostKind == "nixos-home-manager" {
		if runtime.GOOS == "darwin" {
			return ReadinessCheckResult{"activated_generation", "unsupported", "nixos_unavailable_on_macos", ""}
		}
		activated, installed = spec.Inputs.NixOSCurrent, spec.Inputs.NixOSInstalled
		inactive, missing = "nixos_generation_not_activated", "nixos_generation_missing"
	}
	current := resolveGeneration(activated)
	if current == "" {
		if resolveGeneration(installed) != "" {
			return ReadinessCheckResult{"activated_generation", "fail", inactive, ""}
		}
		return ReadinessCheckResult{"activated_generation", "fail", missing, ""}
	}
	if spec.Expect.GenerationDigest == "" {
		return ReadinessCheckResult{"activated_generation", "unknown", "missing_generation_digest", ""}
	}
	if current != spec.Expect.GenerationDigest {
		return ReadinessCheckResult{"activated_generation", "fail", "generation_digest_mismatch", current}
	}
	if other := resolveGeneration(installed); other != "" && other != current {
		return ReadinessCheckResult{"activated_generation", "fail", inactive, current}
	}
	return ReadinessCheckResult{"activated_generation", "pass", "generation_activated", current}
}

// resolveGeneration turns a profile symlink into the digest of its store
// generation name. The store path itself never leaves this function.
func resolveGeneration(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	base := filepath.Base(strings.TrimRight(resolved, "/"))
	if !storeGeneration.MatchString(base) {
		return ""
	}
	return digestText(base)
}

func checkDoctrineLoader(spec ReadinessSpec) ReadinessCheckResult {
	loader, err := readBounded(spec.Inputs.DoctrineLoader)
	if err != nil {
		return ReadinessCheckResult{"doctrine_loader", "fail", "doctrine_unreadable", ""}
	}
	ref := spec.Expect.DoctrineLoaderRef
	if ref == "" {
		ref = "@./doctrine/docs/AGENTS-KERNEL.md"
	}
	if !strings.Contains(string(loader), ref) {
		return ReadinessCheckResult{"doctrine_loader", "fail", "doctrine_loader_unwired", ""}
	}
	if legacyAutoload.Match(loader) {
		return ReadinessCheckResult{"doctrine_loader", "fail", "doctrine_loader_legacy", ""}
	}
	kernel, err := readBounded(spec.Inputs.DoctrineKernel)
	if err != nil {
		return ReadinessCheckResult{"doctrine_loader", "fail", "doctrine_unreadable", ""}
	}
	observed := digestBytes(kernel)
	if spec.Expect.DoctrineKernelDigest == "" {
		return ReadinessCheckResult{"doctrine_loader", "unknown", "missing_doctrine_digest", observed}
	}
	if observed != spec.Expect.DoctrineKernelDigest {
		return ReadinessCheckResult{"doctrine_loader", "fail", "doctrine_hash_mismatch", observed}
	}
	return ReadinessCheckResult{"doctrine_loader", "pass", "doctrine_loader_verified", observed}
}

// checkWorkspaceIsolation reuses the same fixed-argv git provenance probe that
// guards an actual start, and compares it to the identity the server holds.
func (s *Supervisor) checkWorkspaceIsolation(ctx context.Context, spec ReadinessSpec) ReadinessCheckResult {
	mode := spec.Inputs.WorkspaceMode
	if mode == "" {
		mode = WorkspaceExclusive
	}
	provenance, err := s.InspectWorkspace(ctx, spec.Inputs.WorkspaceRoot, mode)
	if err != nil {
		return ReadinessCheckResult{"workspace_isolation", "fail", "workspace_missing", ""}
	}
	if provenance.Identity != spec.Inputs.WorkspaceIdentity {
		return ReadinessCheckResult{"workspace_isolation", "fail", "workspace_identity_mismatch", ""}
	}
	if provenance.Mode != mode {
		return ReadinessCheckResult{"workspace_isolation", "fail", "workspace_mode_mismatch", ""}
	}
	return ReadinessCheckResult{"workspace_isolation", "pass", "workspace_verified", digestText(provenance.Identity)}
}

// checkToolPrerequisites resolves each declared tool to a canonical executable
// outside the workspace. Only the declared names are digested; the resolved
// paths are never reported.
func checkToolPrerequisites(spec ReadinessSpec) ReadinessCheckResult {
	if len(spec.Expect.Tools) == 0 {
		return ReadinessCheckResult{"tool_prerequisites", "unknown", "no_declared_tools", ""}
	}
	names := append([]string(nil), spec.Expect.Tools...)
	sort.Strings(names)
	workspace := spec.Inputs.WorkspaceRoot
	for _, name := range names {
		path, err := resolvePinnedExecutable("", name, name)
		if err != nil {
			return ReadinessCheckResult{"tool_prerequisites", "fail", "declared_tool_missing", ""}
		}
		if workspace != "" && strings.HasPrefix(path, strings.TrimRight(workspace, "/")+"/") {
			return ReadinessCheckResult{"tool_prerequisites", "fail", "workspace_executable_refused", ""}
		}
	}
	return ReadinessCheckResult{"tool_prerequisites", "pass", "tools_verified", digestText(strings.Join(names, "\x00"))}
}

func checkRuntimeDoctor(ctx context.Context, spec ReadinessSpec) ReadinessCheckResult {
	if spec.Doctor == nil {
		return ReadinessCheckResult{"paimos_runtime_doctor", "unsupported", "missing_integration_paimos_runtime_doctor", ""}
	}
	report, err := spec.Doctor(ctx)
	if err != nil {
		return ReadinessCheckResult{"paimos_runtime_doctor", "unknown", "probe_unavailable", ""}
	}
	if spec.Inputs.Instance == "" || report.Instance != spec.Inputs.Instance {
		return ReadinessCheckResult{"paimos_runtime_doctor", "fail", "invalid_runtime_identity", ""}
	}
	for _, layer := range report.Layers {
		if layer.State == "known" || layer.State == "repaired" {
			continue
		}
		reason := "layer_" + layer.Name
		if !closedReadinessReason(reason) {
			reason = "runtime_layer_unready"
		}
		return ReadinessCheckResult{"paimos_runtime_doctor", "fail", reason, ""}
	}
	if !report.Ready {
		return ReadinessCheckResult{"paimos_runtime_doctor", "fail", "runtime_not_ready", ""}
	}
	return ReadinessCheckResult{"paimos_runtime_doctor", "pass", "runtime_doctor_ready", ""}
}

// checkAccount uses the existing fixed-argv account probe and the named-account
// resolver of the adapter that would actually run the work.
func (s *Supervisor) checkAccount(ctx context.Context, spec ReadinessSpec) ReadinessCheckResult {
	harness := spec.Profile.Harness
	if harness == "" {
		return ReadinessCheckResult{"paimos_account", "unknown", "harness_unknown", ""}
	}
	if harness == AdapterPi || harness == AdapterCursor {
		expected := AccountPiContext
		if harness == AdapterCursor {
			expected = AccountCursorContext
		}
		if spec.AccountLabel != expected {
			return ReadinessCheckResult{"paimos_account", "fail", "account_label_mismatch", ""}
		}
		if spec.AccountKey == "" || !s.HasAccount(harness, spec.AccountKey) {
			return ReadinessCheckResult{"paimos_account", "fail", "named_account_unavailable", ""}
		}
		return ReadinessCheckResult{"paimos_account", "pass", "named_context_selected", digestText(expected)}
	}
	label := s.ProbeAccount(ctx, harness)
	if label == "unknown" {
		return ReadinessCheckResult{"paimos_account", "unknown", "account_probe_unavailable", ""}
	}
	if label != spec.AccountLabel {
		return ReadinessCheckResult{"paimos_account", "fail", "account_label_mismatch", ""}
	}
	if spec.AccountKey != "" && !s.HasAccount(harness, spec.AccountKey) {
		return ReadinessCheckResult{"paimos_account", "fail", "named_account_unavailable", ""}
	}
	return ReadinessCheckResult{"paimos_account", "pass", "account_verified", digestText(label)}
}

func checkDispatchProfile(spec ReadinessSpec) ReadinessCheckResult {
	if err := dispatchprofile.Validate(spec.Profile); err != nil {
		return ReadinessCheckResult{"dispatch_profile", "warn", "profile_unresolved", ""}
	}
	if spec.Inputs.WorkspaceMode != "" && spec.Profile.WorkspaceMode != spec.Inputs.WorkspaceMode {
		return ReadinessCheckResult{"dispatch_profile", "warn", "profile_workspace_mode_mismatch", ""}
	}
	return ReadinessCheckResult{"dispatch_profile", "pass", "profile_resolved",
		digestText(spec.Profile.ID + "\x00" + spec.Profile.Version + "\x00" + spec.Profile.Model + "\x00" + spec.Profile.Effort)}
}

func closedReadinessReason(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for i, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func exists(path string) bool {
	_, err := os.Stat(path) // #nosec G703 -- operator-configured marker path from the daemon's own protected configuration.
	return err == nil
}

func readBounded(path string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("readiness input path is not pinned")
	}
	file, err := os.Open(path) // #nosec G304 -- clean absolute path from the daemon's own protected configuration.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxReadinessFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxReadinessFileBytes {
		return nil, errors.New("readiness input exceeds the bounded read")
	}
	return raw, nil
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestText(value string) string { return digestBytes([]byte(value)) }
