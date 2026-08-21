// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
)

type issueBinding struct {
	deliveryID       int64
	deliveryKey      string
	deliveryRevision int64
	projectID        int64
	projectStatus    string
	issueID          int64
	issueNumber      int64
	issueRevision    int64
	issueUpdatedAt   string
	issuePriority    string
	projectKey       string
	etagDigest       [32]byte
	bindingDigest    [32]byte
}

func resolveIssueBinding(ctx context.Context, tx *sql.Tx, deliveryID int64) (issueBinding, error) {
	if deliveryID <= 0 {
		return issueBinding{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	var binding issueBinding
	err := tx.QueryRowContext(ctx, `SELECT delivery.id,delivery.delivery_key,
		COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event WHERE event.delivery_id=delivery.id),0),
		issue.project_id,project.status,issue.id,issue.issue_number,control_revision.revision,
		issue.updated_at,issue.priority,project.key
		FROM deliveries delivery
		JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		WHERE delivery.id=?`, deliveryID).Scan(&binding.deliveryID, &binding.deliveryKey,
		&binding.deliveryRevision, &binding.projectID, &binding.projectStatus, &binding.issueID,
		&binding.issueNumber, &binding.issueRevision, &binding.issueUpdatedAt,
		&binding.issuePriority, &binding.projectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return issueBinding{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err != nil {
		return issueBinding{}, storageError(ctx, err)
	}
	etag := `"issue-` + strconv.FormatInt(binding.issueID, 10) + `-` + strings.ReplaceAll(binding.issueUpdatedAt, " ", "T") + `"`
	binding.etagDigest = sha256.Sum256([]byte(etag))
	binding.bindingDigest = canonicalDigest("issue-binding",
		intField("delivery_id", binding.deliveryID), stringField("delivery_key", binding.deliveryKey),
		intField("delivery_revision", binding.deliveryRevision), intField("project_id", binding.projectID),
		intField("issue_id", binding.issueID), intField("issue_revision", binding.issueRevision),
		stringField("issue_etag_digest", digestHex(binding.etagDigest)))
	return binding, nil
}

func (binding issueBinding) issueKey() string {
	return binding.projectKey + "-" + strconv.FormatInt(binding.issueNumber, 10)
}

func actionsForGrant(ctx context.Context, tx *sql.Tx, binding issueBinding) ([]Action, error) {
	if binding.projectStatus == "deleted" {
		return nil, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if binding.projectStatus != "active" && binding.projectStatus != "frozen" && binding.projectStatus != "archived" {
		return nil, domainError(ErrNotFound, CodeTargetNotFound)
	}
	actions := []Action{}
	if binding.projectStatus == "active" || binding.projectStatus == "frozen" {
		actions = append(actions, "issue.priority.set")
	}
	var runID int64
	var status string
	err := tx.QueryRowContext(ctx, `SELECT run.id,run.status
		FROM delivery_agent_run_links link
		JOIN agent_runs run ON run.id=link.agent_run_id AND run.delivery_instrumentation_version=1
		JOIN delivery_stage_latest latest ON latest.delivery_id=link.delivery_id AND latest.attempt_id=link.attempt_id
		 AND latest.stage_key=link.stage_key AND latest.execution_number=link.execution_number
		 AND latest.execution_start_stage_event_id=link.execution_start_stage_event_id
		 AND latest.current_reporter_id=link.reporter_id
		WHERE link.delivery_id=? AND run.status IN ('queued','running')
		ORDER BY run.id DESC LIMIT 1`, binding.deliveryID).Scan(&runID, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, storageError(ctx, err)
	}
	if err == nil && status == "queued" {
		actions = append(actions, "run.cancel.queued")
	}
	if err == nil && status == "running" {
		rows, queryErr := tx.QueryContext(ctx, `SELECT action.action
			FROM control_capability_leases lease
			JOIN control_capability_lease_seals seal ON seal.lease_id=lease.lease_id AND seal.lease_revision=lease.revision
			JOIN control_capability_lease_actions action ON action.lease_id=lease.lease_id AND action.lease_revision=lease.revision
			WHERE lease.agent_run_id=? AND lease.delivery_id=? AND lease.issue_revision=?
			 AND lease.delivery_revision=? AND lease.revoked_at IS NULL
			 AND lease.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')
			ORDER BY CASE action.action
			 WHEN 'run.cancel.running' THEN 1 WHEN 'input.respond' THEN 2
			 WHEN 'run.pause' THEN 3 WHEN 'run.resume' THEN 4 ELSE 99 END`,
			runID, binding.deliveryID, binding.issueRevision, binding.deliveryRevision)
		if queryErr != nil {
			return nil, storageError(ctx, queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			var action Action
			if scanErr := rows.Scan(&action); scanErr != nil {
				return nil, storageError(ctx, scanErr)
			}
			if action == "run.cancel.running" || binding.projectStatus != "archived" {
				actions = append(actions, action)
			}
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return nil, storageError(ctx, rowsErr)
		}
	}
	return canonicalActions(actions, false)
}

func (s *Service) IssueActorGrant(ctx context.Context, principal auth.Principal, request GrantIssueRequest) (GrantProjection, error) {
	keyDigest, err := operationKeyDigest(request.OperationKeyDigest)
	if err != nil || request.DeliveryID <= 0 {
		if err != nil {
			return GrantProjection{}, err
		}
		return GrantProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	requestDigest := canonicalDigest("grant.put", intField("delivery_id", request.DeliveryID))
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeActorWrite)
	if err != nil {
		return GrantProjection{}, err
	}
	defer authz.tx.Rollback()
	binding, err := resolveIssueBinding(ctx, authz.tx, request.DeliveryID)
	if err != nil {
		return GrantProjection{}, err
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, binding.projectID); err != nil {
		return GrantProjection{}, err
	}
	actions, err := actionsForGrant(ctx, authz.tx, binding)
	if err != nil {
		return GrantProjection{}, err
	}
	if len(actions) == 0 {
		return GrantProjection{}, domainError(ErrConflict, CodeCapabilityUnavailable)
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "grant.put", keyDigest, requestDigest)
	if err != nil {
		return GrantProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadGrantProjectionTx(ctx, authz.tx, authz.principal.UserID(), replay.grantID.String, true)
		if loadErr != nil {
			return GrantProjection{}, loadErr
		}
		if finishErr := s.finishRead(authz); finishErr != nil {
			return GrantProjection{}, finishErr
		}
		return projection, nil
	}

	actionDigest := digestActions(actions)
	grantID, revision, current, currentCredential, currentBinding, currentActions, expiresAt, err := currentGrantTx(ctx, authz.tx, authz.principal.UserID(), binding.deliveryID)
	if err != nil {
		return GrantProjection{}, err
	}
	credential := authz.principal.SafeCredentialID()
	if current && expiredAt(s.clock.Now().UTC(), expiresAt) {
		if err := revokeGrantRow(ctx, authz.tx, grantID, revision, "grant_expired", "capability_expired"); err != nil {
			return GrantProjection{}, err
		}
		current = false
		revision++
	}
	if current && currentCredential == credential && bytes.Equal(currentBinding, binding.bindingDigest[:]) && bytes.Equal(currentActions, actionDigest[:]) {
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_capability_grants SET
			expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+5 minutes'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=?`, grantID, revision); err != nil {
			return GrantProjection{}, sqliteConflict(err)
		}
		if err := insertGrantEvent(ctx, authz.tx, grantID, revision, "grant_renewed", ""); err != nil {
			return GrantProjection{}, err
		}
	} else {
		if current {
			reason := "authority_changed"
			if currentCredential != credential {
				reason = "credential_revoked"
			}
			if err := revokeGrantRow(ctx, authz.tx, grantID, revision, "grant_revoked", reason); err != nil {
				return GrantProjection{}, err
			}
			revision++
		}
		if grantID == "" {
			grantID, err = safeID(s.ids)
			if err != nil {
				return GrantProjection{}, err
			}
			revision = 1
		}
		if err := insertGrant(ctx, authz.tx, authz.principal, binding, grantID, revision, actions, actionDigest); err != nil {
			return GrantProjection{}, err
		}
		kind := "grant_issued"
		if revision > 1 {
			kind = "grant_renewed"
		}
		if err := insertGrantEvent(ctx, authz.tx, grantID, revision, kind, ""); err != nil {
			return GrantProjection{}, err
		}
	}
	projection, err := loadGrantProjectionTx(ctx, authz.tx, authz.principal.UserID(), grantID, true)
	if err != nil {
		return GrantProjection{}, err
	}
	resultDigest := grantProjectionDigest(projection)
	if err := insertOperation(ctx, authz.tx, authz.principal, "grant.put", keyDigest, requestDigest, resultDigest, "grant_id", grantID); err != nil {
		return GrantProjection{}, err
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return GrantProjection{}, err
	}
	return projection, nil
}

func currentGrantTx(ctx context.Context, tx *sql.Tx, userID, deliveryID int64) (id string, revision int64, current bool,
	credential string, binding, actions []byte, expiresAt time.Time, returnErr error) {
	var session sql.NullString
	var apiKey sql.NullInt64
	var expiry string
	err := tx.QueryRowContext(ctx, `SELECT grant_id,revision,actor_session_credential_id,actor_api_key_id,
		binding_digest,action_set_digest,expires_at,revoked_at IS NULL
		FROM control_capability_grants WHERE user_id=? AND delivery_id=?
		ORDER BY revision DESC LIMIT 1`, userID, deliveryID).
		Scan(&id, &revision, &session, &apiKey, &binding, &actions, &expiry, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, "", nil, nil, time.Time{}, nil
	}
	if err != nil {
		return "", 0, false, "", nil, nil, time.Time{}, storageError(ctx, err)
	}
	parsed, err := parseControlTime(expiry)
	if err != nil {
		return "", 0, false, "", nil, nil, time.Time{}, err
	}
	if session.Valid {
		credential = session.String
	} else if apiKey.Valid {
		credential = strconv.FormatInt(apiKey.Int64, 10)
	}
	return id, revision, current, credential, binding, actions, parsed, nil
}

func insertGrant(ctx context.Context, tx *sql.Tx, principal auth.Principal, binding issueBinding, grantID string, revision int64,
	actions []Action, actionDigest [32]byte) error {
	kind, session, apiKey, ok := credentialColumns(principal)
	if !ok {
		return domainError(ErrForbidden, CodeCredentialRevoked)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_capability_grants(
		grant_id,revision,actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,
		delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,issue_etag_digest,
		binding_digest,action_set_digest,action_count,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now','+5 minutes'))`,
		grantID, revision, principal.UserID(), principal.UserID(), kind, session, apiKey,
		binding.deliveryID, binding.deliveryKey, binding.deliveryRevision, binding.projectID, binding.issueID,
		binding.issueRevision, binding.etagDigest[:], binding.bindingDigest[:], actionDigest[:], len(actions))
	if err != nil {
		return sqliteConflict(err)
	}
	for _, action := range actions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action)
			VALUES(?,?,?)`, grantID, revision, action); err != nil {
			return sqliteConflict(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_capability_grant_seals(
		grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,?,?,?,?)`,
		grantID, revision, binding.bindingDigest[:], actionDigest[:], len(actions)); err != nil {
		return sqliteConflict(err)
	}
	return nil
}

func revokeGrantRow(ctx context.Context, tx *sql.Tx, id string, revision int64, eventKind, reason string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE control_capability_grants SET
		revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE grant_id=? AND revision=? AND revoked_at IS NULL`, id, revision); err != nil {
		return sqliteConflict(err)
	}
	return insertGrantEvent(ctx, tx, id, revision, eventKind, reason)
}

func insertGrantEvent(ctx context.Context, tx *sql.Tx, id string, revision int64, eventKind, reason string) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM control_events WHERE grant_id=?`, id).Scan(&sequence); err != nil {
		return storageError(ctx, err)
	}
	var safeReason any
	if reason != "" {
		safeReason = reason
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_events(
		sequence,event_kind,grant_id,grant_revision,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,
		binding_digest,action_set_digest,subject_expires_at,subject_updated_at,safe_reason)
		SELECT ?,?,grant_id,revision,actor_user_id,user_id,principal_kind,
		 actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,
		 binding_digest,action_set_digest,expires_at,updated_at,?
		FROM control_capability_grants WHERE grant_id=? AND revision=?`, sequence, eventKind, safeReason, id, revision)
	if err != nil {
		return sqliteConflict(err)
	}
	return nil
}

func loadGrantProjectionTx(ctx context.Context, tx *sql.Tx, userID int64, grantID string, requireLive bool) (GrantProjection, error) {
	if !validUUID(grantID) {
		return GrantProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	var projection GrantProjection
	var expiry string
	var projectID int64
	query := `SELECT grant.grant_id,grant.revision,grant.delivery_key,project.key||'-'||issue.issue_number,
		grant.expires_at,grant.project_id
		FROM control_capability_grants grant
		JOIN issues issue ON issue.id=grant.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id
		WHERE grant.grant_id=? AND grant.user_id=?`
	if requireLive {
		query += ` AND grant.revision=(SELECT MAX(revision) FROM control_capability_grants WHERE grant_id=grant.grant_id)
		 AND grant.revoked_at IS NULL AND grant.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	}
	query += ` ORDER BY grant.revision DESC LIMIT 1`
	err := tx.QueryRowContext(ctx, query, grantID, userID).Scan(&projection.GrantID, &projection.Revision,
		&projection.DeliveryKey, &projection.IssueKey, &expiry, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return GrantProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err != nil {
		return GrantProjection{}, storageError(ctx, err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT action FROM control_capability_grant_actions
		WHERE grant_id=? AND grant_revision=? ORDER BY CASE action
		WHEN 'issue.priority.set' THEN 1 WHEN 'run.cancel.queued' THEN 2 WHEN 'run.cancel.running' THEN 3
		WHEN 'input.respond' THEN 4 WHEN 'run.pause' THEN 5 WHEN 'run.resume' THEN 6 ELSE 99 END`,
		projection.GrantID, projection.Revision)
	if err != nil {
		return GrantProjection{}, storageError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var action Action
		if err := rows.Scan(&action); err != nil {
			return GrantProjection{}, storageError(ctx, err)
		}
		projection.Actions = append(projection.Actions, action)
	}
	if err := requireProjectStatusForActions(ctx, tx, projectID, projection.Actions); err != nil {
		return GrantProjection{}, err
	}
	projection.ExpiresAt, err = parseControlTime(expiry)
	return projection, err
}

func requireProjectStatusForActions(ctx context.Context, tx *sql.Tx, projectID int64, actions []Action) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM projects WHERE id=?`, projectID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainError(ErrNotFound, CodeTargetNotFound)
		}
		return storageError(ctx, err)
	}
	if status == "active" || status == "frozen" {
		return nil
	}
	if status == "archived" {
		for _, action := range actions {
			if action != "run.cancel.queued" && action != "run.cancel.running" {
				return domainError(ErrConflict, CodeStaleTarget)
			}
		}
		return nil
	}
	return domainError(ErrNotFound, CodeTargetNotFound)
}

func grantProjectionDigest(projection GrantProjection) [32]byte {
	fields := []digestField{stringField("grant_id", projection.GrantID), intField("revision", projection.Revision),
		stringField("delivery_key", projection.DeliveryKey), stringField("issue_key", projection.IssueKey),
		stringField("expires_at", projection.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z"))}
	fields = append(fields, actionFields("action", projection.Actions)...)
	return canonicalDigest("grant-projection", fields...)
}

func requireCurrentGrantBindingTx(ctx context.Context, tx *sql.Tx, principal auth.Principal, grantID string, revision int64) error {
	kind, session, apiKey, ok := credentialColumns(principal)
	if !ok {
		return domainError(ErrForbidden, CodeCredentialRevoked)
	}
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM control_capability_grants grant
		JOIN deliveries delivery ON delivery.id=grant.delivery_id AND delivery.delivery_key=grant.delivery_key
		 AND delivery.issue_id=grant.root_issue_id
		JOIN issues issue ON issue.id=grant.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=grant.project_id AND project.id=issue.project_id
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		WHERE grant.grant_id=? AND grant.revision=? AND grant.user_id=? AND grant.principal_kind=?
		 AND grant.actor_session_credential_id IS ? AND grant.actor_api_key_id IS ?
		 AND grant.revoked_at IS NULL AND grant.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 AND control_revision.revision=grant.issue_revision
		 AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
		 WHERE event.delivery_id=delivery.id),0)=grant.delivery_revision
		 AND (project.status IN ('active','frozen') OR (project.status='archived' AND NOT EXISTS(
		 SELECT 1 FROM control_capability_grant_actions action WHERE action.grant_id=grant.grant_id
		 AND action.grant_revision=grant.revision AND action.action NOT IN ('run.cancel.queued','run.cancel.running'))))`,
		grantID, revision, principal.UserID(), kind, session, apiKey).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return domainError(ErrConflict, CodeStaleTarget)
	}
	if err != nil {
		return storageError(ctx, err)
	}
	return nil
}

func (s *Service) GetActorGrant(ctx context.Context, principal auth.Principal, request GrantGetRequest) (GrantProjection, error) {
	if !validUUID(request.GrantID) || request.Revision < 0 {
		return GrantProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	authz, err := s.beginAuthorized(ctx, principal, true, ScopeActorWrite)
	if err != nil {
		return GrantProjection{}, err
	}
	defer authz.tx.Rollback()
	projection, err := loadGrantProjectionTx(ctx, authz.tx, authz.principal.UserID(), request.GrantID, true)
	if err != nil {
		return GrantProjection{}, err
	}
	if request.Revision > 0 && projection.Revision != request.Revision {
		return GrantProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if err := requireCurrentGrantBindingTx(ctx, authz.tx, authz.principal, projection.GrantID, projection.Revision); err != nil {
		return GrantProjection{}, err
	}
	var projectID int64
	if err := authz.tx.QueryRowContext(ctx, `SELECT project_id FROM control_capability_grants
		WHERE grant_id=? AND revision=?`, projection.GrantID, projection.Revision).Scan(&projectID); err != nil {
		return GrantProjection{}, storageError(ctx, err)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, projectID); err != nil {
		return GrantProjection{}, err
	}
	if err := s.finishRead(authz); err != nil {
		return GrantProjection{}, err
	}
	return projection, nil
}

func (s *Service) RevokeActorGrant(ctx context.Context, principal auth.Principal, request GrantRevokeRequest) (GrantProjection, error) {
	keyDigest, err := operationKeyDigest(request.OperationKeyDigest)
	if err != nil || !validUUID(request.GrantID) || request.Revision <= 0 {
		if err != nil {
			return GrantProjection{}, err
		}
		return GrantProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	requestDigest := canonicalDigest("grant.revoke", stringField("grant_id", request.GrantID), intField("revision", request.Revision))
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeActorWrite)
	if err != nil {
		return GrantProjection{}, err
	}
	defer authz.tx.Rollback()
	var projectID, ownerID int64
	if err := authz.tx.QueryRowContext(ctx, `SELECT project_id,user_id FROM control_capability_grants WHERE grant_id=? AND revision=?`,
		request.GrantID, request.Revision).Scan(&projectID, &ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GrantProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
		}
		return GrantProjection{}, storageError(ctx, err)
	}
	if ownerID != authz.principal.UserID() {
		return GrantProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, projectID); err != nil {
		return GrantProjection{}, err
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "grant.revoke", keyDigest, requestDigest)
	if err != nil {
		return GrantProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadGrantProjectionTx(ctx, authz.tx, authz.principal.UserID(), request.GrantID, false)
		if loadErr != nil {
			return GrantProjection{}, loadErr
		}
		if err := s.finishRead(authz); err != nil {
			return GrantProjection{}, err
		}
		return projection, nil
	}
	projection, err := loadGrantProjectionTx(ctx, authz.tx, authz.principal.UserID(), request.GrantID, true)
	if err != nil {
		return GrantProjection{}, err
	}
	if projection.Revision != request.Revision {
		return GrantProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if err := revokeGrantRow(ctx, authz.tx, request.GrantID, request.Revision, "grant_revoked", "capability_revoked"); err != nil {
		return GrantProjection{}, err
	}
	resultDigest := grantProjectionDigest(projection)
	if err := insertOperation(ctx, authz.tx, authz.principal, "grant.revoke", keyDigest, requestDigest, resultDigest,
		"grant_id", request.GrantID); err != nil {
		return GrantProjection{}, err
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return GrantProjection{}, err
	}
	return projection, nil
}
