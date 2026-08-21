// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"context"
	"database/sql"
	"errors"

	"github.com/inspr-at/paimos/backend/auth"
)

// Reconcile terminalizes only state owned by the caller's exact current
// credential. Actor and runner reconciliation are deliberately separate: an
// actor credential cannot enumerate runner state, and a runner key is bounded
// to one exact device and retained lease revision lineage per call.
func (s *Service) Reconcile(ctx context.Context, principal auth.Principal, request ReconcileRequest) (ReconcileProjection, error) {
	limit := request.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return ReconcileProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}

	requiredScope := ScopeActorWrite
	switch request.Mode {
	case ReconcileActor:
		if request.LeaseID != "" || request.LeaseRevision != 0 || request.DeviceID != "" {
			return ReconcileProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
		}
	case ReconcileRunner:
		requiredScope = ScopeRunner
		if !validUUID(request.LeaseID) || request.LeaseRevision <= 0 || validateDevice(request.DeviceID) != nil ||
			principal.Kind() != auth.PrincipalAPIKey {
			return ReconcileProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
		}
	default:
		return ReconcileProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}

	authz, err := s.beginAuthorized(ctx, principal, false, requiredScope)
	if err != nil {
		return ReconcileProjection{}, err
	}
	defer authz.tx.Rollback()
	if authz.user == nil || authz.user.Role == "external" {
		return ReconcileProjection{}, domainError(ErrForbidden, CodeForbidden)
	}

	var projection ReconcileProjection
	switch request.Mode {
	case ReconcileActor:
		err = s.reconcileActorTx(ctx, authz, limit, &projection)
	case ReconcileRunner:
		err = s.reconcileRunnerTx(ctx, authz, request, limit, &projection)
	}
	if err != nil {
		return ReconcileProjection{}, err
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return ReconcileProjection{}, err
	}
	return projection, nil
}

func (s *Service) reconcileActorTx(ctx context.Context, authz *authorizedTx, limit int, projection *ReconcileProjection) error {
	kind, session, apiKey, ok := credentialColumns(authz.principal)
	if !ok {
		return domainError(ErrForbidden, CodeCredentialRevoked)
	}
	admin := 0
	if auth.IsAdmin(authz.user) {
		admin = 1
	}
	accessArgs := []any{admin, authz.user.Role, authz.user.ID}
	now := s.controlNow()

	// Access is resolved against the issue's current project, not the project
	// snapshot captured by the command. Applying the access predicate before
	// LIMIT prevents inaccessible rows from starving this caller's batch.
	args := []any{now, authz.principal.UserID(), kind, session, apiKey}
	args = append(args, accessArgs...)
	args = append(args, limit)
	rows, err := authz.tx.QueryContext(ctx, `SELECT command.command_id
		FROM control_commands command
		JOIN issues issue ON issue.id=command.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id
		WHERE command.status='pending_confirmation'
		 AND command.expires_at<=?
		 AND command.user_id=? AND command.principal_kind=?
		 AND command.actor_session_credential_id IS ? AND command.actor_api_key_id IS ?
		 AND project.status IN ('active','frozen','archived')
		 AND (?=1 OR (?='member' AND COALESCE((SELECT member.access_level FROM project_members member
		  WHERE member.user_id=? AND member.project_id=project.id),'editor')='editor'))
		ORDER BY command.expires_at,command.command_id LIMIT ?`, args...)
	if err != nil {
		return storageError(ctx, err)
	}
	commandIDs, err := scanStrings(rows)
	if err != nil {
		return storageError(ctx, err)
	}
	for _, id := range commandIDs {
		result, updateErr := authz.tx.ExecContext(ctx, `UPDATE control_commands SET status='expired',status_revision=status_revision+1,
			safe_reason='confirmation_expired',terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=? AND status='pending_confirmation'
			AND expires_at<=?`, id, now)
		if updateErr != nil {
			return sqliteConflict(updateErr)
		}
		if changed(result) {
			if err := insertCommandEvent(ctx, authz.tx, id, "command_expired"); err != nil {
				return err
			}
			projection.ExpiredCommands++
		}
	}

	args = []any{now, authz.principal.UserID(), kind, session, apiKey}
	args = append(args, accessArgs...)
	args = append(args, limit)
	rows, err = authz.tx.QueryContext(ctx, `SELECT grant.grant_id,grant.revision
		FROM control_capability_grants grant
		JOIN issues issue ON issue.id=grant.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id
		WHERE grant.revoked_at IS NULL AND grant.expires_at<=?
		 AND grant.user_id=? AND grant.principal_kind=?
		 AND grant.actor_session_credential_id IS ? AND grant.actor_api_key_id IS ?
		 AND project.status IN ('active','frozen','archived')
		 AND (?=1 OR (?='member' AND COALESCE((SELECT member.access_level FROM project_members member
		  WHERE member.user_id=? AND member.project_id=project.id),'editor')='editor'))
		ORDER BY grant.expires_at,grant.grant_id LIMIT ?`, args...)
	if err != nil {
		return storageError(ctx, err)
	}
	type grantCandidate struct {
		id       string
		revision int64
	}
	grants := make([]grantCandidate, 0)
	for rows.Next() {
		var candidate grantCandidate
		if err := rows.Scan(&candidate.id, &candidate.revision); err != nil {
			rows.Close()
			return storageError(ctx, err)
		}
		grants = append(grants, candidate)
	}
	if err := rows.Close(); err != nil {
		return storageError(ctx, err)
	}
	for _, candidate := range grants {
		result, updateErr := authz.tx.ExecContext(ctx, `UPDATE control_capability_grants SET
			revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=? AND revoked_at IS NULL
			AND expires_at<=?`, candidate.id, candidate.revision, now)
		if updateErr != nil {
			return sqliteConflict(updateErr)
		}
		if changed(result) {
			if err := insertGrantEvent(ctx, authz.tx, candidate.id, candidate.revision, "grant_expired", "capability_expired"); err != nil {
				return err
			}
			projection.ExpiredGrants++
		}
	}
	return nil
}

func (s *Service) reconcileRunnerTx(ctx context.Context, authz *authorizedTx, request ReconcileRequest, limit int,
	projection *ReconcileProjection) error {
	now := s.controlNow()
	// A revoked/expired lease remains the immutable ownership record for its
	// inputs and effects. Resolve that retained record, exact credential/device,
	// and the issue's current project before looking at any child state.
	var currentProjectID int64
	err := authz.tx.QueryRowContext(ctx, `SELECT issue.project_id
		FROM control_capability_leases lease
		JOIN issues issue ON issue.id=lease.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id AND project.status IN ('active','frozen','archived')
		WHERE lease.lease_id=? AND lease.revision=? AND lease.user_id=?
		 AND lease.principal_kind='api_key' AND lease.actor_api_key_id=? AND lease.device_id=?`,
		request.LeaseID, request.LeaseRevision, authz.principal.UserID(), authz.principal.APIKeyID(), request.DeviceID).
		Scan(&currentProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return domainError(ErrForbidden, CodeForbidden)
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, currentProjectID); err != nil {
		return err
	}

	result, err := authz.tx.ExecContext(ctx, `UPDATE control_capability_leases SET
		revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=? AND user_id=? AND actor_api_key_id=? AND device_id=?
		 AND revoked_at IS NULL AND expires_at<=?`,
		request.LeaseID, request.LeaseRevision, authz.principal.UserID(), authz.principal.APIKeyID(), request.DeviceID, now)
	if err != nil {
		return sqliteConflict(err)
	}
	if changed(result) {
		if err := insertLeaseEvent(ctx, authz.tx, request.LeaseID, request.LeaseRevision, "lease_expired", "lease_expired"); err != nil {
			return err
		}
		projection.ExpiredLeases++
	}

	rows, err := authz.tx.QueryContext(ctx, `SELECT request.request_id,request.revision,
		CASE WHEN EXISTS(SELECT 1 FROM agent_run_cancellation_facts fact WHERE fact.run_id=request.agent_run_id)
		 THEN 'cancelled'
		 WHEN EXISTS(SELECT 1 FROM agent_runs run WHERE run.id=request.agent_run_id
		  AND run.status NOT IN ('queued','running') AND run.finished_at IS NOT NULL) THEN 'run_terminal'
		 ELSE 'expired' END
		FROM control_input_requests request
		JOIN control_input_request_states state ON state.request_id=request.request_id
		 AND state.current_revision=request.revision AND state.terminal_event_id IS NULL
		WHERE request.lease_id=? AND request.lease_revision=?
		 AND (request.expires_at<=? OR EXISTS(SELECT 1 FROM agent_run_cancellation_facts fact
		  WHERE fact.run_id=request.agent_run_id) OR EXISTS(SELECT 1 FROM agent_runs run
		  WHERE run.id=request.agent_run_id AND run.status NOT IN ('queued','running') AND run.finished_at IS NOT NULL))
		ORDER BY request.expires_at,request.request_id LIMIT ?`, request.LeaseID, request.LeaseRevision, now, limit)
	if err != nil {
		return storageError(ctx, err)
	}
	type inputCandidate struct {
		id       string
		revision int64
		kind     string
	}
	inputs := make([]inputCandidate, 0)
	for rows.Next() {
		var candidate inputCandidate
		if err := rows.Scan(&candidate.id, &candidate.revision, &candidate.kind); err != nil {
			rows.Close()
			return storageError(ctx, err)
		}
		inputs = append(inputs, candidate)
	}
	if err := rows.Close(); err != nil {
		return storageError(ctx, err)
	}
	for _, candidate := range inputs {
		reason := map[string]string{"expired": "input_expired", "cancelled": "cancelled", "run_terminal": "run_terminal"}[candidate.kind]
		digest := canonicalDigest("input."+candidate.kind, stringField("request_id", candidate.id), intField("revision", candidate.revision))
		result, insertErr := authz.tx.ExecContext(ctx, `INSERT INTO control_input_resolution_events(request_id,
			request_revision,sequence,event_kind,event_digest,safe_reason)
			SELECT ?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM control_input_request_states
			WHERE request_id=? AND current_revision=? AND terminal_event_id IS NULL)`, candidate.id, candidate.revision,
			candidate.revision, candidate.kind, digest[:], reason, candidate.id, candidate.revision)
		if insertErr != nil {
			return sqliteConflict(insertErr)
		}
		if !changed(result) {
			continue
		}
		terminalID, insertErr := result.LastInsertId()
		if insertErr != nil {
			return storageError(ctx, insertErr)
		}
		if _, updateErr := authz.tx.ExecContext(ctx, `UPDATE control_input_request_states SET
			state_revision=state_revision+1,terminal_event_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE request_id=? AND current_revision=? AND terminal_event_id IS NULL`, terminalID, candidate.id, candidate.revision); updateErr != nil {
			return sqliteConflict(updateErr)
		}
		auditKind := map[string]string{"expired": "input_expired", "cancelled": "input_cancelled", "run_terminal": "input_run_terminal"}[candidate.kind]
		if err := insertInputEvent(ctx, authz.tx, candidate.id, candidate.revision, auditKind); err != nil {
			return err
		}
		switch candidate.kind {
		case "expired":
			projection.ExpiredInputs++
		case "cancelled":
			projection.CancelledInputs++
		case "run_terminal":
			projection.TerminalInputs++
		}
	}

	return s.reconcileOutboxTx(ctx, authz, request, now, limit, projection)
}

func (s *Service) reconcileOutboxTx(ctx context.Context, authz *authorizedTx, request ReconcileRequest, now string, limit int,
	projection *ReconcileProjection) error {
	rows, err := authz.tx.QueryContext(ctx, `WITH bound(now) AS (VALUES(?))
		SELECT outbox.command_id,outbox.delivery_state,
		CASE WHEN outbox.delivery_state='claimed' THEN 'runner_lost'
		 WHEN EXISTS(SELECT 1 FROM control_capability_grants grant_row WHERE grant_row.grant_id=command.grant_id
		  AND grant_row.revision=command.grant_revision AND grant_row.revoked_at IS NOT NULL) THEN 'capability_revoked'
		 WHEN command.grant_expires_at<=(SELECT now FROM bound) THEN 'capability_expired'
		 WHEN EXISTS(SELECT 1 FROM control_capability_leases lease WHERE lease.lease_id=command.lease_id
		  AND lease.revision=command.lease_revision AND lease.revoked_at IS NOT NULL) THEN 'lease_revoked'
		 ELSE 'lease_expired' END
		FROM control_outbox outbox
		JOIN control_commands command ON command.command_id=outbox.command_id
		WHERE outbox.lease_id=? AND outbox.lease_revision=?
		 AND ((outbox.delivery_state='queued' AND (NOT EXISTS(
		 SELECT 1 FROM control_capability_grants grant_row WHERE grant_row.grant_id=command.grant_id
		  AND grant_row.revision=command.grant_revision AND grant_row.revoked_at IS NULL
		  AND grant_row.expires_at>(SELECT now FROM bound)) OR NOT EXISTS(
		 SELECT 1 FROM control_capability_leases lease WHERE lease.lease_id=command.lease_id
		  AND lease.revision=command.lease_revision AND lease.revoked_at IS NULL
		  AND lease.user_id=? AND lease.actor_api_key_id=? AND lease.device_id=?
		  AND lease.expires_at>(SELECT now FROM bound))))
		 OR (outbox.delivery_state='claimed' AND outbox.safe_reason IS NULL AND command.outcome IS NULL
		 AND outbox.claim_user_id=? AND outbox.claim_api_key_id=? AND outbox.claim_device_id=?
		 AND NOT EXISTS(
		 SELECT 1 FROM control_capability_leases lease
		 JOIN api_keys runner_key ON runner_key.id=lease.actor_api_key_id AND runner_key.user_id=lease.user_id
		  AND runner_key.disabled_at IS NULL
		  AND (runner_key.expires_at IS NULL OR runner_key.expires_at>(SELECT now FROM bound))
		 JOIN users runner_user ON runner_user.id=lease.user_id AND runner_user.status='active'
		 JOIN deliveries delivery ON delivery.id=lease.delivery_id AND delivery.issue_id=lease.root_issue_id
		 JOIN issues issue ON issue.id=lease.root_issue_id AND issue.deleted_at IS NULL
		 JOIN projects project ON project.id=issue.project_id AND project.status IN ('active','frozen','archived')
		 JOIN issue_control_revisions issue_revision ON issue_revision.issue_id=issue.id
		  AND issue_revision.revision=lease.issue_revision
		 JOIN agent_runs run ON run.id=lease.agent_run_id AND run.status='running'
		 JOIN delivery_agent_run_activations activation ON activation.delivery_id=lease.delivery_id
		  AND activation.attempt_id=lease.attempt_id AND activation.stage_key=lease.stage_key
		  AND activation.execution_number=lease.execution_number AND activation.authority_epoch=lease.authority_epoch
		  AND activation.authority_stage_event_id=lease.authority_stage_event_id
		  AND activation.reporter_id=lease.reporter_id AND activation.agent_run_id=lease.agent_run_id
		 JOIN delivery_stage_latest latest ON latest.delivery_id=lease.delivery_id AND latest.attempt_id=lease.attempt_id
		  AND latest.stage_key=lease.stage_key AND latest.execution_number=lease.execution_number
		  AND latest.execution_start_stage_event_id=lease.execution_start_stage_event_id
		  AND latest.authority_epoch=lease.authority_epoch AND latest.authority_stage_event_id=lease.authority_stage_event_id
		  AND latest.current_reporter_id=lease.reporter_id
		 WHERE lease.lease_id=outbox.lease_id AND lease.revision=outbox.lease_revision
		  AND lease.user_id=? AND lease.actor_api_key_id=? AND lease.device_id=?
		  AND lease.revoked_at IS NULL AND lease.expires_at>(SELECT now FROM bound)
		  AND lease.user_id=outbox.claim_user_id AND lease.actor_api_key_id=outbox.claim_api_key_id
		  AND lease.device_id=outbox.claim_device_id
		  AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
		   WHERE event.delivery_id=delivery.id),0)=lease.delivery_revision)))
		ORDER BY outbox.id LIMIT ?`, now, request.LeaseID, request.LeaseRevision,
		authz.principal.UserID(), authz.principal.APIKeyID(), request.DeviceID,
		authz.principal.UserID(), authz.principal.APIKeyID(), request.DeviceID,
		authz.principal.UserID(), authz.principal.APIKeyID(), request.DeviceID, limit)
	if err != nil {
		return storageError(ctx, err)
	}
	type candidate struct{ id, state, reason string }
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.state, &item.reason); err != nil {
			rows.Close()
			return storageError(ctx, err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return storageError(ctx, err)
	}
	for _, item := range candidates {
		if item.state == "claimed" {
			result, updateErr := authz.tx.ExecContext(ctx, `UPDATE control_commands SET status_revision=status_revision+1,
				outcome='outcome_unknown',safe_reason='runner_lost',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
				WHERE command_id=? AND status='accepted' AND outcome IS NULL`, item.id)
			if updateErr != nil {
				return sqliteConflict(updateErr)
			}
			if !changed(result) {
				continue
			}
			if _, updateErr := authz.tx.ExecContext(ctx, `UPDATE control_outbox SET safe_reason='runner_lost',
				updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=? AND delivery_state='claimed'
				AND safe_reason IS NULL`, item.id); updateErr != nil {
				return sqliteConflict(updateErr)
			}
			if err := insertCommandEvent(ctx, authz.tx, item.id, "effect_outcome_unknown"); err != nil {
				return err
			}
			projection.UnknownOutcomes++
			continue
		}
		digest := canonicalDigest("effect.abandoned", stringField("command_id", item.id), stringField("reason", item.reason))
		result, updateErr := authz.tx.ExecContext(ctx, `UPDATE control_commands SET status='rejected',status_revision=status_revision+1,
			outcome='rejected',safe_reason=?,result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=? AND status='accepted'`,
			item.reason, digest[:], item.id)
		if updateErr != nil {
			return sqliteConflict(updateErr)
		}
		if !changed(result) {
			continue
		}
		if err := insertCommandEvent(ctx, authz.tx, item.id, "command_rejected"); err != nil {
			return err
		}
		if _, updateErr := authz.tx.ExecContext(ctx, `UPDATE control_outbox SET delivery_state='abandoned',safe_reason=?,
			acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=? AND delivery_state='queued'`, item.reason, item.id); updateErr != nil {
			return sqliteConflict(updateErr)
		}
		if err := insertCommandEvent(ctx, authz.tx, item.id, "effect_abandoned"); err != nil {
			return err
		}
		projection.AbandonedEffects++
	}
	return nil
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func changed(result sql.Result) bool {
	count, err := result.RowsAffected()
	return err == nil && count == 1
}
