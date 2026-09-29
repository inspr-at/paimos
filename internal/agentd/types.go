// SPDX-License-Identifier: AGPL-3.0-only

// Package agentd owns local harness processes for AEON runs. It never stores
// credentials or vendor protocol payloads in AEON.
package agentd

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

const (
	Codex  = "codex"
	Claude = "claude"
	Pi     = "pi"
	Cursor = "cursor"
	Grok   = "grok"
)

var (
	ErrScope                = errors.New("run control scope mismatch")
	ErrGeneration           = errors.New("run generation mismatch")
	ErrReplay               = errors.New("control correlation replay conflict")
	ErrNotOwned             = errors.New("run is not owned by this daemon generation")
	ErrUnsupported          = errors.New("adapter operation unsupported")
	ErrHarnessArchived      = errors.New("harness generation archived; detach without signaling")
	ErrControlUnconfirmed   = errors.New("control outcome unconfirmed; reporting failure does not authorize termination")
	ErrForceExitUnconfirmed = errors.New("owned group signalled; root exit unconfirmed")
)

// Run is the content-free AEON run projection returned by /runs endpoints.
type Run struct {
	Purpose                   string `json:"purpose,omitempty"`
	VerificationTask          string `json:"verification_task,omitempty"`
	MaxDurationSeconds        *int64 `json:"max_duration_seconds,omitempty"`
	VerificationPolicy        string `json:"verification_policy,omitempty"`
	RepositoryMutationAllowed *bool  `json:"repository_mutation_allowed,omitempty"`
	ID                        string `json:"id"`
	WorkOrderID               string `json:"work_order_id"`
	AgentPrincipalID          string `json:"agent_principal_id"`
	ModelProfileID            string `json:"model_profile_id"`
	AccountID                 string `json:"account_id"`
	RequestedAccountID        string `json:"requested_account_id"`
	Status                    string `json:"status"`
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
	Activity         string                 `json:"-"`
	Ownership        *ownedprocess.Identity `json:"-"`
	ActivitySequence int64                  `json:"-"`
	ID               string                 `json:"id"`
	ProjectID        string                 `json:"project_id"`
	Lease            string                 `json:"-"`
	Model            string                 `json:"model,omitempty"`
	ReasoningEffort  string                 `json:"reasoning_effort,omitempty"`
	AccountLabel     string                 `json:"account_label,omitempty"`
}

type HarnessControl struct {
	Value             string                 `json:"value,omitempty"`
	Text              string                 `json:"text,omitempty"`
	ExpiresAt         *time.Time             `json:"expires_at,omitempty"`
	ExpectedOwnership *ownedprocess.Identity `json:"expected_ownership,omitempty"`
	ID                string                 `json:"id"`
	Kind              string                 `json:"kind"`
}

type HarnessDelivery struct {
	Outcome           string `json:"-"`
	FailureReason     string `json:"-"`
	ID                string `json:"delivery_id"`
	MessageID         string `json:"message_id"`
	Cursor            int64  `json:"cursor"`
	SenderPrincipalID string `json:"sender_principal_id"`
	Body              string `json:"body"`
}

type WorkOrder struct {
	NodeID             string          `json:"node_id"`
	Status             string          `json:"status"`
	Revision           int64           `json:"revision"`
	Criteria           []WorkCriterion `json:"criteria"`
	MaxDurationSeconds *int64          `json:"max_duration_seconds"`
}

type WorkCriterion struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	CheckedAt   *time.Time `json:"checked_at"`
}

// Telemetry carries content-free, nonnegative deltas. TurnCountDelta is one
// accepted user turn; token and cost deltas come from vendor usage reports.
type Telemetry struct {
	Sequence          int64  `json:"sequence"`
	Kind              string `json:"kind"`
	Status            string `json:"status,omitempty"`
	InputTokensDelta  int64  `json:"input_tokens_delta,omitempty"`
	OutputTokensDelta int64  `json:"output_tokens_delta,omitempty"`
	CostMicrosDelta   int64  `json:"cost_micros_delta,omitempty"`
	TurnCountDelta    int64  `json:"turn_count_delta,omitempty"`
	EffectiveModel    string `json:"effective_model,omitempty"`
	ModelEvidence     string `json:"model_evidence,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
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
	AccountID    string        `json:"account_id"`
	AccountKey   string        `json:"account_key"`
	AccountLabel string        `json:"account_label"`
	DaemonID     string        `json:"daemon_id"`
	Reservations []Reservation `json:"reservations"`
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
	InboxEnabled bool   // Keep the owned process alive between turns for leased inbox delivery.
	Rules        string // Ephemeral ADR-004 merge, never a repository instruction file.
	MaxTurns     int64
	MaxTokens    int64
	TenantID     string
	PrincipalID  string
	Run          Run
	Profile      Profile
	AccountKey   string
	Workspace    string
	StateRoot    string
	Prompt       string
	Generation   string
	// Tools is a loopback MCP capability for this run; the daemon key stays in
	// the supervisor. It expires when the owned process exits.
	Tools *RunTools
}

type RunTools struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type AdapterEvent struct {
	Activity          string // busy or idle, independent of the run process lifetime.
	BudgetExhausted   string
	BudgetTurnsDelta  int64
	SessionUsage      *sessionusage.UsageReport
	Kind              string
	VendorSessionID   string
	HarnessModel      string // Resolved model from this owned adapter connection only.
	HarnessEffort     string // Omitted when the vendor has not established an effort.
	EffectiveModel    string
	ModelEvidence     string
	InputTokensDelta  int64
	OutputTokensDelta int64
	CostMicrosDelta   int64
	TurnCountDelta    int64
	ErrorCode         string
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
}

type ControlRequest struct {
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
