// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package models

// SessionHomeSnapshot is the strict product-session home read
// model. It is deliberately separate from the frozen Agent Mode schema v1.
type SessionHomeSnapshot struct {
	SchemaVersion int                  `json:"schema_version"`
	ProjectID     int64                `json:"project_id"`
	Sessions      []SessionHomeSession `json:"sessions"`
	Totals        SessionHomeTotals    `json:"totals"`
}

type SessionHomeSession struct {
	ProductSessionID string               `json:"product_session_id"`
	Title            string               `json:"title"`
	Summary          string               `json:"summary"`
	Revision         int64                `json:"revision"`
	UpdatedAt        string               `json:"updated_at"`
	Target           SessionHomeTarget    `json:"target"`
	Status           SessionHomeStatus    `json:"status"`
	Harness          *SessionHomeHarness  `json:"harness"`
	Controls         SessionHomeControls  `json:"controls"`
	Node             *ProductSessionNode  `json:"node"`
	Inbox            SessionHomeInbox     `json:"inbox"`
	Attention        SessionHomeAttention `json:"attention"`
}

type SessionHomeTarget struct {
	Kind           string  `json:"kind"`
	ProjectAgentID *int64  `json:"project_agent_id"`
	AgentName      *string `json:"agent_name"`
	Address        *string `json:"address"`
}

type SessionHomeStatus struct {
	Phase              string `json:"phase"`
	Reason             string `json:"reason"`
	ActivityState      string `json:"activity_state"`
	ActivityReason     string `json:"activity_reason"`
	ActivityAgeSeconds *int64 `json:"activity_age_seconds"`
	ClosedReason       string `json:"closed_reason"`
}

type SessionHomeHarness struct {
	Harness        string              `json:"harness"`
	ManagementMode string              `json:"management_mode"`
	Capabilities   HarnessCapabilities `json:"advertised_capabilities"`
}

type SessionHomeControls struct {
	Steer     string `json:"steer"`
	Interrupt bool   `json:"interrupt"`
	Stop      bool   `json:"stop"`
}

type SessionHomeInbox struct {
	UnreadCount    int64   `json:"unread_count"`
	LatestUnreadAt *string `json:"latest_unread_at"`
}

type SessionHomeAttention struct {
	Required           bool    `json:"required"`
	ExceptionCount     int64   `json:"exception_count"`
	ActionRequestCount int64   `json:"action_request_count"`
	Reason             *string `json:"reason"`
}

type SessionHomeTotals struct {
	Sessions  int   `json:"sessions"`
	Unread    int64 `json:"unread"`
	Attention int   `json:"attention"`
}
