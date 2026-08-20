// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package deliverytrust

import (
	"errors"
	"time"
)

const (
	SchemaVersion          = 1
	EstimatorPolicyVersion = 1
	MaxHistorySamples      = 100
	MaxETASeconds          = int64(365 * 24 * 60 * 60)
)

var ErrInvalidInput = errors.New("invalid delivery trust input")

type StageKey string

const (
	StageSpecification  StageKey = "specification"
	StageImplementation StageKey = "implementation"
	StageQA             StageKey = "qa"
	StageDeployment     StageKey = "deployment"
	StageVerification   StageKey = "verification"
)

var canonicalStages = [...]StageKey{
	StageSpecification,
	StageImplementation,
	StageQA,
	StageDeployment,
	StageVerification,
}

func Stages() []StageKey {
	return append([]StageKey(nil), canonicalStages[:]...)
}

func DefaultWeight(stage StageKey) int {
	switch stage {
	case StageSpecification:
		return 10
	case StageImplementation:
		return 45
	case StageQA:
		return 20
	case StageDeployment:
		return 15
	case StageVerification:
		return 10
	default:
		return 0
	}
}

type ReporterKind string

const (
	ReporterAgentRun ReporterKind = "agent_run"
	ReporterExternal ReporterKind = "external"
	ReporterUser     ReporterKind = "user"
	ReporterSystem   ReporterKind = "system"
	ReporterUnknown  ReporterKind = "unknown"
)

type EstimateSource string

const (
	SourceAgent    EstimateSource = "agent"
	SourceAdapter  EstimateSource = "adapter"
	SourceProvider EstimateSource = "provider"
	SourceTool     EstimateSource = "tool"
	SourceExternal EstimateSource = "external"
	SourceHistory  EstimateSource = "history"
)

type CompletionStatus string

const (
	CompletionPending   CompletionStatus = "pending"
	CompletionSucceeded CompletionStatus = "succeeded"
	CompletionFailed    CompletionStatus = "failed"
	CompletionCancelled CompletionStatus = "cancelled"
	CompletionConflict  CompletionStatus = "conflict"
)

type ConfidenceLabel string

const (
	ConfidenceUnknown ConfidenceLabel = "unknown"
	ConfidenceLow     ConfidenceLabel = "low"
	ConfidenceMedium  ConfidenceLabel = "medium"
	ConfidenceHigh    ConfidenceLabel = "high"
)

type SuppressionCode string

const (
	SuppressTerminalComplete   SuppressionCode = "terminal_complete"
	SuppressCancelled          SuppressionCode = "cancelled"
	SuppressTerminalFailed     SuppressionCode = "terminal_failed"
	SuppressWaitingOnHuman     SuppressionCode = "waiting_on_human"
	SuppressBlocked            SuppressionCode = "blocked"
	SuppressStale              SuppressionCode = "stale"
	SuppressUnknownReporter    SuppressionCode = "unknown_reporter"
	SuppressNoSignal           SuppressionCode = "no_signal"
	SuppressEstimateExpired    SuppressionCode = "estimate_expired"
	SuppressOutlierHeavy       SuppressionCode = "outlier_heavy"
	SuppressInsufficientBasis  SuppressionCode = "insufficient_basis"
	SuppressMissingContributor SuppressionCode = "missing_contributor"
)

type Flag string

const (
	FlagSourceBackslideIgnored   Flag = "source_backslide_ignored"
	FlagAgentHistoryDisagreement Flag = "agent_history_disagreement"
	FlagHistoryQualityDowngraded Flag = "history_quality_downgraded"
	FlagHistoryOutlierHeavy      Flag = "history_outlier_heavy"
	FlagHistoryInsufficientBasis Flag = "history_insufficient_basis"
	FlagOwnerEstimateInvalid     Flag = "owner_estimate_invalid"
	FlagOwnerEstimateExpired     Flag = "owner_estimate_expired"
	FlagDeployedUnverified       Flag = "deployed_unverified"
	FlagFailedNeedsRetry         Flag = "failed_needs_retry"
)

type ContributorKind string

const (
	ContributorAgent        ContributorKind = "agent"
	ContributorHistory      ContributorKind = "history"
	ContributorAgentHistory ContributorKind = "agent_history"
)

// Scope is an immutable authority/reset identity supplied by the PAI-802
// integration. ReporterID and RunLinkID participate in matching and hashing,
// but are never copied to Output.
type Scope struct {
	AttemptID   string
	PlanID      string
	ExecutionID string
	AuthorityID string
	ResetID     string
	ReporterID  string
	RunLinkID   string
}

type PublicScope struct {
	AttemptID   string `json:"attempt_id"`
	PlanID      string `json:"plan_id"`
	ExecutionID string `json:"execution_id"`
	AuthorityID string `json:"authority_id"`
	ResetID     string `json:"reset_id"`
}

type StagePolicy struct {
	Stage    StageKey
	Required bool
	Weight   int
	Identity string
}

func DefaultPolicy() []StagePolicy {
	policy := make([]StagePolicy, 0, len(canonicalStages))
	for _, stage := range canonicalStages {
		policy = append(policy, StagePolicy{
			Stage: stage, Required: true, Weight: DefaultWeight(stage),
			Identity: "default:" + string(stage),
		})
	}
	return policy
}

type CompletionInput struct {
	Status             CompletionStatus
	Eligible           bool
	SemanticIdentity   string
	EvidenceIdentities []string
}

type EstimateRange struct {
	MinimumSeconds int64
	MaximumSeconds int64
	PointSeconds   *int64
}

// EstimateFact contains one immutable fact after ingestion validation. Scope
// matching is still repeated here so a stale fact cannot become authoritative
// through an integration mistake.
type EstimateFact struct {
	Identity         string
	Reporter         ReporterKind
	Scope            Scope
	Revision         uint64
	Sequence         uint64
	Source           EstimateSource
	ServerReceivedAt time.Time
	Confidence       float64
	Basis            string
	ProgressPercent  *float64
	ETA              *EstimateRange
}

type DurationSample struct {
	Identity         string
	StageExecutionID uint64
	ProjectIdentity  string
	Stage            StageKey
	PolicyVersion    int
	CompletedAt      time.Time
	FullLeadSeconds  int64
	ActiveSeconds    int64
	BlockedSeconds   int64
	HumanWaitSeconds int64
}

// StageSignals are already-authorized current-stage facts. TransitionAt holds
// liveness/freshness deadlines; the evaluator chooses the earliest future one.
type StageSignals struct {
	// SemanticIdentity is the immutable current-lineage activity/event fact.
	// It is hashed but never returned. Clock-derived booleans and transition
	// instants deliberately do not participate in immutable trust identity.
	SemanticIdentity string
	WaitingOnHuman   bool
	Blocked          bool
	Stale            bool
	UnknownReporter  bool
	NoSignal         bool
	TransitionAt     []time.Time
}

type StageInput struct {
	Stage              StageKey
	Scope              Scope
	Reporter           ReporterKind
	ExecutionStartedAt *time.Time
	Completion         CompletionInput
	Signals            StageSignals
	Estimates          []EstimateFact
	History            []DurationSample
}

type Input struct {
	DeliveryIdentity string
	ProjectIdentity  string
	Instrumented     bool
	CalculatedAt     time.Time
	PolicyVersion    int
	Policy           []StagePolicy
	Stages           []StageInput
}

type SourceAttribution struct {
	Identity     string          `json:"identity"`
	ReporterKind ReporterKind    `json:"reporter_kind"`
	Source       EstimateSource  `json:"source"`
	Revision     uint64          `json:"revision"`
	Sequence     uint64          `json:"sequence"`
	Confidence   float64         `json:"confidence"`
	Label        ConfidenceLabel `json:"label"`
	Basis        string          `json:"basis,omitempty"`
}

type ComponentMedians struct {
	FullLeadSeconds  int64 `json:"full_lead_seconds"`
	ActiveSeconds    int64 `json:"active_seconds"`
	BlockedSeconds   int64 `json:"blocked_seconds"`
	HumanWaitSeconds int64 `json:"human_wait_seconds"`
}

type Contributor struct {
	Stage                   StageKey           `json:"stage"`
	Kind                    ContributorKind    `json:"kind"`
	CurrentStage            bool               `json:"current_stage"`
	Source                  *SourceAttribution `json:"source,omitempty"`
	Confidence              float64            `json:"confidence"`
	ConfidenceLabel         ConfidenceLabel    `json:"confidence_label"`
	MinimumRemainingSeconds int64              `json:"minimum_remaining_seconds"`
	MaximumRemainingSeconds int64              `json:"maximum_remaining_seconds"`
	PointRemainingSeconds   *int64             `json:"point_remaining_seconds,omitempty"`
	RawSampleCount          int                `json:"raw_sample_count"`
	InlierSampleCount       int                `json:"inlier_sample_count"`
	RejectedSampleCount     int                `json:"rejected_sample_count"`
	ComponentMedians        *ComponentMedians  `json:"component_medians,omitempty"`
	Flags                   []Flag             `json:"flags"`
}

type Output struct {
	SchemaVersion           int                `json:"schema_version"`
	EstimatorPolicyVersion  int                `json:"estimator_policy_version"`
	TrustRevision           string             `json:"trust_revision"`
	DeliveryIdentity        string             `json:"delivery_identity"`
	ServerTime              time.Time          `json:"server_time"`
	CalculatedAt            time.Time          `json:"calculated_at"`
	NextTrustTransitionAt   *time.Time         `json:"next_trust_transition_at,omitempty"`
	Instrumented            bool               `json:"instrumented"`
	CurrentStage            *StageKey          `json:"current_stage,omitempty"`
	Scope                   *PublicScope       `json:"scope,omitempty"`
	Completed               bool               `json:"completed"`
	Deployed                bool               `json:"deployed"`
	Verified                bool               `json:"verified"`
	DeployedUnverified      bool               `json:"deployed_unverified"`
	Unverified              bool               `json:"unverified"`
	FailedNeedsRetry        bool               `json:"failed_needs_retry"`
	ProgressKnown           bool               `json:"progress_known"`
	ProgressPercent         *int               `json:"progress_percent,omitempty"`
	OwnerSource             *SourceAttribution `json:"owner_source,omitempty"`
	ProgressSource          *SourceAttribution `json:"progress_source,omitempty"`
	Confidence              *float64           `json:"confidence,omitempty"`
	ConfidenceLabel         ConfidenceLabel    `json:"confidence_label"`
	OptimisticLandingAt     *time.Time         `json:"optimistic_landing_at,omitempty"`
	PessimisticLandingAt    *time.Time         `json:"pessimistic_landing_at,omitempty"`
	LandingAt               *time.Time         `json:"landing_at,omitempty"`
	RemainingMinimumSeconds *int64             `json:"remaining_minimum_seconds,omitempty"`
	RemainingMaximumSeconds *int64             `json:"remaining_maximum_seconds,omitempty"`
	RemainingSeconds        *int64             `json:"remaining_seconds,omitempty"`
	RangeOnly               bool               `json:"range_only"`
	Suppression             SuppressionCode    `json:"suppression,omitempty"`
	Flags                   []Flag             `json:"flags"`
	Contributors            []Contributor      `json:"contributors"`
}
