// SPDX-License-Identifier: AGPL-3.0-only

// Package agentd owns local harness processes for AEON runs. It never stores
// credentials or vendor protocol payloads in AEON.
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/agentactivity"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/modelreport"
	"github.com/inspr-at/paimos/internal/openrouter"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

const (
	Codex    = "codex"
	Claude   = "claude"
	Pi       = "pi"
	Cursor   = "cursor"
	Grok     = "grok"
	Gemini   = "gemini"
	OpenCode = "opencode"
)

var (
	ErrScope              = errors.New("run control scope mismatch")
	ErrGeneration         = errors.New("run generation mismatch")
	ErrReplay             = errors.New("control correlation replay conflict")
	ErrNotOwned           = errors.New("run is not owned by this daemon generation")
	ErrUnsupported        = errors.New("adapter operation unsupported")
	ErrHarnessArchived    = errors.New("harness generation archived; detach without signaling")
	ErrControlUnconfirmed = errors.New("control outcome unconfirmed; reporting failure does not authorize termination")
	// ErrControlTerminal means this completion cannot be recorded. The server
	// already finished the control, or it refused an applied setting because the
	// database deadline passed. The daemon drops that control from the queue head.
	// Other conflicts stay retryable.
	ErrControlTerminal      = errors.New("control completion is terminal")
	ErrForceExitUnconfirmed = errors.New("owned group signalled; root exit unconfirmed")
)

// Run is the content-free AEON run projection returned by /runs endpoints.
type Run struct {
	Trace                     json.RawMessage `json:"trace,omitempty"`
	RecoveryBrief             string          `json:"recovery_brief,omitempty"`
	RecoveryTier              string          `json:"recovery_service_tier,omitempty"`
	RecoveryLabel             *string         `json:"recovery_display_label,omitempty"`
	ReadOnlyReview            bool            `json:"read_only_review,omitempty"`
	RetryOfRunID              string          `json:"retry_of_run_id"`
	CapacityHandoff           bool            `json:"capacity_handoff,omitempty"`
	Purpose                   string          `json:"purpose,omitempty"`
	VerificationTask          string          `json:"verification_task,omitempty"`
	MaxDurationSeconds        *int64          `json:"max_duration_seconds,omitempty"`
	VerificationPolicy        string          `json:"verification_policy,omitempty"`
	RepositoryMutationAllowed *bool           `json:"repository_mutation_allowed,omitempty"`
	ID                        string          `json:"id"`
	WorkOrderID               string          `json:"work_order_id"`
	AgentPrincipalID          string          `json:"agent_principal_id"`
	ModelProfileID            string          `json:"model_profile_id"`
	AccountID                 string          `json:"account_id"`
	RequestedAccountID        string          `json:"requested_account_id"`
	Status                    string          `json:"status"`
}

// requestedAccount retains the approved enrollment before Route fills AccountID.
// Older/already-routed projections may carry only AccountID.
func (r Run) requestedAccount() string {
	if r.RequestedAccountID != "" {
		return r.RequestedAccountID
	}
	return r.AccountID
}

type Profile struct {
	Family  string `json:"family"`
	ID      string `json:"id"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

type Node struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// HarnessSession is the public binding plus the private worker lease held only
// by this daemon generation. The lease is never persisted in the run journal.
type HarnessSession struct {
	AttachedHook     bool                    `json:"-"`
	DisplayLabel     *string                 `json:"display_label,omitempty"`
	ServiceTier      string                  `json:"service_tier,omitempty"`
	Doing            string                  `json:"-"`
	DoingAt          time.Time               `json:"-"`
	ToolActivity     *agentactivity.Activity `json:"-"`
	Activity         string                  `json:"-"`
	Ownership        *ownedprocess.Identity  `json:"-"`
	ActivitySequence int64                   `json:"-"`
	ID               string                  `json:"id"`
	ProjectID        string                  `json:"project_id"`
	Lease            string                  `json:"-"`
	Harness          string                  `json:"-"`
	Model            string                  `json:"model,omitempty"`
	ReasoningEffort  string                  `json:"reasoning_effort,omitempty"`
	AccountLabel     string                  `json:"account_label,omitempty"`
}

type HarnessControl struct {
	ExpectedGeneration string `json:"expected_generation"`
	RequestPayload     *struct {
		StopNow bool `json:"stop_now"`
	} `json:"request_payload,omitempty"`
	// deadline is local, monotonic, and never serialized or persisted.
	deadline          time.Time
	ExpiresInMS       int64                  `json:"expires_in_ms"`
	Value             string                 `json:"value,omitempty"`
	Text              string                 `json:"text,omitempty"`
	ExpiresAt         *time.Time             `json:"expires_at,omitempty"`
	ExpectedOwnership *ownedprocess.Identity `json:"expected_ownership,omitempty"`
	ID                string                 `json:"id"`
	Kind              string                 `json:"kind"`
}

// InboxReplyTarget is captured from an authenticated drain, never from tool input.
type InboxReplyTarget struct {
	PrincipalID        string
	ProjectID          string
	ReplyToID          string
	SenderSessionID    string
	RecipientSessionID *string
}

type HarnessDelivery struct {
	Level             string  `json:"delivery_level,omitempty"`
	ProjectID         string  `json:"project_id,omitempty"`
	ReplyToID         string  `json:"reply_to_id,omitempty"`
	SenderSessionID   *string `json:"sender_session_id,omitempty"`
	Outcome           string  `json:"-"`
	FailureReason     string  `json:"-"`
	ID                string  `json:"delivery_id"`
	MessageID         string  `json:"message_id"`
	Cursor            int64   `json:"cursor"`
	SenderPrincipalID string  `json:"sender_principal_id"`
	Body              string  `json:"body"`
}

type WorkOrder struct {
	Kind               string              `json:"kind"`
	Review             *reviewgate.Binding `json:"review,omitempty"`
	NodeID             string              `json:"node_id"`
	Status             string              `json:"status"`
	Revision           int64               `json:"revision"`
	Criteria           []WorkCriterion     `json:"criteria"`
	MaxDurationSeconds *int64              `json:"max_duration_seconds"`
}

type WorkCriterion struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	CheckedAt   *time.Time `json:"checked_at"`
}

// Telemetry carries content-free, nonnegative deltas. TurnCountDelta is one
// accepted user turn; token and cost deltas come from vendor usage reports.
type Telemetry struct {
	ProcessState           string                  `json:"process_state,omitempty"`
	ServiceTier            string                  `json:"service_tier,omitempty"`
	ReviewRange            *reviewgate.CommitRange `json:"review_range,omitempty"`
	LimitWindow            string                  `json:"limit_window,omitempty"`
	LimitResetsAt          *time.Time              `json:"limit_resets_at,omitempty"`
	Sequence               int64                   `json:"sequence"`
	Kind                   string                  `json:"kind"`
	Status                 string                  `json:"status,omitempty"`
	InputTokensDelta       int64                   `json:"input_tokens_delta,omitempty"`
	OutputTokensDelta      int64                   `json:"output_tokens_delta,omitempty"`
	CachedInputTokensDelta int64                   `json:"cached_input_tokens_delta,omitempty"`
	ReasoningTokensDelta   int64                   `json:"reasoning_tokens_delta,omitempty"`
	CostMicrosDelta        int64                   `json:"cost_micros_delta,omitempty"`
	TurnCountDelta         int64                   `json:"turn_count_delta,omitempty"`
	EffectiveModel         string                  `json:"effective_model,omitempty"`
	ModelEvidence          string                  `json:"model_evidence,omitempty"`
	ErrorCode              string                  `json:"error_code,omitempty"`
	GitCommits             []GitCommit             `json:"git_commits,omitempty"`
}

// GitCommit is one commit introduced after the run's launch revision.
type GitCommit struct {
	SHA             string `json:"sha"`
	Subject         string `json:"subject"`
	Parents         int    `json:"parents,omitempty"`
	OnDefaultBranch bool   `json:"on_default_branch,omitempty"`
}

type InboxMessage struct {
	ID                string `json:"id"`
	SenderPrincipalID string `json:"sender_principal_id"`
	Body              string `json:"body"`
	SentEventID       int64  `json:"sent_event_id"`
}

type InboxPage struct {
	Items     []InboxMessage `json:"items"`
	NextAfter int64          `json:"next_after"`
}

type Reservation struct {
	ID string `json:"reservation_id"`
}
type Route struct {
	BillingMode       string        `json:"billing_mode"`
	SubscriptionLabel string        `json:"subscription_label,omitempty"`
	AccountID         string        `json:"account_id"`
	AccountKey        string        `json:"account_key"`
	AccountLabel      string        `json:"account_label"`
	DaemonID          string        `json:"daemon_id"`
	Reservations      []Reservation `json:"reservations"`
}

// API is the narrow authenticated AEON boundary. Implementations must use
// /inbox and /runs; the local daemon has no direct database access.
type API interface {
	Identity(context.Context) (tenantID, principalID string, err error)
	Queued(context.Context) ([]Run, error)
	GetRun(context.Context, string) (Run, error)
	Profiles(context.Context) ([]Profile, error)
	Node(context.Context, string) (Node, error)
	WorkOrder(context.Context, string) (WorkOrder, error)
	Route(context.Context, string, string, []string, map[string]int64) (Route, error)
	Claim(context.Context, string, string, string, []string) error
	Report(context.Context, string, Telemetry) error
	Inbox(context.Context, int64) (InboxPage, error)
	Ack(context.Context, string) error
	AddEvidence(context.Context, string, string, string) error
	Probe(context.Context, string, string, string, bool) error
	ProjectForNode(context.Context, string) (string, error)
	RegisterHarness(context.Context, HarnessSession, string, string, string, string, string, []string) (HarnessSession, error)
	HeartbeatHarness(context.Context, HarnessSession, string) error
	YieldHarness(context.Context, HarnessSession) ([]HarnessControl, error)
	DrainHarness(context.Context, HarnessSession) ([]HarnessDelivery, error)
	CompleteHarnessControl(context.Context, HarnessSession, string, string, string) error
	CompleteHarnessDelivery(context.Context, HarnessSession, HarnessDelivery) error
	StopHarness(context.Context, HarnessSession, string) error
}

type StartRequest struct {
	ServiceTier string
	// Lifetime is the accepted run budget rooted in the supervisor lifetime.
	// The Start call's context only covers dispatch/startup and may end as soon
	// as polling finishes. Adapters without a supervisor use the caller context.
	Lifetime      context.Context
	ManagedPolicy bool
	Capabilities  []string // Exact capabilities advertised for this session.
	InboxEnabled  bool     // Keep the owned process alive between turns for leased inbox delivery.
	Rules         string   // Ephemeral ADR-004 merge, never a repository instruction file.
	MaxTurns      int64
	MaxTokens     int64
	TenantID      string
	PrincipalID   string
	Run           Run
	Profile       Profile
	AccountKey    string
	Workspace     string
	StateRoot     string
	Prompt        string
	Generation    string
	// Tools is a loopback MCP capability for this run; the daemon key stays in
	// the supervisor. It expires when the owned process exits.
	Tools *RunTools
}

type RunTools struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type AdapterEvent struct {
	ModelReports []modelreport.Observation
	HarnessTier  string
	Doing        string
	ToolActivity *agentactivity.Activity
	VendorLimit  *capacity.LimitHit

	Activity               string // busy or idle, independent of the run process lifetime.
	Capacity               []capacity.Reading
	BudgetExhausted        string
	BudgetTurnsDelta       int64
	SessionUsage           *sessionusage.UsageReport
	Kind                   string
	VendorSessionID        string
	HarnessModel           string // Resolved model from this owned adapter connection only.
	HarnessEffort          string // Omitted when the vendor has not established an effort.
	EffectiveModel         string
	ModelEvidence          string
	InputTokensDelta       int64
	OutputTokensDelta      int64
	CachedInputTokensDelta int64
	ReasoningTokensDelta   int64
	CostMicrosDelta        int64
	TurnCountDelta         int64
	ErrorCode              string
}

// RecoveryProcess exposes a live child identity and verifies it before force.
type RecoveryProcess interface {
	Ownership() (ownedprocess.Identity, error)
	ForceStop(context.Context, ownedprocess.Identity, time.Time) error
}

// GracefulProcess is optional so a user stop never falls back to an adapter's
// forceful cleanup operation. Unsupported adapters reject the user control.
type GracefulProcess interface{ GracefulStop(context.Context) error }

var ErrGracefulTimeout = errors.New("graceful stop timed out; process may still be running")

type Process interface {
	PID() int
	Wait() error
	Control(context.Context, string, string) error
	Stop(context.Context) error
}

// EvidenceProcess yields a bounded final artifact after the owned child exits.
// The supervisor stores it through the work-order API before marking success.
type EvidenceProcess interface{ Evidence() string }

type Adapter interface {
	Name() string
	Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error)
}

// AccountProber collapses local enrollment health to one boolean. The key,
// auth home, provider identity and vendor output never leave the daemon.
type AccountProber interface {
	Probe(context.Context, string) bool
}

// ProbeStatus is an account probe with its bounded cause. Only ProbeAuthFailed
// and ProbeIdentityMismatch describe confirmed authentication failures. Local
// measurement failures never imply sign-out. The historical account-probe API
// retains auth_failed/unavailable; lifecycle details can retain finer causes.
// Vendor identity and output never leave the daemon.
type ProbeStatus struct {
	BillingMode       string
	OpenRouterCredits *openrouter.Credits
	OK                bool
	Failure           string
	ReasonDetail      string // Fixed publishable phrase; never raw vendor output.
}

const (
	ProbeAuthFailed       = "auth_failed"
	ProbeUnavailable      = "unavailable"
	ProbeIdentityMismatch = "identity_mismatch"
	ProbeTimeout          = "timeout"
	ProbeProtocol         = "protocol"
	ProbeLaunchFailed     = "launch_failed"
	// ProbeUnverified is local evidence, not a confirmed vendor sign-out.
	ProbeUnverified = "sign_in_unverified"
)

// AccountStatusProber is the optional richer prober; adapters without it
// report every failed Probe as unavailable, never as a sign-in problem.
type AccountStatusProber interface {
	ProbeStatus(context.Context, string) ProbeStatus
}

func probeAccount(ctx context.Context, prober AccountProber, key string) ProbeStatus {
	if status, ok := prober.(AccountStatusProber); ok {
		return status.ProbeStatus(ctx, key)
	}
	if prober.Probe(ctx, key) {
		return ProbeStatus{OK: true}
	}
	return ProbeStatus{Failure: ProbeUnavailable}
}

// AccountMetadata is the only publishable part of local enrollment. Home,
// provider identity and CodexBar account selectors must never be added here.
type AccountMetadata struct {
	Label             string   `json:"label"`
	Plan              string   `json:"plan"`
	HostLabel         string   `json:"host_label"`
	AllowedProfileIDs []string `json:"allowed_model_profile_ids"`
}

type EnrolledAccount struct {
	ID, Key, Harness string
	Metadata         *AccountMetadata
	// DependencyBlocked means this enrollment's interpreter pin is missing,
	// partial, drifted, invalid, or unsafe. It stays enrolled and unlaunchable.
	// PinReason is the shared reason code and PinFix the fix kind; the exact
	// command comes from agentsetup.RecoveryFix.
	DependencyBlocked bool
	PinReason         string
	PinFix            string
}

type ControlRequest struct {
	// Only the authenticated yield transport supplies a monotonic deadline.
	deadline          time.Time
	Value             string                 `json:"value,omitempty"`
	ExpiresAt         *time.Time             `json:"expires_at,omitempty"`
	ExpectedOwnership *ownedprocess.Identity `json:"expected_ownership,omitempty"`
	TenantID          string                 `json:"tenant_id"`
	PrincipalID       string                 `json:"principal_id"`
	RunID             string                 `json:"run_id"`
	Generation        string                 `json:"generation"`
	CorrelationID     string                 `json:"correlation_id"`
	Operation         string                 `json:"operation"`
	Text              string                 `json:"text,omitempty"`
}

type Receipt struct {
	RunID         string    `json:"run_id"`
	Generation    string    `json:"generation"`
	CorrelationID string    `json:"correlation_id"`
	Operation     string    `json:"operation"`
	AppliedAt     time.Time `json:"applied_at"`
}
