// SPDX-License-Identifier: AGPL-3.0-only

// Package delivery composes owned native transports with durable metadata and
// explicit final delivery; it never creates or adopts a process.
package delivery

import (
	"context"
	"errors"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"time"
)

func refuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

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

func New(o DeliveryOptions) (*Delivery, error) {
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
