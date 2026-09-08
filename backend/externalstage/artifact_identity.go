// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package externalstage

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/backend/delivery"
)

const (
	ociIndexRefPrefix          = "oci-manifest:sha256:"
	releaseManifestRefPrefix   = "release-manifest:sha256:"
	releaseCoordinateRefPrefix = "release-coordinate:"
	releaseIdentityPrefix      = "inspr-release-v1:"
)

// BuiltOwnerArtifact is the server-owned expected identity for a baseline
// batch. Digest is the running OCI image config. ReleaseManifest is the
// immutable release-set document digest named by owner-v2
// release_manifest_digest. OCIIndex is an optional OCI image index or
// manifest and is never a release-set stand-in. Coordinate, scheme, channel,
// sequence, and version identify the release set. Commit is the source
// revision.
type BuiltOwnerArtifact struct {
	Digest          []byte
	ReleaseManifest []byte
	OCIIndex        []byte
	Commit          string
	Coordinate      string
	Scheme          string
	Channel         string
	Sequence        int64
	Version         string
}

func (a BuiltOwnerArtifact) Complete() bool {
	return len(a.Digest) == 32 && len(a.ReleaseManifest) == 32 && commitPattern.MatchString(a.Commit) &&
		releaseManifestCoordinatePattern.MatchString(a.Coordinate) &&
		(a.Scheme == string(VersionSchemeLegacy) || a.Scheme == string(VersionSchemeINSPRCalendar)) &&
		symbolPattern.MatchString(a.Channel) && a.Sequence >= 0 && versionPattern.MatchString(a.Version)
}

func FormatReleaseIdentity(scheme VersionScheme, channel string, sequence int64, version string) string {
	return fmt.Sprintf("%s%s/%s/%d/%s", releaseIdentityPrefix, scheme, channel, sequence, version)
}

func FormatOCIManifestRef(digestHex string) string {
	return ociIndexRefPrefix + strings.TrimPrefix(digestHex, "sha256:")
}

func FormatReleaseManifestRef(digestHex string) string {
	return releaseManifestRefPrefix + strings.TrimPrefix(digestHex, "sha256:")
}

func FormatReleaseCoordinateRef(coordinate string) string {
	return releaseCoordinateRefPrefix + coordinate
}

func ParseReleaseIdentity(value string) (scheme, channel, version string, sequence int64, ok bool) {
	if !strings.HasPrefix(value, releaseIdentityPrefix) {
		return "", "", "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(value, releaseIdentityPrefix), "/")
	if len(parts) != 4 {
		return "", "", "", 0, false
	}
	scheme, channel, version = parts[0], parts[1], parts[3]
	if scheme != string(VersionSchemeLegacy) && scheme != string(VersionSchemeINSPRCalendar) {
		return "", "", "", 0, false
	}
	if !symbolPattern.MatchString(channel) || !versionPattern.MatchString(version) {
		return "", "", "", 0, false
	}
	if scheme == string(VersionSchemeINSPRCalendar) && !validINSPRCalendarVersion(version) {
		return "", "", "", 0, false
	}
	sequence, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || sequence < 0 {
		return "", "", "", 0, false
	}
	return scheme, channel, version, sequence, true
}

func LoadExplicitBuiltArtifact(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, deliveryID, attemptID int64) (BuiltOwnerArtifact, error) {
	rows, err := q.QueryContext(ctx, `SELECT latest.stage_key,evidence.evidence_type,evidence.reference_kind,
		COALESCE(evidence.reference_value,''),COALESCE(evidence.digest_sha256,'')
		FROM delivery_stage_latest latest
		JOIN delivery_stage_events terminal ON terminal.id=latest.semantic_stage_event_id
		 AND terminal.semantic_state='succeeded'
		JOIN delivery_evidence evidence ON evidence.stage_event_id=latest.semantic_stage_event_id
		WHERE latest.delivery_id=? AND latest.attempt_id=? AND latest.stage_key IN ('implementation','qa')
		ORDER BY evidence.ordinal`, deliveryID, attemptID)
	if err != nil {
		return BuiltOwnerArtifact{}, err
	}
	defer rows.Close()
	var expected BuiltOwnerArtifact
	for rows.Next() {
		var stage, evidenceType, kind, value, digestHex string
		if err := rows.Scan(&stage, &evidenceType, &kind, &value, &digestHex); err != nil {
			return BuiltOwnerArtifact{}, err
		}
		if stage != delivery.StageImplementation {
			continue
		}
		if err := applyImplementationEvidence(&expected, evidenceType, kind, value, digestHex); err != nil {
			return BuiltOwnerArtifact{}, err
		}
	}
	return expected, rows.Err()
}

func applyImplementationEvidence(expected *BuiltOwnerArtifact, evidenceType, kind, value, digestHex string) error {
	switch {
	case evidenceType == "artifact" && kind == "digest" && digestHex != "":
		raw, err := hex.DecodeString(digestHex)
		if err != nil || len(raw) != 32 {
			return ErrInvalid
		}
		return assignDigest(&expected.Digest, raw)
	case evidenceType == "artifact" && kind == "external_ref" && strings.HasPrefix(value, releaseManifestRefPrefix):
		raw, err := decodePrefixedDigest(value, releaseManifestRefPrefix)
		if err != nil {
			return err
		}
		return assignDigest(&expected.ReleaseManifest, raw)
	case evidenceType == "artifact" && kind == "external_ref" && strings.HasPrefix(value, ociIndexRefPrefix):
		raw, err := decodePrefixedDigest(value, ociIndexRefPrefix)
		if err != nil {
			return err
		}
		return assignDigest(&expected.OCIIndex, raw)
	case evidenceType == "artifact" && kind == "external_ref" && strings.HasPrefix(value, releaseCoordinateRefPrefix):
		coordinate := strings.TrimPrefix(value, releaseCoordinateRefPrefix)
		if !releaseManifestCoordinatePattern.MatchString(coordinate) {
			return ErrInvalid
		}
		return assignString(&expected.Coordinate, coordinate)
	case evidenceType == "artifact" && kind == "external_ref" && strings.HasPrefix(value, releaseIdentityPrefix):
		scheme, channel, version, sequence, parsed := ParseReleaseIdentity(value)
		if !parsed {
			return ErrInvalid
		}
		if expected.Scheme != "" {
			if expected.Scheme != scheme || expected.Channel != channel || expected.Version != version || expected.Sequence != sequence {
				return ErrInvalid
			}
			return nil
		}
		expected.Scheme, expected.Channel, expected.Version, expected.Sequence = scheme, channel, version, sequence
		return nil
	case evidenceType == "artifact" && kind == "external_ref":
		return nil
	case evidenceType == "implementation_result" && kind == "commit":
		return assignString(&expected.Commit, value)
	}
	return nil
}

func decodePrefixedDigest(value, prefix string) ([]byte, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || len(raw) != 32 {
		return nil, ErrInvalid
	}
	return raw, nil
}

func assignDigest(dst *[]byte, raw []byte) error {
	if len(*dst) == 0 {
		*dst = raw
		return nil
	}
	if subtle.ConstantTimeCompare(*dst, raw) != 1 {
		return ErrInvalid
	}
	return nil
}

func assignString(dst *string, value string) error {
	if *dst == "" {
		*dst = value
		return nil
	}
	if *dst != value {
		return ErrInvalid
	}
	return nil
}

func baselineOwnedDeliveryTx(ctx context.Context, tx *sql.Tx, deliveryID int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM baseline_batch_batches WHERE delivery_id=?`, deliveryID).Scan(&n)
	return n > 0, err
}

func bindBuiltOwnerArtifactTx(ctx context.Context, tx *sql.Tx, h handoffRow, req ReportRequest, artifactV2 *ArtifactEvidenceV2) error {
	if h.role != string(ReporterRoleOwner) || req.PharosEvidence == nil || req.PharosEvidence.Result != EvidenceResultSucceeded {
		return nil
	}
	kind := req.PharosEvidence.Kind
	if kind != EvidenceKindDeployment && kind != EvidenceKindVerification {
		return nil
	}
	if err := assertSealedPrerequisitesMatchActiveJanusTx(ctx, tx, h); err != nil {
		return err
	}
	if kind != EvidenceKindDeployment {
		return nil
	}
	baseline, err := baselineOwnedDeliveryTx(ctx, tx, h.deliveryID)
	if err != nil {
		return err
	}
	expected, err := LoadExplicitBuiltArtifact(ctx, tx, h.deliveryID, h.attemptID)
	if err != nil {
		return err
	}
	e := req.PharosEvidence
	if e.Workflow != h.workflow || e.Environment != h.environment {
		if baseline || len(expected.Digest) > 0 || expected.Commit != "" {
			return ErrInvalid
		}
	}
	if baseline {
		if !expected.Complete() {
			return ErrInvalid
		}
		if artifactV2 == nil {
			return ErrV2Required
		}
		return matchBuiltOwnerArtifact(expected, e, artifactV2)
	}
	digest, err := decodeWireDigest(e.Artifact.Digest)
	if err != nil {
		return ErrInvalid
	}
	if len(expected.Digest) > 0 && subtle.ConstantTimeCompare(digest, expected.Digest) != 1 {
		return ErrInvalid
	}
	if expected.Commit != "" && e.Artifact.CommitDigest != expected.Commit {
		return ErrInvalid
	}
	return nil
}

func matchBuiltOwnerArtifact(expected BuiltOwnerArtifact, e *PharosEvidence, artifactV2 *ArtifactEvidenceV2) error {
	digest, err := decodeWireDigest(e.Artifact.Digest)
	if err != nil || subtle.ConstantTimeCompare(digest, expected.Digest) != 1 {
		return ErrInvalid
	}
	if e.Artifact.CommitDigest != expected.Commit {
		return ErrInvalid
	}
	manifest, err := decodeWireDigest(artifactV2.ReleaseManifestDigest)
	if err != nil || subtle.ConstantTimeCompare(manifest, expected.ReleaseManifest) != 1 {
		return ErrInvalid
	}
	if artifactV2.ReleaseManifestCoordinate != expected.Coordinate || artifactV2.Version != expected.Version ||
		artifactV2.ReleaseChannel != expected.Channel || artifactV2.ReleaseSequence != expected.Sequence ||
		string(artifactV2.VersionScheme) != expected.Scheme {
		return ErrInvalid
	}
	return nil
}
