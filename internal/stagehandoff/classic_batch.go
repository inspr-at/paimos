// SPDX-License-Identifier: AGPL-3.0-only

package stagehandoff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

var (
	classicCommitRE     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	classicCoordinateRE = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}:[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,189}$`)
	classicVersionRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	classicIdempotency  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{7,79}$`)
)

type classicBatchAliasWrite struct {
	ClassicBatchID               int64           `json:"classic_batch_id"`
	ClassicProjectID             int64           `json:"classic_project_id"`
	ClassicBatch                 json.RawMessage `json:"classic_batch"`
	ImplementationExecution      int64           `json:"implementation_execution"`
	ImplementationAuthorityEpoch int64           `json:"implementation_authority_epoch"`
	AccountKey                   string          `json:"account_key"`
	RuntimeGeneration            string          `json:"runtime_generation"`
}

type classicBuiltReceipt struct {
	IdempotencyKey                       string `json:"idempotency_key"`
	ExpectedAttemptID                    int64  `json:"expected_attempt_id"`
	ExpectedPlanRevision                 int64  `json:"expected_plan_revision"`
	ExpectedImplementationExecution      int64  `json:"expected_implementation_execution"`
	ExpectedImplementationAuthorityEpoch int64  `json:"expected_implementation_authority_epoch"`
	ExpectedAccountKey                   string `json:"expected_account_key,omitempty"`
	ExpectedRuntimeGeneration            string `json:"expected_runtime_generation,omitempty"`
	Commit                               string `json:"commit"`
	OCIConfigDigest                      string `json:"oci_config_digest"`
	ReleaseManifestDigest                string `json:"release_manifest_digest"`
	ReleaseManifestCoordinate            string `json:"release_manifest_coordinate"`
	OCIIndexDigest                       string `json:"oci_index_digest,omitempty"`
	VersionScheme                        string `json:"version_scheme"`
	ReleaseChannel                       string `json:"release_channel"`
	ReleaseSequence                      int64  `json:"release_sequence"`
	Version                              string `json:"version"`
	QADigest                             string `json:"qa_digest"`
}

func classicDigest(raw string) (string, bool) {
	v := strings.TrimPrefix(strings.TrimSpace(raw), "sha256:")
	return v, hexRE.MatchString(v)
}

func validateClassicBuilt(in classicBuiltReceipt) error {
	if !classicIdempotency.MatchString(in.IdempotencyKey) || in.ExpectedAttemptID < 1 || in.ExpectedPlanRevision < 1 ||
		in.ExpectedImplementationExecution < 0 || in.ExpectedImplementationAuthorityEpoch < 0 ||
		(in.ExpectedImplementationExecution == 0) != (in.ExpectedImplementationAuthorityEpoch == 0) ||
		!classicCommitRE.MatchString(in.Commit) || !classicCoordinateRE.MatchString(in.ReleaseManifestCoordinate) ||
		!slicesContains([]string{"legacy", "inspr-calendar-v1", "inspr-calendar-v2"}, in.VersionScheme) ||
		!symbolicRE.MatchString(in.ReleaseChannel) || in.ReleaseSequence < 0 || !classicVersionRE.MatchString(in.Version) {
		return fail(400, "invalid built receipt")
	}
	for _, value := range []string{in.OCIConfigDigest, in.ReleaseManifestDigest, in.QADigest} {
		if _, ok := classicDigest(value); !ok {
			return fail(400, "invalid built receipt digest")
		}
	}
	if in.OCIIndexDigest != "" {
		if _, ok := classicDigest(in.OCIIndexDigest); !ok {
			return fail(400, "invalid built receipt digest")
		}
	}
	return nil
}

func slicesContains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func (m *Module) bindClassicBatchAlias(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("handoffId")
	if !uuidRE.MatchString(id) {
		respond(w, 0, nil, fail(404, "handoff not found"))
		return
	}
	var in classicBatchAliasWrite
	if err := decode(w, r, &in); err != nil {
		respond(w, 0, nil, err)
		return
	}
	var snapshot map[string]any
	if in.ClassicBatchID < 1 || in.ClassicProjectID < 1 || in.ImplementationExecution < 0 ||
		in.ImplementationAuthorityEpoch < 0 || (in.ImplementationExecution == 0) != (in.ImplementationAuthorityEpoch == 0) ||
		json.Unmarshal(in.ClassicBatch, &snapshot) != nil || snapshot == nil ||
		snapshot["id"] != float64(in.ClassicBatchID) || snapshot["project_id"] != float64(in.ClassicProjectID) {
		respond(w, 0, nil, fail(400, "invalid classic batch provenance"))
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		h, err := loadHandoff(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if h.Operation != "deploy" {
			return fail(409, "batch alias requires deploy handoff")
		}
		if err := handoffOwner(r.Context(), tx, p, h.ID); err != nil {
			return err
		}
		open, err := authorityOpen(r.Context(), tx, h)
		if err != nil {
			return err
		}
		if !open {
			return fail(409, "handoff is stale")
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO stage_handoff_classic_batch_aliases
		 (tenant_id,project_node_id,classic_batch_id,classic_project_id,handoff_id,classic_batch,implementation_execution,implementation_authority_epoch,account_key,runtime_generation)
		 VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid,$6::jsonb,$7,$8,$9,$10)`, p.TenantID, h.ProjectNodeID, in.ClassicBatchID, in.ClassicProjectID, h.ID, in.ClassicBatch, in.ImplementationExecution, in.ImplementationAuthorityEpoch, in.AccountKey, in.RuntimeGeneration)
		if err != nil {
			return fail(409, "classic batch alias already bound")
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{Type: "stage_handoff.classic_batch_aliased", NodeID: &h.ReleaseNodeID, After: map[string]any{"handoff_id": h.ID, "classic_batch_id": in.ClassicBatchID}})
		return err
	})
	respond(w, 201, map[string]any{"handoff_id": id, "classic_batch_id": in.ClassicBatchID}, err)
}

func handoffOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) error {
	var requester string
	if err := tx.QueryRow(ctx, `SELECT requested_by_principal_id::text FROM stage_handoffs WHERE id=$1::uuid`, id).Scan(&requester); err != nil {
		return err
	}
	if requester == p.ID || authz.RequireTx(ctx, tx, p, "stage_handoffs.decide", authz.Scope{}) == nil {
		return nil
	}
	return fail(403, "handoff owner required")
}

func (m *Module) reportClassicBuilt(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	projectID := r.PathValue("projectId")
	batchID, err := strconv.ParseInt(r.PathValue("batchId"), 10, 64)
	if !uuidRE.MatchString(projectID) || err != nil || batchID < 1 {
		respond(w, 0, nil, fail(404, "batch alias not found"))
		return
	}
	var in classicBuiltReceipt
	if err := decode(w, r, &in); err != nil {
		respond(w, 0, nil, err)
		return
	}
	if err := validateClassicBuilt(in); err != nil {
		respond(w, 0, nil, err)
		return
	}
	var out map[string]any
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var alias classicBatchAliasWrite
		var handoffID string
		err := tx.QueryRow(r.Context(), `SELECT handoff_id::text,classic_batch,implementation_execution,implementation_authority_epoch,account_key,runtime_generation
		 FROM stage_handoff_classic_batch_aliases WHERE project_node_id=$1::uuid AND classic_batch_id=$2`, projectID, batchID).
			Scan(&handoffID, &alias.ClassicBatch, &alias.ImplementationExecution, &alias.ImplementationAuthorityEpoch, &alias.AccountKey, &alias.RuntimeGeneration)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "batch alias not found")
		}
		if err != nil {
			return err
		}
		h, err := loadHandoff(r.Context(), tx, handoffID, true)
		if err != nil {
			return err
		}
		if err := handoffOwner(r.Context(), tx, p, h.ID); err != nil {
			return err
		}
		if h.Operation != "deploy" || int64(h.Attempt) != in.ExpectedAttemptID || h.JourneyRevision != in.ExpectedPlanRevision ||
			alias.ImplementationExecution != in.ExpectedImplementationExecution || alias.ImplementationAuthorityEpoch != in.ExpectedImplementationAuthorityEpoch ||
			alias.AccountKey != in.ExpectedAccountKey || alias.RuntimeGeneration != in.ExpectedRuntimeGeneration {
			return fail(409, "stale batch identity")
		}
		var prior json.RawMessage
		err = tx.QueryRow(r.Context(), `SELECT receipt FROM stage_handoff_build_evidence WHERE handoff_id=$1::uuid`, h.ID).Scan(&prior)
		if err == nil {
			var previous classicBuiltReceipt
			if json.Unmarshal(prior, &previous) != nil || !reflect.DeepEqual(previous, in) {
				return fail(409, "divergent built receipt replay")
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		} else {
			if h.Result != nil || h.State == "revoked" {
				return fail(409, "handoff is terminal")
			}
			valid, err := current(r.Context(), tx, h)
			if err != nil {
				return err
			}
			if !valid {
				return fail(409, "handoff is stale")
			}
			var scheme, version *string
			var sequence int64
			if err := tx.QueryRow(r.Context(), `SELECT version_scheme,version,number FROM journey_releases WHERE release_node_id=$1::uuid`, h.ReleaseNodeID).Scan(&scheme, &version, &sequence); err != nil {
				return err
			}
			if scheme != nil && (*scheme != in.VersionScheme || version == nil || *version != in.Version) || sequence != in.ReleaseSequence {
				return fail(409, "built artifact does not match release version")
			}
			configDigest, _ := classicDigest(in.OCIConfigDigest)
			manifestDigest, _ := classicDigest(in.ReleaseManifestDigest)
			if err := pinReleaseArtifact(r.Context(), tx, h.ReleaseNodeID, Artifact{
				VersionScheme: in.VersionScheme, Version: in.Version, ReleaseChannel: in.ReleaseChannel,
				ReleaseSequence: in.ReleaseSequence, DigestSHA256: configDigest, CommitDigest: in.Commit,
				ManifestCoordinate: in.ReleaseManifestCoordinate, ManifestDigestSHA256: manifestDigest,
			}); err != nil {
				return err
			}
			raw, _ := json.Marshal(in)
			config, _ := classicDigest(in.OCIConfigDigest)
			manifest, _ := classicDigest(in.ReleaseManifestDigest)
			index := ""
			if in.OCIIndexDigest != "" {
				index, _ = classicDigest(in.OCIIndexDigest)
			}
			qa, _ := classicDigest(in.QADigest)
			_, err = tx.Exec(r.Context(), `INSERT INTO stage_handoff_build_evidence
			 (tenant_id,handoff_id,idempotency_key,receipt,commit_digest,oci_config_digest,release_manifest_digest,release_manifest_coordinate,oci_index_digest,version_scheme,release_channel,release_sequence,version,qa_digest)
			 VALUES($1::uuid,$2::uuid,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, p.TenantID, h.ID, in.IdempotencyKey, raw, in.Commit, config, manifest, in.ReleaseManifestCoordinate, index, in.VersionScheme, in.ReleaseChannel, in.ReleaseSequence, in.Version, qa)
			if err != nil {
				return err
			}
			_, err = events.Append(r.Context(), tx, p, events.Change{Type: "stage_handoff.built_reported", NodeID: &h.ReleaseNodeID, After: map[string]any{"handoff_id": h.ID, "attempt": h.Attempt, "qa_digest": qa}})
			if err != nil {
				return err
			}
		}
		if err := json.Unmarshal(alias.ClassicBatch, &out); err != nil {
			return err
		}
		progress, _ := out["progress"].(map[string]any)
		if progress == nil {
			progress = map[string]any{}
		}
		progress["next_action"] = "operator_external_stage_cli"
		out["progress"] = progress
		return nil
	})
	respond(w, 200, out, err)
}
