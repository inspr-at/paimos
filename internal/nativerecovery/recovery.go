// SPDX-License-Identifier: AGPL-3.0-only

// Package nativerecovery reconciles an owned writer before deliberate resume.
// I2 supplies the owning supervisor ports; this package never adopts or signals
// a saved PID, reads vendor history, or reconstructs native input/approvals.
package nativerecovery

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
)

const (
	MaxSessions = 128
	MaxAttempts = 32
)

type Cause string

const (
	ViewerDisconnected Cause = "viewer_disconnected"
	APIUnavailable     Cause = "api_unavailable"
	DaemonLost         Cause = "daemon_lost"
	ChildExited        Cause = "child_exited"
)

// History is a qualified LOCAL mapping, not a caller-supplied native session ID.
// Prepare/Revalidate must freshly resolve it against the same owner account,
// workspace, profile and vendor history. No credentials or transcript enter it.
type History struct {
	ConversationRef, AccountID, WorkspaceID, ResumeRef string
	MappingDigest, AccountBindingDigest                string
	ProfileRevision                                    int64
}

// Budget is aggregate usage in the ORIGINAL grant window, across generations.
// Unknown usage cannot authorize bounded continuation. StartedAt is never reset.
type Budget struct {
	StartedAt             time.Time
	TurnsUsed, TokensUsed int64
	UsageKnown            bool
}

type Snapshot struct {
	Writer        nativecontract.WriterRecord
	Revision      int64
	OwnerPersonID string
	Policy        nativecontract.Policy
	History       History
	Budget        Budget
	Cause         Cause
}

// Absence comes from the qualified owner probe, never a decoded request. An
// unknown possible fork needs attempt-scoped absence, not "PID not found".
// ConfirmAbsent independently checks the attestation under the ownership fence.
type Absence struct {
	Binding           nativecontract.Binding
	AttemptID         string
	Kind              string // never_forked, attempt_absent, owned_exit
	AttestationDigest string
	Evidence          nativecontract.ExitEvidence
}

// Successor carries fresh current server admission and runtime/harness proof.
// AuthorizationDeadline is a MONOTONIC deadline derived from the claim TTL minus
// elapsed request time, not a server wall-clock timestamp or a renewable lease.
type Successor struct {
	Launch                  nativecontract.LaunchSpec
	History                 History
	RuntimeProofDigest      string
	ReconciledEffectsDigest string
	AuthorizationDeadline   time.Time
	ClaimKind               string
	LaunchAuthorized        bool
}

type Effect struct {
	OperationID, Operation, State, SourceSequence string
}

// Effects lists possible native writes which are not proven complete. Context
// resume does not establish exactly-once dispatch, merge or deployment.
type Effects struct {
	Items  []Effect
	Digest string
}

// Runtime is trusted owning-supervisor context, never HTTP request authority.
// WithWriter holds the SAME lock as input, access/controller changes, dispatch,
// takeover and resume, and honors ctx. Read consults the existing possible-fork
// journal AND shared ledger; absence from a RAM registry is not evidence.
// ExpireCallbacks invalidates every old approval/controller, without replay.
// ProveAbsent only observes qualified provenance; it never signals saved PIDs.
// ConfirmAbsent durably settles the existing journal/ledger only on independently
// verified no-fork or exact root/group absence; failures keep occupancy.
// Prepare and Revalidate obtain current policy/admission and RequireTx under the
// final server access fence, including fresh harness/worker proof, account/model,
// local history and any explicit reconciliation of Effects. Attempt rechecks
// that authority at its final fork boundary, journals possible fork in the
// EXISTING journal/ledger before Open, and retains/registers every uncertain fork
// before returning. It commits the new binding/proof only for the owned handle.
// All methods are bounded by ctx; WithWriter must not release its fence while a
// callback is still running. The host also encloses resume in drain.WithAdmission.
type Runtime interface {
	WithWriter(context.Context, nativecontract.Binding, func(context.Context) error) error
	Read(context.Context, nativecontract.Binding) (Snapshot, error)
	ExpireCallbacks(context.Context, nativecontract.Binding) error
	ProveAbsent(context.Context, nativecontract.WriterRecord) (Absence, error)
	ConfirmAbsent(context.Context, nativecontract.WriterRecord, Absence) error
	Prepare(context.Context, nativecontract.Actor, nativecontract.ResumeIntent, Effects) (Successor, error)
	Revalidate(context.Context, nativecontract.Actor, nativecontract.ResumeIntent, Successor, Effects) error
	Attempt(context.Context, Successor) (nativecontract.NativeIdentity, error)
}

type Config struct {
	Runtime  Runtime
	State    *agentsetup.Store // dedicated private directory; no vendor/account state
	Receipts *nativereceipts.Store
	Now      func() time.Time
}

type Coordinator struct {
	cfg       Config
	validator *nativecontract.Validator
	poisoned  bool // accessed only under the private recovery file lock
}

// Status is content-free owner status, not a new public contract/schema.
type Status struct {
	Binding                nativecontract.Binding
	Cause                  Cause
	State                  string
	Reason                 nativecontract.Reason
	ViewerMayReconnect     bool
	ExitConfirmed          bool
	ReconciliationRequired bool
	Effects                Effects
	PendingFinals          int
}

func New(cfg Config) (*Coordinator, error) {
	if cfg.Runtime == nil || cfg.State == nil || cfg.Receipts == nil {
		return nil, fail(nativecontract.ReasonQualificationMissing)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	return &Coordinator{cfg: cfg, validator: v}, nil
}

func fail(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func (c *Coordinator) fenced(ctx context.Context, b nativecontract.Binding, fn func(context.Context) error) error {
	if err := c.validateBinding(b); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	return c.cfg.Runtime.WithWriter(ctx, b, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock, err := c.cfg.State.LockNamed("recovery.lock")
		if err != nil {
			return err
		}
		defer lock.Close()
		if c.poisoned {
			return fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
		return fn(ctx)
	})
}

func (c *Coordinator) status(b nativecontract.Binding, cause Cause) (Status, error) {
	s := Status{Binding: cloneBinding(b), Cause: cause, State: "recovering"}
	e, err := c.effects(b)
	if err != nil {
		return s, err
	}
	s.Effects, s.ReconciliationRequired = e, len(e.Items) > 0
	for _, f := range c.cfg.Receipts.FinalStatuses() {
		if f.State == "pending" {
			s.PendingFinals++
		}
	}
	return s, nil
}

// Reconcile never launches. View loss changes only the viewer; API loss retains
// the current writer and delivery data. Daemon/child loss expires old callbacks
// and settles occupancy only after independently verified absence.
func (c *Coordinator) Reconcile(ctx context.Context, b nativecontract.Binding, cause Cause) (Status, error) {
	var status Status
	if cause != ViewerDisconnected && cause != APIUnavailable && cause != DaemonLost && cause != ChildExited {
		return status, fail(nativecontract.ReasonInvalidInput)
	}
	err := c.fenced(ctx, b, func(ctx context.Context) error {
		snapshot, err := c.cfg.Runtime.Read(ctx, b)
		if err != nil {
			return err
		}
		if err = c.validateSnapshot(snapshot, b); err != nil {
			return err
		}
		status, err = c.status(b, cause)
		if err != nil {
			return err
		}
		if cause == ViewerDisconnected {
			status.State, status.ViewerMayReconnect = "viewer_disconnected", true
			return nil
		}
		if cause == APIUnavailable {
			status.State = "api_unavailable"
			return nil
		}
		if err = c.settle(ctx, snapshot.Writer); err != nil {
			status.State, status.Reason = "ownership_lost", nativecontract.ReasonOwnershipUnconfirmed
			return err
		}
		status.State, status.ExitConfirmed = "awaiting_continue", true
		return nil
	})
	return status, err
}

func (c *Coordinator) settle(ctx context.Context, writer nativecontract.WriterRecord) error {
	if err := c.cfg.Runtime.ExpireCallbacks(ctx, cloneBinding(writer.Binding)); err != nil {
		return err
	}
	absence, err := c.cfg.Runtime.ProveAbsent(ctx, writer)
	if err != nil {
		return err
	}
	if err = c.validateAbsence(writer, absence); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return c.cfg.Runtime.ConfirmAbsent(ctx, writer, absence)
}

// Resume defaults to an explicit owner action. Bounded restart uses at most one
// durable attempt in the original grant window. Neither path replays old inputs,
// approvals or finals; finals keep P3a's independent final-only authority.
func (c *Coordinator) Resume(ctx context.Context, actor nativecontract.Actor, in nativecontract.ResumeIntent) (Status, error) {
	status := Status{Binding: cloneBinding(in.ExpectedBinding), State: "recovering"}
	if err := c.validateResume(actor, in); err != nil {
		return status, err
	}
	err := c.fenced(ctx, in.ExpectedBinding, func(ctx context.Context) error {
		journal, err := c.read(in.ExpectedBinding)
		if err != nil {
			return err
		}
		digest, err := nativereceipts.Digest([]any{in, actor.TenantID, actor.PrincipalID, actor.OwnerPersonID, actor.PrincipalKind, actor.GrantID, actor.GrantRevision, actor.KeyCeiling})
		if err != nil {
			return err
		}
		for _, attempt := range journal.Attempts {
			if attempt.RequestID != in.RequestID {
				continue
			}
			if attempt.Digest != digest {
				return fail(nativecontract.ReasonIdentityMismatch)
			}
			status.Binding = cloneBinding(attempt.Next)
			if attempt.Complete {
				status.State, status.ExitConfirmed = "resumed", true
				// Return the predecessor's unresolved effects honestly on replay.
				prior, err := c.status(in.ExpectedBinding, "")
				status.Effects, status.ReconciliationRequired, status.PendingFinals = prior.Effects, prior.ReconciliationRequired, prior.PendingFinals
				return err
			}
			return fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
		if len(journal.Attempts) == MaxAttempts {
			return fail(nativecontract.ReasonBufferFull)
		}
		snapshot, err := c.cfg.Runtime.Read(ctx, in.ExpectedBinding)
		if err != nil {
			return err
		}
		if err = c.validateSnapshot(snapshot, in.ExpectedBinding); err != nil {
			return err
		}
		if snapshot.Revision != in.ExpectedRevision {
			return fail(nativecontract.ReasonStaleRevision)
		}
		if actor.OwnerPersonID != snapshot.OwnerPersonID {
			return fail(nativecontract.ReasonWrongPrincipal)
		}
		status, err = c.status(in.ExpectedBinding, snapshot.Cause)
		if err != nil {
			return err
		}
		if in.Reason == "approved_restart" {
			if err = c.bounded(snapshot, actor, journal.AutoUsed); err != nil {
				return err
			}
			// Uncertain external effects need reconciliation, never automatic
			// context continuation. Qualified late receipts may resolve them.
			if status.ReconciliationRequired {
				return fail(nativecontract.ReasonInputOutcomeUnconfirmed)
			}
		}
		if err = c.settle(ctx, snapshot.Writer); err != nil {
			return err
		}
		status.ExitConfirmed = true
		next, err := c.cfg.Runtime.Prepare(ctx, actor, in, status.Effects)
		if err != nil {
			return err
		}
		defer next.Launch.Tools.Close()
		if err = c.validateSuccessor(snapshot, actor, in, next, status.Effects); err != nil {
			return err
		}
		if err = c.cfg.Runtime.Revalidate(ctx, actor, in, next, status.Effects); err != nil {
			return err
		}
		journal.AutoUsed = journal.AutoUsed || in.Reason == "approved_restart"
		journal.Attempts = append(journal.Attempts, attempt{RequestID: in.RequestID, Digest: digest, Previous: cloneBinding(in.ExpectedBinding), Next: cloneBinding(next.Launch.Binding)})
		// Commit the continuation consumption before the owner's possible-fork
		// journal/Open. A crash in either window cannot repeat this request.
		if err = c.save(journal); err != nil {
			return err
		}
		// Disk waits cannot carry a grant past expiry or a changed effect set.
		if err = ctx.Err(); err != nil {
			return err
		}
		current, err := c.effects(in.ExpectedBinding)
		if err != nil {
			return err
		}
		if current.Digest != status.Effects.Digest {
			return fail(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
		if err = c.validateSuccessor(snapshot, actor, in, next, current); err != nil {
			return err
		}
		if err = c.cfg.Runtime.Revalidate(ctx, actor, in, next, current); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = c.validateSuccessor(snapshot, actor, in, next, current); err != nil {
			return err
		}
		identity, err := c.cfg.Runtime.Attempt(ctx, next)
		if err != nil {
			return err
		}
		if identity.RuntimeEpoch != next.Launch.Binding.RuntimeEpoch || identity.PID < 1 ||
			!nativereceipts.ValidDigest(identity.ProcessIdentityDigest) || identity.StartedAt.IsZero() || identity.StartedAt.After(c.cfg.Now()) {
			return fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		journal.Attempts[len(journal.Attempts)-1].Complete = true
		if err = c.save(journal); err != nil {
			return err
		}
		status.Binding, status.State = cloneBinding(next.Launch.Binding), "resumed"
		return nil
	})
	if err != nil {
		status.State = "recovery_needed"
		var fault *nativecontract.ValidationError
		if errors.As(err, &fault) {
			status.Reason = fault.Reason
		} else {
			status.Reason = nativecontract.ReasonOwnershipUnconfirmed
		}
	}
	return status, err
}

func (Snapshot) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native recovery snapshot is local authority")
}
func (Absence) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native absence proof is local authority")
}
func (Successor) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native successor is local authority")
}
func (History) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native history mapping is local authority")
}
