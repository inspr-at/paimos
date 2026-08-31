// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package models

// OrchestratorProjection is the deliberately redacted instance-wide identity
// available to ordinary authenticated internal users.
type OrchestratorProjection struct {
	DisplayLabel string `json:"display_label"`
}

// OrchestratorTarget is the full stable assignment visible only on the
// super-admin configuration and audit surfaces.
type OrchestratorTarget struct {
	ProjectID      int64  `json:"project_id"`
	ProjectKey     string `json:"project_key,omitempty"`
	ProjectAgentID int64  `json:"project_agent_id"`
	Key            string `json:"key"`
	DisplayLabel   string `json:"display_label"`
}

type OrchestratorProjectionResponse struct {
	SchemaVersion int                     `json:"schema_version"`
	Revision      int64                   `json:"revision"`
	Orchestrator  *OrchestratorProjection `json:"orchestrator"`
	UpdatedAt     *string                 `json:"updated_at"`
}

type OrchestratorConfigResponse struct {
	SchemaVersion int                 `json:"schema_version"`
	Revision      int64               `json:"revision"`
	Orchestrator  *OrchestratorTarget `json:"orchestrator"`
	UpdatedAt     *string             `json:"updated_at"`
}

type OrchestratorEvent struct {
	EventID        int64               `json:"event_id"`
	Operation      string              `json:"operation"`
	ActorUserID    int64               `json:"actor_user_id"`
	RequestID      string              `json:"request_id"`
	BeforeRevision int64               `json:"before_revision"`
	AfterRevision  int64               `json:"after_revision"`
	Before         *OrchestratorTarget `json:"before"`
	After          *OrchestratorTarget `json:"after"`
	CreatedAt      string              `json:"created_at"`
}

type OrchestratorEventsResponse struct {
	SchemaVersion     int                 `json:"schema_version"`
	Events            []OrchestratorEvent `json:"events"`
	NextAfterRevision *int64              `json:"next_after_revision"`
}
