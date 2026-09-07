// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import "encoding/json"

// StreamingBehavior controls how prompts are queued while the agent streams.
type StreamingBehavior string

const (
	StreamingSteer    StreamingBehavior = "steer"
	StreamingFollowUp StreamingBehavior = "followUp"
)

// Command is one stdin JSONL object sent to pi --mode rpc.
type Command struct {
	ID                 string            `json:"id,omitempty"`
	Type               string            `json:"type"`
	Message            string            `json:"message,omitempty"`
	StreamingBehavior  StreamingBehavior `json:"streamingBehavior,omitempty"`
}

// Response is the correlated command acknowledgement from the child.
// success:true means accepted/queued/handled, not task completion.
type Response struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ClearQueueData is the clear_queue response payload.
type ClearQueueData struct {
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`
}

// StateModel is the provider-bound model object from get_state.
type StateModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// StateData is the get_state response payload subset used by the adapter.
type StateData struct {
	Model               *StateModel `json:"model"`
	ThinkingLevel       string      `json:"thinkingLevel"`
	IsStreaming         bool        `json:"isStreaming"`
	PendingMessageCount int         `json:"pendingMessageCount"`
	SessionID           string      `json:"sessionId,omitempty"`
}

// ExpectedState is the frozen launch intent verified against get_state before
// any prompt or inbox delivery.
type ExpectedState struct {
	Provider      string
	ModelID       string
	ThinkingLevel string
}

// Frame is either a correlated response or an agent event line from stdout.
type Frame struct {
	Raw  json.RawMessage
	Kind FrameKind
}

type FrameKind string

const (
	FrameResponse FrameKind = "response"
	FrameEvent    FrameKind = "event"
	FrameInvalid  FrameKind = "invalid"
)

func ClassifyFrame(raw json.RawMessage) Frame {
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return Frame{Raw: raw, Kind: FrameInvalid}
	}
	switch probe.Type {
	case "response":
		return Frame{Raw: raw, Kind: FrameResponse}
	default:
		return Frame{Raw: raw, Kind: FrameEvent}
	}
}

// Acceptance records command delivery evidence. It is not turn completion.
type Acceptance struct {
	CorrelationID string
	Command       string
	Accepted      bool
	Error         string
}
