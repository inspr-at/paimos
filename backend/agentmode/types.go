// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package agentmode implements the privacy-safe, schema-v1 delivery
// supervision read model. It intentionally owns wire DTOs instead of exposing
// delivery.Snapshot or deliverytrust.Output, both of which contain identities
// and evidence that are internal to the trust calculation.
package agentmode

import (
	"errors"
	"time"
)

const (
	SchemaVersion       = 1
	AggregatesVersion   = 1
	MaxCandidateRoots   = 1000
	MaxAttentionItems   = 12
	MaxSearchBytes      = 160
	CursorEncodedLength = 211
)

var (
	ErrInvalid      = errors.New("invalid agent-mode request")
	ErrNotFound     = errors.New("agent-mode resource not found")
	ErrUnauthorized = errors.New("agent-mode request unauthorized")
	ErrInvariant    = errors.New("agent-mode invariant violation")
	ErrCursor       = errors.New("invalid agent-mode cursor")
)

type Request struct {
	UserID            int64
	RouteProjectID    *int64
	DetailDeliveryKey string
	Filters           Filters
}

type Snapshot struct {
	SchemaVersion    int              `json:"schema_version"`
	ServerTime       time.Time        `json:"server_time"`
	Cursor           string           `json:"cursor"`
	Rows             []DeliveryRow    `json:"rows"`
	SelectedDelivery string           `json:"selected_delivery"`
	SelectedOutside  *SelectedOutside `json:"selected_outside,omitempty"`
	Aggregates       Aggregates       `json:"aggregates"`
}

type SelectedOutsideReason string

const (
	SelectedFilterExcluded   SelectedOutsideReason = "filter_excluded"
	SelectedTerminal         SelectedOutsideReason = "terminal"
	SelectedActiveFallback   SelectedOutsideReason = "active_fallback"
	SelectedTerminalFallback SelectedOutsideReason = "terminal_fallback"
)

type SelectedOutside struct {
	Reason SelectedOutsideReason `json:"reason"`
	Row    DeliveryRow           `json:"row"`
}

type DeliveryRow struct {
	DeliveryID       string        `json:"delivery_id"`
	IssueID          int64         `json:"issue_id"`
	IssueKey         string        `json:"issue_key"`
	Title            string        `json:"title"`
	ProjectID        int64         `json:"project_id"`
	ProjectKey       string        `json:"project_key"`
	ProjectName      string        `json:"project_name"`
	EpicID           *int64        `json:"epic_id"`
	EpicKey          *string       `json:"epic_key"`
	EpicTitle        *string       `json:"epic_title"`
	LaneKey          string        `json:"lane_key"`
	AttemptID        *string       `json:"attempt_id"`
	AttemptNumber    *int64        `json:"attempt_number"`
	AttemptStatus    string        `json:"attempt_status"`
	PlanRevision     *string       `json:"plan_revision"`
	DeliveryRevision string        `json:"delivery_revision"`
	TrustRevision    string        `json:"trust_revision"`
	Tags             []string      `json:"tags"`
	Actor            *Actor        `json:"actor"`
	Activity         Activity      `json:"activity"`
	Stage            StageSummary  `json:"stage"`
	Stages           []Stage       `json:"stages"`
	Evidence         []Evidence    `json:"evidence"`
	Capabilities     Capabilities  `json:"capabilities"`
	Health           Health        `json:"health"`
	Attention        RowAttention  `json:"attention"`
	Freshness        Freshness     `json:"freshness"`
	Blockers         []SafeBlocker `json:"blockers"`
	Progress         *Progress     `json:"progress"`
	ETA              *ETA          `json:"eta"`
	Trust            SafeTrust     `json:"trust"`
	StatusText       string        `json:"status_text,omitempty"`
	UpdatedAt        string        `json:"updated_at,omitempty"`

	active             bool
	state              string
	changeSequence     int64
	attentionFlags     CountFlags
	attentionSince     *time.Time
	nextRefresh        *time.Time
	landingAt          *time.Time
	structuralIdentity string
}

type Actor struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

type Activity struct {
	Kind  string  `json:"kind"`
	Text  string  `json:"text,omitempty"`
	Since *string `json:"since"`
}

type StageSummary struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Index *int   `json:"index"`
	Total *int   `json:"total"`
}

type Stage struct {
	Key         string        `json:"key"`
	Label       string        `json:"label"`
	Status      string        `json:"status"`
	Required    bool          `json:"required"`
	Owner       *Actor        `json:"owner"`
	Activity    string        `json:"activity,omitempty"`
	Blockers    []SafeBlocker `json:"blockers"`
	Evidence    []Evidence    `json:"evidence"`
	StartedAt   *string       `json:"started_at"`
	CompletedAt *string       `json:"completed_at"`
}

type Evidence struct {
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	ReportedAt *string `json:"reported_at"`
}

type SafeBlocker struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

type Capabilities struct {
	ViewIssue        bool `json:"view_issue"`
	EditIssue        bool `json:"edit_issue"`
	Comment          bool `json:"comment"`
	Attach           bool `json:"attach"`
	LiveNote         bool `json:"live_note"`
	OneShotRunActive bool `json:"one_shot_run_active"`
}

type Health string

const (
	HealthHealthy   Health = "healthy"
	HealthAttention Health = "attention"
	HealthBlocked   Health = "blocked"
	HealthStale     Health = "stale"
	HealthUnknown   Health = "unknown"
)

type RowAttention struct {
	Level  int     `json:"level"`
	Reason string  `json:"reason,omitempty"`
	Since  *string `json:"since"`
}

type Freshness struct {
	State        string  `json:"state"`
	LastReportAt *string `json:"last_report_at"`
}

type Progress struct {
	Percent    *int    `json:"percent"`
	Trusted    bool    `json:"trusted"`
	Confidence string  `json:"confidence"`
	Source     *string `json:"source"`
	Basis      *string `json:"basis"`
	Revision   string  `json:"revision"`
}

type ETA struct {
	LandingAt     *time.Time `json:"landing_at"`
	OptimisticAt  *time.Time `json:"optimistic_at"`
	PessimisticAt *time.Time `json:"pessimistic_at"`
	Trusted       bool       `json:"trusted"`
	Confidence    string     `json:"confidence"`
	Basis         *string    `json:"basis"`
	CalculatedAt  time.Time  `json:"calculated_at"`
}

// SafeTrust is the explicit allowlist projection of deliverytrust.Output.
// Reporter/run-link IDs, providers/adapters, contributors, samples and full
// evidence references have no corresponding field and therefore cannot leak.
type SafeTrust struct {
	SchemaVersion        int          `json:"schema_version"`
	TrustRevision        string       `json:"trust_revision"`
	ProgressKnown        bool         `json:"progress_known"`
	ProgressPercent      *int         `json:"progress_percent"`
	ConfidenceLabel      string       `json:"confidence_label"`
	ReporterKind         string       `json:"reporter_kind"`
	SourceKind           string       `json:"source_kind"`
	Basis                string       `json:"basis,omitempty"`
	OptimisticLandingAt  *time.Time   `json:"optimistic_landing_at"`
	PessimisticLandingAt *time.Time   `json:"pessimistic_landing_at"`
	LandingAt            *time.Time   `json:"landing_at"`
	RangeOnly            bool         `json:"range_only"`
	Suppression          string       `json:"suppression,omitempty"`
	Scope                *PublicScope `json:"scope"`
	Flags                []string     `json:"flags"`
}

type PublicScope struct {
	AttemptID   string `json:"attempt_id"`
	PlanID      string `json:"plan_id"`
	ExecutionID string `json:"execution_id"`
	AuthorityID string `json:"authority_id"`
	ResetID     string `json:"reset_id"`
}

type CountSet struct {
	ActiveTotal  int           `json:"active_total"`
	CurrentStage StageCounts   `json:"current_stage"`
	Landing      LandingCounts `json:"landing"`
	Flags        CountFlags    `json:"flags"`
}

type StageCounts struct {
	Specification  int `json:"specification"`
	Implementation int `json:"implementation"`
	QA             int `json:"qa"`
	Deployment     int `json:"deployment"`
	Verification   int `json:"verification"`
	Unknown        int `json:"unknown"`
}

type LandingCounts struct {
	Within4Hours        int `json:"within_4h"`
	Within24Hours       int `json:"within_24h"`
	Within3Days         int `json:"within_3d"`
	Later               int `json:"later"`
	RangeOnly           int `json:"range_only"`
	SuppressedOrUnknown int `json:"suppressed_or_unknown"`
}

type CountFlags struct {
	Attention          int `json:"attention"`
	WaitingNeedsInput  int `json:"waiting_needs_input"`
	Blocked            int `json:"blocked"`
	StaleNoSignal      int `json:"stale_no_signal"`
	FailedNeedsRetry   int `json:"failed_needs_retry"`
	DeployedUnverified int `json:"deployed_unverified"`
	Unverified         int `json:"unverified"`
	UnknownReporter    int `json:"unknown_reporter"`
}

type Aggregates struct {
	SchemaVersion          int                `json:"schema_version"`
	StructuralRevision     string             `json:"structural_revision"`
	ClassificationRevision string             `json:"classification_revision"`
	CalculatedAt           time.Time          `json:"calculated_at"`
	NextRefreshAt          *time.Time         `json:"next_refresh_at"`
	Root                   CountSet           `json:"root"`
	Projects               []ProjectAggregate `json:"projects"`
	Attention              AttentionAggregate `json:"attention"`
}

type ProjectAggregate struct {
	ProjectID   int64           `json:"project_id"`
	ProjectKey  string          `json:"project_key"`
	ProjectName string          `json:"project_name"`
	Counts      CountSet        `json:"counts"`
	Lanes       []LaneAggregate `json:"lanes"`
}

type LaneAggregate struct {
	LaneKey   string   `json:"lane_key"`
	EpicID    *int64   `json:"epic_id"`
	EpicKey   *string  `json:"epic_key"`
	EpicTitle *string  `json:"epic_title"`
	Counts    CountSet `json:"counts"`
}

type AttentionAggregate struct {
	Total int             `json:"total"`
	Items []AttentionItem `json:"items"`
}

type AttentionItem struct {
	DeliveryID    string   `json:"delivery_id"`
	Level         int      `json:"level"`
	PrimaryReason string   `json:"primary_reason"`
	Flags         []string `json:"flags"`
	Since         *string  `json:"since"`
}
