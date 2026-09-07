// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"encoding/json"
	"strings"
)

// PublicEvent is the sanitized event surface for agentd observers. It never
// carries raw prompts, tool arguments, provider payloads, or secrets.
type PublicEvent struct {
	Type string `json:"type"`

	// Terminal completion markers for the owned turn lifecycle.
	Settled bool `json:"settled,omitempty"`
	Ended   bool `json:"ended,omitempty"`

	// Queue visibility without message text.
	SteeringQueued int `json:"steering_queued,omitempty"`
	FollowUpQueued int `json:"follow_up_queued,omitempty"`

	// Protocol or ownership faults surfaced as finite codes.
	ProtocolFault bool `json:"protocol_fault,omitempty"`
}

// SanitizeEvent converts a raw Pi agent event into a public observation.
func SanitizeEvent(raw json.RawMessage) PublicEvent {
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return PublicEvent{Type: "invalid", ProtocolFault: true}
	}
	event := PublicEvent{Type: probe.Type}
	switch probe.Type {
	case "agent_settled":
		event.Settled = true
	case "agent_end":
		event.Ended = true
	case "queue_update":
		var queue struct {
			Steering []string `json:"steering"`
			FollowUp []string `json:"followUp"`
		}
		if json.Unmarshal(raw, &queue) == nil {
			event.SteeringQueued = len(queue.Steering)
			event.FollowUpQueued = len(queue.FollowUp)
		}
	}
	return event
}

// RedactDiagnostic collapses an error for logs without echoing child payloads.
func RedactDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "secret") || strings.Contains(msg, "api_key") || strings.Contains(msg, "token") {
		return "pi rpc operation failed"
	}
	return msg
}
