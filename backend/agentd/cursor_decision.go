// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
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
	return append([]DecisionRefusal(nil), p.refusals...)
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
	parsed, ok := parseCursorDecisionRequest(message, p.sessionID, p.generation)
	if !ok {
		p.failClosedPeer(message.ID, -32602, "invalid params")
		p.noteRefusal(closedPeerMethod(message.Method), "invalid")
		p.recordEvidence(cursorEvidenceRecord{Generation: p.generation, Method: closedPeerMethod(message.Method), Outcome: "refused"})
		p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
		return
	}
	p.stateMu.Lock()
	owned := !p.terminalFailure && (p.sessionID == "" || parsed.public.Generation == p.generation)
	overflow := len(p.held) >= maxCursorPendingDecisions
	ttl := p.decisionTTL
	p.stateMu.Unlock()
	if ttl <= 0 {
		ttl = cursorDecisionTTL
	}
	parsed.public.ExpiresAt = time.Now().UTC().Add(ttl)
	if !owned {
		p.failClosedPeer(message.ID, -32602, "invalid params")
		p.noteRefusal(parsed.public.Method, "unowned")
		return
	}
	if overflow {
		p.cancelRPC(message.ID)
		p.noteRefusal(parsed.public.Method, "overflow")
		p.recordEvidence(cursorEvidenceRecord{Generation: p.generation, RequestID: parsed.public.RequestID, Method: parsed.public.Method, Digest: parsed.public.Digest, Outcome: "refused"})
		p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
		return
	}
	requestID := parsed.public.RequestID
	parsed.timer = time.AfterFunc(ttl, func() { p.expireHeld(requestID) })
	p.stateMu.Lock()
	p.held[requestID] = parsed
	p.stateMu.Unlock()
	p.recordEvidence(cursorEvidenceRecord{
		Generation: p.generation, RequestID: requestID, Method: parsed.public.Method, Digest: parsed.public.Digest,
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
	method = closedPeerMethod(method)
	if method == "" {
		method = "invalid"
	}
	if reason == "" || len(reason) > 32 {
		reason = "refused"
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.refusals = append(p.refusals, DecisionRefusal{Method: method, Reason: reason})
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

func parseCursorDecisionRequest(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, bool) {
	switch message.Method {
	case "session/request_permission":
		return parseCursorPermission(message, sessionID, generation)
	case "cursor/ask_question":
		return parseCursorQuestion(message, sessionID, generation)
	case "cursor/create_plan":
		return parseCursorPlan(message, sessionID, generation)
	default:
		return nil, false
	}
}

func parseCursorPermission(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, bool) {
	var params struct {
		SessionID string `json:"sessionId"`
		ToolCall  *struct {
			ToolCallID string          `json:"toolCallId"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.SessionID == "" || (sessionID != "" && params.SessionID != sessionID) || params.ToolCall == nil {
		return nil, false
	}
	if !validOpaqueID(params.ToolCall.ToolCallID) || len(params.Options) == 0 || len(params.Options) > 8 {
		return nil, false
	}
	toolKind := closedToolKind(params.ToolCall.Kind)
	optionIDs := make([]string, 0, len(params.Options))
	optionKind := map[string]string{}
	for _, option := range params.Options {
		if !validOpaqueID(option.OptionID) || len(option.OptionID) > 64 || optionKind[option.OptionID] != "" {
			return nil, false
		}
		kind := strings.TrimSpace(option.Kind)
		switch kind {
		case "allow_once", "allow_always", "reject_once", "reject_always":
		default:
			return nil, false
		}
		optionIDs = append(optionIDs, option.OptionID)
		optionKind[option.OptionID] = kind
	}
	digest := cursorDecisionDigest(generation, message.Method, params.SessionID, params.ToolCall.ToolCallID, toolKind, params.ToolCall.RawInput, optionIDs)
	return newHeldDecision(message, PendingDecision{
		RequestID: cursorDecisionRequestID(generation, message.ID, message.Method), Generation: generation,
		Method: message.Method, Kind: DecisionPermission, ToolKind: toolKind, Digest: digest, OptionIDs: optionIDs,
	}, optionKind, ""), true
}

func parseCursorQuestion(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, bool) {
	var params struct {
		SessionID  string `json:"sessionId"`
		ToolCallID string `json:"toolCallId"`
		Questions  []struct {
			ID      string `json:"id"`
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
	if params.SessionID != "" && sessionID != "" && params.SessionID != sessionID {
		return nil, false
	}
	if !validOpaqueID(params.ToolCallID) || len(params.Questions) != 1 || params.Questions[0].AllowMultiple {
		return nil, false
	}
	question := params.Questions[0]
	if !validOpaqueID(question.ID) || len(question.Options) == 0 || len(question.Options) > 8 {
		return nil, false
	}
	optionIDs := make([]string, 0, len(question.Options))
	optionKind := map[string]string{}
	for _, option := range question.Options {
		if !validOpaqueID(option.ID) || optionKind[option.ID] != "" {
			return nil, false
		}
		optionIDs = append(optionIDs, option.ID)
		optionKind[option.ID] = "choice"
	}
	raw, _ := json.Marshal(params.Questions)
	digest := cursorDecisionDigest(generation, message.Method, sessionID, params.ToolCallID, "", raw, optionIDs)
	return newHeldDecision(message, PendingDecision{
		RequestID: cursorDecisionRequestID(generation, message.ID, message.Method), Generation: generation,
		Method: message.Method, Kind: DecisionQuestion, Digest: digest, OptionIDs: optionIDs,
	}, optionKind, question.ID), true
}

func parseCursorPlan(message cursorRPCMessage, sessionID, generation string) (*cursorHeldDecision, bool) {
	var params struct {
		SessionID  string `json:"sessionId"`
		ToolCallID string `json:"toolCallId"`
		Plan       string `json:"plan"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return nil, false
	}
	if params.SessionID != "" && sessionID != "" && params.SessionID != sessionID {
		return nil, false
	}
	if !validOpaqueID(params.ToolCallID) || strings.TrimSpace(params.Plan) == "" || len(params.Plan) > maxTextBytes {
		return nil, false
	}
	optionIDs := []string{"accepted", "rejected"}
	optionKind := map[string]string{"accepted": "accepted", "rejected": "rejected"}
	digest := cursorDecisionDigest(generation, message.Method, sessionID, params.ToolCallID, "", []byte(params.Plan), optionIDs)
	return newHeldDecision(message, PendingDecision{
		RequestID: cursorDecisionRequestID(generation, message.ID, message.Method), Generation: generation,
		Method: message.Method, Kind: DecisionPlan, Digest: digest, OptionIDs: optionIDs,
	}, optionKind, ""), true
}

func newHeldDecision(message cursorRPCMessage, public PendingDecision, optionKind map[string]string, questionID string) *cursorHeldDecision {
	return &cursorHeldDecision{public: public, rpcID: append(json.RawMessage(nil), message.ID...), optionKind: optionKind, questionID: questionID}
}

func cursorDecisionRequestID(generation string, rpcID json.RawMessage, method string) string {
	sum := sha256.Sum256([]byte(generation + "\x00" + string(rpcID) + "\x00" + method))
	return hex.EncodeToString(sum[:16])
}

func cursorDecisionDigest(generation, method, sessionID, toolCallID, toolKind string, raw json.RawMessage, optionIDs []string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		generation, method, sessionID, toolCallID, toolKind, hex.EncodeToString(rawSHA(raw)), strings.Join(optionIDs, ","),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func rawSHA(raw json.RawMessage) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}

func closedToolKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "read", "edit", "delete", "move", "search", "execute", "think", "fetch", "other":
		return kind
	case "":
		return "other"
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
