// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package externalstage

// External-stage v2 is additive. The frozen v1 declarations remain in
// contract.go so release checks can continue comparing their exact bytes with
// the certified v1 commit.
const (
	ContractMajorV2 = 2
	MediaTypeV2     = "application/vnd.paimos.external-stage.v2+json"
)

type VersionScheme string

const (
	VersionSchemeLegacy        VersionScheme = "legacy"
	VersionSchemeINSPRCalendar VersionScheme = "inspr-calendar-v1"
)

var VersionSchemesV2 = []string{
	string(VersionSchemeLegacy),
	string(VersionSchemeINSPRCalendar),
}

// ArtifactEvidenceV2 keeps the original v1 version and digest spellings while
// binding them to an explicit release scheme, channel sequence, and immutable
// release-set manifest. Consumers must never infer a scheme from Version.
type ArtifactEvidenceV2 struct {
	VersionScheme             VersionScheme `json:"version_scheme"`
	Version                   string        `json:"version"`
	ReleaseChannel            string        `json:"release_channel"`
	ReleaseSequence           int64         `json:"release_sequence"`
	Digest                    string        `json:"digest"`
	CommitDigest              string        `json:"commit_digest"`
	ReleaseManifestCoordinate string        `json:"release_manifest_coordinate"`
	ReleaseManifestDigest     string        `json:"release_manifest_digest"`
}

type PharosEvidenceV2 struct {
	Kind        EvidenceKind       `json:"kind"`
	Workflow    string             `json:"workflow"`
	Environment string             `json:"environment"`
	Artifact    ArtifactEvidenceV2 `json:"artifact"`
	Result      EvidenceResult     `json:"result"`
	ObservedAt  string             `json:"observed_at"`
}

// ReportRequestV2 is a distinct wire DTO. Janus evidence is unchanged and
// remains value-free; only Pharos artifact evidence gains the v2 identity.
type ReportRequestV2 struct {
	Sequence       int64             `json:"sequence"`
	State          HandoffState      `json:"state"`
	ObservedAt     string            `json:"observed_at"`
	Heartbeat      bool              `json:"heartbeat"`
	BlockerCodes   []BlockerCode     `json:"blocker_codes,omitempty"`
	PharosEvidence *PharosEvidenceV2 `json:"pharos_evidence,omitempty"`
	JanusEvidence  *JanusEvidence    `json:"janus_evidence,omitempty"`
}

// PullResponseV2 advertises the v2 fixture pin while preserving the same
// authority, credential, expiry, and lineage projection as v1.
type PullResponseV2 struct {
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

func NewPullResponseV2(v1 PullResponse, fixtureDigest string) PullResponseV2 {
	return PullResponseV2{
		HandoffID: v1.HandoffID, ContractMajor: ContractMajorV2, FixtureDigest: fixtureDigest,
		CredentialEpoch: v1.CredentialEpoch, ExpiresAt: v1.ExpiresAt, State: v1.State,
		ReporterClass: v1.ReporterClass, ReporterRole: v1.ReporterRole, DependencyKey: v1.DependencyKey,
		EvidenceCeiling: append([]EvidenceKind(nil), v1.EvidenceCeiling...), StageKey: v1.StageKey,
		ExecutionNumber: v1.ExecutionNumber, PlanDigest: v1.PlanDigest,
		PredecessorDigest: v1.PredecessorDigest, AuthorityEpoch: v1.AuthorityEpoch,
		ContextDigest: v1.ContextDigest,
	}
}

func (request ReportRequestV2) v1() ReportRequest {
	converted := ReportRequest{
		Sequence: request.Sequence, State: request.State, ObservedAt: request.ObservedAt,
		Heartbeat: request.Heartbeat, BlockerCodes: append([]BlockerCode(nil), request.BlockerCodes...),
		JanusEvidence: request.JanusEvidence,
	}
	if request.PharosEvidence != nil {
		evidence := request.PharosEvidence
		converted.PharosEvidence = &PharosEvidence{
			Kind: evidence.Kind, Workflow: evidence.Workflow, Environment: evidence.Environment,
			Artifact: ArtifactEvidence{Version: evidence.Artifact.Version, Digest: evidence.Artifact.Digest,
				CommitDigest: evidence.Artifact.CommitDigest},
			Result: evidence.Result, ObservedAt: evidence.ObservedAt,
		}
	}
	return converted
}
