// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxCursorPendingDecisions = 8
	maxCursorDecisionRefusals = 8
	maxCursorContentBlocks    = 8
	maxCursorInspectBytes     = 8 << 10
	maxCursorVisibleBytes     = 16 << 10
	cursorDecisionTTL         = 2 * time.Minute
	cursorPermissionPrimitive = "cursor acp session/request_permission"
	cursorQuestionPrimitive   = "cursor acp cursor/ask_question"
	cursorPlanPrimitive       = "cursor acp cursor/create_plan"
)

type cursorHeldDecision struct {
	public     PendingDecision
	rpcID      json.RawMessage
	optionKind map[string]string
	questionID string
	inspect    DecisionInspect
	used       bool
	timer      *time.Timer
}

func (p *cursorProcess) PendingDecisions() []PendingDecision {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	now := time.Now()
	out := make([]PendingDecision, 0, len(p.held))
	for _, held := range p.held {
		if held == nil || held.used || now.After(held.public.ExpiresAt) {
			continue
		}
		item := held.public
		item.OptionIDs = append([]string(nil), held.public.OptionIDs...)
		out = append(out, item)
	}
	return out
}

func (p *cursorProcess) DecisionRefusals() []DecisionRefusal {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	out := append([]DecisionRefusal(nil), p.refusals...)
	for i := range out {
		if out[i].Structure != nil {
			shape := *out[i].Structure
			out[i].Structure = &shape
		}
	}
	return out
}

func (p *cursorProcess) EvidenceRecords() []cursorEvidenceRecord {
	return p.evidence.snapshot()
}

func (p *cursorProcess) Answer(_ context.Context, request DecisionAnswer) (ControlEffect, error) {
	if request.Authority != DecisionAuthorityLocalOperator {
		return ControlEffect{}, ErrDecisionAuthority
	}
	p.stateMu.Lock()
	if p.terminalFailure || p.sessionID == "" {
		p.stateMu.Unlock()
		return ControlEffect{}, ErrSessionNotRunning
	}
	if request.Generation != p.generation {
		p.stateMu.Unlock()
		return ControlEffect{}, ErrDecisionMismatch
	}
	held := p.held[request.RequestID]
	if held == nil {
		p.stateMu.Unlock()
		return ControlEffect{}, ErrDecisionUnknown
	}
	if held.used {
		p.stateMu.Unlock()
		return ControlEffect{}, ErrDecisionConsumed
	}
	if time.Now().After(held.public.ExpiresAt) {
		p.stateMu.Unlock()
		p.expireHeld(request.RequestID)
		return ControlEffect{}, ErrDecisionExpired
	}
	if request.Digest != held.public.Digest {
		p.stateMu.Unlock()
		return ControlEffect{}, ErrDecisionMismatch
	}
	kind, ok := held.optionKind[request.OptionID]
	if !ok {
		p.stateMu.Unlock()
		return ControlEffect{}, ErrDecisionMismatch
	}
	if held.public.Kind == DecisionPermission && kind == "allow_always" {
		p.stateMu.Unlock()
		return ControlEffect{}, ErrDecisionMismatch
	}
	if held.inspect.Incomplete && cursorOptionApproves(held.public.Kind, kind) {
		p.stateMu.Unlock()
		return ControlEffect{}, ErrDecisionIncomplete
	}
	held.used = true
	if held.timer != nil {
		held.timer.Stop()
	}
	rpcID := held.rpcID
	method := held.public.Method
	digest := held.public.Digest
	toolKind := held.public.ToolKind
	delete(p.held, request.RequestID)
	sessionID := p.sessionID
	p.stateMu.Unlock()

	result, primitive := cursorDecisionResult(held.public.Kind, request.OptionID, held.questionID)
	if err := p.send(map[string]any{"jsonrpc": "2.0", "id": rawJSON(rpcID), "result": result}); err != nil {
		return ControlEffect{}, err
	}
	p.recordEvidence(cursorEvidenceRecord{
		Generation: request.Generation, RequestID: request.RequestID, Method: method, Digest: digest,
		OptionID: request.OptionID, Authority: DecisionAuthorityLocalOperator, Outcome: "selected", ToolKind: toolKind,
	})
	p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	return ControlEffect{Primitive: primitive, CorrelationID: request.CorrelationID, VendorMessageID: sessionID}, nil
}

func cursorOptionApproves(kind DecisionKind, optionKind string) bool {
	switch kind {
	case DecisionPermission:
		return optionKind == "allow_once" || optionKind == "allow_always"
	case DecisionPlan:
		return optionKind == "accepted"
	default:
		return true
	}
}

func (p *cursorProcess) Inspect(request DecisionInspectRequest) (DecisionInspect, error) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if request.RequestID == "" || request.Generation != p.generation || request.Digest == "" {
		return DecisionInspect{}, ErrDecisionMismatch
	}
	if p.terminalFailure {
		return DecisionInspect{}, ErrSessionNotRunning
	}
	held := p.held[request.RequestID]
	if held == nil || held.used || time.Now().After(held.public.ExpiresAt) {
		return DecisionInspect{}, ErrDecisionUnknown
	}
	if request.Digest != held.public.Digest {
		return DecisionInspect{}, ErrDecisionMismatch
	}
	out := held.inspect
	out.OptionIDs = append([]string(nil), held.public.OptionIDs...)
	out.Options = append([]DecisionOption(nil), held.inspect.Options...)
	out.ExpiresAt = held.public.ExpiresAt
	out.Untrusted = true
	return out, nil
}

func (p *cursorProcess) VisibleOutput() (VisibleOutput, error) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	text := p.visible.String()
	sum := sha256.Sum256([]byte(text))
	return VisibleOutput{
		Generation: p.generation, Text: text, Digest: hex.EncodeToString(sum[:]),
		Bytes: len(text), Truncated: p.visibleTruncated,
	}, nil
}

func (p *cursorProcess) appendVisible(text string) {
	if text == "" {
		return
	}
	if _, ok, _ := inspectableText(text, maxCursorVisibleBytes); !ok {
		return
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.visible.Len() >= maxCursorVisibleBytes {
		p.visibleTruncated = true
		return
	}
	remain := maxCursorVisibleBytes - p.visible.Len()
	if len(text) > remain {
		text = string([]byte(text)[:remain])
		for !utf8.ValidString(text) && len(text) > 0 {
			text = text[:len(text)-1]
		}
		p.visibleTruncated = true
	}
	p.visible.WriteString(text)
}

func (p *cursorProcess) clearVisible() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.visible.Reset()
	p.visibleTruncated = false
}

func cursorDecisionResult(kind DecisionKind, optionID, questionID string) (map[string]any, string) {
	switch kind {
	case DecisionQuestion:
		if questionID == "" {
			questionID = "q1"
		}
		return map[string]any{"outcome": map[string]any{
			"outcome": "answered",
			"answers": []map[string]any{{"questionId": questionID, "selectedOptionIds": []string{optionID}}},
		}}, cursorQuestionPrimitive
	case DecisionPlan:
		outcome := "rejected"
		if optionID == "accepted" {
			outcome = "accepted"
		}
		return map[string]any{"outcome": map[string]any{"outcome": outcome}}, cursorPlanPrimitive
	default:
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}, cursorPermissionPrimitive
	}
}

func (p *cursorProcess) holdPeerDecision(message cursorRPCMessage) {
	if len(message.ID) == 0 {
		p.noteRefusal(closedPeerMethod(message.Method), "missing_id")
		return
	}
	p.stateMu.Lock()
	sessionID := p.sessionID
	generation := p.generation
	terminal := p.terminalFailure
	ttl := p.decisionTTL
	p.stateMu.Unlock()
	if ttl <= 0 {
		ttl = cursorDecisionTTL
	}
	if sessionID == "" || terminal {
		p.failClosedPeer(message.ID, -32602, "invalid params")
		p.noteRefusal(closedPeerMethod(message.Method), "unowned")
		p.recordEvidence(cursorEvidenceRecord{Generation: generation, Method: closedPeerMethod(message.Method), Outcome: "refused"})
		p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
		return
	}
	parsed, diagnostic, ok := parseCursorDecisionRequestDetailed(message, sessionID, generation)
	if !ok {
		p.failClosedPeer(message.ID, -32602, "invalid params")
		p.noteRefusalDiagnostic(closedPeerMethod(message.Method), "invalid", diagnostic)
		p.recordEvidence(cursorEvidenceRecord{Generation: generation, Method: closedPeerMethod(message.Method), Outcome: "refused"})
		p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
		return
	}
	parsed.public.ExpiresAt = time.Now().UTC().Add(ttl)
	parsed.inspect.ExpiresAt = parsed.public.ExpiresAt
	requestID := parsed.public.RequestID
	p.stateMu.Lock()
	if p.terminalFailure || p.sessionID == "" || p.sessionID != sessionID {
		p.stateMu.Unlock()
		p.failClosedPeer(message.ID, -32602, "invalid params")
		p.noteRefusal(parsed.public.Method, "unowned")
		p.recordEvidence(cursorEvidenceRecord{Generation: generation, RequestID: requestID, Method: parsed.public.Method, Digest: parsed.public.Digest, Outcome: "refused"})
		p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
		return
	}
	if existing := p.held[requestID]; existing != nil {
		p.stateMu.Unlock()
		p.noteRefusal(parsed.public.Method, "collision")
		p.recordEvidence(cursorEvidenceRecord{Generation: generation, RequestID: requestID, Method: parsed.public.Method, Digest: parsed.public.Digest, Outcome: "refused"})
		p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
		return
	}
	if len(p.held) >= maxCursorPendingDecisions {
		p.stateMu.Unlock()
		p.cancelRPC(message.ID)
		p.noteRefusal(parsed.public.Method, "overflow")
		p.recordEvidence(cursorEvidenceRecord{Generation: generation, RequestID: requestID, Method: parsed.public.Method, Digest: parsed.public.Digest, Outcome: "refused"})
		p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
		return
	}
	parsed.timer = time.AfterFunc(ttl, func() { p.expireHeld(requestID) })
	p.held[requestID] = parsed
	p.stateMu.Unlock()
	p.recordEvidence(cursorEvidenceRecord{
		Generation: generation, RequestID: requestID, Method: parsed.public.Method, Digest: parsed.public.Digest,
		Outcome: "pending", ToolKind: parsed.public.ToolKind,
	})
}

func (p *cursorProcess) expireHeld(requestID string) {
	p.stateMu.Lock()
	held := p.held[requestID]
	if held == nil || held.used {
		p.stateMu.Unlock()
		return
	}
	held.used = true
	rpcID := held.rpcID
	method := held.public.Method
	digest := held.public.Digest
	delete(p.held, requestID)
	p.stateMu.Unlock()
	p.cancelRPC(rpcID)
	p.noteRefusal(method, "expired")
	p.recordEvidence(cursorEvidenceRecord{Generation: p.generation, RequestID: requestID, Method: method, Digest: digest, Outcome: "expired"})
	p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
}

func (p *cursorProcess) cancelHeldDecisions() {
	p.stateMu.Lock()
	held := p.held
	p.held = map[string]*cursorHeldDecision{}
	p.stateMu.Unlock()
	for id, item := range held {
		if item == nil || item.used {
			continue
		}
		item.used = true
		if item.timer != nil {
			item.timer.Stop()
		}
		p.cancelRPC(item.rpcID)
		p.recordEvidence(cursorEvidenceRecord{
			Generation: p.generation, RequestID: id, Method: item.public.Method, Digest: item.public.Digest, Outcome: "cancelled",
		})
	}
}

func (p *cursorProcess) cancelRPC(id json.RawMessage) {
	if len(id) == 0 {
		return
	}
	_ = p.send(map[string]any{
		"jsonrpc": "2.0", "id": rawJSON(id),
		"result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}},
	})
}

func (p *cursorProcess) noteRefusal(method, reason string) {
	p.noteRefusalDiagnostic(method, reason, nil)
}

func (p *cursorProcess) noteRefusalDiagnostic(method, reason string, diagnostic *cursorDecisionDiagnostic) {
	method = closedPeerMethod(method)
	if method == "" {
		method = "invalid"
	}
	if reason == "" || len(reason) > 32 {
		reason = "refused"
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	refusal := DecisionRefusal{Method: method, Reason: reason}
	if diagnostic != nil {
		refusal.Code = diagnostic.code
		refusal.Structure = diagnostic.structure
	}
	p.refusals = append(p.refusals, refusal)
	if len(p.refusals) > maxCursorDecisionRefusals {
		p.refusals = p.refusals[len(p.refusals)-maxCursorDecisionRefusals:]
	}
}

func (p *cursorProcess) recordEvidence(record cursorEvidenceRecord) {
	if record.Generation == "" {
		record.Generation = p.generation
	}
	p.evidence.append(record)
}

func (p *cursorProcess) recordOutputDigest(kind, raw string) {
	if raw == "" {
		return
	}
	sum := sha256.Sum256([]byte(raw))
	p.recordEvidence(cursorEvidenceRecord{
		Generation: p.generation, Method: "session/update", Outcome: "output",
		ToolKind: kind, OutputSHA: hex.EncodeToString(sum[:]), OutputLen: len(raw),
	})
}

type cursorDecisionDiagnostic struct {
	code      string
	structure *DecisionRequestShape
}

func parseCursorDecisionRequest(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, bool) {
	held, _, ok := parseCursorDecisionRequestDetailed(message, sessionID, generation)
	return held, ok
}

func parseCursorDecisionRequestDetailed(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, *cursorDecisionDiagnostic, bool) {
	switch message.Method {
	case "session/request_permission":
		return parseCursorPermissionDetailed(message, sessionID, generation)
	case "cursor/ask_question":
		held, ok := parseCursorQuestion(message, sessionID, generation)
		return held, nil, ok
	case "cursor/create_plan":
		held, ok := parseCursorPlan(message, sessionID, generation)
		return held, nil, ok
	default:
		return nil, nil, false
	}
}

func parseCursorPermission(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, bool) {
	held, _, ok := parseCursorPermissionDetailed(message, sessionID, generation)
	return held, ok
}

func parseCursorPermissionDetailed(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, *cursorDecisionDiagnostic, bool) {
	shape := cursorPermissionShape(message.Params)
	fail := func(code string) (*cursorHeldDecision, *cursorDecisionDiagnostic, bool) {
		return nil, &cursorDecisionDiagnostic{code: code, structure: shape}, false
	}
	var params struct {
		SessionID string `json:"sessionId"`
		ToolCall  *struct {
			ToolCallID string          `json:"toolCallId"`
			Kind       string          `json:"kind"`
			Title      string          `json:"title"`
			Status     string          `json:"status"`
			RawInput   json.RawMessage `json:"rawInput"`
			Content    json.RawMessage `json:"content"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return fail("permission_params_invalid")
	}
	if params.SessionID == "" {
		return fail("permission_session_missing")
	}
	if sessionID == "" || params.SessionID != sessionID {
		return fail("permission_session_mismatch")
	}
	if params.ToolCall == nil {
		return fail("permission_tool_call_missing")
	}
	if !validOpaqueID(params.ToolCall.ToolCallID) {
		return fail("permission_tool_call_id_invalid")
	}
	if len(params.Options) == 0 || len(params.Options) > 8 {
		return fail("permission_options_count_invalid")
	}
	if params.ToolCall.Status != "" && params.ToolCall.Status != "pending" {
		return fail("permission_status_invalid")
	}
	toolKind := closedToolKind(params.ToolCall.Kind)
	title, titleOK, titleTrunc := inspectableText(params.ToolCall.Title, 256)
	if params.ToolCall.Title != "" && !titleOK {
		return fail("permission_title_invalid")
	}
	toolInput, inputOK, inputTrunc := inspectableRawInput(params.ToolCall.RawInput)
	if !inputOK {
		return fail("permission_raw_input_invalid")
	}
	toolContent, contentOK, contentIncomplete := inspectableCursorContent(params.ToolCall.Content)
	if !contentOK {
		return fail("permission_content_invalid")
	}
	optionIDs := make([]string, 0, len(params.Options))
	optionKind := map[string]string{}
	options := make([]DecisionOption, 0, len(params.Options))
	for _, option := range params.Options {
		label, labelOK, labelTrunc := inspectableText(option.Name, 128)
		if !validOpaqueID(option.OptionID) || len(option.OptionID) > 64 {
			return fail("permission_option_id_invalid")
		}
		if optionKind[option.OptionID] != "" {
			return fail("permission_option_id_duplicate")
		}
		if !labelOK || label == "" || labelTrunc {
			return fail("permission_option_label_invalid")
		}
		kind := strings.TrimSpace(option.Kind)
		switch kind {
		case "allow_once", "allow_always", "reject_once", "reject_always":
		default:
			return fail("permission_option_kind_unsupported")
		}
		optionIDs = append(optionIDs, option.OptionID)
		optionKind[option.OptionID] = kind
		options = append(options, DecisionOption{ID: option.OptionID, Label: label, Kind: kind})
	}
	if title == "" && toolInput == "" && toolContent == "" {
		return fail("permission_operation_missing")
	}
	incomplete := titleTrunc || inputTrunc || contentIncomplete
	display := title + "\x00" + toolInput + "\x00" + toolContent + "\x00" + cursorOptionBinding(options)
	digest := cursorDecisionDigest(generation, message.Method, params.SessionID, params.ToolCall.ToolCallID, toolKind, message.Params, optionIDs, display)
	public := PendingDecision{
		RequestID: cursorDecisionRequestID(generation, message.ID, message.Method), Generation: generation,
		Method: message.Method, Kind: DecisionPermission, ToolKind: toolKind, Digest: digest, OptionIDs: optionIDs,
	}
	return newHeldDecision(message, public, optionKind, "", DecisionInspect{
		RequestID: public.RequestID, Generation: generation, Method: message.Method, Kind: DecisionPermission,
		ToolKind: toolKind, Digest: digest, OptionIDs: optionIDs, Options: options, Title: title, ToolInput: toolInput,
		ToolContent: toolContent, Incomplete: incomplete, Untrusted: true,
	}), nil, true
}

func parseCursorQuestion(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, bool) {
	var params struct {
		SessionID  string `json:"sessionId"`
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Questions  []struct {
			ID      string `json:"id"`
			Prompt  string `json:"prompt"`
			Options []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"options"`
			AllowMultiple bool `json:"allowMultiple"`
		} `json:"questions"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return nil, false
	}
	if sessionID == "" || (params.SessionID != "" && params.SessionID != sessionID) {
		return nil, false
	}
	if !validOpaqueID(params.ToolCallID) || len(params.Questions) != 1 || params.Questions[0].AllowMultiple {
		return nil, false
	}
	question := params.Questions[0]
	if !validOpaqueID(question.ID) || len(question.Options) == 0 || len(question.Options) > 8 {
		return nil, false
	}
	title, titleOK, titleTrunc := inspectableText(params.Title, 256)
	if params.Title != "" && !titleOK {
		return nil, false
	}
	prompt, promptOK, promptTrunc := inspectableText(question.Prompt, maxCursorInspectBytes)
	if !promptOK || prompt == "" {
		return nil, false
	}
	optionIDs := make([]string, 0, len(question.Options))
	optionKind := map[string]string{}
	options := make([]DecisionOption, 0, len(question.Options))
	for _, option := range question.Options {
		label, labelOK, labelTrunc := inspectableText(option.Label, 128)
		if !validOpaqueID(option.ID) || optionKind[option.ID] != "" || !labelOK || label == "" || labelTrunc {
			return nil, false
		}
		optionIDs = append(optionIDs, option.ID)
		optionKind[option.ID] = "choice"
		options = append(options, DecisionOption{ID: option.ID, Label: label, Kind: "choice"})
	}
	raw, _ := json.Marshal(params.Questions)
	incomplete := titleTrunc || promptTrunc
	display := title + "\x00" + prompt + "\x00" + cursorOptionBinding(options)
	digest := cursorDecisionDigest(generation, message.Method, sessionID, params.ToolCallID, "", raw, optionIDs, display)
	public := PendingDecision{
		RequestID: cursorDecisionRequestID(generation, message.ID, message.Method), Generation: generation,
		Method: message.Method, Kind: DecisionQuestion, Digest: digest, OptionIDs: optionIDs,
	}
	return newHeldDecision(message, public, optionKind, question.ID, DecisionInspect{
		RequestID: public.RequestID, Generation: generation, Method: message.Method, Kind: DecisionQuestion,
		Digest: digest, OptionIDs: optionIDs, Options: options, Title: title, Detail: prompt, Incomplete: incomplete, Untrusted: true,
	}), true
}

func parseCursorPlan(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, bool) {
	var params struct {
		SessionID  string `json:"sessionId"`
		ToolCallID string `json:"toolCallId"`
		Name       string `json:"name"`
		Overview   string `json:"overview"`
		Plan       string `json:"plan"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return nil, false
	}
	if sessionID == "" || (params.SessionID != "" && params.SessionID != sessionID) {
		return nil, false
	}
	if !validOpaqueID(params.ToolCallID) || strings.TrimSpace(params.Plan) == "" || len(params.Plan) > maxTextBytes {
		return nil, false
	}
	title, titleOK, titleTrunc := inspectableText(params.Name, 256)
	if params.Name != "" && !titleOK {
		return nil, false
	}
	overview, overviewOK, overviewTrunc := inspectableText(params.Overview, 512)
	if params.Overview != "" && !overviewOK {
		return nil, false
	}
	plan, planOK, planTrunc := inspectableText(params.Plan, maxCursorInspectBytes)
	if !planOK || plan == "" {
		return nil, false
	}
	optionIDs := []string{"accepted", "rejected"}
	optionKind := map[string]string{"accepted": "accepted", "rejected": "rejected"}
	options := []DecisionOption{
		{ID: "accepted", Label: "accepted", Kind: "accepted"},
		{ID: "rejected", Label: "rejected", Kind: "rejected"},
	}
	incomplete := titleTrunc || overviewTrunc || planTrunc || len(params.Plan) > maxCursorInspectBytes
	detail := plan
	if overview != "" {
		detail = overview + "\n" + plan
	}
	display := title + "\x00" + detail
	digest := cursorDecisionDigest(generation, message.Method, sessionID, params.ToolCallID, "", []byte(params.Plan), optionIDs, display)
	public := PendingDecision{
		RequestID: cursorDecisionRequestID(generation, message.ID, message.Method), Generation: generation,
		Method: message.Method, Kind: DecisionPlan, Digest: digest, OptionIDs: optionIDs,
	}
	return newHeldDecision(message, public, optionKind, "", DecisionInspect{
		RequestID: public.RequestID, Generation: generation, Method: message.Method, Kind: DecisionPlan,
		Digest: digest, OptionIDs: optionIDs, Options: options, Title: title, Detail: detail, Incomplete: incomplete, Untrusted: true,
	}), true
}

func newHeldDecision(message cursorRPCMessage, public PendingDecision, optionKind map[string]string, questionID string, inspect DecisionInspect) *cursorHeldDecision {
	inspect.ExpiresAt = public.ExpiresAt
	return &cursorHeldDecision{public: public, rpcID: append(json.RawMessage(nil), message.ID...), optionKind: optionKind, questionID: questionID, inspect: inspect}
}

func cursorDecisionRequestID(generation string, rpcID json.RawMessage, method string) string {
	sum := sha256.Sum256([]byte(generation + "\x00" + string(rpcID) + "\x00" + method))
	return hex.EncodeToString(sum[:16])
}

func cursorDecisionDigest(generation, method, sessionID, toolCallID, toolKind string, raw json.RawMessage, optionIDs []string, display string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		generation, method, sessionID, toolCallID, toolKind, hex.EncodeToString(rawSHA(raw)), strings.Join(optionIDs, ","), display,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func rawSHA(raw json.RawMessage) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}

func cursorOptionBinding(options []DecisionOption) string {
	parts := make([]string, 0, len(options))
	for _, option := range options {
		parts = append(parts, option.ID+"="+option.Label)
	}
	return strings.Join(parts, ",")
}

func inspectableRawInput(raw json.RawMessage) (string, bool, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true, false
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return "", false, false
	}
	compact, err := json.Marshal(decoded)
	if err != nil {
		return "", false, false
	}
	return inspectableText(string(compact), maxCursorInspectBytes)
}

func inspectableCursorContent(raw json.RawMessage) (string, bool, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", true, false
	}
	if bytes.Equal(raw, []byte("null")) {
		return "", false, false
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false, false
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return "", false, false
	}
	display, displayOK, displayTrunc := inspectableText(compact.String(), maxCursorInspectBytes)
	if !displayOK {
		return "", false, false
	}
	incomplete := displayTrunc || len(blocks) == 0 || len(blocks) > maxCursorContentBlocks
	for _, block := range blocks {
		supported, valid := supportedCursorContentBlock(block)
		if !valid {
			return "", false, false
		}
		if !supported {
			incomplete = true
		}
	}
	return display, true, incomplete
}

func supportedCursorContentBlock(raw json.RawMessage) (bool, bool) {
	var block map[string]json.RawMessage
	if json.Unmarshal(raw, &block) != nil || block == nil {
		return false, false
	}
	var kind string
	if json.Unmarshal(block["type"], &kind) != nil || kind == "" {
		return false, false
	}
	switch kind {
	case "content":
		var content map[string]json.RawMessage
		if json.Unmarshal(block["content"], &content) != nil || content == nil {
			return false, false
		}
		var contentType, text string
		if json.Unmarshal(content["type"], &contentType) != nil || contentType == "" ||
			json.Unmarshal(content["text"], &text) != nil {
			return false, false
		}
		if _, ok, _ := inspectableText(text, 0); !ok {
			return false, false
		}
		return contentType == "text" && onlyJSONFields(block, "type", "content") && onlyJSONFields(content, "type", "text"), true
	case "diff":
		var path, newText string
		if json.Unmarshal(block["path"], &path) != nil || path == "" ||
			json.Unmarshal(block["newText"], &newText) != nil {
			return false, false
		}
		oldRaw, hasOld := block["oldText"]
		if !hasOld {
			return false, false
		}
		if !bytes.Equal(bytes.TrimSpace(oldRaw), []byte("null")) {
			var oldText string
			if json.Unmarshal(oldRaw, &oldText) != nil {
				return false, false
			}
			if _, ok, _ := inspectableText(oldText, 0); !ok {
				return false, false
			}
		}
		for _, value := range []string{path, newText} {
			if _, ok, _ := inspectableText(value, 0); !ok {
				return false, false
			}
		}
		return onlyJSONFields(block, "type", "path", "oldText", "newText"), true
	default:
		return false, true
	}
}

func onlyJSONFields(value map[string]json.RawMessage, allowed ...string) bool {
	if len(value) != len(allowed) {
		return false
	}
	for _, key := range allowed {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}

func cursorPermissionShape(raw json.RawMessage) *DecisionRequestShape {
	shape := &DecisionRequestShape{
		ParamsType: "absent", SessionIDType: "absent", ToolCallType: "absent", ToolCallIDType: "absent",
		KindType: "absent", TitleType: "absent", StatusType: "absent", RawInputType: "absent",
		ContentType: "absent", OptionsType: "absent",
	}
	shape.ParamsType = jsonStructuralType(raw)
	var params map[string]json.RawMessage
	if json.Unmarshal(raw, &params) != nil || params == nil {
		return shape
	}
	shape.SessionIDType = jsonStructuralType(params["sessionId"])
	shape.ToolCallType = jsonStructuralType(params["toolCall"])
	shape.OptionsType = jsonStructuralType(params["options"])
	shape.OptionCount = boundedJSONArrayCount(params["options"], 8)
	var toolCall map[string]json.RawMessage
	if json.Unmarshal(params["toolCall"], &toolCall) != nil || toolCall == nil {
		return shape
	}
	shape.ToolCallIDType = jsonStructuralType(toolCall["toolCallId"])
	shape.KindType = jsonStructuralType(toolCall["kind"])
	shape.TitleType = jsonStructuralType(toolCall["title"])
	shape.StatusType = jsonStructuralType(toolCall["status"])
	shape.RawInputType = jsonStructuralType(toolCall["rawInput"])
	shape.ContentType = jsonStructuralType(toolCall["content"])
	shape.ContentBlocks = boundedJSONArrayCount(toolCall["content"], maxCursorContentBlocks)
	return shape
}

func jsonStructuralType(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "absent"
	}
	switch raw[0] {
	case 'n':
		return "null"
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	default:
		return "number"
	}
}

func boundedJSONArrayCount(raw json.RawMessage, max int) int {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return 0
	}
	if len(values) > max {
		return max + 1
	}
	return len(values)
}

func inspectableText(value string, max int) (string, bool, bool) {
	if !utf8.ValidString(value) {
		return "", false, false
	}
	for _, r := range value {
		if r == 0 || r == 0x7f || (r < 0x20 && r != '\n' && r != '\t') {
			return "", false, false
		}
	}
	if max > 0 && len(value) > max {
		out := value[:max]
		for !utf8.ValidString(out) && len(out) > 0 {
			out = out[:len(out)-1]
		}
		return out, true, true
	}
	return value, true, false
}

func closedToolKind(kind string) string {
	kind = strings.TrimSpace(kind)
	switch kind {
	case "read", "edit", "delete", "move", "search", "execute", "think", "fetch", "other":
		return kind
	default:
		return "other"
	}
}

func closedPeerMethod(method string) string {
	method = strings.TrimSpace(method)
	if method == "" || len(method) > 64 || !utf8.ValidString(method) || strings.ContainsAny(method, "\x00\r\n") {
		return "invalid"
	}
	return method
}
