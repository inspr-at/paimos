// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxCodexConversationOutputBytes = 256 << 10
	maxCodexConversationEvents      = 512
	maxCodexConversationPrebind     = 256
)

var ErrCodexConversationCursor = errors.New("Codex conversation replay cursor is invalid")

// CodexConversationOptions is required by the explicit conversation entry
// point. Both limits bound retained private answer content; neither limit is
// inferred from ordinary coding-session configuration.
type CodexConversationOptions struct {
	// MaxOutputBytes counts logical UTF-8 assistant output after streamed/final
	// overlap is reconciled.
	MaxOutputBytes int
	// MaxEvents counts correlated deltas, completed items, and terminal
	// notifications, including assistant items embedded in a terminal turn.
	MaxEvents int
	// ScratchRoot, when set, is a private operator-owned directory in which a
	// fresh per-call directory is created. Empty uses the OS temporary root; the
	// created child is still owner-only, identity-pinned, and removed after reap.
	ScratchRoot string
	// OutputSchema is the server-selected structured result contract. A caller
	// cannot select it through ordinary Start, and only a JSON object is accepted.
	OutputSchema json.RawMessage
}

type ConversationOutcome string

const (
	ConversationCompleted ConversationOutcome = "completed"
	ConversationFailed    ConversationOutcome = "failed"
	ConversationCancelled ConversationOutcome = "cancelled"
)

type ConversationFailure string

const (
	ConversationFailureNone           ConversationFailure = ""
	ConversationFailureProtocol       ConversationFailure = "protocol_error"
	ConversationFailureOutputBound    ConversationFailure = "output_bound"
	ConversationFailureEventBound     ConversationFailure = "event_bound"
	ConversationFailureTransportEnded ConversationFailure = "transport_ended"
	ConversationFailureTurnFailed     ConversationFailure = "turn_failed"
	ConversationFailureCancelled      ConversationFailure = "cancelled"
	ConversationFailureDeadline       ConversationFailure = "deadline_exceeded"
)

// CodexConversationDelta is private answer content. Cursor starts at one and
// increases exactly once per retained output-text delta; ReplayConversation
// never evicts or renumbers an event.
type CodexConversationDelta struct {
	Cursor   uint64
	ThreadID string
	TurnID   string
	ItemID   string
	Text     string
}

// CodexConversationResult is closed exactly once. Text is populated only for
// a genuinely completed turn with a completed assistant item. Partial output
// is available through replay for diagnosis but is never promoted to Text.
type CodexConversationResult struct {
	Outcome        ConversationOutcome
	Failure        ConversationFailure
	TerminalStatus string
	ThreadID       string
	TurnID         string
	ItemID         string
	Text           string
	FinalCursor    uint64
}

// CodexConversationExecution is deliberately not part of Adapter or Process.
// Only a future authenticated, isolated owned-conversation binding may obtain
// it by explicitly calling CodexAdapter.StartConversation.
type CodexConversationExecution interface {
	Process
	ConversationIdentity() (string, string, error)
	ReplayConversation(after uint64) ([]CodexConversationDelta, error)
	WaitConversation(context.Context) (CodexConversationResult, error)
}

type codexConversationExecution struct {
	*codexProcess
	scratch         string
	scratchIdentity os.FileInfo
	cleanupOnce     sync.Once
}

func (p *codexConversationExecution) cleanupScratch() {
	p.cleanupOnce.Do(func() {
		canonical, identity, err := canonicalCodexConversationScratch(p.scratch, false)
		if err == nil && canonical == p.scratch && p.scratchIdentity != nil && os.SameFile(identity, p.scratchIdentity) {
			_ = os.RemoveAll(canonical) // #nosec G703 -- the sole constructor stores an MkdirTemp child and its identity; canonical owner/no-symlink and SameFile checks rebind it above.
		}
	})
}

func (p *codexConversationExecution) Wait() error {
	err := p.codexProcess.Wait()
	p.cleanupScratch()
	return err
}

func (p *codexConversationExecution) Stop(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	effect, err := p.codexProcess.Stop(ctx, request)
	if err == nil {
		p.cleanupScratch()
	}
	return effect, err
}

func (p *codexConversationExecution) ConversationIdentity() (string, string, error) {
	return p.codexProcess.target()
}

type codexConversationItem struct {
	streamed string
	final    string
	phase    string
	finalSet bool
}

type codexAssistantItem struct {
	id    string
	text  string
	phase string
}

type codexConversationNotification struct {
	kind      string
	threadID  string
	turnID    string
	itemID    string
	text      string
	status    string
	items     []codexAssistantItem
	malformed bool
}

type codexConversationCollector struct {
	mu               sync.Mutex
	maxOutputBytes   int
	maxEvents        int
	threadID         string
	turnID           string
	pending          []codexConversationNotification
	pendingBytes     int
	pendingOverflow  map[string]ConversationFailure
	pendingAmbiguous bool
	items            map[string]*codexConversationItem
	itemOrder        []string
	deltas           []CodexConversationDelta
	nextCursor       uint64
	usedBytes        int
	eventCount       int
	closed           bool
	result           CodexConversationResult
	done             chan struct{}
}

func newCodexConversationCollector(options CodexConversationOptions) (*codexConversationCollector, error) {
	if options.MaxOutputBytes <= 0 || options.MaxOutputBytes > maxCodexConversationOutputBytes {
		return nil, errors.New("Codex conversation output bound is invalid")
	}
	if options.MaxEvents <= 0 || options.MaxEvents > maxCodexConversationEvents {
		return nil, errors.New("Codex conversation event bound is invalid")
	}
	return &codexConversationCollector{
		maxOutputBytes:  options.MaxOutputBytes,
		maxEvents:       options.MaxEvents,
		items:           make(map[string]*codexConversationItem),
		pendingOverflow: make(map[string]ConversationFailure),
		done:            make(chan struct{}),
	}, nil
}

func (c *codexConversationCollector) bindThread(threadID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.threadID != "" || !validOpaqueID(threadID) {
		return
	}
	c.threadID = threadID
}

func (c *codexConversationCollector) bindTurn(turnID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.turnID != "" || !validOpaqueID(turnID) {
		return
	}
	c.turnID = turnID
	pending := c.pending
	overflow := c.pendingOverflow[turnID]
	ambiguous := c.pendingAmbiguous
	c.pending = nil
	c.pendingBytes = 0
	c.pendingOverflow = nil
	if ambiguous {
		c.finishLocked(ConversationFailed, ConversationFailureProtocol, "", "", "")
		return
	}
	if overflow != ConversationFailureNone {
		c.finishLocked(ConversationFailed, overflow, "", "", "")
		return
	}
	for _, notification := range pending {
		if c.closed {
			return
		}
		if notification.threadID == c.threadID && notification.turnID == c.turnID {
			c.applyLocked(notification)
		}
	}
}

func (c *codexConversationCollector) handleNotification(message codexRPCMessage) {
	notification, ok := decodeCodexConversationNotification(message)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.threadID == "" || notification.threadID != c.threadID {
		return
	}
	if c.turnID == "" {
		// The notification stream may race ahead of the turn/start response.
		// This fixed staging window never contributes foreign text to a bound
		// turn. Any dropped candidate is recorded by turn ID so the selected
		// turn fails explicitly rather than yielding a truncated partial.
		if len(c.pending) >= c.maxEvents || len(c.pending) >= maxCodexConversationPrebind {
			c.recordPendingOverflowLocked(notification.turnID, ConversationFailureEventBound)
			return
		}
		retained := notification.retainedBytes()
		if retained > c.maxOutputBytes {
			c.recordPendingOverflowLocked(notification.turnID, ConversationFailureOutputBound)
			return
		}
		// Deltas and their completed item may temporarily duplicate the same
		// answer before reconciliation, so staging is capped at twice the
		// logical output bound. It remains fixed and private either way.
		if c.pendingBytes+retained > 2*c.maxOutputBytes {
			c.recordPendingOverflowLocked(notification.turnID, ConversationFailureOutputBound)
			return
		}
		c.pending = append(c.pending, notification)
		c.pendingBytes += retained
		return
	}
	if notification.turnID != c.turnID {
		return
	}
	c.applyLocked(notification)
}

func (c *codexConversationCollector) recordPendingOverflowLocked(turnID string, failure ConversationFailure) {
	if _, exists := c.pendingOverflow[turnID]; exists {
		return
	}
	if len(c.pendingOverflow) >= maxCodexConversationPrebind {
		c.pendingAmbiguous = true
		return
	}
	c.pendingOverflow[turnID] = failure
}

func (n codexConversationNotification) retainedBytes() int {
	total := len(n.text)
	for _, item := range n.items {
		total += len(item.text)
	}
	return total
}

func decodeCodexConversationNotification(message codexRPCMessage) (codexConversationNotification, bool) {
	if !utf8.Valid(message.Params) {
		return codexConversationNotification{}, false
	}
	switch message.Method {
	case "item/agentMessage/delta":
		var params struct {
			ThreadID string  `json:"threadId"`
			TurnID   string  `json:"turnId"`
			ItemID   string  `json:"itemId"`
			Delta    *string `json:"delta"`
		}
		if json.Unmarshal(message.Params, &params) != nil || !validOpaqueID(params.ThreadID) || !validOpaqueID(params.TurnID) || !validOpaqueID(params.ItemID) || params.Delta == nil || !utf8.ValidString(*params.Delta) {
			return codexConversationNotification{}, false
		}
		return codexConversationNotification{kind: "delta", threadID: params.ThreadID, turnID: params.TurnID, itemID: params.ItemID, text: *params.Delta}, true
	case "item/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Item     struct {
				ID    string  `json:"id"`
				Type  string  `json:"type"`
				Text  *string `json:"text"`
				Phase *string `json:"phase"`
			} `json:"item"`
		}
		if json.Unmarshal(message.Params, &params) != nil || !validOpaqueID(params.ThreadID) || !validOpaqueID(params.TurnID) {
			return codexConversationNotification{}, false
		}
		if params.Item.Type != "agentMessage" {
			return codexConversationNotification{}, false
		}
		if !validOpaqueID(params.Item.ID) || params.Item.Text == nil || !validConversationPhase(params.Item.Phase) || !utf8.ValidString(*params.Item.Text) {
			return codexConversationNotification{kind: "item", threadID: params.ThreadID, turnID: params.TurnID, malformed: true}, true
		}
		phase := ""
		if params.Item.Phase != nil {
			phase = *params.Item.Phase
		}
		return codexConversationNotification{kind: "item", threadID: params.ThreadID, turnID: params.TurnID, itemID: params.Item.ID, text: *params.Item.Text, status: phase}, true
	case "turn/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string            `json:"id"`
				Status string            `json:"status"`
				Items  []json.RawMessage `json:"items"`
			} `json:"turn"`
		}
		if json.Unmarshal(message.Params, &params) != nil || !validOpaqueID(params.ThreadID) || !validOpaqueID(params.Turn.ID) {
			return codexConversationNotification{}, false
		}
		notification := codexConversationNotification{kind: "terminal", threadID: params.ThreadID, turnID: params.Turn.ID, status: params.Turn.Status}
		for _, raw := range params.Turn.Items {
			var item struct {
				ID    string  `json:"id"`
				Type  string  `json:"type"`
				Text  *string `json:"text"`
				Phase *string `json:"phase"`
			}
			if !utf8.Valid(raw) || json.Unmarshal(raw, &item) != nil {
				notification.malformed = true
				continue
			}
			if item.Type != "agentMessage" {
				continue
			}
			if !validOpaqueID(item.ID) || item.Text == nil || !validConversationPhase(item.Phase) || !utf8.ValidString(*item.Text) {
				notification.malformed = true
				continue
			}
			phase := ""
			if item.Phase != nil {
				phase = *item.Phase
			}
			notification.items = append(notification.items, codexAssistantItem{id: item.ID, text: *item.Text, phase: phase})
		}
		return notification, true
	default:
		return codexConversationNotification{}, false
	}
}

func validConversationPhase(phase *string) bool {
	return phase == nil || *phase == "commentary" || *phase == "final_answer"
}

func (c *codexConversationCollector) applyLocked(notification codexConversationNotification) {
	if notification.malformed {
		if notification.kind == "terminal" {
			c.finishLocked(ConversationFailed, ConversationFailureProtocol, "", "", "")
		}
		return
	}
	if c.eventCount >= c.maxEvents {
		c.finishLocked(ConversationFailed, ConversationFailureEventBound, "", "", "")
		return
	}
	c.eventCount++
	switch notification.kind {
	case "delta":
		c.applyDeltaLocked(notification)
	case "item":
		c.applyFinalItemLocked(codexAssistantItem{id: notification.itemID, text: notification.text, phase: notification.status})
	case "terminal":
		c.applyTerminalLocked(notification)
	}
}

func (c *codexConversationCollector) applyDeltaLocked(notification codexConversationNotification) {
	item := c.itemLocked(notification.itemID)
	streamed := item.streamed + notification.text
	if item.finalSet && !strings.HasPrefix(item.final, streamed) {
		c.finishLocked(ConversationFailed, ConversationFailureProtocol, "", "", "")
		return
	}
	newSize := len(streamed)
	if item.finalSet {
		newSize = len(item.final)
	}
	oldSize := len(item.streamed)
	if item.finalSet {
		oldSize = len(item.final)
	}
	if c.usedBytes-oldSize+newSize > c.maxOutputBytes {
		c.finishLocked(ConversationFailed, ConversationFailureOutputBound, "", "", "")
		return
	}
	item.streamed = streamed
	c.usedBytes = c.usedBytes - oldSize + newSize
	c.nextCursor++
	c.deltas = append(c.deltas, CodexConversationDelta{
		Cursor: c.nextCursor, ThreadID: c.threadID, TurnID: c.turnID,
		ItemID: notification.itemID, Text: notification.text,
	})
}

func (c *codexConversationCollector) applyFinalItemLocked(final codexAssistantItem) {
	item := c.itemLocked(final.id)
	if item.finalSet {
		if item.final != final.text || (item.phase != "" && final.phase != "" && item.phase != final.phase) {
			c.finishLocked(ConversationFailed, ConversationFailureProtocol, "", "", "")
			return
		}
		if item.phase == "" {
			item.phase = final.phase
		}
		return
	}
	if !strings.HasPrefix(final.text, item.streamed) {
		c.finishLocked(ConversationFailed, ConversationFailureProtocol, "", "", "")
		return
	}
	if c.usedBytes-len(item.streamed)+len(final.text) > c.maxOutputBytes {
		c.finishLocked(ConversationFailed, ConversationFailureOutputBound, "", "", "")
		return
	}
	c.usedBytes = c.usedBytes - len(item.streamed) + len(final.text)
	item.final, item.phase, item.finalSet = final.text, final.phase, true
	c.itemOrder = append(c.itemOrder, final.id)
}

func (c *codexConversationCollector) itemLocked(itemID string) *codexConversationItem {
	item := c.items[itemID]
	if item == nil {
		item = &codexConversationItem{}
		c.items[itemID] = item
	}
	return item
}

func (c *codexConversationCollector) applyTerminalLocked(notification codexConversationNotification) {
	switch notification.status {
	case "interrupted":
		c.finishLocked(ConversationCancelled, ConversationFailureCancelled, "interrupted", "", "")
		return
	case "failed":
		c.finishLocked(ConversationFailed, ConversationFailureTurnFailed, "failed", "", "")
		return
	case "completed":
		for _, final := range notification.items {
			if c.eventCount >= c.maxEvents {
				c.finishLocked(ConversationFailed, ConversationFailureEventBound, "", "", "")
				return
			}
			c.eventCount++
			c.applyFinalItemLocked(final)
			if c.closed {
				return
			}
		}
	case "inProgress":
		c.finishLocked(ConversationFailed, ConversationFailureProtocol, "inProgress", "", "")
		return
	default:
		c.finishLocked(ConversationFailed, ConversationFailureProtocol, "", "", "")
		return
	}
	itemID := c.selectFinalItemLocked()
	if itemID == "" {
		c.finishLocked(ConversationFailed, ConversationFailureProtocol, "completed", "", "")
		return
	}
	item := c.items[itemID]
	c.finishLocked(ConversationCompleted, ConversationFailureNone, "completed", itemID, item.final)
}

func (c *codexConversationCollector) selectFinalItemLocked() string {
	fallback := ""
	for _, itemID := range c.itemOrder {
		item := c.items[itemID]
		if item == nil || !item.finalSet || item.phase == "commentary" {
			continue
		}
		if item.phase == "final_answer" {
			fallback = itemID
			continue
		}
		if fallback == "" || c.items[fallback].phase != "final_answer" {
			fallback = itemID
		}
	}
	return fallback
}

func (c *codexConversationCollector) finishLocked(outcome ConversationOutcome, failure ConversationFailure, terminalStatus, itemID, answer string) bool {
	if c.closed {
		return false
	}
	c.closed = true
	c.result = CodexConversationResult{
		Outcome: outcome, Failure: failure, TerminalStatus: terminalStatus,
		ThreadID: c.threadID, TurnID: c.turnID, ItemID: itemID,
		Text: answer, FinalCursor: c.nextCursor,
	}
	close(c.done)
	return true
}

func (c *codexConversationCollector) protocolFailed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finishLocked(ConversationFailed, ConversationFailureProtocol, "", "", "")
}

func (c *codexConversationCollector) transportEnded() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finishLocked(ConversationFailed, ConversationFailureTransportEnded, "", "", "")
}

func (c *codexConversationCollector) cancel(reason ConversationFailure) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.finishLocked(ConversationCancelled, reason, "", "", "")
}

func (c *codexConversationCollector) wait(ctx context.Context) (CodexConversationResult, bool) {
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.result, false
	default:
	}
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.result, false
	case <-ctx.Done():
		reason := ConversationFailureCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = ConversationFailureDeadline
		}
		won := c.cancel(reason)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.result, won
	}
}

func (c *codexConversationCollector) replay(after uint64) ([]CodexConversationDelta, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if after > maxCodexConversationEvents || after > c.nextCursor {
		return nil, ErrCodexConversationCursor
	}
	tail := c.deltas[after:]
	out := make([]CodexConversationDelta, len(tail))
	copy(out, tail)
	return out, nil
}

func (p *codexConversationExecution) ReplayConversation(after uint64) ([]CodexConversationDelta, error) {
	if p.conversation == nil {
		return nil, ErrCapabilityMissing
	}
	return p.conversation.replay(after)
}

func (p *codexConversationExecution) WaitConversation(ctx context.Context) (CodexConversationResult, error) {
	if p.conversation == nil {
		return CodexConversationResult{}, ErrCapabilityMissing
	}
	result, cancelled := p.conversation.wait(ctx)
	if cancelled || result.Outcome != ConversationCompleted {
		p.abortStream()
	}
	// StartConversation rejects persistent sessions, so this also drains and
	// reaps the exact app-server child before the private result is returned.
	waitErr := p.Wait()
	if result.Outcome == ConversationCompleted && waitErr != nil {
		return result, waitErr
	}
	return result, nil
}
