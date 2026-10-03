// SPDX-License-Identifier: AGPL-3.0-only
package stagehandoff

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/releasesettlement"
	"github.com/inspr-at/paimos/internal/tenant"
)

func loadEvidence(ctx context.Context, tx pgx.Tx, id string, sequence int64) (Evidence, error) {
	var e Evidence
	var a Artifact
	var scheme, version, channel, artifactDigest, commit, coordinate, manifestDigest *string
	var releaseSequence *int64
	var plan, host, running, expected *string
	var hostEval, targetPass, backup, restart *bool
	var backupAt *time.Time
	err := tx.QueryRow(ctx, `SELECT sequence,kind,outcome,observed_at,authority_epoch,workflow,environment,version_scheme,version,release_channel,release_sequence,artifact_digest_sha256,commit_digest,manifest_coordinate,manifest_digest_sha256,authorized,credential_ready,received_at,reviewed_plan_digest,host,all_host_eval_passed,target_build_passed,backup_ready,backup_observed_at,restart_required,running_kernel,expected_kernel FROM stage_handoff_evidence WHERE handoff_id=$1::uuid AND sequence=$2`, id, sequence).Scan(&e.Sequence, &e.Kind, &e.Outcome, &e.ObservedAt, &e.AuthorityEpoch, &e.Workflow, &e.Environment, &scheme, &version, &channel, &releaseSequence, &artifactDigest, &commit, &coordinate, &manifestDigest, &e.Authorized, &e.CredentialReady, &e.ReceivedAt, &plan, &host, &hostEval, &targetPass, &backup, &backupAt, &restart, &running, &expected)
	if err != nil {
		return e, err
	}
	if e.Kind == "deployment" || e.Kind == "verification" {
		if scheme == nil || version == nil || channel == nil || releaseSequence == nil || artifactDigest == nil || commit == nil || coordinate == nil || manifestDigest == nil {
			return e, fail(500, "incomplete artifact evidence")
		}
		a = Artifact{*scheme, *version, *channel, *releaseSequence, *artifactDigest, *commit, *coordinate, *manifestDigest}
		e.Artifact = &a
	}
	if e.Kind == "launch_readiness" {
		if plan == nil || host == nil || hostEval == nil || targetPass == nil || backup == nil || backupAt == nil || restart == nil || running == nil || expected == nil {
			return e, fail(500, "incomplete launch readiness")
		}
		e.ReviewedPlanDigest = *plan
		e.Host = *host
		e.AllHostEvalPassed = hostEval
		e.TargetBuildPassed = targetPass
		e.BackupReady = backup
		e.BackupObservedAt = *backupAt
		e.RestartRequired = restart
		e.RunningKernel = *running
		e.ExpectedKernel = *expected
	}
	e.HandoffID = id
	return e, nil
}
func normalizeEvidence(e EvidenceWrite) EvidenceWrite {
	e.ObservedAt = e.ObservedAt.UTC().Truncate(time.Microsecond)
	if !e.BackupObservedAt.IsZero() {
		e.BackupObservedAt = e.BackupObservedAt.UTC().Truncate(time.Microsecond)
	}
	return e
}
func (m *Module) appendEvidence(ctx context.Context, tx pgx.Tx, p tenant.Principal, authorization, id string, in EvidenceWrite) (Evidence, error) {
	if err := requireActiveAgent(ctx, tx, p); err != nil {
		return Evidence{}, err
	}
	h, err := loadHandoff(ctx, tx, id, true)
	if err != nil {
		return Evidence{}, err
	}
	if err := requireRoutedPrincipal(ctx, tx, p, h); err != nil {
		return Evidence{}, err
	}
	allowed, err := agentAllowed(ctx, tx, p, authorization, h)
	if err != nil {
		return Evidence{}, err
	}
	if !allowed {
		return Evidence{}, fail(http.StatusForbidden, "live agent grant required")
	}
	if in.AuthorityEpoch != h.AuthorityEpoch {
		return Evidence{}, fail(409, "stale authority")
	}
	// launch_readiness is Pharos deploy observation. Handoffs requested before
	// the kind was added to the ceiling still accept it from the routed principal.
	if !contains(h.EvidenceCeiling, in.Kind) && !(in.Kind == "launch_readiness" && h.PluginID == "pharos" && h.Operation == "deploy") {
		return Evidence{}, fail(400, "evidence exceeds plugin ceiling")
	}
	if in.Kind == "launch_readiness" && (h.PluginID != "pharos" || h.Operation != "deploy") {
		return Evidence{}, fail(400, "launch readiness requires a Pharos deploy")
	}
	var requestedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT created_at FROM stage_handoffs WHERE id=$1::uuid`, id).Scan(&requestedAt); err != nil {
		return Evidence{}, err
	}
	if in.ObservedAt.Before(requestedAt.Add(-time.Minute)) {
		return Evidence{}, fail(409, "evidence predates handoff")
	}
	if h.Result != nil || h.State == "revoked" {
		old, err := loadEvidence(ctx, tx, id, in.Sequence)
		if err == nil && reflect.DeepEqual(normalizeEvidence(old.EvidenceWrite), normalizeEvidence(in)) {
			return old, nil
		}
		return Evidence{}, fail(409, "handoff is terminal")
	}
	current, err := current(ctx, tx, h)
	if err != nil {
		return Evidence{}, err
	}
	if !current {
		return Evidence{}, fail(409, "handoff is stale")
	}
	old, err := loadEvidence(ctx, tx, id, in.Sequence)
	if err == nil {
		if reflect.DeepEqual(normalizeEvidence(old.EvidenceWrite), normalizeEvidence(in)) {
			return old, nil
		}
		return Evidence{}, fail(409, "divergent evidence replay")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, err
	}
	var maxSeq int64
	err = tx.QueryRow(ctx, `SELECT coalesce(max(sequence),0) FROM stage_handoff_evidence WHERE handoff_id=$1::uuid`, id).Scan(&maxSeq)
	if err != nil {
		return Evidence{}, err
	}
	if in.Sequence != maxSeq+1 {
		return Evidence{}, fail(409, "evidence sequence must be contiguous")
	}
	if in.Artifact != nil {
		if h.Operation == "deploy" && (in.Outcome == "succeeded" || in.Outcome == "satisfied") {
			if err := consumedAdmission(ctx, tx, h, *in.Artifact); err != nil {
				return Evidence{}, err
			}
		}
		var scheme, version *string
		var number int64
		err = tx.QueryRow(ctx, `SELECT version_scheme,version,number FROM journey_releases WHERE release_node_id=$1::uuid`, h.ReleaseNodeID).Scan(&scheme, &version, &number)
		if err != nil {
			return Evidence{}, err
		}
		if scheme != nil && (*scheme != in.Artifact.VersionScheme || *version != in.Artifact.Version) || in.Artifact.ReleaseSequence != number {
			return Evidence{}, fail(409, "artifact does not match release version")
		}
		if h.Operation == "verify" {
			var previous Artifact
			err = tx.QueryRow(ctx, `SELECT e.artifact_digest_sha256,e.commit_digest,e.manifest_coordinate,e.manifest_digest_sha256,e.release_channel,e.version_scheme,e.version,e.release_sequence FROM stage_handoffs d JOIN stage_handoff_results r ON r.tenant_id=d.tenant_id AND r.handoff_id=d.id JOIN stage_handoff_evidence e ON e.tenant_id=d.tenant_id AND e.handoff_id=d.id AND e.sequence=r.terminal_sequence WHERE d.release_node_id=$1::uuid AND d.operation='deploy' AND d.state='succeeded' ORDER BY d.attempt DESC LIMIT 1`, h.ReleaseNodeID).Scan(&previous.DigestSHA256, &previous.CommitDigest, &previous.ManifestCoordinate, &previous.ManifestDigestSHA256, &previous.ReleaseChannel, &previous.VersionScheme, &previous.Version, &previous.ReleaseSequence)
			if err != nil {
				return Evidence{}, fail(409, "deployment artifact missing")
			}
			if previous != *in.Artifact {
				return Evidence{}, fail(409, "verification artifact differs from deployment")
			}
		}
	}
	var a *Artifact = in.Artifact
	if a == nil {
		a = &Artifact{}
	}
	var plan, host, running, expected any
	var hostEval, targetPass, backup, restart any
	var backupAt any
	if in.Kind == "launch_readiness" {
		plan = in.ReviewedPlanDigest
		host = in.Host
		hostEval = *in.AllHostEvalPassed
		targetPass = *in.TargetBuildPassed
		backup = *in.BackupReady
		backupAt = in.BackupObservedAt
		restart = *in.RestartRequired
		running = in.RunningKernel
		expected = in.ExpectedKernel
	}
	_, err = tx.Exec(ctx, `INSERT INTO stage_handoff_evidence(tenant_id,handoff_id,sequence,authority_epoch,kind,outcome,observed_at,workflow,environment,version_scheme,version,release_channel,release_sequence,artifact_digest_sha256,commit_digest,manifest_coordinate,manifest_digest_sha256,authorized,credential_ready,reviewed_plan_digest,host,all_host_eval_passed,target_build_passed,backup_ready,backup_observed_at,restart_required,running_kernel,expected_kernel) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,nullif($10,''),nullif($11,''),nullif($12,''),nullif($13,0),nullif($14,''),nullif($15,''),nullif($16,''),nullif($17,''),$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)`, p.TenantID, id, in.Sequence, in.AuthorityEpoch, in.Kind, in.Outcome, in.ObservedAt, in.Workflow, in.Environment, a.VersionScheme, a.Version, a.ReleaseChannel, a.ReleaseSequence, a.DigestSHA256, a.CommitDigest, a.ManifestCoordinate, a.ManifestDigestSHA256, in.Authorized, in.CredentialReady, plan, host, hostEval, targetPass, backup, backupAt, restart, running, expected)
	if err != nil {
		return Evidence{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE stage_handoffs SET state='active' WHERE id=$1::uuid AND state='requested'`, id); err != nil {
		return Evidence{}, err
	}
	out, err := loadEvidence(ctx, tx, id, in.Sequence)
	if err != nil {
		return Evidence{}, err
	}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "stage_handoff.evidence_added", NodeID: &h.ReleaseNodeID, After: withTargetEvidence(h, map[string]any{"handoff_id": id, "sequence": in.Sequence, "kind": in.Kind, "outcome": in.Outcome})})
	return out, err
}
func contains(set []string, value string) bool {
	for _, s := range set {
		if s == value {
			return true
		}
	}
	return false
}
func (m *Module) close(ctx context.Context, tx pgx.Tx, p tenant.Principal, authorization, id string, in ResultWrite) (Result, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0))`); err != nil {
		return Result{}, err
	}
	var project string
	if err := tx.QueryRow(ctx, `SELECT project_node_id::text FROM stage_handoffs WHERE id=$1::uuid`, id).Scan(&project); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM journey_projects WHERE project_node_id=$1::uuid FOR NO KEY UPDATE`, project); err != nil {
		return Result{}, err
	}

	if err := requireActiveAgent(ctx, tx, p); err != nil {
		return Result{}, err
	}
	h, err := loadHandoff(ctx, tx, id, true)
	if err != nil {
		return Result{}, err
	}
	if err := requireRoutedPrincipal(ctx, tx, p, h); err != nil {
		return Result{}, err
	}
	allowed, err := agentAllowed(ctx, tx, p, authorization, h)
	if err != nil {
		return Result{}, err
	}
	if !allowed {
		return Result{}, fail(403, "live agent grant required")
	}
	if h.Result != nil {
		if sameResult(h.Result, in) {
			return *h.Result, nil
		}
		return Result{}, fail(409, "result already recorded")
	}
	if in.AuthorityEpoch != h.AuthorityEpoch || in.PrerequisiteSealSHA256 != h.PrerequisiteSealSHA256 {
		return Result{}, fail(409, "stale authority or dependency seal")
	}
	current, err := current(ctx, tx, h)
	if err != nil {
		return Result{}, err
	}
	if !current {
		return Result{}, fail(409, "handoff is stale")
	}
	_, _, gate, _ := route(h.Stage, h.Operation)
	if h.Operation == "deploy" {
		candidate, err := gateLive(ctx, tx, h.ReleaseNodeID, "candidate")
		if err != nil {
			return Result{}, err
		}
		if !candidate {
			return Result{}, fail(403, "candidate gate is no longer approved")
		}
	}
	live, err := gateLive(ctx, tx, h.ReleaseNodeID, gate)
	if err != nil {
		return Result{}, err
	}
	if !live {
		return Result{}, fail(403, "stage gate is no longer approved")
	}
	terminal, err := loadEvidence(ctx, tx, id, in.TerminalSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, fail(409, "terminal evidence missing")
	}
	if err != nil {
		return Result{}, err
	}
	var maxSeq int64
	if err = tx.QueryRow(ctx, `SELECT max(sequence) FROM stage_handoff_evidence WHERE handoff_id=$1::uuid`, id).Scan(&maxSeq); err != nil {
		return Result{}, err
	}
	if in.TerminalSequence != maxSeq || terminal.AuthorityEpoch != h.AuthorityEpoch {
		return Result{}, fail(409, "terminal evidence is stale")
	}
	if in.Outcome == "succeeded" {
		if terminal.Outcome != "succeeded" && terminal.Outcome != "satisfied" {
			return Result{}, fail(409, "terminal evidence failed")
		}
		expectedKind := map[string]string{"deploy": "deployment", "verify": "verification", "prepare": "credential_handoff", "apply": "credential_handoff"}[h.Operation]
		if terminal.Kind != expectedKind {
			return Result{}, fail(409, "terminal evidence kind mismatch")
		}
		if h.PluginID == "janus" {
			var authorized, ready bool
			err = tx.QueryRow(ctx, `SELECT coalesce((SELECT authorized AND outcome='satisfied' FROM stage_handoff_evidence WHERE handoff_id=$1::uuid AND kind='authorization' ORDER BY sequence DESC LIMIT 1),false),coalesce((SELECT credential_ready AND outcome='satisfied' FROM stage_handoff_evidence WHERE handoff_id=$1::uuid AND kind='credential_handoff' ORDER BY sequence DESC LIMIT 1),false)`, id).Scan(&authorized, &ready)
			if err != nil {
				return Result{}, err
			}
			if !authorized || !ready {
				return Result{}, fail(409, "Janus checks are incomplete")
			}
		}
	}
	_, bound := m.registry.Step(h.PluginID)
	if !bound {
		return Result{}, fail(503, "stage plugin implementation is unavailable")
	}
	if err := m.registry.AuthorizeHandoff(ctx, p, h.PluginID, h.Operation); err != nil {
		return Result{}, fail(403, "stage plugin refused result")
	}
	state := "failed"
	if in.Outcome == "succeeded" {
		state = "succeeded"
	}
	_, err = tx.Exec(ctx, `INSERT INTO stage_handoff_results(tenant_id,handoff_id,outcome,terminal_sequence,authority_epoch,prerequisite_seal_sha256,blocker_code) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7)`, p.TenantID, id, in.Outcome, in.TerminalSequence, in.AuthorityEpoch, in.PrerequisiteSealSHA256, in.BlockerCode)
	if err != nil {
		return Result{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE stage_handoffs SET state=$2 WHERE id=$1::uuid`, id, state)
	if err != nil {
		return Result{}, err
	}
	if in.Outcome == "succeeded" && h.Operation != "prepare" {
		nextState := "deploying"
		if h.Operation == "apply" {
			nextState = "released"
		}
		if h.Operation == "verify" {
			var access bool
			if err := tx.QueryRow(ctx, `SELECT access_required FROM journey_releases WHERE release_node_id=$1::uuid`, h.ReleaseNodeID).Scan(&access); err != nil {
				return Result{}, err
			}
			if access {
				nextState = "access"
			} else {
				nextState = "released"
			}
		}
		var superseded []string
		if nextState == "released" {
			superseded, err = releasesettlement.SettleTx(ctx, tx, h.ProjectNodeID, h.ReleaseNodeID)
			if errors.Is(err, releasesettlement.ErrVersionConflict) {
				return Result{}, fail(409, err.Error())
			}
		} else {
			_, err = tx.Exec(ctx, `UPDATE journey_releases SET state=$2,revision=revision+1 WHERE release_node_id=$1::uuid`, h.ReleaseNodeID, nextState)
		}
		if err != nil {
			return Result{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE journey_projects SET revision=revision+1,updated_at=now() WHERE project_node_id=$1::uuid`, h.ProjectNodeID)
		if err != nil {
			return Result{}, err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "journey.release_transitioned", NodeID: &h.ReleaseNodeID, After: withTargetEvidence(h, map[string]any{"release_node_id": h.ReleaseNodeID, "state": nextState, "handoff_id": h.ID, "superseded_release_ids": superseded})})
		if err != nil {
			return Result{}, err
		}
	}
	updated, err := loadHandoff(ctx, tx, id, false)
	if err != nil {
		return Result{}, err
	}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "stage_handoff.completed", NodeID: &h.ReleaseNodeID, After: withTargetEvidence(h, map[string]any{"handoff_id": id, "outcome": in.Outcome, "terminal_sequence": in.TerminalSequence, "blocker_code": in.BlockerCode})})
	if err != nil {
		return Result{}, err
	}
	return *updated.Result, nil
}
func sameResult(result *Result, in ResultWrite) bool {
	return result.Outcome == in.Outcome && result.TerminalSequence == in.TerminalSequence && result.AuthorityEpoch == in.AuthorityEpoch && result.PrerequisiteSealSHA256 == in.PrerequisiteSealSHA256 && ((result.BlockerCode == nil && in.BlockerCode == nil) || (result.BlockerCode != nil && in.BlockerCode != nil && *result.BlockerCode == *in.BlockerCode))
}
