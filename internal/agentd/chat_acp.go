// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"encoding/json"
	"unicode/utf8"
)

const chatToolLimit = 128

// Called by the owned event reader, under the adapter's lifecycle lock. Titles
// are category names: ACP display titles can contain commands and private paths.
func (p *wireProcess) observeACPChat(raw json.RawMessage) {
	if p.chatTools == nil {
		p.chatTools = map[string]string{}
	}
	updates, known := normalizeACPChat(raw, p.sessionID, p.chatTools)
	p.publishChatUpdates(updates, known)
}

func normalizeACPChat(raw json.RawMessage, session string, tools map[string]string) ([]ChatUpdate, bool) {
	if len(raw) > 8<<20 || !utf8.Valid(raw) {
		return nil, false
	}
	var f struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind       string          `json:"sessionUpdate"`
				Content    json.RawMessage `json:"content"`
				ToolCallID string          `json:"toolCallId"`
				ToolKind   string          `json:"kind"`
				Status     string          `json:"status"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &f) != nil || !validChatID(session, 128) || f.Params.SessionID != session {
		return nil, false
	}
	if f.Method == "session/request_permission" {
		return []ChatUpdate{chatState("requires_action")}, true
	}
	if f.Method != "session/update" {
		return nil, false
	}
	u := f.Params.Update
	switch u.Kind {
	case "agent_message_chunk":
		var content ChatContent
		if json.Unmarshal(u.Content, &content) != nil || content.Type != "text" {
			return nil, false
		}
		return []ChatUpdate{chatText(content.Text)}, true
	case "tool_call", "tool_call_update":
		if !validChatID(u.ToolCallID, 256) {
			return nil, false
		}
		title, exists := tools[u.ToolCallID]
		if u.Kind == "tool_call_update" && !exists {
			return nil, false
		}
		if u.Kind == "tool_call" || u.ToolKind != "" {
			title = acpToolTitle(u.ToolKind)
		}
		if title == "" { // Updates must refer to an observed owned tool.
			return nil, false
		}
		status := u.Status
		if status == "" && u.Kind == "tool_call_update" {
			return nil, true // Payload-only changes are deliberately excluded.
		}
		if status == "pending" || status == "" {
			status = "in_progress"
		}
		update := chatTool(u.ToolCallID, title, status)
		if !update.valid() || !exists && len(tools) >= chatToolLimit {
			return nil, false
		}
		tools[u.ToolCallID] = title
		return []ChatUpdate{update}, true
	case "agent_thought_chunk", "user_message_chunk", "usage_update", "current_mode_update", "config_option_update", "session_info_update", "plan", "available_commands_update":
		return nil, true
	}
	return nil, false
}

func acpToolTitle(kind string) string {
	switch kind {
	case "execute":
		return "Bash"
	case "read":
		return "Read"
	case "edit":
		return "Edit"
	case "delete":
		return "Delete"
	case "move":
		return "Move"
	case "search":
		return "Search"
	case "fetch":
		return "Fetch"
	case "think":
		return "" // Reasoning has no chat projection.
	default:
		return "Tool"
	}
}
