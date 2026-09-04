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
	ErrSessionNotFound       = errors.New("managed session not found")
	ErrSessionNotRunning     = errors.New("managed session is not running")
	ErrAdapterUnsupported    = errors.New("harness adapter is unsupported")
	ErrCapabilityMissing     = errors.New("managed session capability is unavailable")
	ErrControlScopeMismatch  = errors.New("managed control scope does not match the owned session")
	ErrControlReplayConflict = errors.New("managed control correlation was reused with different input")
	ErrControlReplayCapacity = errors.New("managed control replay bound reached")
	ErrDispatchProfile       = errors.New("managed dispatch profile is unavailable")
	ErrWorkspaceConflict     = errors.New("managed workspace is already owned")
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
}

type AdapterEvent struct {
	Kind             EventKind
	HarnessSessionID string
	CorrelationID    string
	ErrorCode        ErrorCode
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
	ErrorChildStopFailed     ErrorCode = "child_stop_failed"
	ErrorOwnershipLost       ErrorCode = "ownership_lost"
	ErrorReporterUnavailable ErrorCode = "reporter_unavailable"
	ErrorWorkspaceConflict   ErrorCode = "workspace_conflict"
)

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
	AccountLabel        string                   `json:"account_label"`
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
	StartedAt           time.Time                `json:"started_at"`
	HeartbeatAt         time.Time                `json:"heartbeat_at"`
	ExitedAt            *time.Time               `json:"exited_at,omitempty"`
	Reporter            ReporterState            `json:"reporter,omitempty"`
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
