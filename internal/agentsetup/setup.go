// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsecurity"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/grokprobe"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/piprobe"
)

const snapshotName = "pairing.json"
const RuntimeName = "runtime.json"

type LocalCandidate struct {
	Candidate Candidate `json:"candidate"`
	Path      string    `json:"path"`
	Home      string    `json:"home,omitempty"`
	Identity  string    `json:"identity"`
	Version   string    `json:"version"`
}

func (c LocalCandidate) MarshalJSON() ([]byte, error) {
	type plain LocalCandidate
	return json.Marshal(struct {
		plain
		Grok   grokprobe.Binding  `json:"grok,omitempty"`
		PiNode piprobe.Node       `json:"pi_node,omitempty"`
		Node   harnesslaunch.Node `json:"node,omitempty"`
	}{plain(c), c.Candidate.Grok, c.Candidate.PiNode, c.Candidate.Node})
}
func (c *LocalCandidate) UnmarshalJSON(raw []byte) error {
	type plain LocalCandidate
	var v struct {
		plain
		Grok   grokprobe.Binding  `json:"grok"`
		PiNode piprobe.Node       `json:"pi_node"`
		Node   harnesslaunch.Node `json:"node"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	*c = LocalCandidate(v.plain)
	c.Candidate.Grok = v.Grok
	c.Candidate.PiNode = v.PiNode
	c.Candidate.Node = v.Node
	return nil
}

type RuntimeAccount struct {
	Harness   string             `json:"harness"`
	Key       string             `json:"key"`
	AccountID string             `json:"account_id"`
	Home      string             `json:"home,omitempty"`
	Identity  string             `json:"identity,omitempty"`
	Path      string             `json:"path"`
	Grok      grokprobe.Binding  `json:"grok,omitempty"`
	PiNode    piprobe.Node       `json:"pi_node,omitempty"`
	Node      harnesslaunch.Node `json:"node,omitempty"`
}
type RuntimeConfig struct {
	LocalAuthKeyID   string                    `json:"local_auth_key_id,omitempty"`
	AttachIdentities map[string]AttachIdentity `json:"attach_identities,omitempty"`
	Schema           string                    `json:"schema"`
	Origin           string                    `json:"origin"`
	TenantID         string                    `json:"tenant_id"`
	PrincipalID      string                    `json:"principal_id"`
	DaemonID         string                    `json:"daemon_id"`
	ComputerID       string                    `json:"computer_id"`
	Workspace        string                    `json:"workspace"`
	Accounts         []RuntimeAccount          `json:"accounts"`
	NodePath         string                    `json:"node_path,omitempty"`
	ClaudeSDKPath    string                    `json:"claude_sdk_path,omitempty"`
	ClaudeRepinID    string                    `json:"claude_repin_id,omitempty"`
}

type snapshot struct {
	LocalAuthKeyID     string                `json:"local_auth_key_id,omitempty"`
	BoundComputer      string                `json:"bound_computer_id,omitempty"`
	BoundDaemon        string                `json:"bound_daemon_id,omitempty"`
	BoundPrincipal     string                `json:"bound_principal_id,omitempty"`
	Schema             string                `json:"schema"`
	Origin             string                `json:"origin"`
	Request            DeviceRequest         `json:"request"`
	Device             secret                `json:"device_secret"`
	Runtime            secret                `json:"runtime_secret"`
	Lifecycle          secret                `json:"lifecycle_secret"`
	LifecycleRequestID string                `json:"lifecycle_request_id"`
	Response           DeviceResponse        `json:"response"`
	View               View                  `json:"view"`
	Candidates         []LocalCandidate      `json:"candidates"`
	StartService       bool                  `json:"start_service"`
	Service            *ServiceReceipt       `json:"service,omitempty"`
	NodePath           string                `json:"node_path,omitempty"`
	ClaudeSDKPath      string                `json:"claude_sdk_path,omitempty"`
	ClaudeRepinID      string                `json:"claude_repin_id,omitempty"`
	ClaudePinInfo      *ClaudeDependencyInfo `json:"claude_pin_info,omitempty"`
	Phase              string                `json:"phase"`
	DisconnectAll      bool                  `json:"disconnect_all"`
	Removed            map[string]bool       `json:"removed"`
	Cleaned            []string              `json:"cleaned"`
	ComputerCleaned    bool                  `json:"computer_cleaned"`
	NextPoll           time.Time             `json:"next_poll"`
}

// Only Progress is printable. The snapshot and HTTP request bodies contain
// private capabilities and must never be returned as status or diagnostics.
type Progress struct {
	BlockedAccounts   []BlockedAccount         `json:"blocked_accounts,omitempty"`
	HarnessDetails    map[string]HarnessDetail `json:"harness_details,omitempty"`
	HarnessStatuses   map[string]string        `json:"harness_statuses,omitempty"`
	VersionStatus     string                   `json:"version_status,omitempty"`
	AccountingState   string                   `json:"accounting_state,omitempty"`
	Schema            string                   `json:"schema"`
	Stage             string                   `json:"stage"`
	RequestID         string                   `json:"request_id,omitempty"`
	ComputerID        string                   `json:"computer_id,omitempty"`
	UserCode          string                   `json:"user_code,omitempty"`
	VerificationURI   string                   `json:"verification_uri,omitempty"`
	Accounts          []Enrollment             `json:"accounts,omitempty"`
	LocalProcesses    string                   `json:"local_processes"`
	ServerRevocation  string                   `json:"server_revocation,omitempty"`
	Action            string                   `json:"action,omitempty"`
	RetryAfterSeconds int                      `json:"retry_after_seconds,omitempty"`
}
type LocalStatus struct {
	VerificationReasons                    map[string]string
	AccountStatuses                        map[string]HarnessDetail
	ProfilePermissions                     bool
	HarnessFailed                          bool
	HarnessDetails                         map[string]HarnessDetail
	HarnessStatuses                        map[string]string
	HarnessErrors                          map[string]string
	LoginRequired                          bool
	VerificationUnavailable                []string
	Ready                                  bool
	DaemonID, State                        string
	Active, Unconfirmed, SettlementPending []string
	VerificationResults                    map[string]string
	BlockedAccounts                        []BlockedAccount
}

// SavedOptions exposes only noncredential choices to resume the local command.
func (e *Engine) SavedOptions() (Options, error) {
	s, err := e.load()
	if err != nil {
		return Options{}, err
	}
	o := Options{Origin: s.Origin, TenantID: s.Response.TenantID, ComputerName: s.Request.ComputerName, Workspace: s.Request.Workspace, Platform: Platform{OS: s.Request.Platform, Arch: s.Request.Arch}, StartService: s.StartService, NodePath: s.NodePath, ClaudeSDKPath: s.ClaudeSDKPath}
	for _, c := range s.Candidates {
		v := c.Candidate
		v.Path = c.Path
		v.Home = c.Home
		v.Identity = c.Identity
		v.Version = c.Version
		v.Login = "signed_in"
		v.Grok = c.Candidate.Grok
		o.Candidates = append(o.Candidates, v)
	}
	return o, nil
}

type LocalDaemon interface {
	Fence(context.Context, string, string) (LocalStatus, error)
	Status(context.Context, string) (LocalStatus, error)
}
type Engine struct {
	Enclave            agentsecurity.Signer
	Store              *Store
	API                PairingAPI
	Services           *ServiceManager
	Local              LocalDaemon
	ClaudeDependencies ClaudeDependencies
	Now                func() time.Time
	GrokProbe          func(context.Context, grokprobe.Binding) (grokprobe.Identity, error)
}
type Options struct {
	Origin, TenantID, TenantSlug, ComputerName, Workspace string
	Platform                                              Platform
	Candidates                                            []Candidate
	StartService                                          bool
	NodePath, ClaudeSDKPath                               string
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
func uuid() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", errors.New("secure request identity unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func (e *Engine) load() (*snapshot, error) { return e.loadSnapshot(false) }

func (e *Engine) loadSnapshot(readOnly bool) (*snapshot, error) {
	var raw []byte
	var err error
	if readOnly {
		raw, err = e.Store.readSnapshot()
	} else {
		raw, err = e.Store.Read(snapshotName, 1<<20)
	}
	if err != nil {
		return nil, err
	}
	var s snapshot
	if json.Unmarshal(raw, &s) != nil || s.Schema != "aeon.agent-setup.private.v1" || !uuidPattern.MatchString(s.Request.RequestID) || (!s.ComputerCleaned && (!hashPattern.MatchString(string(s.Device)) || !hashPattern.MatchString(string(s.Runtime)))) || !hashPattern.MatchString(string(s.Lifecycle)) {
		return nil, errors.New("private pairing state corrupt; no authority was restored")
	}
	if s.Removed == nil {
		s.Removed = map[string]bool{}
	}
	return &s, nil
}
func (e *Engine) save(s *snapshot, first bool) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return errors.New("private pairing state invalid")
	}
	return e.Store.Write(snapshotName, raw, first)
}
func (e *Engine) progress(s *snapshot) Progress {
	p := Progress{Schema: "aeon.agent-setup.v1", Stage: s.Phase, RequestID: s.Request.RequestID, ComputerID: s.View.ComputerID, Accounts: s.View.Enrollments, LocalProcesses: "unconfirmed"}
	if s.Phase == "awaiting_approval" {
		p.UserCode = s.Response.UserCode
		p.VerificationURI = s.Response.VerificationURI
		p.Action = "Enter this code at the same Aeon instance, review the selected accounts and choices, then Connect your machine."
	}
	if s.DisconnectAll || s.View.ComputerState == "revoked" || s.Phase == "revoked" || s.Phase == "denied" || s.Phase == "expired" {
		p.Action = "Keep this pairing's state for status and cleanup; to pair again, rerun pair with --state-root pointing to a new, empty private folder outside the working folder, then approve the new code in the browser. For Nix/Home Manager, update the service's state root through the owning configuration's review path."
	}
	if e.now().Before(s.NextPoll) {
		p.RetryAfterSeconds = int(s.NextPoll.Sub(e.now()).Seconds()) + 1
	}
	return p
}

func validateOptions(o Options) error {
	if err := ValidateOrigin(o.Origin); err != nil {
		return err
	}
	if _, err := SupportedPlatform(o.Platform.OS, o.Platform.Arch); err != nil {
		return err
	}
	if o.TenantID != "" && o.TenantSlug != "" || o.TenantID != "" && !uuidPattern.MatchString(o.TenantID) {
		return errors.New("choose exactly one valid tenant ID or slug")
	}
	p, err := filepath.EvalSymlinks(o.Workspace)
	if err != nil || !filepath.IsAbs(o.Workspace) || p != o.Workspace {
		return ErrUnsafePath
	}
	if info, err := os.Stat(p); err != nil || !info.IsDir() {
		return ErrUnsafePath
	}
	if !safeLabel.MatchString(o.ComputerName) || len(o.Candidates) < 1 || len(o.Candidates) > 7 {
		return errors.New("select one to seven ready harness accounts and a computer name")
	}
	seen := map[string]bool{}
	for _, c := range o.Candidates {
		if seen[c.Harness] || !candidateReady(c) || !safeLabel.MatchString(c.Label) || !filepath.IsAbs(c.Path) || !safeLabel.MatchString(c.Identity) {
			return errors.New("one explicitly identified signed-in account is required per harness")
		}
		seen[c.Harness] = true
		// Nix-owned vendor executables can be pinned and used without changing
		// their installation. Service ownership is enforced separately below.
		if c.Harness == "claude" && (!filepath.IsAbs(o.NodePath) || !filepath.IsAbs(o.ClaudeSDKPath)) {
			return errors.New("Claude requires pinned Node and Agent SDK paths")
		}
		if c.Harness != "grok" {
			if err := validateNode(c.Path, o.Workspace, c.Interpreter()); err != nil {
				return err
			}
		}
		if c.Harness == "grok" {
			if o.Platform.OS != "darwin" || o.Platform.Arch != "arm64" {
				return errors.New("native Grok guided setup requires macOS arm64")
			}
			if err := validGuidedGrok(c, o.Workspace); err != nil {
				return err
			}
		}
	}
	return nil
}

// Begin persists all three secrets and the request ID before any device POST.
// Repeating Begin with the same choices resumes the same request.
func (e *Engine) Begin(ctx context.Context, o Options) (Progress, error) {
	if err := e.Store.Lock(); err != nil {
		return Progress{}, err
	}
	if s, err := e.load(); err == nil {
		if s.Origin != strings.TrimRight(o.Origin, "/") || s.Request.Workspace != o.Workspace {
			return Progress{}, ErrCollision
		}
		if s.DisconnectAll || s.View.ComputerState == "revoked" || s.Phase == "revoked" {
			p := e.progress(s)
			return p, errors.New("this enrollment is revoked or disconnecting. " + p.Action)
		}
		if s.Phase == "denied" || s.Phase == "expired" {
			p := e.progress(s)
			return p, errors.New("this pairing request has ended. " + p.Action)
		}
		if err := e.checkSavedClaudeDependencies(s, ClaudeDependencies{NodePath: o.NodePath, SDKPath: o.ClaudeSDKPath}, false); err != nil {
			return e.progress(s), err
		}
		for _, local := range s.Candidates {
			if local.Candidate.Harness == "grok" {
				candidate := local.Candidate
				candidate.Path, candidate.Home, candidate.Identity, candidate.Grok = local.Path, local.Home, local.Identity, local.Candidate.Grok
				if err := e.verifyGuidedGrok(ctx, candidate, s.Request.Workspace); err != nil {
					return e.progress(s), err
				}
			}
		}
		return e.Step(ctx)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Progress{}, err
	}
	entries, err := os.ReadDir(e.Store.Path())
	if err != nil {
		return Progress{}, ErrUnsafePath
	}
	for _, entry := range entries {
		if entry.Name() != "setup.lock" && !(e.Store.vault != nil && entry.Name() == "keychain-migration.lock") {
			return Progress{}, ErrCollision
		}
	}
	for _, c := range o.Candidates {
		if c.Harness == "claude" {
			deps, err := (Discovery{}).ResolveClaudeDependencies(ClaudeDependencies{NodePath: o.NodePath, SDKPath: o.ClaudeSDKPath}, o.Workspace)
			if err != nil {
				return Progress{Stage: "blocked", Action: err.Error()}, err
			}
			o.NodePath, o.ClaudeSDKPath = deps.NodePath, deps.SDKPath
			break
		}
	}
	if err := validateOptions(o); err != nil {
		stage := "blocked"
		if errors.Is(err, ErrDeclarative) {
			stage = "managed_plan"
		}
		return Progress{Stage: stage, Action: err.Error()}, err
	}
	if within(o.Workspace, e.Store.Path()) || repositoryPath(e.Store.Path()) {
		return Progress{}, errors.New("private setup state must be outside project repositories")
	}
	if e.Services == nil {
		return Progress{}, errors.New("service ownership preflight unavailable")
	}
	// Declarative setup may request approval and prepare its private runtime,
	// but cannot adopt, install or activate the configuration owner's service.
	// Preflight stops at declarative ownership; further service conflicts are
	// checked only for unmanaged installs. No service is installed or activated here.
	if err := e.Services.Preflight(ctx, e.Store.Path(), nil); err != nil && !(errors.Is(err, ErrDeclarative) && !o.StartService) {
		stage := "service_conflict"
		if errors.Is(err, ErrDeclarative) {
			stage = "managed_plan"
		}
		return Progress{Stage: stage, Action: err.Error()}, err
	}
	if o.TenantID == "" && o.TenantSlug == "" {
		g, err := e.API.Guide(ctx)
		if err != nil {
			return Progress{}, err
		}
		if strings.TrimRight(g.InstanceURL, "/") != strings.TrimRight(o.Origin, "/") || g.Protocol != "pairing-v1" || g.DefaultTenantSlug == "" {
			return Progress{}, errors.New("instance guide binding unavailable")
		}
		o.TenantSlug = g.DefaultTenantSlug
	}
	id, err := uuid()
	if err != nil {
		return Progress{}, err
	}
	device, err := randomSecret()
	if err != nil {
		return Progress{}, err
	}
	runtimeSecret, err := randomSecret()
	if err != nil {
		return Progress{}, err
	}
	life, err := randomSecret()
	if err != nil {
		return Progress{}, err
	}
	s := &snapshot{Schema: "aeon.agent-setup.private.v1", Origin: strings.TrimRight(o.Origin, "/"), Device: device, Runtime: runtimeSecret, Lifecycle: life, LifecycleRequestID: id, StartService: o.StartService, NodePath: o.NodePath, ClaudeSDKPath: o.ClaudeSDKPath, Phase: "requesting", Removed: map[string]bool{}}
	signer := e.Enclave
	if signer == nil {
		signer = agentsecurity.DefaultSigner()
	}
	publicKey, keyErr := signer.Create(ctx, Hash([]byte(e.Store.Path()))+"/"+id)
	if keyErr != nil && !errors.Is(keyErr, agentsecurity.ErrUnavailable) {
		return Progress{}, keyErr
	}
	if publicKey != "" && (o.Platform.OS != "darwin" || attachwatch.LocalAuthPublicKey(publicKey) == nil) {
		return Progress{}, errors.New("invalid enclave public key")
	}
	if publicKey != "" {
		s.LocalAuthKeyID = Hash([]byte(e.Store.Path())) + "/" + id
	}
	s.Request = DeviceRequest{RequestID: id, TenantID: o.TenantID, TenantSlug: o.TenantSlug, DeviceHash: Hash([]byte(device)), RuntimeHash: Hash([]byte(runtimeSecret)), LifecycleHash: Hash([]byte(life)), Details: Details{LocalAuthPublicKey: publicKey, ComputerName: o.ComputerName, Platform: o.Platform.OS, Arch: o.Platform.Arch, Workspace: o.Workspace, Capabilities: []string{"managed_runs"}}}
	for _, c := range o.Candidates {
		key, err := uuid()
		if err != nil {
			return Progress{}, err
		}
		c.Key = key
		s.Request.Accounts = append(s.Request.Accounts, c)
		s.Candidates = append(s.Candidates, LocalCandidate{c, c.Path, c.Home, c.Identity, c.Version})
	}
	if err = e.save(s, true); err != nil {
		return Progress{}, err
	}
	return e.Step(ctx)
}

func (e *Engine) checkSavedClaudeDependencies(s *snapshot, requested ClaudeDependencies, addingClaude bool) error {
	if s.NodePath == "" && s.ClaudeSDKPath == "" {
		if !addingClaude && (requested.NodePath != "" || requested.SDKPath != "") {
			return errors.New("Claude dependency overrides differ from existing pairing; use Add harness for a new Claude enrollment")
		}
		return nil
	}
	if s.NodePath == "" || s.ClaudeSDKPath == "" {
		return errors.New("Claude dependencies changed: run aeon-agentd repin --harness claude; saved dependencies are incomplete")
	}
	valid, err := ResolveClaudeRuntime(ClaudeDependencies{NodePath: s.NodePath, SDKPath: s.ClaudeSDKPath}, s.Request.Workspace)
	if err != nil {
		return errors.New("Claude dependencies changed: run aeon-agentd repin --harness claude; saved dependencies are unavailable or unsafe")
	}
	if requested.NodePath != "" {
		p, err := pinnedRegular(requested.NodePath, s.Request.Workspace, true)
		if err != nil || p != valid.NodePath {
			return errors.New("--node-path conflicts with saved Claude dependency; dependencies changed: run aeon-agentd repin --harness claude; no enrollment was changed")
		}
	}
	if requested.SDKPath != "" {
		p, err := pinnedRegular(requested.SDKPath, s.Request.Workspace, false)
		if err != nil || p != valid.SDKPath {
			return errors.New("--claude-sdk-path conflicts with saved Claude dependency; dependencies changed: run aeon-agentd repin --harness claude; no enrollment was changed")
		}
	}
	return nil
}

func (e *Engine) Step(ctx context.Context) (Progress, error) {
	if err := e.Store.Lock(); err != nil {
		return Progress{}, err
	}
	s, err := e.load()
	if err != nil {
		return Progress{}, err
	}
	if s.DisconnectAll || s.Phase == "draining" || s.Phase == "server_unconfirmed" || s.Phase == "disconnected" {
		return e.reconcile(ctx, s)
	}
	if s.Phase == "connected" || s.Phase == "verification_pending" {
		return e.reconcile(ctx, s)
	}
	if s.Phase == "denied" || s.Phase == "expired" || s.Phase == "revoked" {
		p := e.progress(s)
		return p, errors.New("this pairing request has ended. " + p.Action)
	}
	if e.now().Before(s.NextPoll) {
		return e.progress(s), nil
	}
	if s.Response.RequestID == "" {
		r, err := e.API.Create(ctx, s.Request)
		if err != nil {
			return e.progress(s), e.rateLimit(s, err)
		}
		if r.RequestID != s.Request.RequestID || !uuidPattern.MatchString(r.TenantID) || !hashPattern.MatchString(r.Digest) || r.VerificationURI != s.Origin+"/agents/register-agent" || !regexp.MustCompile(`^[0-9]{3}-[0-9]{3}-[0-9]{3}$`).MatchString(r.UserCode) {
			return e.progress(s), errors.New("device response does not match this instance/request")
		}
		s.Response = r
		s.Phase = "awaiting_approval"
		s.NextPoll = e.now().Add(5 * time.Second)
		if err = e.save(s, false); err != nil {
			return Progress{}, err
		}
		return e.progress(s), nil
	}
	v, err := e.API.Redeem(ctx, ProofRequest{TenantID: s.Response.TenantID, RequestID: s.Request.RequestID, DeviceSecret: s.Device})
	if err != nil {
		return e.progress(s), e.rateLimit(s, err)
	}
	if err = validateView(s, v, true); err != nil {
		return e.progress(s), err
	}
	// The server fills an omitted profile at device creation. Pin its first
	// digest-bound projection so later polls cannot substitute a different one.
	for i := range s.Request.Accounts {
		s.Request.Accounts[i].ProfileID = v.Requested[i].ProfileID
	}
	// A pending/denied Add harness request has no new computer projection.
	// Preserve the healthy shared computer while this separate request waits.
	if s.Request.ExistingComputerID == "" || v.ComputerID != "" {
		s.View = v
	}
	s.NextPoll = e.now().Add(5 * time.Second)
	switch v.State {
	case "pending":
		s.Phase = "awaiting_approval"
	case "denied", "expired", "revoked":
		s.Phase = v.State
	case "redeemed":
		s.Phase = "provisioning"
	default:
		return e.progress(s), errors.New("unrecognized pairing state; execution blocked")
	}
	if err = e.save(s, false); err != nil {
		return Progress{}, err
	}
	if s.Phase == "provisioning" {
		return e.provision(ctx, s)
	}
	return e.progress(s), nil
}
func (e *Engine) rateLimit(s *snapshot, err error) error {
	var a *APIError
	if errors.As(err, &a) && a.Code == "rate_limited" {
		s.NextPoll = e.now().Add(a.RetryAfter)
		if saveErr := e.save(s, false); saveErr != nil {
			return saveErr
		}
	}
	return err
}

func validateView(s *snapshot, v View, request bool) error {
	if v.TenantID != s.Response.TenantID || v.ComputerName != s.Request.ComputerName || v.Platform != s.Request.Platform || v.Arch != s.Request.Arch || v.Workspace != s.Request.Workspace {
		return errors.New("pairing response scope mismatch")
	}
	if request && (v.RequestID != s.Request.RequestID || v.Digest != s.Response.Digest || !sameChoices(v.Requested, s.Request.Accounts)) {
		return errors.New("approved choices do not match local request")
	}
	seen := map[string]bool{}
	for _, a := range v.Enrollments {
		if !uuidPattern.MatchString(a.AccountID) || seen[a.AccountID] {
			return errors.New("invalid enrollment identity")
		}
		seen[a.AccountID] = true
		known := false
		for _, c := range s.Candidates {
			if c.Candidate.Key == a.AccountKey && c.Candidate.Harness == a.Harness && c.Candidate.Label == a.Label {
				known = true
				break
			}
		}
		if !known {
			return errors.New("response contains an unapproved account")
		}
		if a.State != "connected" && a.State != "draining" && a.State != "revoked" {
			return errors.New("unknown enrollment lifecycle state")
		}
	}
	if request && v.ExistingComputerID != s.Request.ExistingComputerID {
		return errors.New("existing computer preview binding changed")
	}
	if v.State == "redeemed" || v.ComputerID != "" {
		if s.BoundComputer != "" && (v.ComputerID != s.BoundComputer || v.DaemonID != s.BoundDaemon || v.PrincipalID != s.BoundPrincipal) {
			return errors.New("immutable enrollment binding changed")
		}
		if s.Request.ExistingComputerID != "" && v.ComputerID != s.Request.ExistingComputerID {
			return errors.New("Add harness computer binding changed")
		}
		if !uuidPattern.MatchString(v.ComputerID) || !uuidPattern.MatchString(v.PrincipalID) || v.DaemonID == "" || strings.ContainsAny(v.DaemonID, "/\\\x00\r\n") {
			return errors.New("approved computer identity unavailable")
		}
		if s.View.ComputerID != "" && (s.View.ComputerID != v.ComputerID || s.View.DaemonID != v.DaemonID || s.View.PrincipalID != v.PrincipalID) {
			return errors.New("existing computer binding changed")
		}
		if v.Revision < s.View.Revision {
			return errors.New("stale computer revision")
		}
	}
	return nil
}

func (e *Engine) provision(ctx context.Context, s *snapshot) (result Progress, resultErr error) {
	if s.View.State != "redeemed" || s.View.ComputerState != "connected" || s.DisconnectAll {
		return e.progress(s), errors.New("approved connected enrollment required")
	}
	defer func() {
		if resultErr == nil {
			return
		}
		state, code := "setup_failed", "installation_failed"
		if errors.Is(resultErr, ErrServiceConflict) {
			state, code = "service_conflict", "service_conflict"
		}
		if errors.Is(resultErr, ErrDeclarative) {
			code = "managed_installation"
		}
		if errors.Is(resultErr, ErrUnsafePath) {
			code = "private_storage_failed"
		}
		proof := e.proof(s)
		proof.Progress = reportRelease(s.View, &SetupProgress{State: state, ErrorCode: code})
		_, _ = e.API.Reconcile(ctx, proof)
	}()
	proof := e.proof(s)
	proof.Progress = reportRelease(s.View, &SetupProgress{State: "provisioning"})
	observed, err := e.API.Reconcile(ctx, proof)
	if err != nil {
		return e.progress(s), err
	}
	if err = validateView(s, observed, false); err != nil {
		return e.progress(s), err
	}
	if observed.ComputerState != "connected" {
		return e.reconcile(ctx, s)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(s.View.RuntimePrefix) {
		return e.progress(s), errors.New("runtime prefix unavailable")
	}
	s.BoundComputer, s.BoundDaemon, s.BoundPrincipal = s.View.ComputerID, s.View.DaemonID, s.View.PrincipalID
	if err := e.save(s, false); err != nil {
		return e.progress(s), err
	}
	config := RuntimeConfig{LocalAuthKeyID: s.LocalAuthKeyID, Schema: "aeon.agent-runtime.v1", Origin: s.Origin, TenantID: s.View.TenantID, PrincipalID: s.View.PrincipalID, DaemonID: s.View.DaemonID, ComputerID: s.View.ComputerID, Workspace: s.Request.Workspace, NodePath: s.NodePath, ClaudeSDKPath: s.ClaudeSDKPath, ClaudeRepinID: s.ClaudeRepinID, Accounts: []RuntimeAccount{}}
	seen := map[string]bool{}
	for _, a := range s.View.Enrollments {
		if a.State != "connected" || s.Removed[a.AccountID] {
			continue
		}
		if !uuidPattern.MatchString(a.AccountID) || seen[a.AccountID] {
			return e.progress(s), errors.New("invalid enrollment binding")
		}
		seen[a.AccountID] = true
		found := false
		for _, c := range s.Candidates {
			if c.Candidate.Key == a.AccountKey && c.Candidate.Harness == a.Harness && c.Candidate.Label == a.Label {
				if a.Harness == "grok" {
					v := c.Candidate
					v.Path, v.Home, v.Identity, v.Grok = c.Path, c.Home, c.Identity, c.Candidate.Grok
					if err := e.verifyGuidedGrok(ctx, v, s.Request.Workspace); err != nil {
						return e.progress(s), err
					}
				}
				config.Accounts = append(config.Accounts, RuntimeAccount{a.Harness, a.AccountKey, a.AccountID, c.Home, c.Identity, c.Path, c.Candidate.Grok, c.Candidate.PiNode, c.Candidate.Node})
				found = true
				break
			}
		}
		if !found {
			return e.progress(s), errors.New("server selected an account absent from local approval")
		}
	}
	if len(config.Accounts) == 0 {
		return e.progress(s), errors.New("no approved local accounts")
	}
	key := []byte("aeon_" + s.View.RuntimePrefix + "_" + string(s.Runtime))
	if err := e.Store.Write("runtime.key", key, true); err != nil {
		if !errors.Is(err, ErrCollision) {
			return e.progress(s), err
		}
		old, e2 := e.Store.Read("runtime.key", 4096)
		if e2 != nil || string(old) != string(key) {
			return e.progress(s), ErrCollision
		}
	}
	var previous RuntimeConfig
	if saved, err := e.Store.Read(RuntimeName, 128<<10); err == nil && json.Unmarshal(saved, &previous) == nil {
		config.preserveAttachIdentities(previous)
	}
	config.RecordAttachIdentities()
	raw, _ := json.Marshal(config)
	if err := e.Store.Write(RuntimeName, raw, false); err != nil {
		return e.progress(s), err
	}
	if s.StartService {
		if e.Services == nil {
			return e.progress(s), errors.New("service manager unavailable")
		}
		receipt, err := e.Services.Install(ctx, e.Store, s.View.ComputerID, true, s.Service)
		s.Service = receipt
		if saveErr := e.save(s, false); saveErr != nil {
			return Progress{}, saveErr
		}
		if err != nil {
			return e.progress(s), err
		}
	}
	s.Phase = "connected"
	if s.View.Verification.Mode == "one_per_harness" {
		s.Phase = "verification_pending"
	}
	if err := e.save(s, false); err != nil {
		return Progress{}, err
	}
	p := e.progress(s)
	if e.Local == nil {
		p.Stage = "provisioning"
		p.Action = "Start the approved daemon to verify connectivity."
		return p, nil
	}
	return e.connectionProgress(ctx, s), nil
}

func ReadRuntimeConfig(root string) (RuntimeConfig, error) {
	s, err := OpenStore(root, false)
	if err != nil {
		return RuntimeConfig{}, err
	}
	defer s.Close()
	return readRuntimeConfig(s)
}

func readRuntimeConfig(s *Store) (RuntimeConfig, error) {
	raw, err := s.Read(RuntimeName, 128<<10)
	if err != nil {
		return RuntimeConfig{}, err
	}
	var c RuntimeConfig
	if json.Unmarshal(raw, &c) != nil || c.Schema != "aeon.agent-runtime.v1" || ValidateOrigin(c.Origin) != nil || !uuidPattern.MatchString(c.TenantID) || !uuidPattern.MatchString(c.PrincipalID) || !uuidPattern.MatchString(c.ComputerID) || c.DaemonID == "" || len(c.DaemonID) > 128 || strings.ContainsAny(c.DaemonID, "/\\\x00\r\n") {
		return RuntimeConfig{}, errors.New("private runtime configuration invalid")
	}
	if s.vault != nil {
		// Public disk state cannot redirect the signed daemon into disclosing a
		// protected lifecycle proof or bearer. This runs before cold-start
		// preflight, and migrates the pairing snapshot on first daemon start.
		engine := Engine{Store: s}
		paired, err := engine.load()
		if err != nil {
			return RuntimeConfig{}, err
		}
		if paired.ComputerCleaned || c.Origin != paired.Origin || c.TenantID != paired.View.TenantID || c.ComputerID != paired.View.ComputerID || c.PrincipalID != paired.View.PrincipalID || c.DaemonID != paired.View.DaemonID || c.Workspace != paired.Request.Workspace || c.LocalAuthKeyID != paired.LocalAuthKeyID {
			return RuntimeConfig{}, errors.New("runtime configuration differs from protected pairing")
		}
	}
	return c, nil
}

// UnpinnedEnrollment reports an older npm Codex, Cursor or pi account whose
// env-node launcher has no interpreter pin. A native wrapper is not unpinned.
func UnpinnedEnrollment(a RuntimeAccount) bool {
	switch a.Harness {
	case "codex", "cursor", "pi", "gemini", "opencode":
	default:
		return false
	}
	node := a.Node
	if a.Harness == "pi" {
		node = a.PiNode
	}
	if node != (harnesslaunch.Node{}) {
		return false
	}
	needed, err := harnesslaunch.NeedsNode(a.Path)
	return err == nil && needed
}

// LaunchableRuntime removes unpinned enrollments so a whole-config check can
// still see the other accounts. The paired daemon does not use it to ignore
// partial, drifted, invalid, or unsafe pins; AccountPinBlocks isolates every
// pin problem to that account.
func LaunchableRuntime(c RuntimeConfig) RuntimeConfig {
	kept := make([]RuntimeAccount, 0, len(c.Accounts))
	for _, a := range c.Accounts {
		if !UnpinnedEnrollment(a) {
			kept = append(kept, a)
		}
	}
	c.Accounts = kept
	return c
}
func ReadRuntime(root string) (RuntimeConfig, secret, error) {
	c, err := ReadRuntimeConfig(root)
	if err != nil {
		return c, "", err
	}
	s, err := OpenStore(root, false)
	if err != nil {
		return c, "", err
	}
	defer s.Close()
	key, err := s.Read("runtime.key", 4096)
	if err != nil {
		return c, "", err
	}
	if !regexp.MustCompile(`^aeon_[A-Za-z0-9_-]{1,64}_[0-9a-f]{64}$`).Match(key) {
		return c, "", errors.New("private runtime credential invalid")
	}
	return c, secret(key), nil
}

// DispatchPermitted is consulted only after a successful tombstone reconciliation.
// It is not execution authority; normal runtime authentication/claim still apply.
func (e *Engine) DispatchPermitted() (bool, error) {
	s, err := e.load()
	if err != nil {
		return false, err
	}
	return !s.DisconnectAll && !s.ComputerCleaned && s.View.ComputerState == "connected", nil
}

func sameChoices(a, b []Candidate) bool {
	if len(a) != len(b) {
		return false
	}
	b = append([]Candidate(nil), b...)
	for i := range b {
		if b[i].ProfileID == "" && uuidPattern.MatchString(a[i].ProfileID) {
			b[i].ProfileID = a[i].ProfileID
		}
	}
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
func repositoryPath(path string) bool {
	return repositoryPathWithLstat(path, os.Lstat)
}

// Private state rejects every repository ancestor, including package-manager
// prefixes. The Homebrew exception belongs only to executable/dependency pins.
func repositoryPathWithLstat(path string, lstat func(string) (os.FileInfo, error)) bool {
	for p := path; p != "/" && p != "."; p = filepath.Dir(p) {
		if _, err := lstat(filepath.Join(p, ".git")); err == nil {
			return true
		}
	}
	return false
}

// Local-profile harnesses establish authentication at session/new, not discovery.
func candidateReady(c Candidate) bool {
	return c.Login == "signed_in" || c.Login == "local_profile" && (c.Harness == "gemini" || c.Harness == "opencode")
}
