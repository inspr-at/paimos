// SPDX-License-Identifier: AGPL-3.0-only
package stagehandoff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

const handoffColumns = `id::text,project_node_id::text,release_node_id::text,stage,operation,plugin_id,attempt,authority_epoch,journey_revision,state,expires_at,evidence_ceiling,plan_digest,predecessor_digest,context_digest,prerequisite_seal_sha256,target,target_digest_sha256`

func loadHandoff(ctx context.Context, tx pgx.Tx, id string, lock bool) (Handoff, error) {
	var h Handoff
	q := `SELECT ` + handoffColumns + ` FROM stage_handoffs WHERE id=$1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	var targetDigest *string
	err := tx.QueryRow(ctx, q, id).Scan(&h.ID, &h.ProjectNodeID, &h.ReleaseNodeID, &h.Stage, &h.Operation, &h.PluginID, &h.Attempt, &h.AuthorityEpoch, &h.JourneyRevision, &h.State, &h.ExpiresAt, &h.EvidenceCeiling, &h.PlanDigest, &h.PredecessorDigest, &h.ContextDigest, &h.PrerequisiteSealSHA256, &h.Target, &targetDigest)
	if err != nil {
		return h, err
	}
	if targetDigest != nil {
		h.TargetDigestSHA256 = *targetDigest
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM stage_handoffs WHERE release_node_id=$1::uuid AND stage=$2 AND operation=$3 AND attempt>$4 ORDER BY attempt DESC LIMIT 1`, h.ReleaseNodeID, h.Stage, h.Operation, h.Attempt).Scan(&h.SupersededBy)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return h, err
	}
	var result Result
	err = tx.QueryRow(ctx, `SELECT outcome,terminal_sequence,authority_epoch,prerequisite_seal_sha256,blocker_code,completed_at FROM stage_handoff_results WHERE handoff_id=$1::uuid`, id).Scan(&result.Outcome, &result.TerminalSequence, &result.AuthorityEpoch, &result.PrerequisiteSealSHA256, &result.BlockerCode, &result.CompletedAt)
	if err == nil {
		result.HandoffID = id
		h.Result = &result
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return h, err
	}
	var admission HandoffAdmissionState
	err = tx.QueryRow(ctx, `SELECT id::text,authority_epoch,expires_at,consumed_at,consumed_by_principal_id::text FROM stage_launch_admissions WHERE handoff_id=$1::uuid ORDER BY expires_at DESC,id DESC LIMIT 1`, id).Scan(&admission.AdmissionID, &admission.Epoch, &admission.ExpiresAt, &admission.ConsumedAt, &admission.ConsumedByPrincipalID)
	if err == nil {
		h.Admission = &admission
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return h, err
	}
	return h, nil
}
func planDigest(ctx context.Context, tx pgx.Tx, releaseID string) (string, error) {
	var plan string
	err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('id',t.ticket_node_id,'feature',t.feature_node_id,'position',t.walker_position,'access',t.access_change,'title',n.title,'body',n.body,'fields',n.fields,'state',n.state,'updated_at',n.updated_at) ORDER BY t.walker_position,t.ticket_node_id),'[]'::jsonb)::text FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id WHERE t.release_node_id=$1::uuid`, releaseID).Scan(&plan)
	if err != nil {
		return "", err
	}
	return digest(plan), nil
}
func gateLive(ctx context.Context, tx pgx.Tx, releaseID, gate string) (bool, error) {
	if gate == "" {
		return true, nil
	}
	var live bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_gates g JOIN approval_requests a ON a.tenant_id=g.tenant_id AND a.id=g.approval_request_id JOIN approval_decisions d ON d.tenant_id=a.tenant_id AND d.request_id=a.id JOIN agent_permission_grants grant_row ON grant_row.tenant_id=a.tenant_id AND grant_row.approval_request_id=a.id WHERE g.release_node_id=$1::uuid AND g.gate=$2 AND a.resource_kind='node' AND a.resource_id=g.release_node_id AND d.decision='approved' AND a.expires_at>now() AND grant_row.revoked_at IS NULL AND grant_row.valid_until>now())`, releaseID, gate).Scan(&live)
	return live, err
}

// dependencySet derives the complete required predecessor set from current
// terminal results. A dependent launch never treats an empty Janus preparation
// as evidence when access is required.
func dependencySet(ctx context.Context, tx pgx.Tx, h Handoff, accessRequired bool) ([]dependency, string, error) {
	required := []struct {
		stage, operation string
		kinds            []string
	}{}
	switch {
	case h.Operation == "deploy" && accessRequired:
		required = append(required, struct {
			stage, operation string
			kinds            []string
		}{"access", "prepare", []string{"authorization", "credential_handoff"}})
	case h.Operation == "verify":
		required = append(required, struct {
			stage, operation string
			kinds            []string
		}{"deploy", "deploy", []string{"deployment"}})
	case h.Operation == "apply":
		required = append(required, struct {
			stage, operation string
			kinds            []string
		}{"deploy", "deploy", []string{"deployment"}})
	}
	var deps []dependency
	for _, req := range required {
		var id string
		var state, outcome, dependencyPlan string
		var expires time.Time
		var seq int64
		var renewed bool
		// A renewal is an explicit new authority boundary. Ordinary journey
		// transitions retain valid predecessor results; gate renewal requires
		// evidence from at least the recorded renewal revision. Both the event
		// and handoff revisions are server-written under the project lock.
		err := tx.QueryRow(ctx, `SELECT h.id::text,h.state,h.plan_digest,h.expires_at,
			EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=h.tenant_id AND e.node_id=h.project_node_id
			  AND e.after->>'current_release_id'=h.release_node_id::text
			  AND (e.type IN ('journey.candidate_renewed','journey.deploy_renewed')
		       OR (e.type='journey.permit_renewed' AND h.stage='access'))
			  AND (e.after->>'revision')::bigint>h.journey_revision)
			FROM stage_handoffs h WHERE h.project_node_id=$4::uuid AND h.release_node_id=$1::uuid AND h.stage=$2 AND h.operation=$3
			ORDER BY h.attempt DESC LIMIT 1`, h.ReleaseNodeID, req.stage, req.operation, h.ProjectNodeID).Scan(&id, &state, &dependencyPlan, &expires, &renewed)
		if err == nil && state == "succeeded" {
			err = tx.QueryRow(ctx, `SELECT outcome,terminal_sequence FROM stage_handoff_results WHERE handoff_id=$1::uuid`, id).Scan(&outcome, &seq)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", fail(409, "required predecessor pending")
		}
		if err != nil {
			return nil, "", err
		}
		if renewed {
			return nil, "", fail(409, "required predecessor predates gate renewal")
		}
		if state != "succeeded" || outcome != "succeeded" || dependencyPlan != h.PlanDigest || !expires.After(time.Now()) {
			return nil, "", fail(409, "required predecessor failed")
		}
		for _, kind := range req.kinds {
			var valid bool
			err := tx.QueryRow(ctx, `SELECT coalesce((SELECT outcome IN ('succeeded','satisfied') AND (authorized IS NULL OR authorized) AND (credential_ready IS NULL OR credential_ready) FROM stage_handoff_evidence WHERE handoff_id=$1::uuid AND kind=$2 ORDER BY sequence DESC LIMIT 1),false)`, id, kind).Scan(&valid)
			if err != nil {
				return nil, "", err
			}
			if !valid {
				return nil, "", fail(409, "required predecessor evidence missing")
			}
			deps = append(deps, dependency{id, kind, seq})
		}
	}
	pieces := []string{}
	for _, d := range deps {
		pieces = append(pieces, d.id, d.kind, fmt.Sprint(d.sequence))
	}
	return deps, digest(pieces...), nil
}

type dependency struct {
	id, kind string
	sequence int64
}

func (m *Module) create(ctx context.Context, tx pgx.Tx, p tenant.Principal, in RequestWrite, plugin string, ceiling []string, gate string) (Handoff, error) {
	var h Handoff
	h.ProjectNodeID = in.ProjectNodeID
	h.ReleaseNodeID = in.ReleaseNodeID
	h.Stage = in.Stage
	h.Operation = in.Operation
	h.PluginID = plugin
	h.EvidenceCeiling = ceiling
	h.Target = in.Target
	h.TargetDigestSHA256 = in.TargetDigestSHA256
	if old, ok, err := existingRequest(ctx, tx, in); err != nil {
		return h, err
	} else if ok {
		return old, nil
	}
	var revision int64
	var releaseState string
	var accessRequired bool
	var releaseRevision int64
	err := tx.QueryRow(ctx, `SELECT j.revision,r.state,r.access_required,r.revision FROM journey_projects j JOIN journey_releases r ON r.tenant_id=j.tenant_id AND r.project_node_id=j.project_node_id WHERE j.project_node_id=$1::uuid AND r.release_node_id=$2::uuid AND j.current_release_node_id=r.release_node_id FOR UPDATE OF j`, in.ProjectNodeID, in.ReleaseNodeID).Scan(&revision, &releaseState, &accessRequired, &releaseRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, fail(404, "release not found")
	}
	if err != nil {
		return h, err
	}
	if revision != in.ExpectedJourneyRevision {
		return h, fail(409, "journey revision changed")
	}
	if releaseState == "released" || releaseState == "superseded" || releaseState == "refused" {
		return h, fail(409, "release is closed")
	}
	if in.Operation == "apply" && !accessRequired {
		return h, fail(409, "access is not required")
	}
	if in.Operation == "deploy" && releaseState != "candidate" && releaseState != "deploying" {
		return h, fail(409, "release is not ready to deploy")
	}
	if in.Operation == "verify" && releaseState != "deploying" {
		return h, fail(409, "release is not deployed")
	}
	if in.Operation == "apply" && releaseState != "access" {
		return h, fail(409, "release is not awaiting access")
	}
	enabled, err := plugins.Enabled(ctx, tx, m.registry, p.TenantID, plugin, in.Operation)
	if err != nil {
		return h, err
	}
	if !enabled {
		return h, fail(403, "stage plugin is not installed")
	}
	_, bound := m.registry.Step(plugin)
	if !bound {
		return h, fail(503, "stage plugin implementation is unavailable")
	}
	if err := m.registry.AuthorizeHandoff(ctx, p, plugin, in.Operation); err != nil {
		return h, fail(403, "stage plugin refused operation")
	}
	if in.Operation == "deploy" {
		candidate, err := gateLive(ctx, tx, in.ReleaseNodeID, "candidate")
		if err != nil {
			return h, err
		}
		if !candidate {
			return h, fail(403, "candidate gate is not approved")
		}
	}
	live, err := gateLive(ctx, tx, in.ReleaseNodeID, gate)
	if err != nil {
		return h, err
	}
	if !live {
		return h, fail(403, "stage gate is not approved")
	}
	if in.Operation == "verify" {
		// Retain audit metadata internally; Handoff excludes it from PHAROS responses.
		var targetDigest *string
		err := tx.QueryRow(ctx, `SELECT target,target_digest_sha256 FROM stage_handoffs WHERE release_node_id=$1::uuid AND stage='deploy' AND operation='deploy' ORDER BY attempt DESC LIMIT 1`, in.ReleaseNodeID).Scan(&h.Target, &targetDigest)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return h, err
		}
		if targetDigest != nil {
			h.TargetDigestSHA256 = *targetDigest
		}
	}
	h.PlanDigest, err = planDigest(ctx, tx, in.ReleaseNodeID)
	if err != nil {
		return h, err
	}
	if old, ok, err := existingRequest(ctx, tx, in); err != nil {
		return h, err
	} else if ok {
		return old, nil
	}
	h.JourneyRevision = revision
	deps, seal, err := dependencySet(ctx, tx, h, accessRequired)
	if err != nil {
		return h, err
	}
	h.PrerequisiteSealSHA256 = seal
	h.PredecessorDigest = seal
	h.ContextDigest = digest(in.ProjectNodeID, in.ReleaseNodeID, in.Stage, in.Operation, fmt.Sprint(revision), fmt.Sprint(releaseRevision), h.PlanDigest, seal)
	err = tx.QueryRow(ctx, `SELECT coalesce(max(attempt),0)+1,coalesce(max(authority_epoch),0)+1 FROM stage_handoffs WHERE release_node_id=$1::uuid AND stage=$2 AND operation=$3`, in.ReleaseNodeID, in.Stage, in.Operation).Scan(&h.Attempt, &h.AuthorityEpoch)
	if err != nil {
		return h, err
	}
	h.State = "requested"
	// PostgreSQL stores timestamptz at microsecond precision. Return the stored
	// instant so the create response can be used as an exact handoff binding.
	h.ExpiresAt = m.clock().UTC().Add(30 * time.Minute).Truncate(time.Microsecond)
	err = tx.QueryRow(ctx, `INSERT INTO stage_handoffs(tenant_id,project_node_id,release_node_id,stage,operation,plugin_id,requested_by_principal_id,idempotency_key,attempt,authority_epoch,journey_revision,plan_digest,predecessor_digest,context_digest,prerequisite_seal_sha256,evidence_ceiling,expires_at,target,target_digest_sha256) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7::uuid,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) RETURNING id::text,expires_at`, p.TenantID, in.ProjectNodeID, in.ReleaseNodeID, in.Stage, in.Operation, plugin, p.ID, in.IdempotencyKey, h.Attempt, h.AuthorityEpoch, revision, h.PlanDigest, h.PredecessorDigest, h.ContextDigest, seal, ceiling, h.ExpiresAt, h.Target, nullableDigest(h.TargetDigestSHA256)).Scan(&h.ID, &h.ExpiresAt)
	if err != nil {
		return h, err
	}
	for _, d := range deps {
		_, err = tx.Exec(ctx, `INSERT INTO stage_handoff_dependencies(tenant_id,handoff_id,dependency_handoff_id,required_kind) VALUES($1::uuid,$2::uuid,$3::uuid,$4)`, p.TenantID, h.ID, d.id, d.kind)
		if err != nil {
			return h, err
		}
	}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "stage_handoff.requested", NodeID: &h.ReleaseNodeID, After: withTargetEvidence(h, map[string]any{"id": h.ID, "stage": h.Stage, "operation": h.Operation, "attempt": h.Attempt, "authority_epoch": h.AuthorityEpoch})})
	return h, err
}
func current(ctx context.Context, tx pgx.Tx, h Handoff) (bool, error) {
	var revision int64
	var releaseRevision int64
	var accessRequired bool
	err := tx.QueryRow(ctx, `SELECT j.revision,r.revision,r.access_required FROM journey_projects j JOIN journey_releases r ON r.tenant_id=j.tenant_id AND r.project_node_id=j.project_node_id WHERE j.project_node_id=$1::uuid AND r.release_node_id=$2::uuid AND j.current_release_node_id=r.release_node_id FOR UPDATE OF j,r`, h.ProjectNodeID, h.ReleaseNodeID).Scan(&revision, &releaseRevision, &accessRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if revision != h.JourneyRevision || time.Now().After(h.ExpiresAt) {
		return false, nil
	}
	var latest int64
	err = tx.QueryRow(ctx, `SELECT max(authority_epoch) FROM stage_handoffs WHERE release_node_id=$1::uuid AND stage=$2 AND operation=$3`, h.ReleaseNodeID, h.Stage, h.Operation).Scan(&latest)
	if err != nil {
		return false, err
	}
	if latest != h.AuthorityEpoch {
		return false, nil
	}
	plan, err := planDigest(ctx, tx, h.ReleaseNodeID)
	if err != nil {
		return false, err
	}
	if plan != h.PlanDigest {
		return false, nil
	}
	_, seal, err := dependencySet(ctx, tx, h, accessRequired)
	if err != nil {
		var e *apiError
		if errors.As(err, &e) {
			return false, nil
		}
		return false, err
	}
	if seal != h.PrerequisiteSealSHA256 {
		return false, nil
	}
	contextDigest := digest(h.ProjectNodeID, h.ReleaseNodeID, h.Stage, h.Operation, fmt.Sprint(revision), fmt.Sprint(releaseRevision), plan, seal)
	return contextDigest == h.ContextDigest, nil
}

// authorityOpen describes whether this attempt can accept another action now.
// It is a projection, never a grant to the caller or a substitute for write checks.
func authorityOpen(ctx context.Context, tx pgx.Tx, h Handoff) (bool, error) {
	if h.SupersededBy != nil || h.Result != nil || closedHandoff(h.State) {
		return false, nil
	}
	return current(ctx, tx, h)
}
func agentAllowed(ctx context.Context, tx pgx.Tx, p tenant.Principal, authorization string, h Handoff) (bool, error) {
	if p.Kind != tenant.Agent {
		return false, nil
	}
	prefix, secret, ok := parseBearer(authorization)
	if !ok {
		return false, nil
	}
	sum := sha256.Sum256([]byte(secret))
	var scopes []string
	err := tx.QueryRow(ctx, `SELECT scopes FROM agent_keys WHERE prefix=$1 AND hash=$2 AND principal_id=$3::uuid AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>now())`, prefix, hex.EncodeToString(sum[:]), p.ID).Scan(&scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	scope := "stage." + h.Operation
	return approvals.LiveGrant(ctx, tx, p.ID, scope, "node", &h.ReleaseNodeID, scopes)
}

// requireRoutedPrincipal is checked after the handoff row is locked and before
// any replay or mutation. A grant for stage.<operation> does not name a plugin.
func requireRoutedPrincipal(ctx context.Context, tx pgx.Tx, p tenant.Principal, h Handoff) error {
	routed, err := principalRoutedTo(ctx, tx, p, h.PluginID)
	if err != nil {
		return err
	}
	if !routed {
		return fail(403, "handoff requires the routed plugin agent")
	}
	return nil
}

// An inactive principal may lose project visibility before the handoff row can
// be locked. Reject it explicitly so write routes still return a clear 403.
func requireActiveAgent(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Agent {
		return fail(403, "handoff requires an active plugin agent")
	}
	var kind, status string
	err := tx.QueryRow(ctx, `SELECT kind,status FROM principals WHERE id=$1::uuid AND tenant_id=$2::uuid`, p.ID, p.TenantID).Scan(&kind, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(403, "handoff requires an active plugin agent")
	}
	if err != nil {
		return err
	}
	if kind != string(tenant.Agent) || status != "active" {
		return fail(403, "handoff requires an active plugin agent")
	}
	return nil
}
func parseBearer(header string) (string, string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "aeon_")
	if !ok {
		return "", "", false
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	return prefix, secret, ok && prefix != "" && secret != "" && !strings.Contains(secret, "_")
}

func existingRequest(ctx context.Context, tx pgx.Tx, in RequestWrite) (Handoff, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM stage_handoffs WHERE project_node_id=$1::uuid AND stage=$2 AND idempotency_key=$3`, in.ProjectNodeID, in.Stage, in.IdempotencyKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Handoff{}, false, nil
	}
	if err != nil {
		return Handoff{}, false, err
	}
	old, err := loadHandoff(ctx, tx, id, false)
	if err != nil {
		return Handoff{}, false, err
	}
	if old.ReleaseNodeID != in.ReleaseNodeID || old.Operation != in.Operation || old.JourneyRevision != in.ExpectedJourneyRevision || (in.Target != nil && old.TargetDigestSHA256 != in.TargetDigestSHA256) {
		return Handoff{}, false, fail(409, "idempotency key was used for a different request")
	}
	return old, true, nil
}

func nullableDigest(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func withTargetEvidence(h Handoff, fields map[string]any) map[string]any {
	if h.Target != nil {
		fields["target"] = h.Target
		fields["target_digest_sha256"] = h.TargetDigestSHA256
	}
	return fields
}
