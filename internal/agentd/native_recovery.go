// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativerecovery"
)

// Recovery retains only the current owning handle. Disk/ledger reads validate
// that handle; they never create an adopter for an arbitrary saved PID.
type nativeRecovery struct {
	e       *nativeOwned
	actor   nativecontract.Actor
	absence *nativerecovery.Absence
	history nativerecovery.History
	result  nativecontract.OperationResult
}

func (h *NativeHost) resumeOwned(ctx context.Context, in nativecontract.ResumeIntent) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	if h.s == nil || h.config.Qualification == nil {
		return out, nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return out, err
	}
	if err = nativereceipts.Validate(v, "NativeResumeIntent", in); err != nil {
		return out, err
	}
	h.mu.Lock()
	var e *nativeOwned
	for _, candidate := range h.entries {
		if candidate.grant.ExpectedBinding != nil && candidate.grant.ExpectedBinding.OwnedSessionID == in.ExpectedBinding.OwnedSessionID {
			e = candidate
			break
		}
	}
	h.mu.Unlock()
	if e == nil {
		return out, ErrNotOwned
	}
	state, err := agentsetup.OpenStore(filepath.Join(h.s.state.Path(), "native-recovery-"+in.ExpectedBinding.OwnedSessionID), true)
	if err != nil {
		return out, err
	}
	defer state.Close()
	actor := nativecontract.Actor{TenantID: e.owner.TenantID, PrincipalID: e.owner.PrincipalID, PrincipalKind: "person", OwnerPersonID: e.owner.OwnerPersonID, GrantID: e.owner.GrantID, GrantRevision: e.owner.GrantRevision}
	runtime := &nativeRecovery{e: e, actor: actor}
	coordinator, err := nativerecovery.New(nativerecovery.Config{Runtime: runtime, State: state, Receipts: e.journal, Now: h.s.capacityNow})
	if err != nil {
		return out, err
	}
	// This actor is a selector for an existing person-issued resume intent.
	// The paired server rechecks that exact request, original person, policy
	// and owned exit at admission; it cannot create authority from this value.
	status, err := coordinator.Resume(ctx, actor, in)
	if err != nil {
		return out, err
	}
	if runtime.result.Session == nil {
		// A completed P5 journal replay may report its prior outcome, never
		// fork again. It is deliberately content-free and bound to the request.
		e.mu.Lock()
		prior, ok := e.recoveryResults[in.RequestID]
		e.mu.Unlock()
		if !ok || prior.Session == nil || prior.Session.CurrentBinding == nil || !prior.Session.CurrentBinding.Equal(status.Binding) {
			return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
		}
		return prior, nil
	}
	if runtime.result.Session.CurrentBinding == nil || !runtime.result.Session.CurrentBinding.Equal(status.Binding) {
		return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	return runtime.result, nil
}

func (r *nativeRecovery) WithWriter(ctx context.Context, b nativecontract.Binding, fn func(context.Context) error) error {
	e := r.e
	if err := e.opMu.LockContext(ctx); err != nil {
		return err
	}
	defer e.opMu.Unlock()
	if err := e.host.s.dispatchMu.LockContext(ctx); err != nil {
		return err
	}
	defer e.host.s.dispatchMu.Unlock()
	if !e.host.s.dispatchAllowed(e.local.Intent.AccountID) || !e.host.s.accountAvailable(e.local.Intent.AccountID) {
		return ErrDraining
	}
	return fn(ctx)
}

func (r *nativeRecovery) Read(ctx context.Context, b nativecontract.Binding) (nativerecovery.Snapshot, error) {
	e := r.e
	var out nativerecovery.Snapshot
	e.mu.Lock()
	transport := e.transport
	grant := e.grant
	e.mu.Unlock()
	if transport == nil || grant.ExpectedBinding == nil || e.transition != nil || grant.ExpectedBinding.OwnedSessionID != b.OwnedSessionID {
		return out, nativeRecoveryUnconfirmed("retained source")
	}
	raw, err := e.host.s.state.Read("lead-runtime-"+e.local.Intent.RequestID+".json", 32768)
	if err != nil {
		return out, err
	}
	record, err := decodeLeadRecord(raw)
	if err != nil || !record.Attempted || record.Abandoned || record.ExitObserved || record.Native == nil || record.Native.SessionID != grant.ExpectedBinding.HarnessSessionID || record.Group != e.group {
		return out, nativeRecoveryUnconfirmed("owner journal")
	}
	identity := transport.Identity()
	ledger, _, err := e.host.s.ledger.Snapshot()
	if err != nil {
		return out, err
	}
	group, ok := ledger.Groups[e.group]
	if !ok || group.Instance != e.host.s.ledgerBinding.Member.ID || group.Generation != e.host.s.ledgerBinding.Generation || group.State != "running" || group.PID != identity.PID || !group.StartedAt.Equal(identity.StartedAt) {
		return out, nativeRecoveryUnconfirmed("shared ledger")
	}
	var view nativecontract.SessionView
	if err = e.call(ctx, "GET", "/api/projects/"+b.ProjectID+"/native-sessions/"+b.OwnedSessionID, nil, &view); err != nil {
		return out, err
	}
	if view.CurrentBinding == nil || !view.CurrentBinding.Equal(*grant.ExpectedBinding) {
		return out, nativeRefusal(nativecontract.ReasonWrongGeneration)
	}
	// These digests bind the retained provider's local conversation to the
	// already-resolved account/workspace. Fresh server admission and the
	// unchanged physical profile are independently required before Open.
	mapping, err := nativereceipts.Digest([]any{view.ConversationRef, e.local.Intent.WorkspaceID, e.local.Cwd, identity.NativeSessionRef, e.profile.ProfileBindingRevision})
	if err != nil {
		return out, err
	}
	account, err := nativereceipts.Digest([]any{r.actor.TenantID, e.owner.OwnerPersonID, e.local.Intent.AccountID, e.local.AccountKey, e.profile.AccountHome, e.profile.ProfileBindingRevision})
	if err != nil {
		return out, err
	}
	history := nativerecovery.History{ConversationRef: view.ConversationRef, AccountID: e.local.Intent.AccountID, WorkspaceID: e.local.Intent.WorkspaceID, ResumeRef: identity.NativeSessionRef, MappingDigest: mapping, AccountBindingDigest: account, ProfileRevision: e.profile.ProfileBindingRevision}
	r.history = history
	out = nativerecovery.Snapshot{Writer: nativecontract.WriterRecord{Binding: *grant.ExpectedBinding, Identity: identity, AttemptID: grant.ExpectedBinding.RuntimeEpoch, PossibleFork: true}, Revision: view.Revision, OwnerPersonID: e.owner.OwnerPersonID, Policy: grant.Policy, History: history, Cause: nativerecovery.ChildExited, Budget: nativerecovery.Budget{StartedAt: identity.StartedAt, UsageKnown: false}}
	return out, nil
}

func (r *nativeRecovery) ExpireCallbacks(ctx context.Context, b nativecontract.Binding) error {
	return r.e.stopEvents(ctx)
}
func (r *nativeRecovery) ProveAbsent(ctx context.Context, w nativecontract.WriterRecord) (nativerecovery.Absence, error) {
	e := r.e
	if e.transport == nil || e.transport.Identity() != w.Identity {
		return nativerecovery.Absence{}, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	x, err := e.transport.Wait(ctx)
	if err != nil {
		return nativerecovery.Absence{}, err
	}
	digest, err := nativereceipts.Digest([]any{w.Binding, w.AttemptID, x})
	if err != nil {
		return nativerecovery.Absence{}, err
	}
	return nativerecovery.Absence{Binding: w.Binding, AttemptID: w.AttemptID, Kind: "owned_exit", AttestationDigest: digest, Evidence: x}, nil
}
func (r *nativeRecovery) ConfirmAbsent(ctx context.Context, w nativecontract.WriterRecord, a nativerecovery.Absence) error {
	current, err := r.ProveAbsent(ctx, w)
	if err != nil {
		return err
	}
	expected, err := nativereceipts.Digest([]any{w.Binding, w.AttemptID, a.Evidence})
	if err != nil {
		return err
	}
	if a.AttestationDigest != expected || !current.Binding.Equal(a.Binding) || current.AttemptID != a.AttemptID || current.Evidence.ProcessIdentityDigest != a.Evidence.ProcessIdentityDigest || !current.Evidence.RootExited || !current.Evidence.GroupAbsent {
		return nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	r.absence = &a
	return nil // Occupancy remains held until the journalled successor attempt.
}

func (r *nativeRecovery) Prepare(ctx context.Context, actor nativecontract.Actor, in nativecontract.ResumeIntent, effects nativerecovery.Effects) (nativerecovery.Successor, error) {
	var out nativerecovery.Successor
	e := r.e
	if r.absence == nil || len(effects.Items) > 0 || e.transition != nil || in.Reason != "person_continue" {
		return out, nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	params, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	g := nativecontract.ClaimResult{OperationID: in.RequestID, ExpectedBinding: &in.ExpectedBinding, Policy: e.grant.Policy, LaunchConfig: e.grant.LaunchConfig, Command: &nativecontract.DaemonCommand{Kind: "resume", Params: params}, CommandActor: &e.owner}
	e.transition = &nativeTransition{record: nativeTransitionRecord{Operation: in.RequestID, Kind: "resume", Phase: "awaiting_admission", Source: in.ExpectedBinding}, command: g, identity: e.transport.Identity()}
	if err = e.save("transition"); err != nil {
		return out, err
	}
	spec, err := e.admitSuccessor(ctx, g, e.transition.identity, r.absence.Evidence, "structured")
	if err != nil {
		return out, err
	}
	digest, err := nativereceipts.Digest([]any{spec.Binding, e.transition.grant.OperationID, e.transition.grant.ExpiresAt})
	if err != nil {
		return out, err
	}
	out = nativerecovery.Successor{Launch: spec, History: r.history, RuntimeProofDigest: digest, AuthorizationDeadline: e.claimDeadline, ClaimKind: "resume", LaunchAuthorized: true}
	return out, nil
}

func (r *nativeRecovery) Revalidate(ctx context.Context, actor nativecontract.Actor, in nativecontract.ResumeIntent, next nativerecovery.Successor, effects nativerecovery.Effects) error {
	e := r.e
	if len(effects.Items) > 0 || e.transition == nil || e.transition.grant.ExpectedBinding == nil || !next.Launch.Binding.Equal(*e.transition.grant.ExpectedBinding) || !reflect.DeepEqual(actor, r.actor) {
		return nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	profile, err := e.host.profiles.Resolve(ctx, nativecontract.ProfileRequest{TenantID: actor.TenantID, OwnerPersonID: e.owner.OwnerPersonID, AccountID: e.local.Intent.AccountID, AccountKey: e.local.AccountKey, WorkspaceID: e.local.Intent.WorkspaceID, Harness: Claude, Policy: next.Launch.Policy, BindingRevision: e.profile.ProfileBindingRevision})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(profile, next.Launch.Profile) {
		return nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	if err = e.host.config.Qualification.Require(ctx, actor, next.Launch, "structured"); err != nil {
		return err
	}
	return e.revalidateSuccessor(ctx)
}
func (r *nativeRecovery) Attempt(ctx context.Context, next nativerecovery.Successor) (nativecontract.NativeIdentity, error) {
	out, err := r.e.openSuccessor(ctx, next.Launch, r.actor)
	if err != nil {
		return nativecontract.NativeIdentity{}, err
	}
	r.result = out
	r.e.mu.Lock()
	if r.e.recoveryResults == nil {
		r.e.recoveryResults = map[string]nativecontract.OperationResult{}
	}
	r.e.recoveryResults[out.Receipt.RequestID] = out
	r.e.mu.Unlock()
	return r.e.transport.Identity(), nil
}

var _ nativerecovery.Runtime = (*nativeRecovery)(nil)

func nativeRecoveryUnconfirmed(stage string) error {
	return fmt.Errorf("native recovery %s: %w", stage, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed))
}
