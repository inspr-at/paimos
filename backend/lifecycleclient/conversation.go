// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package lifecycleclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	ConversationPolicyV1  = "aithema-conversation-v1"
	conversationMaxInput  = 128 << 10
	conversationMaxOutput = 256 << 10
	conversationMaxDelta  = 8 << 10
	conversationMaxEvents = 512
)

var conversationStableValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ConversationCall struct {
	SchemaVersion int    `json:"schema_version"`
	CallID        string `json:"call_id"`
	RequestID     string `json:"request_id"`
	State         string `json:"state"`
	DeadlineAt    string `json:"deadline_at"`
	LastSequence  int    `json:"last_sequence"`
	OutputText    string `json:"output_text,omitempty"`
	OutputSHA256  string `json:"output_sha256,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
}

type ConversationEvent struct {
	Sequence     int    `json:"sequence"`
	Kind         string `json:"kind"`
	Text         string `json:"text,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	OutputSHA256 string `json:"output_sha256,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
}

type ConversationLimits struct {
	MaxOutputBytes int `json:"max_output_bytes"`
	MaxEvents      int `json:"max_events"`
}

type ConversationClaim struct {
	Call                   ConversationCall      `json:"call"`
	ExecutionGeneration    string                `json:"execution_generation"`
	RuntimeGeneration      string                `json:"runtime_generation"`
	AccountKey             string                `json:"account_key"`
	AttachmentRevision     int64                 `json:"attachment_revision"`
	DispatchProfileID      string                `json:"dispatch_profile_id"`
	DispatchProfileVersion string                `json:"dispatch_profile_version"`
	ExecutionPolicyID      string                `json:"execution_policy_id"`
	System                 string                `json:"system"`
	Messages               []ConversationMessage `json:"messages"`
	Purpose                string                `json:"purpose"`
	Limits                 ConversationLimits    `json:"limits"`
	OutputSchema           json.RawMessage       `json:"output_schema,omitempty"`
}

type ConversationControl struct {
	SchemaVersion       int    `json:"schema_version"`
	CallID              string `json:"call_id"`
	ExecutionGeneration string `json:"execution_generation"`
	Continue            bool   `json:"continue"`
	CancelRequested     bool   `json:"cancel_requested"`
	DeadlineAt          string `json:"deadline_at"`
}

type ConversationAuthority interface {
	ClaimConversation(context.Context, string, string) (*ConversationClaim, error)
	ConversationControl(context.Context, string, string, string) (ConversationControl, error)
	ReportConversationEvent(context.Context, string, string, string, string, ConversationEvent) (ConversationCall, error)
}

func (h *HTTP) conversationRoute(runtime, suffix string) string {
	return fmt.Sprintf("/api/projects/%d/runtimes/%s/conversation/v1%s", h.project, runtime, suffix)
}

func (h *HTTP) ClaimConversation(ctx context.Context, runtime, generation string) (*ConversationClaim, error) {
	if uuid.Validate(runtime) != nil || uuid.Validate(generation) != nil {
		return nil, ErrOwnership
	}
	var raw json.RawMessage
	err := h.Request(ctx, http.MethodPost, h.conversationRoute(runtime, "/claim"), nil,
		struct {
			SchemaVersion int    `json:"schema_version"`
			Generation    string `json:"generation"`
		}{1, generation}, &raw)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		SchemaVersion int                `json:"schema_version"`
		Claim         *ConversationClaim `json:"claim"`
	}
	if strictConversationJSON(raw, &envelope) != nil || envelope.SchemaVersion != 1 {
		return nil, ErrOwnership
	}
	if envelope.Claim != nil && validateConversationClaim(*envelope.Claim, runtime, generation, time.Now()) != nil {
		return nil, ErrOwnership
	}
	return envelope.Claim, nil
}

func (h *HTTP) ConversationControl(ctx context.Context, runtime, callID, executionGeneration string) (ConversationControl, error) {
	if uuid.Validate(runtime) != nil || uuid.Validate(callID) != nil || uuid.Validate(executionGeneration) != nil {
		return ConversationControl{}, ErrOwnership
	}
	route := h.conversationRoute(runtime, "/calls/"+callID+"/control?execution_generation="+url.QueryEscape(executionGeneration))
	var raw json.RawMessage
	if err := h.Request(ctx, http.MethodGet, route, nil, nil, &raw); err != nil {
		return ConversationControl{}, err
	}
	var out ConversationControl
	if strictConversationJSON(raw, &out) != nil || out.SchemaVersion != 1 || out.CallID != callID || out.ExecutionGeneration != executionGeneration {
		return ConversationControl{}, ErrOwnership
	}
	if _, err := time.Parse(time.RFC3339, out.DeadlineAt); err != nil {
		return ConversationControl{}, ErrOwnership
	}
	return out, nil
}

func (h *HTTP) ReportConversationEvent(ctx context.Context, runtime, generation, callID, executionGeneration string, event ConversationEvent) (ConversationCall, error) {
	if uuid.Validate(runtime) != nil || uuid.Validate(generation) != nil || uuid.Validate(callID) != nil || uuid.Validate(executionGeneration) != nil || validateConversationEvent(event) != nil {
		return ConversationCall{}, ErrOwnership
	}
	var raw json.RawMessage
	err := h.Request(ctx, http.MethodPost, h.conversationRoute(runtime, "/calls/"+callID+"/events"), nil,
		struct {
			SchemaVersion       int               `json:"schema_version"`
			Generation          string            `json:"generation"`
			ExecutionGeneration string            `json:"execution_generation"`
			Event               ConversationEvent `json:"event"`
		}{1, generation, executionGeneration, event}, &raw)
	if err != nil {
		return ConversationCall{}, err
	}
	var out ConversationCall
	if strictConversationJSON(raw, &out) != nil || validateConversationCall(out) != nil || out.CallID != callID || out.LastSequence < event.Sequence {
		return ConversationCall{}, ErrOwnership
	}
	return out, nil
}

func strictConversationJSON(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ErrOwnership
	}
	return nil
}

func validateConversationClaim(claim ConversationClaim, _ string, generation string, now time.Time) error {
	if validateConversationCall(claim.Call) != nil || claim.Call.State != "claimed" || uuid.Validate(claim.ExecutionGeneration) != nil ||
		claim.RuntimeGeneration != generation || !conversationStableValue.MatchString(claim.AccountKey) || claim.AttachmentRevision <= 0 ||
		!conversationStableValue.MatchString(claim.DispatchProfileID) || !conversationStableValue.MatchString(claim.DispatchProfileVersion) ||
		claim.ExecutionPolicyID != ConversationPolicyV1 || len(claim.Messages) == 0 || len(claim.Messages) > 128 ||
		claim.Limits.MaxOutputBytes <= 0 || claim.Limits.MaxOutputBytes > conversationMaxOutput || claim.Limits.MaxEvents < 2 || claim.Limits.MaxEvents > conversationMaxEvents {
		return ErrOwnership
	}
	deadline, _ := time.Parse(time.RFC3339, claim.Call.DeadlineAt)
	if !now.Before(deadline) || deadline.After(now.Add(181*time.Second)) {
		return ErrOwnership
	}
	total := len(claim.System)
	if !utf8.ValidString(claim.System) {
		return ErrOwnership
	}
	for _, message := range claim.Messages {
		if (message.Role != "user" && message.Role != "assistant") || !utf8.ValidString(message.Content) {
			return ErrOwnership
		}
		total += len(message.Content)
	}
	if total > conversationMaxInput {
		return ErrOwnership
	}
	switch claim.Purpose {
	case "chat":
		if len(claim.OutputSchema) != 0 {
			return ErrOwnership
		}
	case "understand", "interpret":
		var schema map[string]any
		if len(claim.OutputSchema) == 0 || len(claim.OutputSchema) > 64<<10 || strictConversationJSON(claim.OutputSchema, &schema) != nil || schema == nil {
			return ErrOwnership
		}
	default:
		return ErrOwnership
	}
	return nil
}

func validateConversationCall(call ConversationCall) error {
	if call.SchemaVersion != 1 || uuid.Validate(call.CallID) != nil || !conversationStableValue.MatchString(call.RequestID) || call.LastSequence < 0 {
		return ErrOwnership
	}
	if _, err := time.Parse(time.RFC3339, call.DeadlineAt); err != nil {
		return ErrOwnership
	}
	switch call.State {
	case "queued", "claimed", "running", "cancel_requested", "completed", "failed", "cancelled":
	default:
		return ErrOwnership
	}
	if call.OutputSHA256 != "" && !validLowerDigest(call.OutputSHA256) {
		return ErrOwnership
	}
	if len(call.OutputText) > conversationMaxOutput || !utf8.ValidString(call.OutputText) || len(call.ErrorCode) > 64 || strings.ContainsAny(call.ErrorCode, "\x00\r\n") {
		return ErrOwnership
	}
	return nil
}

func validateConversationEvent(event ConversationEvent) error {
	if event.Sequence <= 0 || len(event.Text) > conversationMaxDelta || !utf8.ValidString(event.Text) || len(event.ErrorCode) > 64 || strings.ContainsAny(event.ErrorCode, "\x00\r\n") {
		return ErrOwnership
	}
	ids := event.ThreadID != "" && event.TurnID != "" && conversationStableValue.MatchString(event.ThreadID) && conversationStableValue.MatchString(event.TurnID)
	switch event.Kind {
	case "started":
		if !ids || event.Text != "" || event.OutputSHA256 != "" || event.ErrorCode != "" {
			return ErrOwnership
		}
	case "assistant_delta":
		if !ids || event.Text == "" || event.OutputSHA256 != "" || event.ErrorCode != "" {
			return ErrOwnership
		}
	case "completed":
		if !ids || event.Text != "" || !validLowerDigest(event.OutputSHA256) || event.ErrorCode != "" {
			return ErrOwnership
		}
	case "failed", "cancelled":
		if event.Text != "" || event.OutputSHA256 != "" || event.ErrorCode == "" || (event.ThreadID != "" || event.TurnID != "") && !ids {
			return ErrOwnership
		}
	default:
		return ErrOwnership
	}
	return nil
}

func validLowerDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

var _ ConversationAuthority = (*HTTP)(nil)
