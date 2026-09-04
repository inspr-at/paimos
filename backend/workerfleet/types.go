// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package workerfleet builds the bounded, privacy-safe worker projection used
// by both project and portfolio Agent Mode surfaces.
package workerfleet

import (
	"errors"
	"time"

	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/workshape"
)

const (
	SchemaVersion               = 1
	SchemaVersionV2             = 2
	MaxSample                   = 100
	RecentMessagesPerWorker     = 4
	TerminalGenerationsPerAgent = 1
	RuntimeTrustManagedReporter = "managed_reporter"
	RuntimeTrustUntrusted       = "untrusted"
)

var (
	ErrInvalid   = errors.New("invalid worker fleet request")
	ErrNotFound  = errors.New("worker fleet resource not found")
	ErrInvariant = errors.New("worker fleet invariant violation")
)

type Request struct {
	UserID         int64
	RouteProjectID *int64
	Zoom           string
}

type Snapshot struct {
	SchemaVersion   int        `json:"schema_version"`
	ObservedAt      time.Time  `json:"observed_at"`
	Scope           Scope      `json:"scope"`
	Zoom            string     `json:"zoom"`
	Band            string     `json:"band"`
	SampleLimit     int        `json:"sample_limit"`
	SampleTruncated bool       `json:"sample_truncated"`
	Totals          Totals     `json:"totals"`
	Projects        []Project  `json:"projects"`
	Workers         []Worker   `json:"workers"`
	Provenance      Provenance `json:"provenance"`
}

// SnapshotV2 extends the closed PAI-904 wire contract on a new route. Snapshot
// remains the exact v1 shape so additive runtime and assignment fields cannot
// silently change an already published projection.
type SnapshotV2 struct {
	SchemaVersion   int        `json:"schema_version"`
	ObservedAt      time.Time  `json:"observed_at"`
	Scope           Scope      `json:"scope"`
	Zoom            string     `json:"zoom"`
	Band            string     `json:"band"`
	SampleLimit     int        `json:"sample_limit"`
	SampleTruncated bool       `json:"sample_truncated"`
	Totals          Totals     `json:"totals"`
	Projects        []Project  `json:"projects"`
	Workers         []WorkerV2 `json:"workers"`
	Provenance      Provenance `json:"provenance"`
}

type Scope struct {
	Kind      string `json:"kind"`
	ProjectID *int64 `json:"project_id"`
}

type Totals struct {
	Projects        int64 `json:"projects"`
	SampledProjects int   `json:"sampled_projects"`
	OmittedProjects int64 `json:"omitted_projects"`
	Workers         int64 `json:"workers"`
	SampledWorkers  int   `json:"sampled_workers"`
	OmittedWorkers  int64 `json:"omitted_workers"`
}

type Provenance struct {
	Source                      string `json:"source"`
	Cache                       string `json:"cache"`
	RemoteCache                 bool   `json:"remote_cache"`
	ProjectionVersion           int    `json:"projection_version"`
	TerminalGenerationsPerAgent int    `json:"terminal_generations_per_agent"`
}

type Project struct {
	ID             int64        `json:"id"`
	Key            string       `json:"key"`
	Name           string       `json:"name"`
	TotalWorkers   int64        `json:"total_workers"`
	SampledWorkers int          `json:"sampled_workers"`
	OmittedWorkers int64        `json:"omitted_workers"`
	Orchestrator   Orchestrator `json:"orchestrator"`
}

type Orchestrator struct {
	State     string  `json:"state"`
	Reason    string  `json:"reason"`
	SessionID *string `json:"session_id"`
}

type Worker struct {
	HarnessSessionID           string          `json:"harness_session_id"`
	ParentSessionID            *string         `json:"parent_harness_session_id"`
	ParentInSample             bool            `json:"parent_in_sample"`
	Project                    ProjectIdentity `json:"project"`
	Agent                      AgentIdentity   `json:"agent"`
	Role                       string          `json:"role"`
	Harness                    string          `json:"harness"`
	ManagementMode             string          `json:"management_mode"`
	Ticket                     *Ticket         `json:"ticket"`
	Phase                      string          `json:"phase"`
	Revision                   int64           `json:"revision"`
	Liveness                   Liveness        `json:"liveness"`
	Capabilities               Capabilities    `json:"capabilities"`
	RecentCommunication        []Communication `json:"recent_communication"`
	RecentCommunicationOmitted int64           `json:"recent_communication_omitted"`
	DeliveryTrust              DeliveryTrust   `json:"delivery_trust"`
}

type WorkerV2 struct {
	HarnessSessionID           string                         `json:"harness_session_id"`
	ParentSessionID            *string                        `json:"parent_harness_session_id"`
	ParentInSample             bool                           `json:"parent_in_sample"`
	Project                    ProjectIdentity                `json:"project"`
	Agent                      AgentIdentity                  `json:"agent"`
	Role                       string                         `json:"role"`
	Harness                    string                         `json:"harness"`
	MachineID                  *string                        `json:"machine_id"`
	WorkspaceProvenance        *WorkspaceProvenance           `json:"workspace_provenance"`
	DispatchProfile            *models.HarnessDispatchProfile `json:"dispatch_profile"`
	AccountLabel               string                         `json:"account_label"`
	ManagementMode             string                         `json:"management_mode"`
	RuntimeProvenanceTrust     string                         `json:"runtime_provenance_trust"`
	Ticket                     *Ticket                        `json:"ticket"`
	WorkShape                  string                         `json:"work_shape"`
	WorkContract               workshape.Contract             `json:"work_contract"`
	Phase                      string                         `json:"phase"`
	Revision                   int64                          `json:"revision"`
	Liveness                   Liveness                       `json:"liveness"`
	Capabilities               Capabilities                   `json:"capabilities"`
	RecentCommunication        []Communication                `json:"recent_communication"`
	RecentCommunicationOmitted int64                          `json:"recent_communication_omitted"`
	DeliveryTrust              DeliveryTrust                  `json:"delivery_trust"`
}

// WorkspaceProvenance deliberately excludes raw paths, Git branches, and
// workspace identities from the worker-facing projection. Kind and mode are
// enough to inspect isolation without exposing local machine values.
type WorkspaceProvenance struct {
	Kind string `json:"kind"`
	Mode string `json:"mode"`
}

type ProjectIdentity struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type AgentIdentity struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Ticket struct {
	ID               int64   `json:"id"`
	DetailsAvailable bool    `json:"details_available"`
	Key              *string `json:"key"`
	Title            *string `json:"title"`
}

type Liveness struct {
	State              string    `json:"state"`
	Reason             string    `json:"reason"`
	ObservedAt         time.Time `json:"observed_at"`
	Source             string    `json:"source"`
	ReporterAgeSeconds *int64    `json:"reporter_age_seconds"`
	ClosedReason       string    `json:"closed_reason,omitempty"`
}

type Capabilities struct {
	Inbox     bool `json:"inbox"`
	Status    bool `json:"status"`
	Steer     bool `json:"steer"`
	Interrupt bool `json:"interrupt"`
	Stop      bool `json:"stop"`
}

type Communication struct {
	MessageID      string  `json:"message_id"`
	DeliveryID     *string `json:"delivery_id"`
	Direction      string  `json:"direction"`
	Attribution    string  `json:"attribution"`
	RequestedLevel *string `json:"requested_level"`
	EffectiveLevel *string `json:"effective_level"`
	State          *string `json:"state"`
	FallbackCode   *string `json:"fallback_code"`
	ErrorCode      *string `json:"error_code"`
	OccurredAt     string  `json:"occurred_at"`
}

type DeliveryTrust struct {
	ProgressTrusted bool       `json:"progress_trusted"`
	ETATrusted      bool       `json:"eta_trusted"`
	Reason          string     `json:"reason"`
	TrustRevision   *string    `json:"trust_revision"`
	ObservedAt      *time.Time `json:"observed_at"`
	Progress        *int       `json:"progress_percent"`
	ETA             *time.Time `json:"eta"`
}
