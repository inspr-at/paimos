// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package models

// HarnessCapabilities is the closed set of capabilities advertised by one
// harness session. The server caps these claims against the bound adapter;
// they are never a server certification of vendor behavior.
type HarnessCapabilities struct {
	Inbox     bool `json:"inbox"`
	Status    bool `json:"status"`
	Steer     bool `json:"steer"`
	Interrupt bool `json:"interrupt"`
	Stop      bool `json:"stop"`
}

// HarnessSession is the durable control-plane identity for one agent process
// or externally managed vendor session. The attribution session created by
// `paimos session start` is a different resource and never appears here.
type HarnessSession struct {
	ID               string              `json:"id"`
	ProjectID        int64               `json:"project_id"`
	ProjectAgentID   int64               `json:"project_agent_id"`
	AgentName        string              `json:"agent_name"`
	Harness          string              `json:"harness"`
	Host             string              `json:"host"`
	MessageTargetID  string              `json:"message_target_id,omitempty"`
	ManagementMode   string              `json:"management_mode"`
	Role             string              `json:"role"`
	ParentSessionID  *string             `json:"parent_harness_session_id"`
	TicketID         *int64              `json:"ticket_id"`
	SteerMode        string              `json:"steer_mode"`
	Capabilities     HarnessCapabilities `json:"advertised_capabilities"`
	Phase            string              `json:"phase"`
	HeartbeatAt      string              `json:"heartbeat_at,omitempty"`
	ActivityState    string              `json:"activity_state"`
	ActivityReason   string              `json:"activity_reason"`
	ActivityKind     string              `json:"activity_event_kind,omitempty"`
	ActivityAt       string              `json:"activity_at,omitempty"`
	ActivityAge      *int64              `json:"activity_age_seconds"`
	ActivitySequence int64               `json:"activity_sequence"`
	ClosedReason     string              `json:"closed_reason,omitempty"`
	YieldedAt        string              `json:"yielded_at,omitempty"`
	YieldSequence    int64               `json:"yield_sequence"`
	Revision         int64               `json:"revision"`
	CreatedAt        string              `json:"created_at"`
	UpdatedAt        string              `json:"updated_at"`
}

// HarnessSessionEvent is an immutable, content-free state transition for one
// public worker generation. EventSequence follows the session revision and is
// therefore stable across exact retries without exposing vendor payloads.
type HarnessSessionEvent struct {
	ID                    int64   `json:"id"`
	HarnessSessionID      string  `json:"harness_session_id"`
	EventSequence         int64   `json:"event_sequence"`
	Operation             string  `json:"operation"`
	Phase                 string  `json:"phase"`
	ActivityState         string  `json:"activity_state"`
	ActivityReason        string  `json:"activity_reason"`
	ActivityKind          string  `json:"activity_event_kind,omitempty"`
	ActivitySequence      int64   `json:"activity_sequence"`
	AssignmentPresent     *bool   `json:"assignment_present,omitempty"`
	ClosedReason          string  `json:"closed_reason,omitempty"`
	BeforeParentSessionID *string `json:"before_parent_harness_session_id"`
	AfterParentSessionID  *string `json:"after_parent_harness_session_id"`
	BeforeTicketID        *int64  `json:"before_ticket_id"`
	AfterTicketID         *int64  `json:"after_ticket_id"`
	CreatedAt             string  `json:"created_at"`
}

// HarnessOrchestratorProjection resolves a project coordinator only when the
// durable harness-session state has exactly one live coordinator candidate.
// The unset and ambiguous states deliberately omit Session.
type HarnessOrchestratorProjection struct {
	State   string          `json:"state"`
	Reason  string          `json:"reason"`
	Session *HarnessSession `json:"session"`
}

// HarnessControl is a typed interrupt/stop request. Free-form command text is
// intentionally absent: the daemon maps these closed values to a child it owns.
type HarnessControl struct {
	ID                string `json:"id"`
	HarnessSessionID  string `json:"harness_session_id"`
	Sequence          int64  `json:"sequence"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	Reason            string `json:"reason,omitempty"`
	RequestedByUserID int64  `json:"requested_by_user_id"`
	RequestedAt       string `json:"requested_at"`
	ClaimedAt         string `json:"claimed_at,omitempty"`
	CompletedAt       string `json:"completed_at,omitempty"`
}

// HarnessControlOutcome is the read-only, project-scoped operator view of one
// typed control. CorrelationID deliberately repeats the public control UUID:
// agentd uses that exact value as its local owned-effect correlation key.
type HarnessControlOutcome struct {
	ID               string `json:"id"`
	ProjectID        int64  `json:"project_id"`
	HarnessSessionID string `json:"harness_session_id"`
	CorrelationID    string `json:"correlation_id"`
	Sequence         int64  `json:"sequence"`
	Kind             string `json:"kind"`
	State            string `json:"state"`
	Outcome          string `json:"outcome,omitempty"`
	Reason           string `json:"reason,omitempty"`
	RequestedAt      string `json:"requested_at"`
	ClaimedAt        string `json:"claimed_at,omitempty"`
	CompletedAt      string `json:"completed_at,omitempty"`
}
