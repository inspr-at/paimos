// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

const (
	LaunchAdmissionMediaType = "application/vnd.paimos.external-stage-launch-admission.v1+json"
	LaunchAdmissionSchema    = "paimos.external-stage-launch-admission"
	LaunchAdmissionVersion   = 1
	LaunchCandidatePath      = "/api/external-stage/handoffs/{handoffID}/launch-candidates"
	LaunchConsumePath        = "/api/external-stage/handoffs/{handoffID}/launch-admissions/{admissionID}/consume"
)

var LaunchRoutes = []Route{
	{OperationID: "admitExternalStageLaunch", Method: "POST", Path: LaunchCandidatePath, Audience: "external"},
	{OperationID: "consumeExternalStageLaunch", Method: "POST", Path: LaunchConsumePath, Audience: "external"},
}

type LaunchCandidate struct {
	Schema                 string             `json:"schema"`
	Version                int                `json:"version"`
	TargetRef              string             `json:"target_ref"`
	Workflow               string             `json:"workflow"`
	Environment            string             `json:"environment"`
	Artifact               ArtifactEvidenceV2 `json:"artifact"`
	ReviewedPlanDigest     string             `json:"reviewed_plan_digest"`
	OperationBindingDigest string             `json:"operation_binding_digest"`
	ObservedAt             string             `json:"observed_at"`
}

type LaunchAdmission struct {
	Schema                 string             `json:"schema"`
	Version                int                `json:"version"`
	GrantID                string             `json:"grant_id"`
	GrantRevision          int                `json:"grant_revision"`
	GrantDigest            string             `json:"grant_digest"`
	AdmissionID            string             `json:"admission_id"`
	AdmissionDigest        string             `json:"admission_digest"`
	HandoffID              string             `json:"handoff_id"`
	CredentialEpoch        int64              `json:"credential_epoch"`
	TargetRef              string             `json:"target_ref"`
	Workflow               string             `json:"workflow"`
	Environment            string             `json:"environment"`
	Artifact               ArtifactEvidenceV2 `json:"artifact"`
	Stage                  string             `json:"stage"`
	Attempt                int64              `json:"attempt"`
	Plan                   int64              `json:"plan"`
	Execution              int64              `json:"execution"`
	Authority              int64              `json:"authority"`
	PlanDigest             string             `json:"plan_digest"`
	PredecessorDigest      string             `json:"predecessor_digest"`
	ContextDigest          string             `json:"context_digest"`
	ReviewedPlanDigest     string             `json:"reviewed_plan_digest"`
	OperationBindingDigest string             `json:"operation_binding_digest"`
	MaxLaunches            int                `json:"max_launches"`
	UsedLaunches           int                `json:"used_launches"`
	IssuedAt               string             `json:"issued_at"`
	ExpiresAt              string             `json:"expires_at"`
	State                  string             `json:"state"`
}

type ConsumeLaunchAdmissionRequest struct {
	Schema          string `json:"schema"`
	Version         int    `json:"version"`
	AdmissionDigest string `json:"admission_digest"`
}

type LaunchReceipt struct {
	Schema          string `json:"schema"`
	Version         int    `json:"version"`
	AdmissionID     string `json:"admission_id"`
	AdmissionDigest string `json:"admission_digest"`
	HandoffID       string `json:"handoff_id"`
	CredentialEpoch int64  `json:"credential_epoch"`
	LaunchNumber    int    `json:"launch_number"`
	State           string `json:"state"`
	ConsumedAt      string `json:"consumed_at"`
}
