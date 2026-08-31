// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package models

// ProductSession is the durable Paimos 6 work-conversation resource. Its
// typed identifier deliberately cannot be mistaken for an attribution header
// session, harness session, agent run, delivery, or intake session.
type ProductSession struct {
	ProductSessionID     string              `json:"product_session_id"`
	ProjectID            int64               `json:"project_id"`
	TargetKind           string              `json:"target_kind"`
	TargetProjectAgentID *int64              `json:"target_project_agent_id"`
	TargetAgentName      string              `json:"target_agent_name"`
	NodeID               *int64              `json:"node_id"`
	Node                 *ProductSessionNode `json:"node"`
	Title                string              `json:"title"`
	Summary              string              `json:"summary"`
	Revision             int64               `json:"revision"`
	CreatedByUserID      *int64              `json:"created_by_user_id"`
	UpdatedByUserID      *int64              `json:"updated_by_user_id"`
	CreatedAt            string              `json:"created_at"`
	UpdatedAt            string              `json:"updated_at"`
}

type ProductSessionNode struct {
	NodeID  int64  `json:"node_id"`
	NodeKey string `json:"node_key"`
	Label   string `json:"label"`
}

// ProductSessionEvent is immutable audit evidence for one durable session
// lifecycle mutation. Node IDs are snapshots, deliberately not live joins.
type ProductSessionEvent struct {
	EventID          int64  `json:"event_id"`
	ProductSessionID string `json:"product_session_id"`
	EventSequence    int64  `json:"event_sequence"`
	Operation        string `json:"operation"`
	ActorUserID      int64  `json:"actor_user_id"`
	BeforeNodeID     *int64 `json:"before_node_id"`
	AfterNodeID      *int64 `json:"after_node_id"`
	BeforeRevision   int64  `json:"before_revision"`
	AfterRevision    int64  `json:"after_revision"`
	CreatedAt        string `json:"created_at"`
}
