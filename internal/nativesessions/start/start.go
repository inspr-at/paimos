// SPDX-License-Identifier: AGPL-3.0-only

// Package start prepares a locally resolved launch after durable server claim.
// It never runs a vendor CLI itself. I1 passes the owning supervisor's Launcher
// and shared-ledger guard; P4 supplies the account ProfileResolver.
package start

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type Claims interface {
	Claim(context.Context, nativecontract.DaemonActor, nativecontract.Claim) (nativecontract.ClaimResult, error)
}

// LaunchGuard holds the local shared ledger/writer lock from the final check
// through Launcher.Open. It validates monotonic expiry, exact binding/grant,
// current profile identity/pins and possible-fork journal before calling open.
// A failed/uncertain open keeps its ledger reservation until proven no-fork.
type LaunchGuard interface {
	Open(context.Context, nativecontract.ClaimResult, nativecontract.LaunchSpec, time.Time, func(context.Context, nativecontract.LaunchSpec) (nativecontract.NativeTransport, error)) (nativecontract.NativeTransport, error)
}

type Starter struct {
	Claims   Claims
	Profiles nativecontract.ProfileResolver
	Launcher nativecontract.Launcher
	Guard    LaunchGuard
	Lifetime context.Context  // owning supervisor, independent of request/viewer
	Now      func() time.Time // time.Now preserves monotonic time in production
}

func refusal(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func (s Starter) Start(ctx context.Context, actor nativecontract.DaemonActor, claim nativecontract.Claim, local nativecontract.LocalStart) (nativecontract.NativeTransport, error) {
	if s.Claims == nil || s.Profiles == nil || s.Launcher == nil || s.Guard == nil || s.Lifetime == nil {
		return nil, refusal(nativecontract.ReasonQualificationMissing)
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if err = validate(v, "NativeLocalStart", local); err != nil {
		return nil, err
	}
	// Existing resume/after-exit flow belongs to the owning supervisor. This
	// first-start helper never substitutes a PID or public ID for exit proof.
	if local.AfterExit != nil || local.NativeResumeRef != nil || local.Intent.ResumeFromOwnedSessionID != nil {
		return nil, refusal(nativecontract.ReasonUnsupported)
	}
	started := s.Now()
	result, err := s.Claims.Claim(ctx, actor, claim)
	if err != nil {
		return nil, err
	}
	if err = validate(v, "NativeClaimResult", result); err != nil {
		return nil, err
	}
	if !result.LaunchAuthorized || result.ClaimKind != "start" || result.Command != nil || result.ExpectedBinding == nil {
		return nil, refusal(nativecontract.ReasonGrantMissing)
	}
	b := *result.ExpectedBinding
	if result.OperationID != claim.OperationID || b.TenantID != actor.TenantID || b.DaemonID != actor.DaemonID || b.DaemonGeneration != actor.DaemonGeneration || result.OwnedSessionID != b.OwnedSessionID || !reflect.DeepEqual(result.Intent, local.Intent) || local.Intent.ComputerID != actor.ComputerID {
		return nil, refusal(nativecontract.ReasonIdentityMismatch)
	}
	if actor.OwnerPersonID == "" {
		return nil, refusal(nativecontract.ReasonWrongPrincipal)
	}
	deadline := started.Add(time.Duration(result.AuthorizationTTLMS) * time.Millisecond)
	if !s.Now().Before(deadline) {
		return nil, refusal(nativecontract.ReasonGrantExpired)
	}
	profile, err := s.Profiles.Resolve(ctx, nativecontract.ProfileRequest{TenantID: actor.TenantID, OwnerPersonID: actor.OwnerPersonID, AccountID: result.Intent.AccountID, AccountKey: local.AccountKey, WorkspaceID: result.Intent.WorkspaceID, Harness: result.Intent.Harness, Policy: result.Policy, BindingRevision: b.BindingRevision})
	if err != nil {
		return nil, err
	}
	if profile.AccountID != result.Intent.AccountID || profile.ProfileBindingRevision != b.BindingRevision {
		return nil, refusal(nativecontract.ReasonIdentityMismatch)
	}
	cwd, err := physicalCwd(profile.WorkspaceRoot, local.Cwd)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(profile.AccountHome) || !filepath.IsAbs(profile.UserHome) || !filepath.IsAbs(profile.ConversationRoot) || !filepath.IsAbs(profile.Binary.Path) || len(profile.Binary.Path) > 4096 || !pinDigest(profile.Binary.SHA256) {
		return nil, refusal(nativecontract.ReasonPinInvalid)
	}
	if !s.Now().Before(deadline) {
		return nil, refusal(nativecontract.ReasonGrantExpired)
	}
	spec := nativecontract.LaunchSpec{Binding: b, Profile: profile, Policy: result.Policy, Cwd: cwd, Mode: "structured", OwnedWriterID: b.RuntimeEpoch, Options: result.LaunchConfig, Lifetime: s.Lifetime}
	return s.Guard.Open(ctx, result, spec, deadline, s.Launcher.Open)
}

func physicalCwd(root, cwd string) (string, error) {
	if len(root) > 4096 || len(cwd) > 4096 || !filepath.IsAbs(root) || !filepath.IsAbs(cwd) {
		return "", refusal(nativecontract.ReasonInvalidInput)
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", refusal(nativecontract.ReasonInvalidInput)
	}
	physical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", refusal(nativecontract.ReasonInvalidInput)
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		return "", refusal(nativecontract.ReasonInvalidInput)
	}
	rel, err := filepath.Rel(physicalRoot, physical)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", refusal(nativecontract.ReasonPermissionRevoked)
	}
	return physical, nil
}

func pinDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}
