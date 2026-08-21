// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package externalstage owns the frozen v1 wire contract for external delivery
// stage handoffs. Persistence and service behavior are deliberately separate;
// this file is the compile-time authority for route, media, header, enum, and
// DTO spelling shared by handlers, OpenAPI, schema discovery, and adapters.
package externalstage

import "net/http"

const (
	ContractMajor       = 1
	MediaTypeV1         = "application/vnd.paimos.external-stage.v1+json"
	SecretMediaTypeV1   = "application/vnd.paimos.external-stage-secret.v1"
	HandoffSecretHeader = "X-PAIMOS-Handoff-Secret"
	OneTimeSecretBytes  = 32

	InternalCreatePath = "/api/agent-mode/deliveries/{deliveryKey}/external-stage-handoffs"
	InternalMintPath   = "/api/agent-mode/external-stage-handoffs/{handoffID}/mint"
	InternalRotatePath = "/api/agent-mode/external-stage-handoffs/{handoffID}/rotate"
	InternalRevokePath = "/api/agent-mode/external-stage-handoffs/{handoffID}/revoke"
	ExternalPullPath   = "/api/external-stage/handoffs/{handoffID}"
	ExternalAcceptPath = "/api/external-stage/handoffs/{handoffID}/accept"
	ExternalReportPath = "/api/external-stage/handoffs/{handoffID}/reports"
)

// Route is one literal v1 operation. Action suffixes stay separate so chi,
// privacy classification, and OpenAPI can never interpret caller text as an
// operation selector.
type Route struct {
	OperationID string
	Method      string
	Path        string
	Audience    string
}

var Routes = []Route{
	{OperationID: "createExternalStageHandoff", Method: http.MethodPost, Path: InternalCreatePath, Audience: "internal"},
	{OperationID: "mintExternalStageHandoffSecret", Method: http.MethodPost, Path: InternalMintPath, Audience: "internal"},
	{OperationID: "rotateExternalStageHandoffSecret", Method: http.MethodPost, Path: InternalRotatePath, Audience: "internal"},
	{OperationID: "revokeExternalStageHandoff", Method: http.MethodPost, Path: InternalRevokePath, Audience: "internal"},
	{OperationID: "pullExternalStageHandoff", Method: http.MethodGet, Path: ExternalPullPath, Audience: "external"},
	{OperationID: "acceptExternalStageHandoff", Method: http.MethodPost, Path: ExternalAcceptPath, Audience: "external"},
	{OperationID: "reportExternalStageHandoff", Method: http.MethodPost, Path: ExternalReportPath, Audience: "external"},
}

type ReporterClass string
type ReporterRole string
type HandoffState string
type EvidenceKind string
type EvidenceResult string
type BlockerCode string

const (
	ReporterClassPharos ReporterClass = "pharos"
	ReporterClassJanus  ReporterClass = "janus"

	ReporterRoleOwner      ReporterRole = "owner"
	ReporterRoleDependency ReporterRole = "dependency"

	HandoffStateIssued    HandoffState = "issued"
	HandoffStateAccepted  HandoffState = "accepted"
	HandoffStateActive    HandoffState = "active"
	HandoffStateWaiting   HandoffState = "waiting"
	HandoffStateBlocked   HandoffState = "blocked"
	HandoffStateSucceeded HandoffState = "succeeded"
	HandoffStateFailed    HandoffState = "failed"

	EvidenceKindDeployment        EvidenceKind = "deployment"
	EvidenceKindVerification      EvidenceKind = "verification"
	EvidenceKindAuthorization     EvidenceKind = "authorization"
	EvidenceKindCredentialHandoff EvidenceKind = "credential_handoff"

	EvidenceResultSucceeded EvidenceResult = "succeeded"
	EvidenceResultFailed    EvidenceResult = "failed"
	EvidenceResultSatisfied EvidenceResult = "satisfied"
	EvidenceResultBlocked   EvidenceResult = "blocked"

	BlockerDependencyPending BlockerCode = "dependency_pending"
	BlockerDependencyFailed  BlockerCode = "dependency_failed"
	BlockerReporterStale     BlockerCode = "reporter_stale"
	BlockerExternalWaiting   BlockerCode = "external_waiting"
)

var (
	ReporterClasses = []string{string(ReporterClassPharos), string(ReporterClassJanus)}
	ReporterRoles   = []string{string(ReporterRoleOwner), string(ReporterRoleDependency)}
	HandoffStates   = []string{
		string(HandoffStateIssued), string(HandoffStateAccepted), string(HandoffStateActive),
		string(HandoffStateWaiting), string(HandoffStateBlocked), string(HandoffStateSucceeded),
		string(HandoffStateFailed),
	}
	EvidenceKinds = []string{
		string(EvidenceKindDeployment), string(EvidenceKindVerification),
		string(EvidenceKindAuthorization), string(EvidenceKindCredentialHandoff),
	}
)

// CreateHandoffRequest selects a current server-owned execution and a current
// reporter registration. Class, role, dependency, ceiling, plan, lineage, and
// authority are resolved from those rows and cannot be supplied by the caller.
type CreateHandoffRequest struct {
	StageKey               string `json:"stage_key"`
	ExecutionNumber        int64  `json:"execution_number"`
	ExpectedPlanRevision   int64  `json:"expected_plan_revision"`
	ExpectedAuthorityEpoch int64  `json:"expected_authority_epoch"`
	ReporterRegistrationID int64  `json:"reporter_registration_id"`
	ExpiresAt              string `json:"expires_at"`
}

type CredentialEpochRequest struct {
	ExpectedCredentialEpoch int64 `json:"expected_credential_epoch"`
}

type RevokeHandoffRequest struct {
	ExpectedCredentialEpoch int64 `json:"expected_credential_epoch"`
}

// HandoffMetadata is the safe internal projection. The one-time secret is
// never a DTO field or response header. Mint and rotate return exactly 32 raw
// bytes under SecretMediaTypeV1 and are never response-cached.
type HandoffMetadata struct {
	HandoffID                  string         `json:"handoff_id"`
	DeliveryKey                string         `json:"delivery_key"`
	IssueKey                   string         `json:"issue_key"`
	AttemptNumber              int64          `json:"attempt_number"`
	PlanRevision               int64          `json:"plan_revision"`
	PlanDigest                 string         `json:"plan_digest"`
	StageKey                   string         `json:"stage_key"`
	ExecutionNumber            int64          `json:"execution_number"`
	ExecutionStartStageEventID int64          `json:"execution_start_stage_event_id"`
	PredecessorDigest          string         `json:"predecessor_digest"`
	AuthorityEpoch             int64          `json:"authority_epoch"`
	AuthorityStageEventID      int64          `json:"authority_stage_event_id"`
	ReporterRegistrationID     int64          `json:"reporter_registration_id"`
	ReporterClass              ReporterClass  `json:"reporter_class"`
	ReporterRole               ReporterRole   `json:"reporter_role"`
	DependencyKey              string         `json:"dependency_key,omitempty"`
	EvidenceCeiling            []EvidenceKind `json:"evidence_ceiling"`
	ContractMajor              int            `json:"contract_major"`
	FixtureDigest              string         `json:"fixture_digest"`
	CredentialEpoch            int64          `json:"credential_epoch"`
	ExpiresAt                  string         `json:"expires_at"`
	ContextDigest              string         `json:"context_digest"`
	State                      HandoffState   `json:"state"`
	CreatedAt                  string         `json:"created_at"`
	RevokedAt                  string         `json:"revoked_at,omitempty"`
}

// PullResponse is the value-free external projection. Internal row IDs and
// reporter registration IDs do not cross this boundary.
type PullResponse struct {
	HandoffID         string         `json:"handoff_id"`
	ContractMajor     int            `json:"contract_major"`
	FixtureDigest     string         `json:"fixture_digest"`
	CredentialEpoch   int64          `json:"credential_epoch"`
	ExpiresAt         string         `json:"expires_at"`
	State             HandoffState   `json:"state"`
	ReporterClass     ReporterClass  `json:"reporter_class"`
	ReporterRole      ReporterRole   `json:"reporter_role"`
	DependencyKey     string         `json:"dependency_key,omitempty"`
	EvidenceCeiling   []EvidenceKind `json:"evidence_ceiling"`
	StageKey          string         `json:"stage_key"`
	ExecutionNumber   int64          `json:"execution_number"`
	PlanDigest        string         `json:"plan_digest"`
	PredecessorDigest string         `json:"predecessor_digest"`
	AuthorityEpoch    int64          `json:"authority_epoch"`
	ContextDigest     string         `json:"context_digest"`
}

type AcceptRequest struct {
	Sequence   int64  `json:"sequence"`
	ObservedAt string `json:"observed_at"`
}

type ArtifactEvidence struct {
	Version      string `json:"version"`
	Digest       string `json:"digest"`
	CommitDigest string `json:"commit_digest"`
}

// PharosEvidence contains only symbolic server-configured workflow and
// environment names plus exact artifact identity and closed outcome facts.
type PharosEvidence struct {
	Kind        EvidenceKind     `json:"kind"`
	Workflow    string           `json:"workflow"`
	Environment string           `json:"environment"`
	Artifact    ArtifactEvidence `json:"artifact"`
	Result      EvidenceResult   `json:"result"`
	ObservedAt  string           `json:"observed_at"`
}

// JanusEvidence is intentionally value-free: no IDs, free text, paths, URLs,
// digests, versions, ciphertext, commands, or callback material have a field.
type JanusEvidence struct {
	Kind            EvidenceKind   `json:"kind"`
	Result          EvidenceResult `json:"result"`
	Authorized      *bool          `json:"authorized,omitempty"`
	CredentialReady *bool          `json:"credential_ready,omitempty"`
	ObservedAt      string         `json:"observed_at"`
}

type ReportRequest struct {
	Sequence       int64           `json:"sequence"`
	State          HandoffState    `json:"state"`
	ObservedAt     string          `json:"observed_at"`
	Heartbeat      bool            `json:"heartbeat"`
	BlockerCodes   []BlockerCode   `json:"blocker_codes,omitempty"`
	PharosEvidence *PharosEvidence `json:"pharos_evidence,omitempty"`
	JanusEvidence  *JanusEvidence  `json:"janus_evidence,omitempty"`
}

type ReportReceipt struct {
	HandoffID        string       `json:"handoff_id"`
	Sequence         int64        `json:"sequence"`
	State            HandoffState `json:"state"`
	CredentialEpoch  int64        `json:"credential_epoch"`
	Duplicate        bool         `json:"duplicate"`
	ServerReceivedAt string       `json:"server_received_at"`
}
