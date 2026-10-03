// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"encoding/json"
	"time"
)

const (
	scopeRead  = "intake.read"
	scopeWrite = "intake.write"

	evSourceRecorded = "intake.source_recorded"
	evTurnAppended   = "intake.turn_appended"
	evDraftProposed  = "intake.draft_proposed"
	evDraftAccepted  = "intake.draft_accepted"
	evNodeCreated    = "node.created"
	evNodeUpdated    = "node.updated"
)

type sourceWrite struct {
	Kind           string  `json:"kind"`
	Label          string  `json:"label"`
	Locator        *string `json:"locator,omitempty"`
	FileID         *string `json:"file_id,omitempty"`
	ContentSHA256  string  `json:"content_sha256"`
	IdempotencyKey string  `json:"idempotency_key"`
}

type sourceView struct {
	sourceWrite
	ID            string    `json:"id"`
	ProjectNodeID string    `json:"project_node_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type turnWrite struct {
	SourceID           string  `json:"source_id"`
	Ordinal            *int    `json:"ordinal"`
	Speaker            string  `json:"speaker"`
	SpeakerPrincipalID *string `json:"speaker_principal_id"`
	Body               string  `json:"body"`
	IdempotencyKey     string  `json:"idempotency_key"`
}

type turnView struct {
	SourceID           string    `json:"source_id"`
	Ordinal            int       `json:"ordinal"`
	Speaker            string    `json:"speaker"`
	SpeakerPrincipalID string    `json:"speaker_principal_id"`
	Body               string    `json:"body"`
	IdempotencyKey     string    `json:"idempotency_key"`
	ID                 string    `json:"id"`
	CreatedAt          time.Time `json:"created_at"`
}

type citationWrite struct {
	SourceID    string  `json:"source_id"`
	TurnID      *string `json:"turn_id,omitempty"`
	Locator     string  `json:"locator"`
	QuoteSHA256 *string `json:"quote_sha256,omitempty"`
}

type suggestionWrite struct {
	Title          string      `json:"title"`
	EstimatedHours json.Number `json:"estimated_hours"`
	Later          *bool       `json:"later"`
	AccessChange   *bool       `json:"access_change"`
}

type suggestionView struct {
	Title          string      `json:"title"`
	EstimatedHours json.Number `json:"estimated_hours"`
	Later          bool        `json:"later"`
	AccessChange   bool        `json:"access_change"`
}

type draftWrite struct {
	RequesterPrincipalID *string           `json:"requester_principal_id,omitempty"`
	Kind                 string            `json:"kind"`
	RequirementKind      *string           `json:"requirement_kind"`
	TargetNodeID         *string           `json:"target_node_id"`
	Title                string            `json:"title"`
	Body                 string            `json:"body"`
	Extensions           json.RawMessage   `json:"extensions,omitempty"`
	DocumentBytes        *string           `json:"document_bytes,omitempty"`
	BaseEventID          *int64            `json:"base_event_id"`
	Citations            []citationWrite   `json:"citations"`
	Suggestions          []suggestionWrite `json:"ticket_suggestions"`
	IdempotencyKey       string            `json:"idempotency_key"`
}

type draftView struct {
	RequesterPrincipalID *string          `json:"requester_principal_id,omitempty"`
	SupersedesDraftID    *string          `json:"supersedes_draft_id,omitempty"`
	Kind                 string           `json:"kind"`
	RequirementKind      *string          `json:"requirement_kind,omitempty"`
	TargetNodeID         *string          `json:"target_node_id,omitempty"`
	Title                string           `json:"title"`
	Body                 string           `json:"body"`
	Extensions           json.RawMessage  `json:"extensions,omitempty"`
	DocumentBytes        *string          `json:"document_bytes,omitempty"`
	BaseEventID          int64            `json:"base_event_id"`
	Citations            []citationWrite  `json:"citations"`
	Suggestions          []suggestionView `json:"ticket_suggestions"`
	IdempotencyKey       string           `json:"idempotency_key"`
	ID                   string           `json:"id"`
	Status               string           `json:"status"`
	ProposedAt           time.Time        `json:"proposed_at"`
	AcceptedAt           *time.Time       `json:"accepted_at"`
}

type acceptWrite struct {
	ExpectedBaseEventID *int64 `json:"expected_base_event_id"`
}

type snapshot struct {
	Sources []sourceView `json:"sources"`
	Turns   []turnView   `json:"turns"`
	Drafts  []draftView  `json:"drafts"`
}

type nodeSnap struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	KindID    string          `json:"kind_id"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Fields    json.RawMessage `json:"fields"`
	State     string          `json:"state"`
	ParentID  *string         `json:"parent_id"`
	Position  string          `json:"position"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *time.Time      `json:"deleted_at"`
}
