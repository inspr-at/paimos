// SPDX-License-Identifier: AGPL-3.0-only

package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"time"

	n "github.com/inspr-at/paimos/internal/nativecontract"
)

// observe runs under the native I/O lock, after RPC replies have bound the
// current thread/turn. No native JSON, reasoning or tool body is exported.
func (s *transport) observe(ctx context.Context, raw json.RawMessage) error {
	var f nativeFrame
	if err := decode(raw, &f); err != nil {
		return err
	}
	if f.Method == "account/updated" {
		s.final = nil
		clear(s.approvals)
		return ErrIdentity
	}
	p := f.Params
	if f.Method == "account/rateLimits/updated" {
		if len(f.ID) != 0 {
			return ErrProtocol
		}
		if err := s.authorize(ctx); err != nil {
			return err
		}
		for _, bucket := range []struct {
			name   string
			window *rateWindow
		}{{"primary", p.RateLimits.Primary}, {"secondary", p.RateLimits.Secondary}} {
			if bucket.window == nil {
				continue
			}
			w := bucket.window
			if w.Used == nil || w.Minutes == nil || w.Resets == nil || math.IsNaN(*w.Used) || math.IsInf(*w.Used, 0) ||
				*w.Used < 0 || *w.Used > 100 || *w.Minutes < 1 || *w.Minutes > 525600 || *w.Resets < 1 {
				return ErrProtocol
			}
			if !validRef(p.RateLimits.LimitID) {
				return ErrProtocol
			}
			now := s.clock.Now()
			reset := time.Unix(*w.Resets, 0).UTC()
			state := "known"
			if *w.Used == 100 {
				state = "limited"
			}
			q := n.Quota{AccountID: s.spec.Profile.AccountID, ProfileBindingRevision: s.spec.Profile.ProfileBindingRevision,
				Source: "codex_stream", Scope: p.RateLimits.LimitID + "/" + bucket.name, State: state, UsedPercent: w.Used,
				ObservedAt: now, FreshUntil: now.Add(time.Minute), ResetsAt: &reset}
			if len(q.Scope) > 128 {
				return ErrProtocol
			}
			if err := s.emit(n.ProviderEvent{Quota: &q}); err != nil {
				return err
			}
		}
		return nil
	}
	// Unknown methods are deliberately omitted. Unknown server requests cannot
	// obtain a tool/permission/OAuth response from this codec.
	switch f.Method {
	case "turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta",
		"thread/tokenUsage/updated", "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
	default:
		if len(f.ID) != 0 {
			return ErrQualification
		}
		return nil
	}
	if p.ThreadID != s.identity.NativeThreadRef || p.ThreadID == "" {
		return ErrBinding
	}
	turn := p.TurnID
	if f.Method == "turn/started" || f.Method == "turn/completed" {
		turn = p.Turn.ID
	}
	if !s.active || turn != s.turn || !validRef(turn) || (p.TurnID != "" && p.Turn.ID != "" && p.TurnID != p.Turn.ID) {
		return ErrTurn
	}
	if len(f.ID) != 0 && f.Method != "item/commandExecution/requestApproval" && f.Method != "item/fileChange/requestApproval" {
		return ErrProtocol
	}
	switch f.Method {
	case "turn/started":
		if p.Turn.Status != "inProgress" || s.started {
			return ErrProtocol
		}
		s.started = true
		e := s.evidence("turn_started", s.input, s.turn)
		return s.emit(n.ProviderEvent{Receipt: &n.Receipt{RequestID: s.input.RequestID, OperationID: s.input.RequestID,
			Binding: &s.spec.Binding, Operation: "input", State: e.Stage, InputID: &e.InputID, TurnRef: &e.TurnRef,
			SourceSequence: strconv.FormatUint(s.seq+1, 10), UpdatedAt: s.clock.Now()}})
	case "item/agentMessage/delta":
		if !validRef(p.ItemID) || !validText(p.Delta) {
			return ErrProtocol
		}
		return s.structured("assistant_delta", p.Delta, opaque(s.spec.Binding, "item", p.ItemID), "")
	case "item/started", "item/completed":
		if !validRef(p.Item.ID) {
			return ErrProtocol
		}
		if p.Item.Type == "agentMessage" {
			if f.Method == "item/completed" && (p.Item.Phase == "final_answer" || p.Item.Phase == "") {
				if !validText(p.Item.Text) || s.final != nil {
					return ErrProtocol
				}
				item := p.Item
				s.final = &item
			}
			return nil // commentary is never a final; no token concatenation
		}
		switch p.Item.Type {
		case "commandExecution", "fileChange", "mcpToolCall":
		default:
			return nil
		}
		state := "running"
		if f.Method == "item/completed" {
			if p.Item.Status != "completed" && p.Item.Status != "failed" && p.Item.Status != "declined" {
				return ErrProtocol
			}
			state = p.Item.Status
		}
		return s.structured("tool_status", "", opaque(s.spec.Binding, "item", p.Item.ID), state)
	case "turn/completed":
		if p.Turn.Status != "completed" && p.Turn.Status != "failed" && p.Turn.Status != "interrupted" {
			return ErrProtocol
		}
		if p.Turn.Status == "completed" && len(p.Turn.Error) > 0 && string(p.Turn.Error) != "null" {
			return ErrProtocol
		}
		if p.Turn.Status == "completed" && s.final != nil {
			if err := s.publishFinal(ctx); err != nil {
				s.final = nil
				return err
			}
		}
		s.final = nil
		s.active = false
		clear(s.approvals)
		return s.structured("turn_state", "", s.turnRef(), p.Turn.Status)
	case "thread/tokenUsage/updated":
		t := p.TokenUsage.Total
		if t.Total == nil || t.Input == nil || t.Output == nil || t.Cached == nil || t.Reasoning == nil ||
			*t.Total < s.tokens || *t.Total > 100000000 || *t.Input < 0 || *t.Output < 0 || *t.Cached < 0 ||
			*t.Cached > *t.Input || *t.Reasoning < 0 || *t.Reasoning > *t.Output || *t.Total != *t.Input+*t.Output {
			return ErrProtocol
		}
		s.tokens = *t.Total
		// C0 has no token-usage DTO. Export bounded counters as the existing
		// allowlisted Codex Expert envelope; never export raw reasoning/body.
		params := map[string]any{"threadId": opaque(s.spec.Binding, "thread", p.ThreadID), "turnId": s.turnRef(),
			"tokenUsage": map[string]any{"total": map[string]int64{"totalTokens": *t.Total, "inputTokens": *t.Input,
				"outputTokens": *t.Output, "cachedInputTokens": *t.Cached, "reasoningOutputTokens": *t.Reasoning}}}
		body := encode(map[string]any{"method": f.Method, "params": params})
		return s.emit(n.ProviderEvent{Expert: &n.ExpertFrame{Provider: "codex", EventName: f.Method,
			NativeThreadRef: ptr(opaque(s.spec.Binding, "thread", p.ThreadID)), NativeTurnRef: ptr(s.turnRef()),
			FrameJSON: string(body), RedactedPaths: []string{"params.threadId", "params.turnId"}, OriginalBytes: int64(len(raw))}})
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if len(f.ID) == 0 || string(f.ID) == "null" || !validRef(p.ItemID) || len(s.approvals) >= 32 {
			return ErrProtocol
		}
		var id any
		if json.Unmarshal(f.ID, &id) != nil {
			return ErrProtocol
		}
		switch v := id.(type) {
		case string:
			if !validRef(v) {
				return ErrProtocol
			}
		case float64:
			if v < 0 || v > 9007199254740991 || math.Trunc(v) != v {
				return ErrProtocol
			}
		default:
			return ErrProtocol
		}
		// Hash the complete effect locally. No command/cwd/path/reason enters
		// public summaries; approval authority compares this exact effect.
		h := sha256.Sum256(raw)
		approvalID := opaqueUUID(s.spec.Binding, "approval", string(f.ID))
		if _, exists := s.approvals[approvalID]; exists {
			return ErrProtocol
		}
		kind, summary := "command", "Native command approval"
		if f.Method == "item/fileChange/requestApproval" {
			kind, summary = "file_change", "Native file change approval"
		}
		expires := s.clock.Now().Add(5 * time.Minute)
		if s.spec.Policy.ExpiresAt.Before(expires) {
			expires = s.spec.Policy.ExpiresAt
		}
		a := n.Approval{RequestID: opaqueUUID(s.spec.Binding, "approval-request", string(f.ID)), Binding: s.spec.Binding,
			TurnRef: s.turnRef(), NativeRequestRef: opaque(s.spec.Binding, "rpc", string(f.ID)), Kind: kind,
			EffectDigest: hex.EncodeToString(h[:]), Summary: summary, AllowedDecisions: []string{"allow_once", "deny"},
			ExpiresAt: expires, ApprovalID: approvalID, ExpiresInMS: expires.Sub(s.clock.Now()).Milliseconds(), StepUpRequired: true}
		if a.ExpiresInMS <= 0 {
			return ErrExpired
		}
		s.approvals[approvalID] = pendingApproval{a, append(json.RawMessage(nil), f.ID...)}
		return s.emit(n.ProviderEvent{Approval: &a})
	}
	return nil
}

func ptr(s string) *string { return &s }

func (s *transport) publishFinal(ctx context.Context) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	finalID := opaqueUUID(s.spec.Binding, "final", s.turn+"\x00"+s.final.ID)
	f, ok := s.grants.(FinalGrants)
	if !ok {
		return s.structured("final_reference", "", finalID, "publication_unavailable")
	}
	g, err := f.FinalDrain(ctx, s.spec.Binding)
	if err != nil || !sameBinding(g.Binding, s.spec.Binding) || !g.ExpiresAt.After(s.clock.Now()) ||
		g.GrantID == "" || g.ConversationRef != opaque(s.spec.Binding, "thread", s.identity.NativeThreadRef) ||
		g.MaximumFinals < 1 || g.MaximumBytes < int64(len(s.final.Text)) {
		return ErrExpired
	}
	// Revalidate after the network grant read under the same I/O lock.
	if err := s.authorize(ctx); err != nil {
		return err
	}
	if !g.ExpiresAt.After(s.clock.Now()) {
		return ErrExpired
	}
	if s.finalGrantID != g.GrantID {
		s.finalGrantID, s.finals = g.GrantID, 0
	}
	if s.finals >= g.MaximumFinals {
		return ErrExpired
	}
	s.finals++
	return s.emit(n.ProviderEvent{Final: &n.FinalPublish{RequestID: s.input.RequestID, ExpectedBinding: s.spec.Binding,
		FinalID: finalID, InputID: s.input.InputID, TurnRef: s.turnRef(), Text: s.final.Text, DrainGrantID: g.GrantID}})
}
