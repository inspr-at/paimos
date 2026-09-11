// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package agentd owns operator-local harness child processes and exposes a
// private last-meter transport. It never accepts or persists vendor tokens.
package agentd

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const (
	AdapterCodex  = "codex"
	AdapterClaude = "claude"
	AdapterPi     = "pi"
	AdapterCursor = "cursor"

	// AccountPiContext is the closed class for an operator-selected Pi
	// PI_CODING_AGENT_DIR. It is not a verified provider identity.
	AccountPiContext = "pi_context"

	// AccountCursorContext is the closed class for an operator-selected
	// Cursor identity mapping. It is not a subscription tier and is not
	// claimed from an unkeyed status probe.
	AccountCursorContext = "cursor_context"

	maxPromptBytes = 256 << 10
	maxTextBytes   = 64 << 10
)

type Capability string

const (
	CapabilityInbox     Capability = "inbox"
	CapabilityStatus    Capability = "status"
	CapabilitySteer     Capability = "steer"
	CapabilityInterrupt Capability = "interrupt"
	CapabilityStop      Capability = "stop"
)

var (
	ErrSessionNotFound             = errors.New("managed session not found")
	ErrSessionNotRunning           = errors.New("managed session is not running")
	ErrAdapterUnsupported          = errors.New("harness adapter is unsupported")
	ErrCapabilityMissing           = errors.New("managed session capability is unavailable")
	ErrControlScopeMismatch        = errors.New("managed control scope does not match the owned session")
	ErrControlReplayConflict       = errors.New("managed control correlation was reused with different input")
	ErrControlReplayCapacity       = errors.New("managed control replay bound reached")
	ErrDispatchProfile             = errors.New("managed dispatch profile is unavailable")
	ErrWorkspaceConflict           = errors.New("managed workspace is already owned")
	ErrDecisionUnknown             = errors.New("managed decision request was not found")
	ErrDecisionMismatch            = errors.New("managed decision binding does not match the owned request")
	ErrDecisionExpired             = errors.New("managed decision request expired")
	ErrDecisionConsumed            = errors.New("managed decision request was already used")
	ErrDecisionAuthority           = errors.New("managed decision authority is unavailable")
	ErrDecisionIncomplete          = errors.New("managed decision is missing inspectable context")
	ErrAccountLifecycleUnavailable = errors.New("owned account lifecycle journal unavailable")
	ErrAccountLifecycleConflict    = errors.New("owned account lifecycle key was reused with different input")
	ErrAccountLifecycleRejected    = errors.New("owned account lifecycle selection was rejected")
	ErrAccountInUse                = errors.New("account owns active or unsettled work")
	ErrAccountDrainRequired        = errors.New("account disconnect requires drain or settlement first")
	ErrAccountStale                = errors.New("account lifecycle selection is stale")
	ErrAccountUnsupported          = errors.New("account lifecycle adapter is unsupported")
	ErrAccountDetached             = errors.New("managed account is not attached to this runtime")
	ErrAccountForeign              = errors.New("account lifecycle project is not bound to this runtime")
)

const (
	WorkspaceExclusive = "exclusive"
	WorkspaceShared    = "shared"
	WorkspaceDirectory = "directory"
	WorkspacePrimary   = "git_primary"
	WorkspaceWorktree  = "git_worktree"
)

// WorkspaceProvenance is collected before spawn with a fixed-argv Git probe.
// Identity is a digest of the physical Git top-level and Git directory; it is
// safe to compare while preserving linked-worktree distinction.
type WorkspaceProvenance struct {
	CanonicalPath string `json:"canonical_path"`
	GitTopLevel   string `json:"git_top_level,omitempty"`
	GitBranch     string `json:"git_branch,omitempty"`
	Identity      string `json:"identity"`
	Kind          string `json:"kind"`
	Mode          string `json:"mode"`
}

type StartRequest struct {
	KeepAlive              bool                     `json:"-"`
	IdempotencyKey         string                   `json:"idempotency_key,omitempty"`
	ExpectedAccountLabel   string                   `json:"expected_account_label,omitempty"`
	AccountKey             string                   `json:"account_key,omitempty"`
	AttachmentRevision     int64                    `json:"attachment_revision,omitempty"`
	ExpectedMachineID      string                   `json:"expected_machine_id,omitempty"`
	Adapter                string                   `json:"adapter"`
	Workspace              string                   `json:"workspace"`
	WorkspaceMode          string                   `json:"workspace_mode,omitempty"`
	Prompt                 string                   `json:"prompt"`
	Identity               string                   `json:"identity"`
	ProjectID              int64                    `json:"project_id"`
	Role                   string                   `json:"role,omitempty"`
	ParentSessionID        string                   `json:"parent_harness_session_id,omitempty"`
	TicketID               int64                    `json:"ticket_id,omitempty"`
	WorkShape              string                   `json:"work_shape,omitempty"`
	DispatchProfileID      string                   `json:"dispatch_profile_id,omitempty"`
	DispatchProfileVersion string                   `json:"dispatch_profile_version,omitempty"`
	ResolvedProfile        *dispatchprofile.Profile `json:"-"`
	queue                  *piQueueStore            `json:"-"`
	generation             string                   `json:"-"`
	cursorEvidence         *cursorEvidenceStore     `json:"-"`
}

const (
	AccountLifecycleConnect             = "connect"
	AccountLifecycleDisconnect          = "disconnect"
	AccountLifecycleLegacy              = "legacy"
	AccountLifecycleExplicit            = "explicit"
	AccountAdvertisementCommitted       = "committed"
	AccountAdvertisementRestartRequired = "restart_required"
)

// AccountLifecycleRequest is an explicit reviewed attach/detach of an already
// enrolled opaque account. Labels, homes, emails and credentials are never
// accepted as authority.
type AccountLifecycleRequest struct {
	IdempotencyKey    string `json:"idempotency_key"`
	Operation         string `json:"operation"`
	ProjectID         int64  `json:"project_id"`
	RuntimeGeneration string `json:"runtime_generation"`
	AccountKey        string `json:"account_key"`
	Adapter           string `json:"adapter"`
	ExpectedRevision  int64  `json:"expected_revision"`
}

type AccountLifecycleResult struct {
	ProjectID           int64    `json:"project_id"`
	Adapter             string   `json:"adapter"`
	AccountKey          string   `json:"account_key"`
	Operation           string   `json:"operation"`
	State               string   `json:"state"`
	Mode                string   `json:"mode"`
	Revision            int64    `json:"revision"`
	RuntimeGeneration   string   `json:"runtime_generation"`
	AttachedKeys        []string `json:"attached_keys"`
	Advertisement       string   `json:"advertisement"`
	AdvertisementReason string   `json:"advertisement_reason,omitempty"`
	Replayed            bool     `json:"replayed,omitempty"`
}

type RuntimeAccountState struct {
	ProjectID         int64    `json:"project_id"`
	Adapter           string   `json:"adapter"`
	Mode              string   `json:"mode"`
	Revision          int64    `json:"revision"`
	Keys              []string `json:"keys"`
	RuntimeGeneration string   `json:"runtime_generation"`
}

type AdapterEvent struct {
	Kind             EventKind
	HarnessSessionID string
	CorrelationID    string
	ErrorCode        ErrorCode
	EffectiveModel   string
	ModelEvidence    ModelEvidenceStatus
}

// ModelEvidenceStatus is deliberately closed. An alias in a dispatch profile
// remains requested intent; only the owned vendor session's init frame can
// establish a vendor-reported effective identity.
type ModelEvidenceStatus string

const (
	ModelEvidenceUnverified     ModelEvidenceStatus = "unverified"
	ModelEvidenceVendorReported ModelEvidenceStatus = "vendor_reported"
)

// ModelEvidence is content-free execution provenance for one owned generation
// and vendor session. The enclosing Session.ID binds the generation, while
// HarnessSessionID prevents evidence from being moved between vendor sessions.
// RequestedModel is history, not proof that an alias resolved to any specific
// model generation.
type ModelEvidence struct {
	RequestedModel   string              `json:"requested_model,omitempty"`
	EffectiveModel   string              `json:"effective_model,omitempty"`
	Status           ModelEvidenceStatus `json:"status"`
	HarnessSessionID string              `json:"harness_session_id,omitempty"`
}

type EventKind string

const (
	EventSessionStarted EventKind = "session_started"
	EventToolStarted    EventKind = "tool_started"
	EventControlApplied EventKind = "control_applied"
	EventTurnStarted    EventKind = "turn_started"
	EventTurnCompleted  EventKind = "turn_completed"
)

// ErrorCode is deliberately finite so adapter events and durable status can
// never become a side channel for stderr, vendor payloads, or message text.
type ErrorCode string

const (
	ErrorEventStreamBound    ErrorCode = "event_stream_bound"
	ErrorAppServerProtocol   ErrorCode = "app_server_protocol"
	ErrorChildExitFailed     ErrorCode = "child_exit_failed"
	ErrorTurnFailed          ErrorCode = "turn_failed"
	ErrorChildStopFailed     ErrorCode = "child_stop_failed"
	ErrorOwnershipLost       ErrorCode = "ownership_lost"
	ErrorReporterUnavailable ErrorCode = "reporter_unavailable"
	ErrorWorkspaceConflict   ErrorCode = "workspace_conflict"
	ErrorDecisionRefused     ErrorCode = "decision_refused"
)

const DecisionAuthorityLocalOperator = "local_operator"

type DecisionKind string

const (
	DecisionPermission DecisionKind = "permission"
	DecisionQuestion   DecisionKind = "question"
	DecisionPlan       DecisionKind = "plan"
)

// PendingDecision is the content-free operator view of an owned Cursor ACP
// permission, question, or plan request. Raw tool input and model text stay
// owner-private; this projection carries only identifiers, a digest, and the
// exact offered option IDs.
type PendingDecision struct {
	RequestID  string       `json:"request_id"`
	Generation string       `json:"generation"`
	Method     string       `json:"method"`
	Kind       DecisionKind `json:"kind"`
	ToolKind   string       `json:"tool_kind,omitempty"`
	Digest     string       `json:"digest"`
	OptionIDs  []string     `json:"option_ids"`
	ExpiresAt  time.Time    `json:"expires_at"`
}

type DecisionRefusal struct {
	Method    string                `json:"method"`
	Reason    string                `json:"reason"`
	Code      string                `json:"code,omitempty"`
	Structure *DecisionRequestShape `json:"structure,omitempty"`
}

// DecisionRequestShape is a value-free structural description of a malformed
// permission request. It records JSON types and bounded collection counts, but
// never request values such as commands, paths, arguments, or content.
type DecisionRequestShape struct {
	ParamsType     string `json:"params_type"`
	SessionIDType  string `json:"session_id_type"`
	ToolCallType   string `json:"tool_call_type"`
	ToolCallIDType string `json:"tool_call_id_type"`
	KindType       string `json:"kind_type"`
	TitleType      string `json:"title_type"`
	StatusType     string `json:"status_type"`
	RawInputType   string `json:"raw_input_type"`
	ContentType    string `json:"content_type"`
	ContentBlocks  int    `json:"content_blocks"`
	OptionsType    string `json:"options_type"`
	OptionCount    int    `json:"option_count"`
}

type DecisionAnswer struct {
	Instance      string `json:"instance"`
	ProjectID     int64  `json:"project_id"`
	Identity      string `json:"identity"`
	CorrelationID string `json:"correlation_id"`
	RequestID     string `json:"request_id"`
	Generation    string `json:"generation"`
	Digest        string `json:"digest"`
	OptionID      string `json:"option_id"`
	Authority     string `json:"authority"`
}

type DecisionInspectRequest struct {
	Instance   string `json:"instance"`
	ProjectID  int64  `json:"project_id"`
	Identity   string `json:"identity"`
	RequestID  string `json:"request_id"`
	Generation string `json:"generation"`
	Digest     string `json:"digest"`
}

type DecisionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind,omitempty"`
}

// DecisionInspect is owner-only Unix-socket display data. It is untrusted,
// never an authority token, and never written into public status or PPM.
type DecisionInspect struct {
	RequestID   string           `json:"request_id"`
	Generation  string           `json:"generation"`
	Method      string           `json:"method"`
	Kind        DecisionKind     `json:"kind"`
	ToolKind    string           `json:"tool_kind,omitempty"`
	Digest      string           `json:"digest"`
	OptionIDs   []string         `json:"option_ids"`
	Options     []DecisionOption `json:"options"`
	Title       string           `json:"title,omitempty"`
	Detail      string           `json:"detail,omitempty"`
	ToolInput   string           `json:"tool_input,omitempty"`
	ToolContent string           `json:"tool_content,omitempty"`
	Incomplete  bool             `json:"incomplete"`
	Untrusted   bool             `json:"untrusted"`
	ExpiresAt   time.Time        `json:"expires_at"`
}

type VisibleOutput struct {
	Generation string `json:"generation"`
	Text       string `json:"text"`
	Digest     string `json:"digest"`
	Bytes      int    `json:"bytes"`
	Truncated  bool   `json:"truncated"`
}

type ControlRequest struct {
	Instance      string `json:"instance"`
	ProjectID     int64  `json:"project_id"`
	Identity      string `json:"identity"`
	CorrelationID string `json:"correlation_id"`
	Text          string `json:"text,omitempty"`
}

type ControlEffect struct {
	Primitive       string `json:"primitive"`
	CorrelationID   string `json:"correlation_id"`
	VendorMessageID string `json:"vendor_message_id,omitempty"`
}

type Process interface {
	PID() int
	Wait() error
	Steer(context.Context, ControlRequest) (ControlEffect, error)
	Interrupt(context.Context, ControlRequest) (ControlEffect, error)
	Stop(context.Context, ControlRequest) (ControlEffect, error)
}

// Adapter is the PAI-849/PAI-850 handoff. Implementations start only a fresh
// child process and may control only the harness thread reported by that child.
// InboxProcess supports a documented simple handoff to the owned child.
type InboxProcess interface {
	Inbox(context.Context, ControlRequest) (ControlEffect, error)
}

// DecisionProcess holds scoped ACP permission/question/plan requests until a
// local operator answers the exact generation/request/digest. Codex, Claude,
// and Pi do not implement it.
type DecisionProcess interface {
	PendingDecisions() []PendingDecision
	DecisionRefusals() []DecisionRefusal
	Answer(context.Context, DecisionAnswer) (ControlEffect, error)
	Inspect(DecisionInspectRequest) (DecisionInspect, error)
	VisibleOutput() (VisibleOutput, error)
}

type Adapter interface {
	Name() string
	Capabilities() []Capability
	Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error)
}

// AccountProber is optional. Implementations execute only a documented,
// fixed-argv authentication-status command and collapse its bounded result to
// a closed non-secret label. Absence or ambiguity is always "unknown".
type AccountProber interface {
	AccountLabel(context.Context) string
}

// MachineProber returns the stable host identity accepted by the authenticated
// reporter authority. It is operator-supplied provenance, not hardware attestation.
type MachineProber interface {
	AuthenticatedMachineID(context.Context) (string, error)
}

// DispatchResolver asks the authenticated execution-options authority for an
// exact immutable profile before any vendor process is started.
type DispatchResolver interface {
	ResolveDispatchProfile(context.Context, string, string, string) (dispatchprofile.Profile, error)
}

type SessionState string

const (
	StateStarting      SessionState = "starting"
	StateRunning       SessionState = "running"
	StateStopping      SessionState = "stopping"
	StateStopped       SessionState = "stopped"
	StateExited        SessionState = "exited"
	StateFailed        SessionState = "failed"
	StateOwnershipLost SessionState = "ownership_lost"
)

type Session struct {
	ID                  string                   `json:"id"`
	Identity            string                   `json:"identity"`
	ProjectID           int64                    `json:"project_id"`
	Role                string                   `json:"role"`
	ParentSessionID     string                   `json:"parent_harness_session_id,omitempty"`
	TicketID            int64                    `json:"ticket_id,omitempty"`
	WorkShape           string                   `json:"work_shape,omitempty"`
	Adapter             string                   `json:"adapter"`
	Workspace           string                   `json:"workspace"`
	WorkspaceProvenance WorkspaceProvenance      `json:"workspace_provenance"`
	DispatchProfile     *dispatchprofile.Profile `json:"dispatch_profile,omitempty"`
	ModelEvidence       *ModelEvidence           `json:"model_evidence,omitempty"`
	AccountLabel        string                   `json:"account_label"`
	AccountKey          string                   `json:"account_key,omitempty"`
	HarnessSessionID    string                   `json:"harness_session_id,omitempty"`
	Capabilities        []Capability             `json:"capabilities"`
	Managed             bool                     `json:"managed"`
	Steerable           bool                     `json:"steerable"`
	State               SessionState             `json:"state"`
	PID                 int                      `json:"pid,omitempty"`
	LastEventKind       EventKind                `json:"last_event_kind,omitempty"`
	ActivitySequence    int64                    `json:"activity_sequence"`
	ActivityAt          time.Time                `json:"activity_at,omitempty"`
	LastCorrelationID   string                   `json:"last_correlation_id,omitempty"`
	LastErrorCode       ErrorCode                `json:"last_error_code,omitempty"`
	PendingDecisions    []PendingDecision        `json:"pending_decisions,omitempty"`
	DecisionRefusals    []DecisionRefusal        `json:"decision_refusals,omitempty"`
	StartedAt           time.Time                `json:"started_at"`
	HeartbeatAt         time.Time                `json:"heartbeat_at"`
	ExitedAt            *time.Time               `json:"exited_at,omitempty"`
	Reporter            ReporterState            `json:"reporter,omitempty"`
}

// QueueRetentionReport is the content-free operator view of owned Pi queue
// retention. Exact instruction text stays owner-private in HeldQueue and is
// never copied into this report, receipts, status, or logs.
type QueueRetentionReport struct {
	Generation string `json:"generation"`
	Outcome    string `json:"outcome"`
	Steering   int    `json:"steering"`
	FollowUp   int    `json:"follow_up"`
}

type ReporterState struct {
	PublicSessionID string              `json:"public_session_id,omitempty"`
	Capabilities    []Capability        `json:"capabilities,omitempty"`
	Pending         *ReporterCompletion `json:"pending,omitempty"`
	RemoteClosed    bool                `json:"remote_closed,omitempty"`
	Closed          bool                `json:"closed,omitempty"`
}

type ReporterCompletion struct {
	ControlID string `json:"control_id"`
	Kind      string `json:"kind"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason"`
}

type Status struct {
	DaemonID             string    `json:"daemon_id"`
	Instance             string    `json:"instance"`
	HeartbeatAt          time.Time `json:"heartbeat_at"`
	Sessions             []Session `json:"sessions"`
	ReporterErrorCode    ErrorCode `json:"reporter_error_code,omitempty"`
	ReporterFailureCount int64     `json:"reporter_failure_count,omitempty"`
}

// Reporter is the narrow PAI-848 handoff. Its implementation authenticates
// to PPM and upserts M161 harness_sessions; agentd owns no DB/API schema.
type Reporter interface {
	ReportStatus(context.Context, Status) error
}

type Controller interface {
	Interrupt(context.Context, string, ControlRequest) (Receipt, error)
	Stop(context.Context, string, ControlRequest) (Receipt, error)
	Reject(context.Context, string, ControlRequest, ErrorCode) error
	CheckpointReporter(context.Context, string, ControlRequest, ReporterState) error
}

// ControllerBindingReporter consumes M161 typed controls. The supervisor
// binds it before starting the reporting goroutine.
type ControllerBindingReporter interface {
	BindController(Controller) error
}

// Receipt is the exact local effect evidence the PAI-848 hub integration must
// bind to its existing durable message/control ledger row. There is no local
// queue fallback for managed control operations.
type Receipt struct {
	Operation       string    `json:"operation"`
	SessionID       string    `json:"session_id"`
	Instance        string    `json:"instance"`
	ProjectID       int64     `json:"project_id"`
	Identity        string    `json:"identity"`
	RequestedLevel  string    `json:"requested_level"`
	EffectiveLevel  string    `json:"effective_level"`
	FallbackReason  string    `json:"fallback_reason"`
	Primitive       string    `json:"primitive"`
	CorrelationID   string    `json:"correlation_id"`
	VendorMessageID string    `json:"vendor_message_id,omitempty"`
	AppliedAt       time.Time `json:"applied_at"`
}
