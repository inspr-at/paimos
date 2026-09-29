// SPDX-License-Identifier: AGPL-3.0-only

package stagehandoff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
)

const candidateReceiptSchema = "aeon.candidate-artifact.v1"

type candidateArtifactWrite struct {
	HandoffID               string   `json:"handoff_id"`
	ExpectedAttempt         int      `json:"expected_attempt"`
	ExpectedAuthorityEpoch  int64    `json:"expected_authority_epoch"`
	ExpectedJourneyRevision int64    `json:"expected_journey_revision"`
	IdempotencyKey          string   `json:"idempotency_key"`
	Artifact                Artifact `json:"artifact"`
	QADigest                string   `json:"qa_digest"`
}

type candidateReceipt struct {
	Schema       string                 `json:"schema"`
	RegisteredBy string                 `json:"registered_by_principal_id"`
	Request      candidateArtifactWrite `json:"request"`
}

type candidateArtifactView struct {
	ProjectNodeID   string                 `json:"project_node_id"`
	ReleaseNodeID   string                 `json:"release_node_id"`
	VersionScheme   *string                `json:"version_scheme"`
	Version         *string                `json:"version"`
	ReleaseSequence int64                  `json:"release_sequence"`
	Registration    *candidateRegistration `json:"registration"`
}

type candidateRegistration struct {
	HandoffID       string    `json:"handoff_id"`
	Attempt         int       `json:"attempt"`
	AuthorityEpoch  int64     `json:"authority_epoch"`
	JourneyRevision int64     `json:"journey_revision"`
	Artifact        Artifact  `json:"artifact"`
	QADigest        string    `json:"qa_digest"`
	RecordedAt      time.Time `json:"recorded_at"`
}

var candidateCalendarV1 = regexp.MustCompile(`^[1-9][0-9]\.[0-9]{2}\.[0-9]{2}(?:\.[0-9]{2}\.[0-9]{2}\.[0-9]{2})?$`)

func validCandidateVersion(scheme, version string) bool {
	switch scheme {
	case "legacy":
		return classicVersionRE.MatchString(version)
	case releasehistory.SchemeCalVer3, releasehistory.SchemeCalVer2:
		return releasehistory.ValidVersion(version)
	case "inspr-calendar-v1":
		if !candidateCalendarV1.MatchString(version) {
			return false
		}
		layout := "2006.01.02"
		if len(version) == 17 {
			layout += ".15.04.05"
		}
		_, err := time.Parse(layout, "20"+version)
		return err == nil
	}
	return false
}

func validateCandidateArtifact(in candidateArtifactWrite) error {
	a := in.Artifact
	coordinate, coordinateErr := url.Parse(a.ManifestCoordinate)
	if !uuidRE.MatchString(in.HandoffID) || in.ExpectedAttempt < 1 || in.ExpectedAuthorityEpoch < 1 || in.ExpectedJourneyRevision < 1 || !classicIdempotency.MatchString(in.IdempotencyKey) ||
		!validCandidateVersion(a.VersionScheme, a.Version) || a.ReleaseSequence < 1 || !symbolicRE.MatchString(a.ReleaseChannel) ||
		!hexRE.MatchString(a.DigestSHA256) || !hexRE.MatchString(a.ManifestDigestSHA256) || !hexRE.MatchString(in.QADigest) ||
		!classicCommitRE.MatchString(a.CommitDigest) || !classicCoordinateRE.MatchString(a.ManifestCoordinate) || coordinateErr != nil || coordinate.User != nil {
		return fail(400, "invalid candidate artifact")
	}
	return nil
}

func candidateAccess(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, permission string) error {
	var kind, status string
	err := tx.QueryRow(ctx, `SELECT kind,status FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid FOR SHARE`, p.TenantID, p.ID).Scan(&kind, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(403, "active principal required")
	}
	if err != nil {
		return err
	}
	if status != "active" || kind != string(p.Kind) || (p.Kind != tenant.Person && p.Kind != tenant.Agent) {
		return fail(403, "active principal required")
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: projectID}); err != nil {
		return fail(403, "candidate artifact permission required")
	}
	return nil
}

func candidateOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal, h Handoff) error {
	var requester string
	if err := tx.QueryRow(ctx, `SELECT requested_by_principal_id::text FROM stage_handoffs WHERE id=$1::uuid`, h.ID).Scan(&requester); err != nil {
		return err
	}
	if requester == p.ID {
		return nil
	}
	if authz.RequireTx(ctx, tx, p, "stage_handoffs.decide", authz.Scope{ProjectID: h.ProjectNodeID}) == nil {
		return nil
	}
	return fail(403, "handoff owner required")
}

func (m *Module) getCandidateArtifact(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project, release := r.PathValue("projectId"), r.PathValue("releaseId")
	if !uuidRE.MatchString(project) || !uuidRE.MatchString(release) {
		respond(w, 0, nil, fail(404, "release not found"))
		return
	}
	var out candidateArtifactView
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := candidateAccess(r.Context(), tx, p, project, "stage_handoffs.read"); err != nil {
			return err
		}
		var err error
		out, err = readCandidateArtifact(r.Context(), tx, project, release, "")
		return err
	})
	respond(w, 200, out, err)
}

func (m *Module) putCandidateArtifact(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project, release := r.PathValue("projectId"), r.PathValue("releaseId")
	if !uuidRE.MatchString(project) || !uuidRE.MatchString(release) {
		respond(w, 0, nil, fail(404, "release not found"))
		return
	}
	var in candidateArtifactWrite
	if err := decode(w, r, &in); err != nil {
		respond(w, 0, nil, err)
		return
	}
	if err := validateCandidateArtifact(in); err != nil {
		respond(w, 0, nil, err)
		return
	}
	var out candidateArtifactView
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := candidateAccess(ctx, tx, p, project, "stage_handoffs.write"); err != nil {
			return err
		}
		// Match launch/consume's lock order: handoff, then current()'s project/release.
		h, err := loadHandoff(ctx, tx, in.HandoffID, true)
		if err != nil {
			return err
		}
		if h.ProjectNodeID != project || h.ReleaseNodeID != release {
			return fail(404, "handoff does not belong to release")
		}
		if err := candidateOwner(ctx, tx, p, h); err != nil {
			return err
		}
		if h.Stage != "deploy" || h.Operation != "deploy" || h.PluginID != "pharos" || h.Attempt != in.ExpectedAttempt || h.AuthorityEpoch != in.ExpectedAuthorityEpoch || h.JourneyRevision != in.ExpectedJourneyRevision {
			return fail(409, "candidate handoff identity changed")
		}
		var prior json.RawMessage
		err = tx.QueryRow(ctx, `SELECT receipt FROM stage_handoff_build_evidence WHERE handoff_id=$1::uuid`, h.ID).Scan(&prior)
		if err == nil {
			var saved candidateReceipt
			if json.Unmarshal(prior, &saved) != nil || saved.Schema != candidateReceiptSchema || saved.RegisteredBy != p.ID || saved.Request != in {
				return fail(409, "divergent candidate artifact replay")
			}
			// Historical replay grants no new authority, and performs no writes.
			out, err = readCandidateArtifact(ctx, tx, project, release, h.ID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		open, err := authorityOpen(ctx, tx, h)
		if err != nil {
			return err
		}
		if !open {
			return fail(409, "handoff is stale or terminal")
		}
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE release_node_id=$1::uuid`, release).Scan(&state); err != nil {
			return err
		}
		if state != "candidate" && state != "deploying" {
			return fail(409, "release is not a deployment candidate")
		}
		for _, gate := range []string{"candidate", "deploy"} {
			live, err := gateLive(ctx, tx, release, gate)
			if err != nil {
				return err
			}
			if !live {
				return fail(403, "stage gate is not approved")
			}
		}
		if err := pinReleaseArtifact(ctx, tx, release, in.Artifact); err != nil {
			return err
		}
		raw, err := json.Marshal(candidateReceipt{Schema: candidateReceiptSchema, RegisteredBy: p.ID, Request: in})
		if err != nil {
			return err
		}
		a := in.Artifact
		_, err = tx.Exec(ctx, `INSERT INTO stage_handoff_build_evidence(tenant_id,handoff_id,idempotency_key,receipt,commit_digest,oci_config_digest,release_manifest_digest,release_manifest_coordinate,version_scheme,release_channel,release_sequence,version,qa_digest)
   VALUES($1::uuid,$2::uuid,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, p.TenantID, h.ID, in.IdempotencyKey, raw, a.CommitDigest, a.DigestSHA256, a.ManifestDigestSHA256, a.ManifestCoordinate, a.VersionScheme, a.ReleaseChannel, a.ReleaseSequence, a.Version, in.QADigest)
		if err != nil {
			return err
		}
		out, err = readCandidateArtifact(ctx, tx, project, release, h.ID)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "stage_handoff.candidate_artifact_registered", NodeID: &h.ReleaseNodeID, After: out})
		return err
	})
	respond(w, 200, out, err)
}

// The release row serializes native and compatibility receipts across attempts.
// Historical rows stay immutable. The entire identity, not just the version or
// image digest, is pinned. This does not bump either revision or stale a handoff.
func pinReleaseArtifact(ctx context.Context, tx pgx.Tx, release string, a Artifact) error {
	var scheme, version *string
	var number int64
	if err := tx.QueryRow(ctx, `SELECT version_scheme,version,number FROM journey_releases WHERE release_node_id=$1::uuid FOR UPDATE`, release).Scan(&scheme, &version, &number); err != nil {
		return err
	}
	if (scheme == nil) != (version == nil) || number != a.ReleaseSequence || scheme != nil && (*scheme != a.VersionScheme || *version != a.Version) {
		return fail(409, "artifact does not match release version")
	}
	rows, err := tx.Query(ctx, `SELECT b.version_scheme,b.version,b.release_channel,b.release_sequence,b.oci_config_digest,b.commit_digest,b.release_manifest_coordinate,b.release_manifest_digest
  FROM stage_handoff_build_evidence b JOIN stage_handoffs h ON h.tenant_id=b.tenant_id AND h.id=b.handoff_id WHERE h.release_node_id=$1::uuid`, release)
	if err != nil {
		return err
	}
	for rows.Next() {
		var previous Artifact
		if err := rows.Scan(&previous.VersionScheme, &previous.Version, &previous.ReleaseChannel, &previous.ReleaseSequence, &previous.DigestSHA256, &previous.CommitDigest, &previous.ManifestCoordinate, &previous.ManifestDigestSHA256); err != nil {
			rows.Close()
			return err
		}
		if previous != a {
			rows.Close()
			return fail(409, "release candidate artifact is immutable")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if scheme == nil {
		_, err = tx.Exec(ctx, `UPDATE journey_releases SET version_scheme=$2,version=$3 WHERE release_node_id=$1::uuid AND version_scheme IS NULL AND version IS NULL`, release, a.VersionScheme, a.Version)
	}
	return err
}

func readCandidateArtifact(ctx context.Context, tx pgx.Tx, project, release, handoff string) (candidateArtifactView, error) {
	out := candidateArtifactView{ProjectNodeID: project, ReleaseNodeID: release}
	err := tx.QueryRow(ctx, `SELECT version_scheme,version,number FROM journey_releases WHERE project_node_id=$1::uuid AND release_node_id=$2::uuid FOR SHARE`, project, release).Scan(&out.VersionScheme, &out.Version, &out.ReleaseSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fail(404, "release not found")
	}
	if err != nil {
		return out, err
	}
	var rec candidateRegistration
	a := &rec.Artifact
	err = tx.QueryRow(ctx, `SELECT h.id::text,h.attempt,h.authority_epoch,h.journey_revision,b.version_scheme,b.version,b.release_channel,b.release_sequence,b.oci_config_digest,b.commit_digest,b.release_manifest_coordinate,b.release_manifest_digest,b.qa_digest,b.observed_at
  FROM stage_handoff_build_evidence b JOIN stage_handoffs h ON h.tenant_id=b.tenant_id AND h.id=b.handoff_id
  WHERE h.project_node_id=$1::uuid AND h.release_node_id=$2::uuid AND ($3='' OR h.id::text=$3)
  ORDER BY h.attempt DESC,b.observed_at DESC LIMIT 1`, project, release, handoff).Scan(&rec.HandoffID, &rec.Attempt, &rec.AuthorityEpoch, &rec.JourneyRevision, &a.VersionScheme, &a.Version, &a.ReleaseChannel, &a.ReleaseSequence, &a.DigestSHA256, &a.CommitDigest, &a.ManifestCoordinate, &a.ManifestDigestSHA256, &rec.QADigest, &rec.RecordedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Registration = &rec
	return out, nil
}
