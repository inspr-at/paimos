// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"errors"
	"time"

	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

var (
	ErrInvalid      = errors.New("baseline_batch_invalid")
	ErrUnauthorized = errors.New("baseline_batch_unauthorized")
	ErrForbidden    = errors.New("baseline_batch_forbidden")
	ErrNotFound     = errors.New("baseline_batch_not_found")
	ErrConflict     = errors.New("baseline_batch_conflict")
	ErrStale        = errors.New("baseline_batch_stale")
	ErrBlocked      = errors.New("baseline_batch_blocked")
	ErrUnavailable  = errors.New("baseline_batch_unavailable")
)

const (
	HandoverVersion = "aithema.handover/0.1"

	ModeManual    = "manual"
	ModeAssisted  = "assisted"
	ModeAutomatic = "automatic"

	DraftOpen      = "open"
	DraftReviewing = "reviewing"
	DraftClosed    = "closed"

	BatchQueued    = "queued"
	BatchActive    = "active"
	BatchPaused    = "paused"
	BatchBlocked   = "blocked"
	BatchCompleted = "completed"
	BatchCancelled = "cancelled"

	ForecastMeasured = "measured"
	ForecastWorker   = "worker_estimate"
	ForecastGuess    = "educated_guess"

	// BasisOwnedDaemonProbe names the only readiness basis this service accepts.
	BasisOwnedDaemonProbe = "owned_daemon_probe"

	ControlStarted   = "started"
	ControlPaused    = "paused"
	ControlCancelled = "cancelled"

	ImportedClaimAuthenticity = "untrusted_imported_claim"

	SetupRequiredPharosRegistration = "pharos_owner_registration"
	SetupRequiredHandoffSecretMint  = "handoff_secret_mint"
	SetupRequiredHandoffConfig      = "handoff_config"
	SetupRequiredPrerequisiteSeal   = "prerequisite_seal"
	SetupRequiredHandoffRevoked     = "handoff_revoked"
	SetupRequiredPrerequisiteReview = "prerequisite_review"
	SetupRequiredBuiltArtifact      = "built_artifact_identity"
	SetupRequiredV2Report           = "v2_report"

	NextActionHumanReview            = "human_review_required"
	NextActionImplementationEvidence = "implementation_evidence"
	NextActionQAEvidence             = "qa_evidence"
	NextActionPharosRegistration     = "pharos_owner_registration"
	NextActionAuthorizeHandoff       = "authorize_pharos_handoff"
	NextActionMintHandoffSecret      = "mint_handoff_secret"
	NextActionDeploymentReceipt      = "pharos_deployment_receipt"
	NextActionVerificationHandoff    = "authorize_verification_handoff"
	NextActionVerificationObserve    = "pharos_verification_observation"
	NextActionRotateHandoff          = "rotate_revoked_handoff"
	NextActionExternalStageCLI       = "operator_external_stage_cli"

	maxImportBytes    = 256 << 10
	maxRequirements   = 64
	maxConstraints    = 64
	maxProposals      = 64
	maxStatementBytes = 4096
	maxRefBytes       = 128
)

type Actor struct {
	Kind                string
	UserID              int64
	SessionCredentialID string
	APIKeyID            int64
	Impersonated        bool
}

type ProjectAuthority struct {
	ProjectID int64
	Status    string
	CanView   bool
	CanEdit   bool
}

type Scope struct {
	RequirementRefs []string `json:"requirement_refs"`
	ConstraintRefs  []string `json:"constraint_refs"`
}

type WorkerSelection struct {
	WorkerName        string `json:"worker_name,omitempty"`
	AccountLabel      string `json:"account_label,omitempty"`
	AccountKey        string `json:"account_key,omitempty"`
	ProfileID         string `json:"profile_id,omitempty"`
	ProfileVersion    string `json:"profile_version,omitempty"`
	WorkspaceHandle   string `json:"workspace_handle,omitempty"`
	RuntimeID         string `json:"runtime_id,omitempty"`
	RuntimeGeneration string `json:"runtime_generation,omitempty"`
}

type BaselineClaim struct {
	BaselineRef               string `json:"baseline_ref"`
	Revision                  int    `json:"revision"`
	ContentDigest             string `json:"content_digest"`
	RevisionSeal              string `json:"revision_seal"`
	ImportedClaimedApprovedBy string `json:"imported_claimed_approved_by"`
	ImportedClaimedApprovedAt string `json:"imported_claimed_approved_at"`
	Authenticity              string `json:"authenticity"`
	StreamRef                 string `json:"stream_ref"`
}

type UnresolvedItem struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Summary string `json:"summary"`
}

// Forecast always states its basis. Observed and Fresh describe the evidence
// behind it and stay independent of the number itself: a guess is never
// observed, and an observation can be real but stale.
type Forecast struct {
	Subject    string  `json:"subject"`
	Percent    float64 `json:"percent"`
	ETASeconds *int64  `json:"eta_seconds"`
	Kind       string  `json:"kind"`
	Basis      string  `json:"basis"`
	AsOf       string  `json:"as_of"`
	Label      string  `json:"label"`
	Observed   bool    `json:"observed"`
	Fresh      bool    `json:"fresh"`
	// Educated ETA is populated only when this forecast has no measured/worker
	// ETA of its own. It is always labelled guessed and never upgrades observed
	// evidence or unlocks gates.
	EducatedETASeconds *int64 `json:"educated_eta_seconds,omitempty"`
	EducatedETABasis   string `json:"educated_eta_basis,omitempty"`
	EducatedETAAsOf    string `json:"educated_eta_as_of,omitempty"`
	EducatedETALabel   string `json:"educated_eta_label,omitempty"`
}

// ImpactEstimate is the pre-start disclosure an authorized human reads before
// confirming: how much work the selection represents and how much of it is
// still unresolved. Its forecast is always an educated guess; nothing here has
// been observed yet, and it unlocks nothing.
type ImpactEstimate struct {
	RequirementCount        int      `json:"requirement_count"`
	AcceptanceCriteriaCount int      `json:"acceptance_criteria_count"`
	ConstraintCount         int      `json:"constraint_count"`
	UnresolvedCount         int      `json:"unresolved_count"`
	Forecast                Forecast `json:"forecast"`
}

type Draft struct {
	ID              int64                     `json:"id"`
	ProjectID       int64                     `json:"project_id"`
	Revision        int64                     `json:"revision"`
	Status          string                    `json:"status"`
	Baseline        BaselineClaim             `json:"baseline"`
	Requirements    []Requirement             `json:"requirements"`
	Constraints     []Constraint              `json:"constraints"`
	Selected        Scope                     `json:"selected"`
	Unresolved      []UnresolvedItem          `json:"unresolved"`
	ExecutionMode   string                    `json:"execution_mode"`
	Worker          WorkerSelection           `json:"worker"`
	DelegatedLaunch *DelegatedLaunchSelection `json:"delegated_launch,omitempty"`
	ReviewID        *int64                    `json:"review_id,omitempty"`
	ReviewValid     bool                      `json:"review_valid"`
	Impact          ImpactEstimate            `json:"impact"`
	CreatedAt       string                    `json:"created_at"`
	UpdatedAt       string                    `json:"updated_at"`
}

type Review struct {
	ID               int64   `json:"id"`
	DraftID          int64   `json:"draft_id"`
	DraftRevision    int64   `json:"draft_revision"`
	BindingHash      string  `json:"binding_hash"`
	ExecutionMode    string  `json:"execution_mode"`
	HumanUserID      int64   `json:"human_user_id"`
	CreatedAt        string  `json:"created_at"`
	InvalidatedAt    *string `json:"invalidated_at,omitempty"`
	InvalidateReason string  `json:"invalidate_reason,omitempty"`
}

// StageView is the batch-scoped projection of one canonical delivery stage.
// Everything in it comes from the delivery read model; nothing is written by
// this feature.
type StageView struct {
	StageKey      string `json:"stage_key"`
	Applicability string `json:"applicability"`
	Weight        int    `json:"weight"`
	State         string `json:"state"`
	Phase         string `json:"phase"`
	Activity      string `json:"activity"`
	NeedsInput    bool   `json:"needs_input"`
	Performed     bool   `json:"performed"`
	Satisfied     bool   `json:"policy_satisfied"`
	Stale         bool   `json:"stale"`
	NeverSignaled bool   `json:"never_signaled"`
	LastSignalAt  string `json:"last_signal_at,omitempty"`
}

// Progress separates what the delivery ledger says happened from how fresh
// that evidence is. Completion never follows from a worker saying it finished.
type Progress struct {
	Stages           []StageView `json:"stages"`
	IntentState      string      `json:"intent_state,omitempty"`
	IntentReason     string      `json:"intent_reason,omitempty"`
	SessionID        string      `json:"session_id,omitempty"`
	SessionPhase     string      `json:"session_phase,omitempty"`
	SessionActivity  string      `json:"session_activity,omitempty"`
	EvidenceFresh    bool        `json:"evidence_fresh"`
	EvidenceObserved bool        `json:"evidence_observed"`
	FreshnessAsOf    string      `json:"freshness_as_of,omitempty"`
	BlockingReason   string      `json:"blocking_reason,omitempty"`
	// SetupRequired is a precise operator-provisioning boundary. It is never a
	// secret and never an invented host/command. Empty means this batch is not
	// waiting on missing Pharos/Janus setup.
	SetupRequired string `json:"setup_required,omitempty"`
	// NextAction is the current bound step. It is not an unconditional advance
	// control and does not upgrade guesses or worker-exit into completion.
	NextAction string       `json:"next_action,omitempty"`
	Handoff    *HandoffView `json:"handoff,omitempty"`
}

// HandoffView is the safe projection of the current external-stage handoff.
// Credential bytes never appear here; mint_required means the existing
// owner-only secret file path still has to mint epoch 0.
type HandoffView struct {
	StageKey        string `json:"stage_key"`
	HandoffID       string `json:"handoff_id"`
	State           string `json:"state"`
	CredentialEpoch int64  `json:"credential_epoch"`
	MintRequired    bool   `json:"mint_required"`
}

// ControlOption is a control the current human may actually perform on this
// batch right now. The UI renders only available actions, so no button exists
// without a durable owned effect behind it.
type ControlOption struct {
	Action    string `json:"action"`
	Available bool   `json:"available"`
	Effect    string `json:"effect,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type Batch struct {
	ID                int64                     `json:"id"`
	ProjectID         int64                     `json:"project_id"`
	BatchKey          string                    `json:"batch_key"`
	DraftID           int64                     `json:"draft_id"`
	DraftRevision     int64                     `json:"draft_revision"`
	ReviewID          int64                     `json:"review_id"`
	Baseline          BaselineClaim             `json:"baseline"`
	ExecutionMode     string                    `json:"execution_mode"`
	Scope             Scope                     `json:"scope"`
	Worker            WorkerSelection           `json:"worker"`
	DelegatedLaunch   *DelegatedLaunchSelection `json:"delegated_launch,omitempty"`
	LaunchGrant       *LaunchGrantView          `json:"launch_grant,omitempty"`
	IssueID           int64                     `json:"issue_id"`
	DeliveryID        *int64                    `json:"delivery_id,omitempty"`
	AttemptID         *int64                    `json:"attempt_id,omitempty"`
	LifecycleIntentID string                    `json:"lifecycle_intent_id,omitempty"`
	ReadinessIntentID string                    `json:"readiness_intent_id,omitempty"`
	ControlState      string                    `json:"control_state"`
	ControlReason     string                    `json:"control_reason,omitempty"`
	Status            string                    `json:"status"`
	WorkflowState     string                    `json:"workflow_state"`
	Progress          Progress                  `json:"progress"`
	Forecasts         []Forecast                `json:"forecasts"`
	Controls          []ControlOption           `json:"controls"`
	Readiness         *ReadinessEvidence        `json:"readiness,omitempty"`
	StartedBy         int64                     `json:"started_by"`
	StartedAt         string                    `json:"started_at"`
}

type Workflow struct {
	ProjectID          int64              `json:"project_id"`
	INSPRStreamEnabled bool               `json:"inspr_stream_enabled"`
	INSPRGating        bool               `json:"inspr_gating"`
	LegacyUnaffected   bool               `json:"legacy_unaffected"`
	Draft              *Draft             `json:"draft"`
	ActiveBatch        *Batch             `json:"active_batch"`
	Batches            []Batch            `json:"batches"`
	Choices            WorkflowChoices    `json:"choices"`
	Readiness          *ReadinessEvidence `json:"readiness,omitempty"`
}

type WorkflowChoices struct {
	ExecutionModes         []string                `json:"execution_modes"`
	Runtimes               []RuntimeChoice         `json:"runtimes"`
	DelegatedLaunchTargets []DelegatedLaunchTarget `json:"delegated_launch_targets"`
	Note                   string                  `json:"note"`
}

type DelegatedLaunchSelection struct {
	TargetRef   string `json:"target_ref"`
	Workflow    string `json:"workflow"`
	Environment string `json:"environment"`
	ExpiresAt   string `json:"expires_at"`
	MaxLaunches int    `json:"max_launches"`
}

type DelegatedLaunchTarget struct {
	TargetRef   string `json:"target_ref"`
	Environment string `json:"environment"`
	Label       string `json:"label"`
}

type LaunchGrantView struct {
	GrantID      string `json:"grant_id"`
	Revision     int    `json:"revision"`
	GrantDigest  string `json:"grant_digest"`
	TargetRef    string `json:"target_ref"`
	Workflow     string `json:"workflow"`
	Environment  string `json:"environment"`
	MaxLaunches  int    `json:"max_launches"`
	UsedLaunches int    `json:"used_launches"`
	IssuedAt     string `json:"issued_at"`
	ExpiresAt    string `json:"expires_at"`
	State        string `json:"state"`
}

type RuntimeChoice struct {
	RuntimeID         string                          `json:"runtime_id"`
	RuntimeGeneration string                          `json:"runtime_generation"`
	AccountLabel      string                          `json:"account_label,omitempty"`
	Accounts          []AccountChoice                 `json:"accounts"`
	Profiles          []ProfileChoice                 `json:"profiles"`
	AccountScopes     []lifecycleintents.AccountScope `json:"account_scopes,omitempty"`
	SchemaVersion     int                             `json:"schema_version,omitempty"`
	Workspaces        []WorkspaceChoice               `json:"workspaces"`
	ExpiresAt         string                          `json:"expires_at"`
}

type AccountChoice struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type ProfileChoice struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type WorkspaceChoice struct {
	Handle   string `json:"handle"`
	Identity string `json:"identity"`
	Label    string `json:"label,omitempty"`
}

type ReadinessCheckView struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// ReadinessEvidence is what the current owned observation proves about one
// reviewed worker selection. It is a projection of a durable row, not a value
// minted at read time: ObservedAt is the daemon's observation time and
// FreshUntil the stored deadline, already clamped to runtime ownership.
type ReadinessEvidence struct {
	Status             string               `json:"status"`
	Basis              string               `json:"basis"`
	ContractVersion    string               `json:"contract_version"`
	IntentID           string               `json:"intent_id,omitempty"`
	ObservedAt         string               `json:"observed_at,omitempty"`
	FreshUntil         string               `json:"fresh_until,omitempty"`
	BlockingReason     string               `json:"blocking_reason,omitempty"`
	NextAction         string               `json:"next_action,omitempty"`
	HostKind           string               `json:"host_kind,omitempty"`
	ProbeIntentID      string               `json:"probe_intent_id,omitempty"`
	ProbeState         string               `json:"probe_state,omitempty"`
	ProbeReason        string               `json:"probe_reason,omitempty"`
	NamedAccountProof  bool                 `json:"named_account_proof"`
	ModelProfileProof  bool                 `json:"model_profile_proof"`
	WorkspaceProof     bool                 `json:"workspace_proof"`
	ClientReadyIgnored bool                 `json:"client_ready_ignored"`
	RuntimeID          string               `json:"runtime_id,omitempty"`
	RuntimeGeneration  string               `json:"runtime_generation,omitempty"`
	AccountLabel       string               `json:"account_label,omitempty"`
	AccountKey         string               `json:"account_key,omitempty"`
	ProfileID          string               `json:"dispatch_profile_id,omitempty"`
	ProfileVersion     string               `json:"dispatch_profile_version,omitempty"`
	WorkspaceHandle    string               `json:"workspace_handle,omitempty"`
	WorkspaceIdentity  string               `json:"workspace_identity,omitempty"`
	BaselineDigest     string               `json:"baseline_digest,omitempty"`
	Checks             []ReadinessCheckView `json:"checks"`
}

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }
