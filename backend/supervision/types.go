// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package supervision implements the provider-neutral supervisory-control
// domain. It deliberately has no HTTP vocabulary: callers map the closed
// errors and projections below onto their own transport.
package supervision

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/controlcontract"
)

const (
	GrantTTL          = 5 * time.Minute
	LeaseTTL          = 90 * time.Second
	LeaseRenewAfter   = 30 * time.Second
	InputTTL          = 60 * time.Second
	ChallengeTTL      = 120 * time.Second
	PullPageSize      = 100
	PullProbePageSize = PullPageSize + 1
)

var (
	ErrInvalid     = errors.New("supervision invalid")
	ErrForbidden   = errors.New("supervision forbidden")
	ErrNotFound    = errors.New("supervision not found")
	ErrConflict    = errors.New("supervision conflict")
	ErrUnavailable = errors.New("supervision unavailable")
	ErrInvariant   = errors.New("supervision invariant")
)

// SafeCode is a transport-safe, closed explanation. It never contains a
// provider error, bearer, API-key material, database text, or arbitrary prose.
type SafeCode string

const (
	CodeInvalidRequest        SafeCode = "invalid_request"
	CodeInvalidOperationKey   SafeCode = "invalid_operation_key"
	CodeInvalidAction         SafeCode = "invalid_action"
	CodeInvalidChoice         SafeCode = "invalid_choice"
	CodeInvalidDevice         SafeCode = "invalid_device"
	CodeForbidden             SafeCode = "forbidden"
	CodeScopeRevoked          SafeCode = "scope_revoked"
	CodeCredentialRevoked     SafeCode = "credential_revoked"
	CodeTargetNotFound        SafeCode = "target_not_found"
	CodeCapabilityUnavailable SafeCode = "capability_unavailable"
	CodeStaleTarget           SafeCode = "stale_target"
	CodeIdempotencyConflict   SafeCode = "idempotency_conflict"
	CodeSemanticConflict      SafeCode = "semantic_conflict"
	CodeAlreadyClaimed        SafeCode = "already_claimed"
	CodeExpired               SafeCode = "expired"
	CodeDependencyUnavailable SafeCode = "dependency_unavailable"
	CodeStorageUnavailable    SafeCode = "storage_unavailable"
	CodeInvariant             SafeCode = "invariant"
)

// Error carries only a sentinel class and a closed safe code. The underlying
// database or provider error is intentionally not retained in the value that
// crosses the domain boundary.
type Error struct {
	kind error
	code SafeCode
}

func (e *Error) Error() string  { return string(e.code) }
func (e *Error) Unwrap() error  { return e.kind }
func (e *Error) Code() SafeCode { return e.code }

func domainError(kind error, code SafeCode) error { return &Error{kind: kind, code: code} }

// ErrorCode returns a closed code for a supervision error.
func ErrorCode(err error) SafeCode {
	var domain *Error
	if errors.As(err, &domain) {
		return domain.Code()
	}
	return CodeInvariant
}

// IsCode reports whether err is a supervision error with the exact closed code.
func IsCode(err error, code SafeCode) bool { return ErrorCode(err) == code }

type Action string
type CommandStatus string
type Outcome string
type SafeReason string
type ChallengeTemplate string
type InputKind string
type InputPromptTemplate string
type InputResponseKind string
type RuntimeState string
type ReconcileMode string

const (
	// ReconcileActor expires only commands and grants owned by the caller's
	// exact immutable actor credential.
	ReconcileActor ReconcileMode = "actor"
	// ReconcileRunner expires or terminalizes only state belonging to the
	// caller's exact API-key, device, and lease revision lineage.
	ReconcileRunner ReconcileMode = "runner"
)

func Actions() []Action {
	values := controlcontract.Actions()
	out := make([]Action, len(values))
	for i, value := range values {
		out[i] = Action(value)
	}
	return out
}

func CommandStatuses() []CommandStatus {
	values := controlcontract.CommandStatuses()
	out := make([]CommandStatus, len(values))
	for i, value := range values {
		out[i] = CommandStatus(value)
	}
	return out
}

func SafeReasons() []SafeReason {
	values := controlcontract.SafeReasons()
	out := make([]SafeReason, len(values))
	for i, value := range values {
		out[i] = SafeReason(value)
	}
	return out
}

// DisplayValues is closed typed data used to render a server-generated
// challenge. Values are safe identifiers/enums, never provider prose.
type DisplayValues struct {
	IssueKey        string
	DeliveryKey     string
	Priority        string
	RunID           int64
	InputKind       InputKind
	ChoiceOrdinal   int
	ChoiceCode      string
	RuntimeState    RuntimeState
	RuntimeRevision int64
}

type GrantProjection struct {
	GrantID     string
	Revision    int64
	DeliveryKey string
	IssueKey    string
	Actions     []Action
	Targets     []GrantTarget
	ExpiresAt   time.Time
}

// GrantTarget is a live, authorized and secret-free description of one exact
// command target. It is deliberately not part of the stable grant digest.
type GrantTarget struct {
	Action               Action
	RunID                int64
	RuntimeState         RuntimeState
	RuntimeRevision      int64
	InputRequestID       string
	InputRequestRevision int64
	InputKind            InputKind
	OptionCodes          []string
}

type LeaseProjection struct {
	LeaseID     string
	Revision    int64
	DeliveryKey string
	IssueKey    string
	Actions     []Action
	ExpiresAt   time.Time
	Target      RunnerTarget
}

type InputRequestProjection struct {
	RequestID      string
	Revision       int64
	Kind           InputKind
	PromptTemplate InputPromptTemplate
	OptionCodes    []string
	ExpiresAt      time.Time
	Target         RunnerTarget
}

type CommandProjection struct {
	CommandID         string
	StatusRevision    int64
	Action            Action
	Status            CommandStatus
	Outcome           Outcome
	Reason            SafeReason
	ChallengeTemplate ChallengeTemplate
	Display           DisplayValues
	ExpiresAt         time.Time
}

// RunnerTarget is the exact safe execution binding carried by runner effects.
// It contains no raw agent-run row, actor identity, or credential material.
type RunnerTarget struct {
	DeliveryID                 int64
	DeliveryKey                string
	DeliveryRevision           int64
	RootIssueID                int64
	IssueRevision              int64
	AttemptID                  int64
	AttemptNumber              int64
	PlanRevision               int64
	StageKey                   string
	ExecutionNumber            int64
	ExecutionStartStageEventID int64
	AuthorityEpoch             int64
	AuthorityStageEventID      int64
	ReporterID                 int64
	RunID                      int64
}

type EffectProjection struct {
	OutboxID        int64
	CommandID       string
	Action          Action
	EffectSequence  int64
	LeaseID         string
	LeaseRevision   int64
	Target          RunnerTarget
	InputRequestID  string
	InputRevision   int64
	InputResponse   InputResponseKind
	ChoiceOrdinal   int
	ChoiceCode      string
	RuntimeRevision int64
}

type PullProjection struct {
	SnapshotHighWater int64
	NextCursor        int64
	HasMore           bool
	Effects           []EffectProjection
}

type ReconcileProjection struct {
	ExpiredCommands  int
	ExpiredGrants    int
	ExpiredLeases    int
	ExpiredInputs    int
	CancelledInputs  int
	TerminalInputs   int
	AbandonedEffects int
	UnknownOutcomes  int
}

type GrantIssueRequest struct {
	DeliveryID         int64
	OperationKeyDigest [32]byte
}

type GrantGetRequest struct {
	GrantID  string
	Revision int64
}

type GrantRevokeRequest struct {
	GrantID            string
	Revision           int64
	OperationKeyDigest [32]byte
}

type LeaseIssueRequest struct {
	RunID              int64
	DeviceID           string
	SupportedActions   []Action
	OperationKeyDigest [32]byte
}

type LeaseRenewRequest struct {
	LeaseID            string
	Revision           int64
	DeviceID           string
	SupportedActions   []Action
	OperationKeyDigest [32]byte
}

type LeaseRevokeRequest struct {
	LeaseID            string
	Revision           int64
	DeviceID           string
	OperationKeyDigest [32]byte
}

type InputCreateRequest struct {
	LeaseID            string
	LeaseRevision      int64
	RequestID          string // empty starts a lineage; non-empty supersedes its current revision
	Kind               InputKind
	PromptTemplate     InputPromptTemplate
	OptionCodes        []string
	OperationKeyDigest [32]byte
}

type CommandCreateRequest struct {
	GrantID              string
	GrantRevision        int64
	Action               Action
	Priority             string
	RunID                int64
	InputRequestID       string
	InputRequestRevision int64
	InputResponse        InputResponseKind
	ChoiceOrdinal        int
	RuntimeRevision      int64
	OperationKeyDigest   [32]byte
}

type CommandGetRequest struct {
	CommandID string
}

type CommandConfirmRequest struct {
	CommandID          string
	StatusRevision     int64
	OperationKeyDigest [32]byte
}

type CommandWithdrawRequest struct {
	CommandID          string
	StatusRevision     int64
	OperationKeyDigest [32]byte
}

type PullRequest struct {
	LeaseID       string
	LeaseRevision int64
	DeviceID      string
	Cursor        int64
}

type ClaimRequest struct {
	CommandID          string
	LeaseID            string
	LeaseRevision      int64
	EffectSequence     int64
	DeviceID           string
	OperationKeyDigest [32]byte
}

type ResultRequest struct {
	CommandID          string
	LeaseID            string
	LeaseRevision      int64
	EffectSequence     int64
	ClaimSequence      int64
	ResultSequence     int64
	DeviceID           string
	Outcome            Outcome
	Reason             SafeReason
	OperationKeyDigest [32]byte
}

type ReconcileRequest struct {
	Mode          ReconcileMode
	Limit         int
	LeaseID       string
	LeaseRevision int64
	DeviceID      string
}

// SynchronousMutator owns the existing issue/run history and mutation truth.
// Every call receives the same transaction as command/result/audit writes.
type SynchronousMutator interface {
	SetIssuePriorityTx(context.Context, *sql.Tx, PriorityMutation) error
	CancelQueuedRunTx(context.Context, *sql.Tx, RunCancellationMutation) error
	CancelRunningRunTx(context.Context, *sql.Tx, RunCancellationMutation) error
}

type PriorityMutation struct {
	IssueID            int64
	ExpectedRevision   int64
	ExpectedETagDigest [32]byte
	Priority           string
	ActorUserID        int64
	CommandID          string
}

type RunCancellationMutation struct {
	IssueID       int64
	RunID         int64
	ExpectedState string
	ActorUserID   int64
	CommandID     string
}

// ControlChange is the bounded PAI-804 durable hint attribution.
type ControlChange struct {
	IssueID   int64
	RunID     int64
	CommandID string
	Action    Action
}

// CommitWake is an opaque safe handle. It must not contain a callback because
// callbacks can accidentally fire before the transaction commits.
type CommitWake struct {
	ID int64
}

// ChangeRecorder appends or captures exactly one PAI-804 hint inside the
// caller's transaction. Wake is invoked only after Commit succeeds.
type ChangeRecorder interface {
	RecordControlChangeTx(context.Context, *sql.Tx, ControlChange) (CommitWake, error)
	WakeControlChange(context.Context, CommitWake)
}

type Clock interface{ Now() time.Time }
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type IDSource interface{ NewID() string }
type IDSourceFunc func() string

func (f IDSourceFunc) NewID() string { return f() }

type Options struct {
	Clock   Clock
	IDs     IDSource
	Mutator SynchronousMutator
	Changes ChangeRecorder
}

const (
	ScopeActorWrite = "agent-controls:write"
	ScopeRunner     = "agent-controls:runner"
)

// The explicit principal argument ensures callers cannot substitute a user ID
// or bearer string for the authenticated, immutable credential identity.
type Principal = auth.Principal
