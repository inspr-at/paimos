// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package agentd owns operator-local harness child processes and exposes a
// private last-meter transport. It never accepts or persists vendor tokens.
package agentd

import (
	"context"
	"errors"
	"time"
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
	ErrSessionNotFound    = errors.New("managed session not found")
	ErrSessionNotRunning  = errors.New("managed session is not running")
	ErrAdapterUnsupported = errors.New("harness adapter is unsupported")
	ErrCapabilityMissing  = errors.New("managed session capability is unavailable")
)

type StartRequest struct {
	Adapter   string `json:"adapter"`
	Workspace string `json:"workspace"`
	Prompt    string `json:"prompt"`
	Identity  string `json:"identity"`
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
)

// ErrorCode is deliberately finite so adapter events and durable status can
// never become a side channel for stderr, vendor payloads, or message text.
type ErrorCode string

const (
	ErrorEventStreamBound  ErrorCode = "event_stream_bound"
	ErrorAppServerProtocol ErrorCode = "app_server_protocol"
	ErrorChildExitFailed   ErrorCode = "child_exit_failed"
	ErrorChildStopFailed   ErrorCode = "child_stop_failed"
	ErrorOwnershipLost     ErrorCode = "ownership_lost"
)

type ControlRequest struct {
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
	ID                string       `json:"id"`
	Identity          string       `json:"identity"`
	Adapter           string       `json:"adapter"`
	Workspace         string       `json:"workspace"`
	HarnessSessionID  string       `json:"harness_session_id,omitempty"`
	Capabilities      []Capability `json:"capabilities"`
	Managed           bool         `json:"managed"`
	Steerable         bool         `json:"steerable"`
	State             SessionState `json:"state"`
	PID               int          `json:"pid,omitempty"`
	LastEventKind     EventKind    `json:"last_event_kind,omitempty"`
	LastCorrelationID string       `json:"last_correlation_id,omitempty"`
	LastErrorCode     ErrorCode    `json:"last_error_code,omitempty"`
	StartedAt         time.Time    `json:"started_at"`
	HeartbeatAt       time.Time    `json:"heartbeat_at"`
	ExitedAt          *time.Time   `json:"exited_at,omitempty"`
}

type Status struct {
	DaemonID    string    `json:"daemon_id"`
	Instance    string    `json:"instance"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	Sessions    []Session `json:"sessions"`
}

// Reporter is the narrow PAI-848 handoff. Its implementation authenticates
// to PPM and upserts M161 harness_sessions; agentd owns no DB/API schema.
type Reporter interface {
	ReportStatus(context.Context, Status) error
}

// Receipt is the exact local effect evidence the PAI-848 hub integration must
// bind to its existing durable message/control ledger row. There is no local
// queue fallback for managed control operations.
type Receipt struct {
	Operation       string    `json:"operation"`
	SessionID       string    `json:"session_id"`
	Identity        string    `json:"identity"`
	RequestedLevel  string    `json:"requested_level"`
	EffectiveLevel  string    `json:"effective_level"`
	FallbackReason  string    `json:"fallback_reason"`
	Primitive       string    `json:"primitive"`
	CorrelationID   string    `json:"correlation_id"`
	VendorMessageID string    `json:"vendor_message_id,omitempty"`
	AppliedAt       time.Time `json:"applied_at"`
}
