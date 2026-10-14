// SPDX-License-Identifier: AGPL-3.0-only

// Package nativeintegration composes the qualified LEAD pilot boundaries.
// Construction supplies no consent and enables no runtime or automatic lead.
package nativeintegration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/nativeadmission"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options is supplied only by the server/owning-supervisor composition, never
// from an API body. Enablement still requires the coordinator's terms, native
// actor/history/tool grant and pin qualification. Supplying ports issues none.
type Options struct {
	Enabled   bool
	Services  nativecontract.Services
	Host      nativecontract.Host
	Launcher  nativecontract.Launcher
	Admission *RootAuthority
}

type Pilot struct {
	HTTP      *nativesessions.Module
	Host      nativecontract.Host
	Launcher  nativecontract.Launcher
	Admission *RootAuthority
}

// New defaults off even when qualified-looking ports are supplied. It mounts
// only C0's disabled routes until ALL production ports are deliberately wired;
// neither construction nor a person request can enable automatic LeadRuntime.
func New(opts Options) (*Pilot, error) {
	if !opts.Enabled {
		return &Pilot{HTTP: nativesessions.NewDisabled()}, nil
	}
	if opts.Host == nil || opts.Launcher == nil || opts.Admission == nil || opts.Admission.Pool == nil || opts.Admission.Grants == nil {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	module, err := nativesessions.New(opts.Services)
	if err != nil {
		return nil, err
	}
	return &Pilot{HTTP: module, Host: opts.Host, Launcher: opts.Launcher, Admission: opts.Admission}, nil
}

// RootAuthority connects P4c's local admission port to the exact server-owned
// native root. It never invents a worker run, authenticating principal or key.
// Grants must re-read the ORIGINAL actor's current native consent in each final
// transaction. Changing a local profile requires its own explicit authority;
// absent that callback, profile changes remain unavailable.
type RootAuthority struct {
	Pool          *pgxpool.Pool
	Grants        agentaccounts.NativeSessionAuthority
	ProfileChange func(context.Context, nativecontract.ProfileRequest) error
	Now           func() time.Time
}

func refuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func (a RootAuthority) AuthorizeProfileChange(ctx context.Context, request nativecontract.ProfileRequest) error {
	if a.ProfileChange == nil {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	return a.ProfileChange(ctx, request)
}

func (a RootAuthority) Revalidate(ctx context.Context, request nativeadmission.Request) error {
	if a.Pool == nil || a.Grants == nil {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	p, ok := tenant.PrincipalFrom(ctx)
	b := request.Binding
	if !ok || p.Kind != tenant.Agent || p.KeyID == "" || p.TenantID != b.TenantID || b.RunID != nil ||
		request.Profile.TenantID != p.TenantID || request.Profile.AccountID == "" ||
		request.Profile.BindingRevision != b.BindingRevision {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	for _, id := range []string{b.TenantID, b.ProjectID, b.OwnedSessionID, b.HarnessSessionID, b.RuntimeEpoch,
		request.Profile.AccountID, request.Profile.OwnerPersonID, request.Launch.ModelProfileID} {
		if !workorders.UUID(id) {
			return refuse(nativecontract.ReasonInvalidInput)
		}
	}
	for _, value := range []string{b.DaemonID, b.DaemonGeneration, request.Profile.AccountKey, request.Profile.WorkspaceID,
		request.Profile.Harness, request.AccountBindingDigest, request.Launch.Model} {
		if len(value) > 256 {
			return refuse(nativecontract.ReasonBufferFull)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Include a delegated original actor's key BEFORE the resource transaction.
	// Its immutable identity is checked again under the final pairing fence.
	var originalKey string
	if err := db.InTenant(ctx, a.Pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(actor_key_id::text,'') FROM native_session_intents
 WHERE owned_session_id=$1 AND project_id=$2`, b.OwnedSessionID, b.ProjectID).Scan(&originalKey)
	}); err != nil {
		return err
	}
	if originalKey != "" {
		ctx = db.WithKeyScopeUse(ctx, p.TenantID, originalKey)
	}
	return db.InTenant(ctx, a.Pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := accountuse.LockShared(ctx, tx); err != nil {
			return err
		}
		var in agentaccounts.NativeAdmission
		var policyRaw, launchRaw []byte
		var workspace, key string
		err := tx.QueryRow(ctx, `SELECT b.owner_person_id::text,b.account_id::text,b.computer_id::text,b.harness,
 b.model_profile_id::text,b.ledger_generation,b.workspace_id,coalesce(i.actor_key_id::text,''),i.policy,i.launch_config
 FROM native_session_bindings b JOIN native_session_intents i ON i.tenant_id=b.tenant_id AND i.owned_session_id=b.id
 JOIN native_session_roots root ON root.tenant_id=b.tenant_id AND root.owned_session_id=b.id AND root.runtime_epoch=b.runtime_epoch
 JOIN harness_sessions h ON h.tenant_id=b.tenant_id AND h.id=b.current_harness_session_id
 JOIN nodes n ON n.tenant_id=b.tenant_id AND n.id=b.project_id
 WHERE b.tenant_id=$1 AND b.id=$2 AND b.project_id=$3 AND b.paired_principal_id=$4
 AND b.current_harness_session_id=$5 AND b.runtime_epoch=$6 AND b.binding_revision=$7
 AND b.daemon_id=$8 AND b.daemon_generation=$9 AND b.run_id IS NULL
 AND root.project_id=b.project_id AND root.owner_person_id=b.owner_person_id
 AND root.account_id=b.account_id AND root.computer_id=b.computer_id
 AND root.run_id IS NULL AND root.released_at IS NULL AND h.stopped_at IS NULL AND h.archived_at IS NULL
 AND n.deleted_at IS NULL FOR NO KEY UPDATE OF b,i,root,h`, p.TenantID, b.OwnedSessionID, b.ProjectID,
			p.ID, b.HarnessSessionID, b.RuntimeEpoch, b.BindingRevision, b.DaemonID, b.DaemonGeneration).Scan(
			&in.OwnerPersonID, &in.AccountID, &in.ComputerID, &in.Harness, &in.ModelProfileID, &in.LedgerGeneration, &workspace, &key, &policyRaw, &launchRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			return refuse(nativecontract.ReasonWrongGeneration)
		}
		if err != nil {
			return err
		}
		if len(policyRaw) > 16384 || len(launchRaw) > 16384 {
			return refuse(nativecontract.ReasonBufferFull)
		}
		var policy nativecontract.Policy
		if err = json.Unmarshal(policyRaw, &policy); err != nil {
			return err
		}
		var launch nativecontract.LaunchConfig
		if err = json.Unmarshal(launchRaw, &launch); err != nil {
			return err
		}
		if key != originalKey || request.Profile.OwnerPersonID != in.OwnerPersonID ||
			request.Profile.AccountID != in.AccountID || request.Profile.Harness != in.Harness ||
			request.Profile.WorkspaceID != workspace || request.Launch.ModelProfileID != in.ModelProfileID ||
			!reflect.DeepEqual(request.Launch, launch) ||
			!samePolicy(policy, request.Profile.Policy) || request.Launch.ToolPolicyDigest != policy.ToolPolicyDigest ||
			request.Launch.SettingsPolicyDigest != policy.SettingsPolicyDigest {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		in.TenantID, in.ProjectID, in.PrincipalID, in.OwnedSessionID = p.TenantID, b.ProjectID, p.ID, b.OwnedSessionID
		in.DaemonID, in.DaemonGeneration = b.DaemonID, b.DaemonGeneration
		account, err := agentaccounts.BindNativeAccountTx(ctx, tx, in)
		if err != nil {
			return err
		}
		if account.AccountKey != request.Profile.AccountKey || account.Digest() != request.AccountBindingDigest {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		now := time.Now()
		if a.Now != nil {
			now = a.Now()
		}
		return agentaccounts.RequireNativeSessionTx(ctx, tx, p, in, account, b.OwnedSessionID, now, a.Grants)
	})
}

func samePolicy(a, b nativecontract.Policy) bool {
	aTime, bTime := a.ExpiresAt, b.ExpiresAt
	a.ExpiresAt, b.ExpiresAt = time.Time{}, time.Time{}
	return a == b && aTime.Equal(bTime)
}

var _ nativeadmission.Authority = RootAuthority{}

// Delivery wires an already-owned, claimed transport to P3's local journal and
// server receipt/final sinks. Its lifetime belongs to the owning supervisor;
// no viewer is retained here and detach cannot close or release the writer.
// Construction does not launch/adopt a process or issue any authority.
type Delivery struct {
	mu        chan struct{}
	binding   nativecontract.Binding
	identity  nativecontract.NativeIdentity
	journal   *nativereceipts.Store
	transport nativecontract.NativeTransport
	authority nativereceipts.WriteAuthority
	reports   nativereceipts.ReportSink
	finals    nativecontract.FinalSink
	retention func(context.Context, nativecontract.FinalPublish) (nativecontract.FinalDrainGrant, nativereceipts.RetentionApproval, error)
	release   func(context.Context, nativecontract.Binding, nativecontract.ExitEvidence) error
	now       func() time.Time
	validator *nativecontract.Validator
}

type DeliveryOptions struct {
	Binding   nativecontract.Binding
	Journal   *nativereceipts.Store
	Transport nativecontract.NativeTransport
	Authority nativereceipts.WriteAuthority
	Reports   nativereceipts.ReportSink
	Finals    nativecontract.FinalSink
	// Retention revalidates current explicit local-retention approval. The
	// final sink independently revalidates original actor/drain/chat authority
	// in the actual final write transaction. A policy label is not consent.
	Retention func(context.Context, nativecontract.FinalPublish) (nativecontract.FinalDrainGrant, nativereceipts.RetentionApproval, error)
	// Release settles server/local reservations only after owned root/group
	// exit AND the exit receipt have been persisted. Failure retains the hold.
	Release func(context.Context, nativecontract.Binding, nativecontract.ExitEvidence) error
	Now     func() time.Time
}

func NewDelivery(o DeliveryOptions) (*Delivery, error) {
	if o.Journal == nil || o.Transport == nil || o.Authority == nil || o.Reports == nil || o.Finals == nil || o.Retention == nil || o.Release == nil {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if err := nativereceipts.Validate(v, "NativeBinding", o.Binding); err != nil {
		return nil, err
	}
	i := o.Transport.Identity()
	if i.RuntimeEpoch != o.Binding.RuntimeEpoch || !nativereceipts.ValidDigest(i.ProcessIdentityDigest) || i.PID < 1 || i.StartedAt.IsZero() {
		return nil, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	b := o.Binding
	if b.RunID != nil {
		id := *b.RunID
		b.RunID = &id
	}
	return &Delivery{mu: make(chan struct{}, 1), binding: b, identity: i, journal: o.Journal, transport: o.Transport, authority: o.Authority, reports: o.Reports, finals: o.Finals, retention: o.Retention, release: o.Release, now: o.Now, validator: v}, nil
}

// Send never retries a possible native write. A failed report returns its real
// error alongside the native outcome, preserving the journal for bounded flush.
func (d *Delivery) Send(ctx context.Context, actor, operation string, in nativecontract.Input) (nativecontract.Receipt, error) {
	ctx, unlock, err := d.lock(ctx)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	defer unlock()
	if !d.binding.Equal(in.ExpectedBinding) {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonWrongGeneration)
	}
	w := nativereceipts.InputWriter{Journal: d.journal, Transport: d.transport, Authority: d.authority}
	r, err := w.Send(ctx, actor, operation, in)
	if err != nil {
		return r, err
	}
	_, err = d.journal.FlushReports(ctx, d.binding, 100, d.reports)
	return r, err
}

// Observe accepts only the qualified owned provider reader's correlated
// evidence. It cannot turn token chunks, queueing, or another input into a turn.
func (d *Delivery) Observe(ctx context.Context, operation string, identity nativecontract.NativeIdentity, evidence nativecontract.NativeEvidence) (nativecontract.Receipt, error) {
	ctx, unlock, err := d.lock(ctx)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	defer unlock()
	if identity != d.identity || d.transport.Identity() != d.identity {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	r, err := d.journal.Observe(ctx, d.binding, operation, evidence)
	if err != nil {
		return r, err
	}
	_, err = d.journal.FlushReports(ctx, d.binding, 100, d.reports)
	return r, err
}

// Final accepts the provider's explicit final AFTER exact completion evidence.
// A provider that emits its final before completion keeps it in its bounded RAM
// reader until that evidence arrives; this method never assembles token chunks.
func (d *Delivery) Final(ctx context.Context, operation string, in nativecontract.FinalPublish) (nativereceipts.FinalStatus, error) {
	ctx, unlock, err := d.lock(ctx)
	if err != nil {
		return nativereceipts.FinalStatus{}, err
	}
	defer unlock()
	if !d.binding.Equal(in.ExpectedBinding) {
		return nativereceipts.FinalStatus{}, refuse(nativecontract.ReasonWrongGeneration)
	}
	if len(in.Text) > nativereceipts.MaxTextBytes {
		return nativereceipts.FinalStatus{}, refuse(nativecontract.ReasonBufferFull)
	}
	completed, err := d.journal.Get(d.binding, operation)
	if err != nil {
		return nativereceipts.FinalStatus{}, err
	}
	grant, approval, err := d.retention(ctx, in)
	if err != nil {
		return nativereceipts.FinalStatus{}, err
	}
	status, err := d.journal.RetainFinal(ctx, in, grant, approval, completed)
	if err != nil {
		return status, err
	}
	if _, err = d.journal.FlushReports(ctx, d.binding, 100, d.reports); err != nil {
		return status, err
	}
	return d.journal.RetryFinal(ctx, in.FinalID, d.finals)
}

func (d *Delivery) Interrupt(ctx context.Context, actor, operation string, in nativecontract.Interrupt) (nativecontract.Receipt, error) {
	ctx, unlock, err := d.lock(ctx)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	defer unlock()
	if !d.binding.Equal(in.ExpectedBinding) {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonWrongGeneration)
	}
	if len(in.RequestID) > 36 || len(in.ExpectedTurnRef) > 128 {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonBufferFull)
	}
	if err := nativereceipts.Validate(d.validator, "NativeInterrupt", in); err != nil {
		return nativecontract.Receipt{}, err
	}
	page, err := d.journal.Page(d.binding, "", 100)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	if page.Truncated {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonBufferFull)
	}
	active := false
	for _, receipt := range page.Items {
		active = active || receipt.Operation == "input" && receipt.State == "turn_started" && receipt.TurnRef != nil && *receipt.TurnRef == in.ExpectedTurnRef
	}
	if !active {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	intent, err := d.controlIntent(actor, operation, in.RequestID, "interrupt", in, in.ExpiresAt)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	if err = d.beginControl(ctx, intent); err != nil {
		return nativecontract.Receipt{}, err
	}
	e, err := d.transport.Interrupt(ctx, in)
	if err != nil {
		return d.uncertain(ctx, operation, err)
	}
	r, err := d.journal.Observe(ctx, d.binding, operation, e)
	if err != nil {
		return d.uncertain(ctx, operation, err)
	}
	_, err = d.journal.FlushReports(ctx, d.binding, 100, d.reports)
	return r, err
}

// Close consumes a trusted supervisor reader sequence, never a wire-supplied
// assertion. A close acknowledgement is insufficient: root/group identity and
// the persisted exit receipt both precede any reservation-release callback.
func (d *Delivery) Close(ctx context.Context, actor, operation string, in nativecontract.Stop, sequence uint64) (nativecontract.Receipt, error) {
	ctx, unlock, err := d.lock(ctx)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	defer unlock()
	if !d.binding.Equal(in.ExpectedBinding) {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonWrongGeneration)
	}
	if len(in.RequestID) > 36 || len(in.Kind) > 32 || len(in.ExpectedProcessIdentityDigest) > 64 {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonBufferFull)
	}
	if err := nativereceipts.Validate(d.validator, "NativeStop", in); err != nil {
		return nativecontract.Receipt{}, err
	}
	if in.Kind != "graceful" || in.ForceAuthorizationID != nil {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonUnsupported)
	}
	if in.ExpectedProcessIdentityDigest != d.identity.ProcessIdentityDigest {
		return nativecontract.Receipt{}, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	intent, err := d.controlIntent(actor, operation, in.RequestID, "stop", in, in.ExpiresAt)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	if err = d.beginControl(ctx, intent); err != nil {
		return nativecontract.Receipt{}, err
	}
	exit, err := d.transport.Close(ctx)
	if err != nil {
		return d.uncertain(ctx, operation, err)
	}
	r, err := d.journal.ConfirmExit(ctx, d.binding, operation, sequence, exit)
	if err != nil {
		return d.uncertain(ctx, operation, err)
	}
	if _, err = d.journal.FlushReports(ctx, d.binding, 100, d.reports); err != nil {
		return r, err
	}
	return r, d.release(ctx, d.binding, exit)
}

// ConfirmExit is called only by the owned supervisor's exit observer. Saved
// identities and close replies cannot call it as a recovery/adoption shortcut.
// It also retries a previously failed exit-report/release without another close.
func (d *Delivery) ConfirmExit(ctx context.Context, operation string, sequence uint64, exit nativecontract.ExitEvidence) (nativecontract.Receipt, error) {
	ctx, unlock, err := d.lock(ctx)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	defer unlock()
	r, err := d.journal.ConfirmExit(ctx, d.binding, operation, sequence, exit)
	if err != nil {
		return r, err
	}
	if _, err = d.journal.FlushReports(ctx, d.binding, 100, d.reports); err != nil {
		return r, err
	}
	return r, d.release(ctx, d.binding, exit)
}

func (d *Delivery) controlIntent(actor, operation, request, kind string, value any, expires time.Time) (nativereceipts.Intent, error) {
	digest, err := nativereceipts.Digest(value)
	b := d.binding
	return nativereceipts.Intent{Receipt: nativecontract.Receipt{RequestID: request, OperationID: operation, Binding: &b, Operation: kind, State: "accepted", SourceSequence: "0", UpdatedAt: d.now().UTC()}, ActorID: actor, PayloadSHA256: digest, ExpiresAt: expires, ProcessIdentityDigest: d.identity.ProcessIdentityDigest}, err
}
func (d *Delivery) beginControl(ctx context.Context, intent nativereceipts.Intent) error {
	if d.transport.Identity() != d.identity {
		return refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	r, err := d.journal.Accept(ctx, intent)
	if err != nil {
		return err
	}
	if r.State != "accepted" && r.State != "queued" {
		return refuse(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if err = d.authority.RequireWrite(ctx, d.binding, intent); err != nil {
		return err
	}
	if _, err = d.journal.BeginWrite(ctx, d.binding, intent.Receipt.OperationID); err != nil {
		return err
	}
	if err = d.authority.RequireWrite(ctx, d.binding, intent); err != nil {
		return err
	}
	if d.transport.Identity() != d.identity {
		return refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return nil
}
func (d *Delivery) uncertain(ctx context.Context, operation string, cause error) (nativecontract.Receipt, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	reason := nativecontract.ReasonInputOutcomeUnconfirmed
	r, err := d.journal.SetIntentState(cleanup, d.binding, operation, "uncertain", &reason)
	return r, errors.Join(cause, err)
}

func (d *Delivery) lock(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	select {
	case d.mu <- struct{}{}:
		return ctx, func() { <-d.mu; cancel() }, nil
	case <-ctx.Done():
		cancel()
		return ctx, nil, ctx.Err()
	}
}
