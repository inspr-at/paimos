// SPDX-License-Identifier: AGPL-3.0-only

// Package chat owns the opt-in chat-v1 conversation identity boundary.
// History, delivery and receiver qualification are separate later packages.
// Native references retain their first bound person, project and chat role.
// Harness registration propagates that ownership to both supplied aliases,
// even for an unbound replacement, and rejects conflicting history atomically.
// Replays and paused continuations use the same guard before event writes;
// binding also checks older registrations that predate the creation guard.
package chat

type RoleCreate struct {
	Kind    string `json:"kind"`
	SlotKey string `json:"slot_key"`
}

type ThreadResolve struct {
	RoleID string `json:"role_id"`
}

type BindingWrite struct {
	ExpectedEpoch string `json:"expected_epoch"`
	SessionID     string `json:"session_id"`
}

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
