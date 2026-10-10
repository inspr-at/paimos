// SPDX-License-Identifier: AGPL-3.0-only

// Package chat owns the opt-in chat-v1 conversation identity boundary.
// History, delivery and receiver qualification are separate later packages.
// Native aliases form a durable graph: once owned, the connected references
// belong to exactly one (person, role, project), with no implicit release.
// aeon_store_chat_native atomically links, validates and inherits that owner;
// registration/context triggers close missed application paths. First binding
// includes aliases from earlier unbound generations, without requiring replay.
// Components are capped at 1024 digests and oversized components fail closed.
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

type CurrentBindingRequest struct {
	SessionID string `json:"session_id"`
}

// CurrentBinding names only the caller's own exact binding; it carries no
// message content, role, person or session metadata.
type CurrentBinding struct {
	Contract       string `json:"contract"`
	ConversationID string `json:"conversation_id"`
	BindingEpoch   string `json:"binding_epoch"`
}
