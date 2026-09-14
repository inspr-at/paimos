// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package conversationturns owns the durable Aithema conversation-service
// authority. It stores only bounded inference input and assistant output;
// executable policy, runtime, profile, and account selection are server-bound.
package conversationturns

import (
	"errors"
	"time"
)

const (
	SchemaVersion      = 1
	ExecutionPolicyID  = "aithema-conversation-v1"
	ActorIssuerHeader  = "X-Paimos-Actor-Issuer"
	ActorSubjectHeader = "X-Paimos-Actor-Subject"
	RuntimeLeaseHeader = "X-Paimos-Runtime-Lease"

	MaximumInputBytes  = 128 * 1024
	MaximumMessages    = 128
	MaximumOutputBytes = 256 * 1024
	MaximumEventBytes  = 8 * 1024
	MaximumEvents      = 512
	MaximumTimeoutMS   = 180_000

	// JSON escaping can expand otherwise-valid UTF-8 input. These caps bound
	// transport memory without accidentally shrinking the decoded contracts.
	MaximumRequestJSONBytes = MaximumInputBytes*6 + 32*1024
	MaximumReportJSONBytes  = MaximumEventBytes*6 + 8*1024
)

var (
	ErrInvalid     = errors.New("conversation_invalid")
	ErrUnavailable = errors.New("conversation_unavailable")
	ErrConflict    = errors.New("conversation_conflict")
	ErrTooLarge    = errors.New("conversation_too_large")
	ErrStorage     = errors.New("conversation_storage_unavailable")
)

type Actor struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	SchemaVersion   int       `json:"schema_version"`
	RequestID       string    `json:"request_id"`
	BindingID       string    `json:"binding_id"`
	BindingRevision int64     `json:"binding_revision"`
	Actor           Actor     `json:"actor"`
	ProjectRef      string    `json:"project_ref"`
	ConversationID  string    `json:"conversation_id"`
	TurnID          string    `json:"turn_id"`
	Purpose         string    `json:"purpose"`
	System          string    `json:"system"`
	Messages        []Message `json:"messages"`
	TimeoutMS       int64     `json:"timeout_ms"`
}

type Call struct {
	SchemaVersion int    `json:"schema_version"`
	CallID        string `json:"call_id"`
	RequestID     string `json:"request_id"`
	State         string `json:"state"`
	DeadlineAt    string `json:"deadline_at"`
	LastSequence  int64  `json:"last_sequence"`
	OutputText    string `json:"output_text,omitempty"`
	OutputSHA256  string `json:"output_sha256,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
}

type Event struct {
	Sequence     int64  `json:"sequence"`
	Kind         string `json:"kind"`
	Text         string `json:"text,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	OutputSHA256 string `json:"output_sha256,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
}

type EventsPage struct {
	SchemaVersion int     `json:"schema_version"`
	CallID        string  `json:"call_id"`
	Events        []Event `json:"events"`
	Call          Call    `json:"call"`
}

type Limits struct {
	MaxInputBytes  int64 `json:"max_input_bytes"`
	MaxMessages    int64 `json:"max_messages"`
	MaxOutputBytes int64 `json:"max_output_bytes"`
	MaxEventBytes  int64 `json:"max_event_bytes"`
	MaxEvents      int64 `json:"max_events"`
	MaxTimeoutMS   int64 `json:"max_timeout_ms"`
}

type ActorMapping struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	UserID  int64  `json:"user_id"`
}

type Enrollment struct {
	SchemaVersion          int            `json:"schema_version"`
	Name                   string         `json:"name"`
	ProjectID              int64          `json:"project_id"`
	HostID                 string         `json:"host_id"`
	ProjectRef             string         `json:"project_ref"`
	RuntimeID              string         `json:"runtime_id"`
	RuntimeGeneration      string         `json:"runtime_generation"`
	AccountKey             string         `json:"account_key"`
	AttachmentRevision     int64          `json:"attachment_revision"`
	DispatchProfileID      string         `json:"dispatch_profile_id"`
	DispatchProfileVersion string         `json:"dispatch_profile_version"`
	Actors                 []ActorMapping `json:"actors"`
	Limits                 Limits         `json:"limits"`
	ExpiresAt              string         `json:"expires_at"`
}

type Binding struct {
	ID                     string `json:"binding_id"`
	Revision               int64  `json:"revision"`
	ProjectID              int64  `json:"project_id"`
	HostID                 string `json:"host_id"`
	ProjectRef             string `json:"project_ref"`
	RuntimeID              string `json:"runtime_id"`
	RuntimeGeneration      string `json:"runtime_generation"`
	AccountLabel           string `json:"-"`
	AccountKey             string `json:"account_key"`
	AttachmentRevision     int64  `json:"attachment_revision"`
	DispatchProfileID      string `json:"dispatch_profile_id"`
	DispatchProfileVersion string `json:"dispatch_profile_version"`
	ExecutionPolicyID      string `json:"execution_policy_id"`
	Limits                 Limits `json:"limits"`
}

type ClaimRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Generation    string `json:"generation"`
}

type Claim struct {
	Call                   Call           `json:"call"`
	ExecutionGeneration    string         `json:"execution_generation"`
	RuntimeGeneration      string         `json:"runtime_generation"`
	AccountKey             string         `json:"account_key"`
	AttachmentRevision     int64          `json:"attachment_revision"`
	DispatchProfileID      string         `json:"dispatch_profile_id"`
	DispatchProfileVersion string         `json:"dispatch_profile_version"`
	ExecutionPolicyID      string         `json:"execution_policy_id"`
	System                 string         `json:"system"`
	Messages               []Message      `json:"messages"`
	Purpose                string         `json:"purpose"`
	Limits                 ClaimLimits    `json:"limits"`
	OutputSchema           map[string]any `json:"output_schema,omitempty"`
}

type ClaimEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Claim         *Claim `json:"claim"`
}

type ClaimLimits struct {
	MaxOutputBytes int64 `json:"max_output_bytes"`
	MaxEvents      int64 `json:"max_events"`
}

type ReportRequest struct {
	SchemaVersion       int    `json:"schema_version"`
	Generation          string `json:"generation"`
	ExecutionGeneration string `json:"execution_generation"`
	Event               Event  `json:"event"`
}

type Control struct {
	SchemaVersion       int    `json:"schema_version"`
	CallID              string `json:"call_id"`
	ExecutionGeneration string `json:"execution_generation"`
	Continue            bool   `json:"continue"`
	CancelRequested     bool   `json:"cancel_requested"`
	DeadlineAt          string `json:"deadline_at"`
}

type row struct {
	Call
	BindingID              string
	BindingRevision        int64
	APIKeyID               int64
	ProjectID              int64
	Actor                  Actor
	ActorUserID            int64
	ProjectRef             string
	ConversationID         string
	TurnID                 string
	Purpose                string
	RequestJSON            string
	RequestDigest          []byte
	ExecutionGeneration    string
	RuntimeID              string
	RuntimeGeneration      string
	AccountKey             string
	AttachmentRevision     int64
	DispatchProfileID      string
	DispatchProfileVersion string
	ExecutionPolicyID      string
	NativeThreadID         string
	NativeTurnID           string
	AssembledText          string
	CreatedAt              string
	UpdatedAt              string
}

func parseTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed.UTC(), err == nil
}
