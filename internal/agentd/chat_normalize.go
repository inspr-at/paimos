// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

const chatChunkBytes = 64 << 10

// ChatUpdate follows ACP's update vocabulary, with a state projection for AEON.
// It contains no tool input, tool result, reasoning, or vendor payload.
type ChatUpdate struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       *ChatContent `json:"content,omitempty"`
	ToolCallID    string       `json:"toolCallId,omitempty"`
	Title         string       `json:"title,omitempty"`
	Status        string       `json:"status,omitempty"`
	State         string       `json:"state,omitempty"`
}

type ChatContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func chatState(state string) ChatUpdate { return ChatUpdate{SessionUpdate: "state", State: state} }

func chatText(text string) ChatUpdate {
	return ChatUpdate{SessionUpdate: "agent_message_chunk", Content: &ChatContent{Type: "text", Text: text}}
}

func chatTool(id, title, status string) ChatUpdate {
	return ChatUpdate{SessionUpdate: "tool_call", ToolCallID: id, Title: title, Status: status}
}

func validChatID(s string, bound int) bool {
	return s != "" && len(s) <= bound && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

func (u ChatUpdate) valid() bool {
	switch u.SessionUpdate {
	case "agent_message_chunk":
		return u.Content != nil && u.Content.Type == "text" && len(u.Content.Text) > 0 && len(u.Content.Text) <= chatChunkBytes && utf8.ValidString(u.Content.Text) && !strings.ContainsRune(u.Content.Text, 0) && u.ToolCallID == "" && u.Title == "" && u.Status == "" && u.State == ""
	case "tool_call":
		return u.Content == nil && validChatID(u.ToolCallID, 256) && validChatID(u.Title, 128) && (u.Status == "in_progress" || u.Status == "completed" || u.Status == "failed") && u.State == ""
	case "state":
		return u.Content == nil && u.ToolCallID == "" && u.Title == "" && u.Status == "" && (u.State == "running" || u.State == "idle" || u.State == "requires_action")
	}
	return false
}

// observeChat is called only by the event reader of an owned native connection.
// False includes unknown shapes and out-of-scope frames; none are logged.
func (p *wireProcess) observeChat(harness string, raw json.RawMessage) {
	updates, known := normalizeChat(harness, raw, p.threadID, p.turnID)
	if !known {
		p.chatDropped.Add(1)
		return
	}
	for _, update := range updates {
		if !update.valid() {
			p.chatDropped.Add(1)
			continue
		}
		if p.observe != nil {
			p.observe(AdapterEvent{Chat: &update})
		}
	}
}

// Transport framing bounds raw before decoding. This function also serves the
// fixture boundary, so enforce the bound here before any JSON allocation.
func normalizeChat(harness string, raw json.RawMessage, thread, turn string) ([]ChatUpdate, bool) {
	if len(raw) > 8<<20 || !utf8.Valid(raw) {
		return nil, false
	}
	switch harness {
	case Claude:
		var f struct {
			Kind   string     `json:"kind"`
			Update ChatUpdate `json:"update"`
		}
		if json.Unmarshal(raw, &f) != nil {
			return nil, false
		}
		switch f.Kind {
		case "chat":
			return []ChatUpdate{f.Update}, true
		case "turn_started":
			return []ChatUpdate{chatState("running")}, true
		case "turn_completed":
			return []ChatUpdate{chatState("idle")}, true
		case "session_started", "settings_changed", "budget_exhausted", "capacity", "usage", "tool_started", "control_applied", "control_failed", "review_result", "native_message":
			return nil, true
		}
	case Codex:
		return normalizeCodexChat(raw, thread, turn)
	case Pi:
		return normalizePiChat(raw)
	}
	return nil, false
}

func normalizeCodexChat(raw json.RawMessage, thread, turn string) ([]ChatUpdate, bool) {
	var f struct {
		Method string `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
			Item struct {
				ID     string `json:"id"`
				Type   string `json:"type"`
				Tool   string `json:"tool"`
				Status string `json:"status"`
			} `json:"item"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return nil, false
	}
	switch f.Method {
	case "account/rateLimits/updated", "rateLimits/updated", "thread/settings/updated", "thread/tokenUsage/updated", "model/rerouted", "thread/started":
		return nil, true
	}
	if thread == "" || turn == "" || f.Params.ThreadID != thread || firstNonempty(f.Params.TurnID, f.Params.Turn.ID) != turn {
		return nil, false
	}
	switch f.Method {
	case "item/agentMessage/delta":
		return []ChatUpdate{chatText(f.Params.Delta)}, true
	case "turn/started":
		return []ChatUpdate{chatState("running")}, true
	case "turn/completed", "turn/failed":
		return []ChatUpdate{chatState("idle")}, true
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request":
		return []ChatUpdate{chatState("requires_action")}, true
	case "item/started", "item/completed":
		i := f.Params.Item
		title := ""
		switch i.Type {
		case "commandExecution":
			title = "Bash"
		case "fileChange":
			title = "Edit"
		case "mcpToolCall", "dynamicToolCall":
			title = i.Tool
		default:
			return nil, true // Text snapshots and reasoning are not deltas.
		}
		status := "in_progress"
		if f.Method == "item/completed" {
			switch i.Status {
			case "completed":
				status = "completed"
			case "failed", "declined":
				status = "failed"
			default:
				return nil, false
			}
		}
		return []ChatUpdate{chatTool(i.ID, title, status)}, true
	}
	return nil, false
}

func normalizePiChat(raw json.RawMessage) ([]ChatUpdate, bool) {
	var f struct {
		Type                  string          `json:"type"`
		ToolCallID            string          `json:"toolCallId"`
		ToolName              string          `json:"toolName"`
		IsError               bool            `json:"isError"`
		Method                string          `json:"method"`
		Message               json.RawMessage `json:"message"`
		AssistantMessageEvent struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return nil, false
	}
	switch f.Type {
	case "agent_start":
		return []ChatUpdate{chatState("running")}, true
	case "agent_end", "agent_settled":
		return []ChatUpdate{chatState("idle")}, true
	case "message_update":
		var message struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(f.Message, &message) != nil || message.Role != "assistant" {
			return nil, false
		}
		if f.AssistantMessageEvent.Type == "text_delta" {
			return []ChatUpdate{chatText(f.AssistantMessageEvent.Delta)}, true
		}
		return nil, true // Never expose reasoning or partial tool arguments.
	case "tool_execution_start":
		return []ChatUpdate{chatTool(f.ToolCallID, f.ToolName, "in_progress")}, true
	case "tool_execution_end":
		status := "completed"
		if f.IsError {
			status = "failed"
		}
		return []ChatUpdate{chatTool(f.ToolCallID, f.ToolName, status)}, true
	case "extension_ui_request":
		switch f.Method {
		case "select", "confirm", "input", "editor":
			return []ChatUpdate{chatState("requires_action")}, true
		}
		return nil, true
	case "response", "message_start", "message_end", "turn_start", "turn_end", "tool_execution_update", "queue_update", "auto_compaction_start", "auto_compaction_end", "auto_retry_start", "auto_retry_end":
		return nil, true
	}
	return nil, false
}

type pendingCodexChat struct {
	turn   string
	update ChatUpdate
}

// Fast app-server output can precede turn/start's reply. Hold only normalized
// projections, then release them only if the acknowledged owned turn agrees.
func (p *codexProcess) observeCodexChat(raw json.RawMessage) {
	if p.acknowledged {
		p.observeChat(Codex, raw)
		return
	}
	var f struct {
		Params struct {
			TurnID string `json:"turnId"`
			Turn   struct {
				ID string `json:"id"`
			} `json:"turn"`
		} `json:"params"`
	}
	if len(raw) > 8<<20 || json.Unmarshal(raw, &f) != nil {
		p.chatDropped.Add(1)
		return
	}
	turn := firstNonempty(f.Params.TurnID, f.Params.Turn.ID)
	updates, known := normalizeChat(Codex, raw, p.threadID, turn)
	if !known {
		p.chatDropped.Add(1)
		return
	}
	for _, update := range updates {
		// At most 4 large chunks (256 KiB) before acknowledgement.
		if !validChatID(turn, 256) || !update.valid() || len(p.chatPending) >= 4 {
			p.chatDropped.Add(1)
			continue
		}
		p.chatPending = append(p.chatPending, pendingCodexChat{turn, update})
	}
}

func (p *codexProcess) flushCodexChat(turn string) {
	for _, pending := range p.chatPending {
		if pending.turn == turn && p.observe != nil {
			update := pending.update
			p.observe(AdapterEvent{Chat: &update})
		} else {
			p.chatDropped.Add(1)
		}
	}
	p.chatPending = nil
}
