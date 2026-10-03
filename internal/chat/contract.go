// SPDX-License-Identifier: AGPL-3.0-only

// Package chat owns the opt-in chat-v1 conversation identity boundary.
// History, delivery and receiver qualification are separate later packages.
package chat

type Role struct {
	Contract          string `json:"contract"`
	ID                string `json:"role_id"`
	ProjectID         string `json:"project_id"`
	Kind              string `json:"kind"`
	SlotKey           string `json:"slot_key"`
	ConversationScope string `json:"conversation_scope"`
}

type Readiness struct {
	State        string   `json:"state"`
	Capabilities []string `json:"capabilities"`
	Reason       string   `json:"reason"`
}

type Thread struct {
	Contract     string    `json:"contract"`
	ID           string    `json:"conversation_id"`
	Role         Role      `json:"role"`
	Revision     string    `json:"revision"`
	BindingEpoch string    `json:"binding_epoch"`
	Readiness    Readiness `json:"readiness"`
}

type WorkerBindingRequest struct {
	ConversationID string `json:"conversation_id"`
	SessionID      string `json:"session_id"`
	BindingEpoch   string `json:"binding_epoch"`
}
