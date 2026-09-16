// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxGrokACPFrame = 512 << 10

var errGrokProtocol = errors.New("restricted Grok ACP protocol is unavailable")

// GrokConversationOptions are private one-turn bounds, supplied by the owning
// conversation controller after its existing claim and journal checks.
type GrokConversationOptions struct {
	MaxOutputBytes int
	MaxEvents      int
	// ACP v1 on this pinned native build has no reviewed structured-output
	// control. The adapter refuses a schema until that gate is proved.
	OutputSchema json.RawMessage
}

// grokACPWire is one private ACP stdio connection. It never logs vendor frames.
// Calls are serialized because a conversation owns exactly one turn.
type grokACPWire struct {
	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex
	nextID  int
}

type grokACPMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func newGrokACPWire(reader io.Reader, writer io.Writer) *grokACPWire {
	return &grokACPWire{reader: bufio.NewReaderSize(reader, 16<<10), writer: writer}
}

func (w *grokACPWire) send(method string, params any, id int) error {
	message := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id > 0 {
		message["id"] = id
	}
	raw, err := json.Marshal(message)
	if err != nil || len(raw) > maxGrokACPFrame-1 {
		return errGrokProtocol
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	_, err = w.writer.Write(append(raw, '\n'))
	if err != nil {
		return errGrokProtocol
	}
	return nil
}

func (w *grokACPWire) read() (grokACPMessage, error) {
	var message grokACPMessage
	var line []byte
	for {
		part, err := w.reader.ReadSlice('\n')
		if len(line)+len(part) > maxGrokACPFrame {
			return message, errGrokProtocol
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil || len(line) == 0 || line[len(line)-1] != '\n' || !utf8.Valid(line) || json.Unmarshal(line, &message) != nil {
			return message, errGrokProtocol
		}
		break
	}
	if bytes.Equal(message.ID, []byte("null")) ||
		(len(message.ID) == 0 && message.Method == "") ||
		(len(message.ID) > 0 && message.Method != "") {
		return message, errGrokProtocol
	}
	return message, nil
}

func (w *grokACPWire) call(method string, params any) (json.RawMessage, error) {
	w.nextID++
	id := w.nextID
	if err := w.send(method, params, id); err != nil {
		return nil, err
	}
	for {
		message, err := w.read()
		if err != nil {
			return nil, err
		}
		if len(message.ID) > 0 {
			var received int
			if json.Unmarshal(message.ID, &received) != nil || received != id || len(message.Error) > 0 {
				return nil, errGrokProtocol
			}
			return message.Result, nil
		}
		if !safeGrokNotification(message, "", nil) {
			return nil, errGrokProtocol
		}
	}
}

// safeGrokNotification refuses every client request and every tool-bearing
// event, including vendor extensions. Known content-free updates are allowed.
func safeGrokNotification(message grokACPMessage, sessionID string, answer *grokAnswer) bool {
	if len(message.ID) > 0 || message.Method == "" {
		return false
	}
	if answer != nil {
		answer.events++
		if answer.events > answer.maxEvents {
			answer.failure = ConversationFailureEventBound
			return false
		}
	}
	if bytes.Contains(message.Params, []byte(`"toolCallId"`)) ||
		bytes.Contains(message.Params, []byte(`"web_search"`)) ||
		bytes.Contains(message.Params, []byte(`"x_search"`)) {
		return false
	}
	if strings.HasPrefix(message.Method, "_x.ai/") {
		if message.Method == "_x.ai/mcp/servers_updated" {
			var params struct {
				Servers json.RawMessage `json:"mcpServers"`
			}
			return json.Unmarshal(message.Params, &params) == nil && bytes.Equal(bytes.TrimSpace(params.Servers), []byte("[]"))
		}
		if message.Method == "_x.ai/mcp_initialized" {
			var params struct {
				Count *int `json:"mcpToolCount"`
			}
			return json.Unmarshal(message.Params, &params) == nil && params.Count != nil && *params.Count == 0
		}
		lower := strings.ToLower(message.Method + string(message.Params))
		if strings.Contains(lower, "tool") || strings.Contains(lower, "search") {
			return false
		}
		return true
	}
	if message.Method != "session/update" {
		return false
	}
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind    string `json:"sessionUpdate"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(message.Params, &params) != nil || (sessionID != "" && params.SessionID != sessionID) {
		return false
	}
	switch params.Update.Kind {
	case "agent_message_chunk":
		if answer == nil || params.Update.Content.Type != "text" || !utf8.ValidString(params.Update.Content.Text) {
			return false
		}
		if len(answer.text)+len(params.Update.Content.Text) > answer.maxOutputBytes {
			answer.failure = ConversationFailureOutputBound
			return false
		}
		answer.text += params.Update.Content.Text
		answer.deltas = append(answer.deltas, CodexConversationDelta{
			Cursor: uint64(len(answer.deltas) + 1), ThreadID: sessionID, TurnID: answer.turnID,
			ItemID: "assistant", Text: params.Update.Content.Text,
		})
		return true
	case "agent_thought_chunk", "current_mode_update", "session_info_update", "usage_update", "plan", "available_commands_update":
		return true
	default:
		return false
	}
}

type grokAnswer struct {
	turnID         string
	text           string
	deltas         []CodexConversationDelta
	events         int
	maxEvents      int
	maxOutputBytes int
	failure        ConversationFailure
}

func (w *grokACPWire) prompt(sessionID, turnID, text string, limits GrokConversationOptions) (CodexConversationResult, []CodexConversationDelta) {
	answer := &grokAnswer{turnID: turnID, maxEvents: limits.MaxEvents, maxOutputBytes: limits.MaxOutputBytes}
	result := CodexConversationResult{Outcome: ConversationFailed, Failure: ConversationFailureProtocol, ThreadID: sessionID, TurnID: turnID}
	w.nextID++
	id := w.nextID
	if w.send("session/prompt", map[string]any{"sessionId": sessionID, "prompt": []any{map[string]string{"type": "text", "text": text}}}, id) != nil {
		return result, nil
	}
	for {
		message, err := w.read()
		if err != nil {
			return result, nil
		}
		if len(message.ID) > 0 {
			var received int
			if json.Unmarshal(message.ID, &received) != nil || received != id || len(message.Error) > 0 {
				return result, nil
			}
			var reply struct {
				StopReason string `json:"stopReason"`
			}
			if json.Unmarshal(message.Result, &reply) != nil {
				return result, nil
			}
			answer.events++
			if answer.events > answer.maxEvents {
				result.Failure = ConversationFailureEventBound
				return result, nil
			}
			result.TerminalStatus = reply.StopReason
			switch reply.StopReason {
			case "end_turn":
				if answer.text != "" {
					result.Outcome, result.Failure, result.Text, result.ItemID = ConversationCompleted, ConversationFailureNone, answer.text, "assistant"
					result.FinalCursor = uint64(len(answer.deltas))
					return result, answer.deltas
				}
			case "cancelled":
				result.Outcome, result.Failure = ConversationCancelled, ConversationFailureCancelled
			case "refusal":
				result.Failure = ConversationFailureRefusal
			case "max_tokens", "max_turn_requests":
				result.Failure = ConversationFailureTruncated
			}
			return result, nil
		}
		if !safeGrokNotification(message, sessionID, answer) {
			if answer.failure != ConversationFailureNone {
				result.Failure = answer.failure
			}
			return result, nil
		}
	}
}

// replayGrokDeltas is deliberately a read-only view of the already owned turn.
// It cannot send another ACP prompt or start another child.
func replayGrokDeltas(deltas []CodexConversationDelta, after uint64) ([]CodexConversationDelta, error) {
	if after > uint64(len(deltas)) {
		return nil, ErrCodexConversationCursor
	}
	out := append([]CodexConversationDelta(nil), deltas[after:]...)
	return out, nil
}

func grokContextFailure(ctx context.Context) ConversationFailure {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ConversationFailureDeadline
	}
	return ConversationFailureCancelled
}
