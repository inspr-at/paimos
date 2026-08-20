// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	StageSpecification  = "specification"
	StageImplementation = "implementation"
	StageQA             = "qa"
	StageDeployment     = "deployment"
	StageVerification   = "verification"
)

var CanonicalStages = []string{
	StageSpecification,
	StageImplementation,
	StageQA,
	StageDeployment,
	StageVerification,
}

var DefaultWeights = map[string]int{
	StageSpecification:  10,
	StageImplementation: 45,
	StageQA:             20,
	StageDeployment:     15,
	StageVerification:   10,
}

var (
	ErrNotFound       = errors.New("delivery not found")
	ErrConflict       = errors.New("delivery conflict")
	ErrUnauthorized   = errors.New("delivery operation unauthorized")
	ErrInvalid        = errors.New("invalid delivery input")
	ErrInvariant      = errors.New("delivery invariant violation")
	ErrStaleAuthority = errors.New("stale delivery authority")
)

type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type Actor struct {
	Type      string
	OpaqueKey string
}

type AuthorizationRequest struct {
	Action          string
	Actor           Actor
	IssueID         int64
	ProjectID       *int64
	PolicyReference string
	ReasonCode      string
	ReasonText      string
}

type Authorizer interface {
	Authorize(context.Context, AuthorizationRequest) error
}

type AuthorizerFunc func(context.Context, AuthorizationRequest) error

func (f AuthorizerFunc) Authorize(ctx context.Context, req AuthorizationRequest) error {
	return f(ctx, req)
}

type ChangeHint struct {
	InternalID       int64
	CursorToken      string
	DeliveryID       int64
	RootIssueID      int64
	DeliveryKey      string
	ProjectIDHint    *int64
	ChangeSequence   int64
	DeliveryRevision int64
	Kind             string
	SourceKind       string
	SourceID         *int64
	SourceSequence   *int64
	ServerReceivedAt string
}

type CommitObserver func(context.Context, ChangeHint)

// Effects accumulates committed-change callbacks for a caller-owned SQL
// transaction. Dispatch must be called only after Commit succeeds. Durable
// change rows are already in the transaction; observers are merely wakeups.
type Effects struct {
	observer CommitObserver
	hints    []ChangeHint
	done     bool
}

func NewEffects(observer CommitObserver) *Effects { return &Effects{observer: observer} }

func (e *Effects) add(h ChangeHint) {
	if e == nil || e.done {
		return
	}
	e.hints = append(e.hints, h)
}

func (e *Effects) Dispatch(ctx context.Context) {
	if e == nil || e.done {
		return
	}
	e.done = true
	if e.observer == nil {
		return
	}
	for _, hint := range e.hints {
		e.observer(ctx, hint)
	}
}

func (e *Effects) Hints() []ChangeHint {
	if e == nil {
		return nil
	}
	return append([]ChangeHint(nil), e.hints...)
}

type Policy struct {
	StageKey        string
	Applicability   string
	Weight          int
	PolicyReference string
	ReasonCode      string
	ReasonText      string
}

func DefaultPolicy() []Policy {
	out := make([]Policy, 0, len(CanonicalStages))
	for _, stage := range CanonicalStages {
		out = append(out, Policy{StageKey: stage, Applicability: "required", Weight: DefaultWeights[stage]})
	}
	return out
}

type Attempt struct {
	ID                int64
	DeliveryID        int64
	AttemptNumber     int64
	PlanRevision      int64
	PreviousAttemptID *int64
	ProjectIDAtStart  *int64
	ReasonCode        string
	ReasonText        string
	CreatedAt         string
	Policies          []Policy
	LinkedRuns        []AttemptRunOutcome
}

// AttemptRunOutcome is the bounded lifecycle audit seam for an immutable run
// link. Logs, prompts, provider payloads, and credentials never enter delivery
// history; a late terminal result updates only this run-owned outcome.
type AttemptRunOutcome struct {
	RunID            int64
	StageKey         string
	ExecutionNumber  int64
	Status           string
	FinishedAt       *string
	LinkedAt         string
	LifecycleEventID *int64
}

type AttemptRequest struct {
	IssueID        int64
	Actor          Actor
	Policies       []Policy
	ReasonCode     string
	ReasonText     string
	IdempotencyKey string
}

type StageRef struct {
	DeliveryID            int64
	AttemptID             int64
	AttemptNumber         int64
	StageKey              string
	ExecutionNumber       int64
	AuthorityEpoch        int64
	ReporterID            int64
	ExecutionStartEventID int64
	BasedOnStageEventID   *int64
}

type StageStartRequest struct {
	IssueID        int64
	AttemptNumber  int64
	StageKey       string
	Reporter       Actor
	ReasonCode     string
	ReasonText     string
	IdempotencyKey string
}

type HandoffRequest struct {
	IssueID         int64
	AttemptNumber   int64
	StageKey        string
	ExecutionNumber int64
	AuthorityEpoch  int64
	From            Actor
	To              Actor
	ReasonCode      string
	ReasonText      string
	IdempotencyKey  string
}

type ProgressResetRequest struct {
	IssueID         int64
	AttemptNumber   int64
	StageKey        string
	ExecutionNumber int64
	AuthorityEpoch  int64
	Actor           Actor
	ReasonCode      string
	ReasonText      string
	IdempotencyKey  string
}

type Evidence struct {
	Type           string
	Outcome        string
	ReferenceKind  string
	ReferenceValue string
	DigestSHA256   string
	AttachmentID   *int64
}

type Blocker struct {
	Key       string
	Class     string
	Summary   string
	HumanWait bool
}

type EstimateEvidence struct {
	Revision   *int64
	Progress   *float64
	ETASeconds *int64
	ETAMin     *int64
	ETAMax     *int64
	Source     string
	Confidence *float64
	Basis      string
}

// FreshnessPolicy is shared by the delivery projection and the active-run
// reconciler. A source that has never emitted a heartbeat is distinct from a
// source whose previously observed heartbeat or estimate has become stale.
type FreshnessPolicy struct {
	FirstSignalTimeout time.Duration
	HeartbeatTimeout   time.Duration
	EstimateTimeout    time.Duration
}

func DefaultFreshnessPolicy() FreshnessPolicy {
	return FreshnessPolicy{
		FirstSignalTimeout: time.Minute,
		HeartbeatTimeout:   90 * time.Second,
		EstimateTimeout:    90 * time.Second,
	}
}

type EstimateSnapshot struct {
	SourceKind       string
	SourceID         int64
	SourceSequence   int64
	Revision         int64
	Progress         *float64
	ETASeconds       *int64
	ETAMin           *int64
	ETAMax           *int64
	Source           string
	Confidence       float64
	Basis            string
	ServerReceivedAt string
}

type ProgressResetBoundary struct {
	StageEventID                int64
	ResetEpoch                  int64
	AuthorityAnchorStageEventID int64
	StageSourceSequenceCutoff   int64
	SourceKind                  string
	AgentRunID                  *int64
	TelemetrySequenceCutoff     *int64
}

type StageReport struct {
	IssueID         int64
	AttemptNumber   int64
	StageKey        string
	ExecutionNumber int64
	AuthorityEpoch  int64
	Reporter        Actor
	IdempotencyKey  string
	SourceSequence  *int64
	Kind            string // semantic, heartbeat, estimate
	State           string
	Activity        string
	NeedsInput      bool
	Blockers        []Blocker
	Evidence        []Evidence
	Estimate        EstimateEvidence
	ReasonCode      string
	ReasonText      string
}

type StageSnapshot struct {
	StageKey                  string
	SortOrder                 int
	Applicability             string
	Weight                    int
	ExecutionNumber           int64
	AuthorityEpoch            int64
	ReporterType              string
	SemanticState             string
	Phase                     string
	Activity                  string
	NeedsInput                bool
	TelemetryBlockerState     string
	CurrentBlockers           []Blocker
	Evidence                  []Evidence
	EligibleSuccess           bool
	SemanticEventID           *int64
	ExecutionStartEventID     *int64
	BasedOnStageEventID       *int64
	CurrentLineage            bool
	PolicySatisfied           bool
	Performed                 bool
	OwnerRunID                *int64
	AuthorityActivationCutoff int64
	AuthorityActivatedAt      *string
	LastHeartbeatAt           *string
	LastSemanticAt            *string
	LatestEstimateAt          *string
	LatestEstimate            *EstimateSnapshot
	ProgressReset             *ProgressResetBoundary
	SignalState               string
	NeverSignaled             bool
	HeartbeatStale            bool
	EstimateStale             bool
	Stale                     bool
	NextFreshnessTransitionAt *string
}

type LegacyRun struct {
	RunID     int64  `json:"run_id"`
	Status    string `json:"status"`
	AgentName string `json:"agent_name"`
}

type Snapshot struct {
	IssueID                     int64
	DeliveryKey                 string
	DeliveryID                  *int64
	Instrumented                bool
	DeliveryRevision            int64
	ChangeSequence              int64
	AttemptID                   *int64
	AttemptNumber               int64
	PlanRevision                int64
	SpecRevision                int64
	State                       string
	AttentionFlags              []string
	PrimaryAttention            string
	Deployed                    bool
	Verified                    bool
	DeploymentPolicySatisfied   bool
	VerificationPolicySatisfied bool
	Unverified                  bool
	Failed                      bool
	FailedNeedsRetry            bool
	Cancelled                   bool
	Stages                      []StageSnapshot
	LegacyActiveRuns            []LegacyRun
}

type RunBootstrap struct {
	IssueID        int64
	RunID          int64
	Mode           string // implementation or draft
	Actor          Actor
	IdempotencyKey string
}

type RunNormalization struct {
	RunID          int64
	Status         string
	CommitSHA      string
	AttachmentID   *int64
	IdempotencyKey string
}
