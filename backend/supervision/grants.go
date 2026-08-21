// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"sort"
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

func targetsForGrant(ctx context.Context, tx *sql.Tx, binding issueBinding, allowed []Action) ([]GrantTarget, error) {
	if binding.projectStatus == "deleted" {
		return nil, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if binding.projectStatus != "active" && binding.projectStatus != "frozen" && binding.projectStatus != "archived" {
		return nil, domainError(ErrNotFound, CodeTargetNotFound)
	}
	var current int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM deliveries delivery
		JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		WHERE delivery.id=? AND delivery.delivery_key=? AND issue.id=? AND issue.project_id=?
		 AND control_revision.revision=? AND COALESCE((SELECT MAX(event.delivery_revision)
		 FROM delivery_events event WHERE event.delivery_id=delivery.id),0)=?`, binding.deliveryID,
		binding.deliveryKey, binding.issueID, binding.projectID, binding.issueRevision, binding.deliveryRevision).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError(ctx, err)
	}
	allowedSet := make(map[Action]bool, len(allowed))
	for _, action := range allowed {
		allowedSet[action] = true
	}
	permitted := func(action Action) bool { return len(allowedSet) == 0 || allowedSet[action] }
	targets := []GrantTarget{}
	if binding.projectStatus == "active" || binding.projectStatus == "frozen" {
		if permitted("issue.priority.set") {
			targets = append(targets, GrantTarget{Action: "issue.priority.set"})
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT run.id
		FROM delivery_agent_run_links link
		JOIN agent_runs run ON run.id=link.agent_run_id AND run.delivery_instrumentation_version=1
		JOIN deliveries delivery ON delivery.id=link.delivery_id AND delivery.issue_id=run.issue_id
		JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		JOIN delivery_stage_latest latest ON latest.delivery_id=link.delivery_id AND latest.attempt_id=link.attempt_id
		 AND latest.stage_key=link.stage_key AND latest.execution_number=link.execution_number
		 AND latest.execution_start_stage_event_id=link.execution_start_stage_event_id
		 AND latest.current_reporter_id=link.reporter_id
		WHERE link.delivery_id=? AND issue.id=? AND control_revision.revision=?
		 AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
		  WHERE event.delivery_id=delivery.id),0)=? AND run.status='queued' ORDER BY run.id`,
		binding.deliveryID, binding.issueID, binding.issueRevision, binding.deliveryRevision)
	if err != nil {
		return nil, storageError(ctx, err)
	}
	for rows.Next() {
		var runID int64
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return nil, storageError(ctx, err)
		}
		if permitted("run.cancel.queued") {
			targets = append(targets, GrantTarget{Action: "run.cancel.queued", RunID: runID})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, storageError(ctx, err)
	}
	rows.Close()

	// Enumerate all current runner-backed non-input targets in one query. The
	// joins intentionally mirror activation, credential and revision authority
	// rather than relying on the later command-acceptance trigger.
	rows, err = tx.QueryContext(ctx, `SELECT lease.agent_run_id,action.action,
		COALESCE(runtime.state,''),COALESCE(runtime.revision,0)
		FROM control_capability_leases lease
		JOIN control_capability_lease_seals seal ON seal.lease_id=lease.lease_id
		 AND seal.lease_revision=lease.revision AND seal.binding_digest=lease.binding_digest
		 AND seal.action_set_digest=lease.action_set_digest AND seal.action_count=lease.action_count
		JOIN control_capability_lease_actions action ON action.lease_id=lease.lease_id
		 AND action.lease_revision=lease.revision
		JOIN api_keys runner_key ON runner_key.id=lease.actor_api_key_id AND runner_key.user_id=lease.user_id
		 AND runner_key.disabled_at IS NULL
		 AND (runner_key.expires_at IS NULL OR runner_key.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		JOIN users runner_user ON runner_user.id=lease.user_id AND runner_user.status='active'
		JOIN agent_runs run ON run.id=lease.agent_run_id AND run.status='running'
		 AND run.delivery_instrumentation_version=1 AND run.device_id=lease.device_id
		JOIN delivery_agent_run_links link ON link.agent_run_id=run.id AND link.delivery_id=lease.delivery_id
		 AND link.attempt_id=lease.attempt_id AND link.stage_key=lease.stage_key
		 AND link.execution_number=lease.execution_number
		 AND link.execution_start_stage_event_id=lease.execution_start_stage_event_id
		 AND link.reporter_id=lease.reporter_id
		JOIN delivery_agent_run_activations activation ON activation.delivery_id=lease.delivery_id
		 AND activation.attempt_id=lease.attempt_id AND activation.stage_key=lease.stage_key
		 AND activation.execution_number=lease.execution_number AND activation.agent_run_id=run.id
		 AND activation.authority_epoch=lease.authority_epoch
		 AND activation.authority_stage_event_id=lease.authority_stage_event_id
		 AND activation.reporter_id=lease.reporter_id
		JOIN delivery_stage_latest latest ON latest.delivery_id=lease.delivery_id
		 AND latest.attempt_id=lease.attempt_id AND latest.stage_key=lease.stage_key
		 AND latest.execution_number=lease.execution_number
		 AND latest.execution_start_stage_event_id=lease.execution_start_stage_event_id
		 AND latest.authority_epoch=lease.authority_epoch
		 AND latest.authority_stage_event_id=lease.authority_stage_event_id
		 AND latest.current_reporter_id=lease.reporter_id
		JOIN deliveries delivery ON delivery.id=lease.delivery_id AND delivery.delivery_key=lease.delivery_key
		 AND delivery.issue_id=lease.root_issue_id
		JOIN issues issue ON issue.id=lease.root_issue_id AND issue.deleted_at IS NULL
		 AND issue.project_id=lease.project_id
		JOIN projects project ON project.id=lease.project_id
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		 AND control_revision.revision=lease.issue_revision
		LEFT JOIN control_runtime_states runtime ON runtime.agent_run_id=run.id
		 AND runtime.delivery_id=lease.delivery_id AND runtime.root_issue_id=lease.root_issue_id
		 AND runtime.attempt_id=lease.attempt_id AND runtime.stage_key=lease.stage_key
		 AND runtime.execution_number=lease.execution_number
		 AND runtime.execution_start_stage_event_id=lease.execution_start_stage_event_id
		WHERE lease.delivery_id=? AND lease.root_issue_id=? AND lease.issue_revision=?
		 AND lease.delivery_revision=? AND lease.revoked_at IS NULL
		 AND lease.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
		  WHERE event.delivery_id=delivery.id),0)=lease.delivery_revision
		 AND project.status IN ('active','frozen','archived')
		 AND action.action IN ('run.cancel.running','run.pause','run.resume')
		ORDER BY CASE action.action WHEN 'run.cancel.running' THEN 1 WHEN 'run.pause' THEN 2
		 WHEN 'run.resume' THEN 3 ELSE 99 END,lease.agent_run_id`, binding.deliveryID,
		binding.issueID, binding.issueRevision, binding.deliveryRevision)
	if err != nil {
		return nil, storageError(ctx, err)
	}
	for rows.Next() {
		var target GrantTarget
		if err := rows.Scan(&target.RunID, &target.Action, &target.RuntimeState, &target.RuntimeRevision); err != nil {
			rows.Close()
			return nil, storageError(ctx, err)
		}
		if !permitted(target.Action) || (binding.projectStatus == "archived" && target.Action != "run.cancel.running") {
			continue
		}
		if target.Action == "run.cancel.running" {
			target.RuntimeState, target.RuntimeRevision = "", 0
		} else if target.Action == "run.pause" && target.RuntimeState != "running" ||
			target.Action == "run.resume" && target.RuntimeState != "paused" || target.RuntimeRevision <= 0 {
			continue
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, storageError(ctx, err)
	}
	rows.Close()

	if binding.projectStatus != "archived" && permitted("input.respond") {
		rows, err = tx.QueryContext(ctx, `SELECT request.request_id,request.revision,request.request_kind,
			COALESCE(option.option_code,'')
			FROM control_input_requests request
			JOIN control_input_request_seals request_seal ON request_seal.request_id=request.request_id
			 AND request_seal.request_revision=request.revision
			JOIN control_input_request_states state ON state.request_id=request.request_id
			 AND state.current_revision=request.revision AND state.terminal_event_id IS NULL
			JOIN control_capability_leases lease ON lease.lease_id=request.lease_id
			 AND lease.revision=request.lease_revision AND lease.delivery_id=request.delivery_id
			 AND lease.delivery_revision=request.delivery_revision AND lease.root_issue_id=request.root_issue_id
			 AND lease.issue_revision=request.issue_revision AND lease.agent_run_id=request.agent_run_id
			JOIN control_capability_lease_seals lease_seal ON lease_seal.lease_id=lease.lease_id
			 AND lease_seal.lease_revision=lease.revision AND lease_seal.binding_digest=lease.binding_digest
			 AND lease_seal.action_set_digest=lease.action_set_digest AND lease_seal.action_count=lease.action_count
			JOIN control_capability_lease_actions action ON action.lease_id=lease.lease_id
			 AND action.lease_revision=lease.revision AND action.action='input.respond'
			JOIN api_keys runner_key ON runner_key.id=lease.actor_api_key_id AND runner_key.user_id=lease.user_id
			 AND runner_key.disabled_at IS NULL
			 AND (runner_key.expires_at IS NULL OR runner_key.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			JOIN users runner_user ON runner_user.id=lease.user_id AND runner_user.status='active'
			JOIN agent_runs run ON run.id=lease.agent_run_id AND run.status='running'
			 AND run.delivery_instrumentation_version=1 AND run.device_id=lease.device_id
			JOIN delivery_agent_run_links link ON link.agent_run_id=run.id AND link.delivery_id=lease.delivery_id
			 AND link.attempt_id=lease.attempt_id AND link.stage_key=lease.stage_key
			 AND link.execution_number=lease.execution_number
			 AND link.execution_start_stage_event_id=lease.execution_start_stage_event_id
			 AND link.reporter_id=lease.reporter_id
			JOIN delivery_agent_run_activations activation ON activation.delivery_id=lease.delivery_id
			 AND activation.attempt_id=lease.attempt_id AND activation.stage_key=lease.stage_key
			 AND activation.execution_number=lease.execution_number AND activation.agent_run_id=run.id
			 AND activation.authority_epoch=lease.authority_epoch
			 AND activation.authority_stage_event_id=lease.authority_stage_event_id
			 AND activation.reporter_id=lease.reporter_id
			JOIN delivery_stage_latest latest ON latest.delivery_id=lease.delivery_id
			 AND latest.attempt_id=lease.attempt_id AND latest.stage_key=lease.stage_key
			 AND latest.execution_number=lease.execution_number
			 AND latest.execution_start_stage_event_id=lease.execution_start_stage_event_id
			 AND latest.authority_epoch=lease.authority_epoch
			 AND latest.authority_stage_event_id=lease.authority_stage_event_id
			 AND latest.current_reporter_id=lease.reporter_id
			JOIN deliveries delivery ON delivery.id=lease.delivery_id AND delivery.delivery_key=lease.delivery_key
			 AND delivery.issue_id=lease.root_issue_id
			JOIN issues issue ON issue.id=lease.root_issue_id AND issue.deleted_at IS NULL
			 AND issue.project_id=lease.project_id
			JOIN projects project ON project.id=lease.project_id AND project.status IN ('active','frozen')
			JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
			 AND control_revision.revision=lease.issue_revision
			LEFT JOIN control_input_request_options option ON option.request_id=request.request_id
			 AND option.request_revision=request.revision
			WHERE request.delivery_id=? AND request.root_issue_id=? AND request.issue_revision=?
			 AND request.delivery_revision=? AND request.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')
			 AND lease.revoked_at IS NULL AND lease.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')
			 AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
			  WHERE event.delivery_id=delivery.id),0)=lease.delivery_revision
			ORDER BY request.request_id,request.revision,option.ordinal`, binding.deliveryID,
			binding.issueID, binding.issueRevision, binding.deliveryRevision)
		if err != nil {
			return nil, storageError(ctx, err)
		}
		var current *GrantTarget
		for rows.Next() {
			var requestID, kind, option string
			var revision int64
			if err := rows.Scan(&requestID, &revision, &kind, &option); err != nil {
				rows.Close()
				return nil, storageError(ctx, err)
			}
			if current == nil || current.InputRequestID != requestID || current.InputRequestRevision != revision {
				targets = append(targets, GrantTarget{Action: "input.respond", InputRequestID: requestID,
					InputRequestRevision: revision, InputKind: InputKind(kind)})
				current = &targets[len(targets)-1]
			}
			if option != "" {
				current.OptionCodes = append(current.OptionCodes, option)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, storageError(ctx, err)
		}
		rows.Close()
	}

	actionRank := make(map[Action]int, len(Actions()))
	for index, action := range Actions() {
		actionRank[action] = index
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if actionRank[targets[i].Action] != actionRank[targets[j].Action] {
			return actionRank[targets[i].Action] < actionRank[targets[j].Action]
		}
		if targets[i].RunID != targets[j].RunID {
			return targets[i].RunID < targets[j].RunID
		}
		if targets[i].InputRequestID != targets[j].InputRequestID {
			return targets[i].InputRequestID < targets[j].InputRequestID
		}
		return targets[i].InputRequestRevision < targets[j].InputRequestRevision
	})
	return dedupeTargets(targets), nil
}

func dedupeTargets(targets []GrantTarget) []GrantTarget {
	out := make([]GrantTarget, 0, len(targets))
	for _, target := range targets {
		if len(out) > 0 {
			prior := out[len(out)-1]
			if prior.Action == target.Action && prior.RunID == target.RunID &&
				prior.RuntimeRevision == target.RuntimeRevision && prior.InputRequestID == target.InputRequestID &&
				prior.InputRequestRevision == target.InputRequestRevision {
				continue
			}
		}
		out = append(out, target)
	}
	return out
}

func actionsFromTargets(targets []GrantTarget) []Action {
	seen := make(map[Action]bool, len(targets))
	for _, target := range targets {
		seen[target.Action] = true
	}
	out := make([]Action, 0, len(seen))
	for _, action := range Actions() {
		if seen[action] {
			out = append(out, action)
		}
	}
	return out
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
	targets, err := targetsForGrant(ctx, authz.tx, binding, nil)
	if err != nil {
		return GrantProjection{}, err
	}
	actions := actionsFromTargets(targets)
	if len(actions) == 0 {
		return GrantProjection{}, domainError(ErrConflict, CodeCapabilityUnavailable)
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "grant.put", keyDigest, requestDigest)
	if err != nil {
		return GrantProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadGrantProjectionTx(ctx, authz.tx, authz.principal, replay.grantID.String, 0, true)
		if loadErr != nil {
			return GrantProjection{}, loadErr
		}
		if bindErr := requireCurrentGrantBindingTx(ctx, authz.tx, authz.principal, projection.GrantID, projection.Revision); bindErr != nil {
			return GrantProjection{}, bindErr
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
	deadline := s.controlDeadline(s.grantTTL)
	if current && expiredAt(s.clock.Now().UTC(), expiresAt) {
		if err := revokeGrantRow(ctx, authz.tx, grantID, revision, "grant_expired", "capability_expired"); err != nil {
			return GrantProjection{}, err
		}
		current = false
		revision++
	}
	if current && currentCredential == credential && bytes.Equal(currentBinding, binding.bindingDigest[:]) && bytes.Equal(currentActions, actionDigest[:]) {
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_capability_grants SET
			expires_at=?,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=?`, deadline, grantID, revision); err != nil {
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
		if err := insertGrant(ctx, authz.tx, authz.principal, binding, grantID, revision, actions, actionDigest, deadline); err != nil {
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
	projection, err := loadGrantProjectionTx(ctx, authz.tx, authz.principal, grantID, 0, true)
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
	actions []Action, actionDigest [32]byte, expiresAt string) error {
	kind, session, apiKey, ok := credentialColumns(principal)
	if !ok {
		return domainError(ErrForbidden, CodeCredentialRevoked)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_capability_grants(
		grant_id,revision,actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,
		delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,issue_etag_digest,
		binding_digest,action_set_digest,action_count,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		grantID, revision, principal.UserID(), principal.UserID(), kind, session, apiKey,
		binding.deliveryID, binding.deliveryKey, binding.deliveryRevision, binding.projectID, binding.issueID,
		binding.issueRevision, binding.etagDigest[:], binding.bindingDigest[:], actionDigest[:], len(actions), expiresAt)
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

func loadGrantProjectionTx(ctx context.Context, tx *sql.Tx, principal auth.Principal, grantID string, exactRevision int64,
	requireLive bool) (GrantProjection, error) {
	if !validUUID(grantID) {
		return GrantProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	kind, session, apiKey, ok := credentialColumns(principal)
	if !ok {
		return GrantProjection{}, domainError(ErrForbidden, CodeCredentialRevoked)
	}
	var projection GrantProjection
	var expiry string
	var binding issueBinding
	query := `SELECT grant.grant_id,grant.revision,grant.delivery_key,project.key||'-'||issue.issue_number,
		grant.expires_at,grant.delivery_id,grant.delivery_revision,grant.project_id,project.status,
		grant.root_issue_id,issue.issue_number,grant.issue_revision,project.key
		FROM control_capability_grants grant
		JOIN deliveries delivery ON delivery.id=grant.delivery_id AND delivery.delivery_key=grant.delivery_key
		 AND delivery.issue_id=grant.root_issue_id
		JOIN issues issue ON issue.id=grant.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=grant.project_id AND project.id=issue.project_id
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		WHERE grant.grant_id=? AND grant.user_id=? AND grant.principal_kind=?
		 AND grant.actor_session_credential_id IS ? AND grant.actor_api_key_id IS ?`
	args := []any{grantID, principal.UserID(), kind, session, apiKey}
	if exactRevision > 0 {
		query += ` AND grant.revision=?`
		args = append(args, exactRevision)
	}
	if requireLive {
		query += ` AND grant.revision=(SELECT MAX(revision) FROM control_capability_grants WHERE grant_id=grant.grant_id)
		 AND grant.revoked_at IS NULL AND grant.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	}
	query += ` ORDER BY grant.revision DESC LIMIT 1`
	err := tx.QueryRowContext(ctx, query, args...).Scan(&projection.GrantID, &projection.Revision,
		&projection.DeliveryKey, &projection.IssueKey, &expiry, &binding.deliveryID, &binding.deliveryRevision,
		&binding.projectID, &binding.projectStatus, &binding.issueID, &binding.issueNumber,
		&binding.issueRevision, &binding.projectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return GrantProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err != nil {
		return GrantProjection{}, storageError(ctx, err)
	}
	binding.deliveryKey = projection.DeliveryKey
	rows, err := tx.QueryContext(ctx, `SELECT action FROM control_capability_grant_actions
		WHERE grant_id=? AND grant_revision=? ORDER BY CASE action
		WHEN 'issue.priority.set' THEN 1 WHEN 'run.cancel.queued' THEN 2 WHEN 'run.cancel.running' THEN 3
		WHEN 'input.respond' THEN 4 WHEN 'run.pause' THEN 5 WHEN 'run.resume' THEN 6 ELSE 99 END`,
		projection.GrantID, projection.Revision)
	if err != nil {
		return GrantProjection{}, storageError(ctx, err)
	}
	for rows.Next() {
		var action Action
		if err := rows.Scan(&action); err != nil {
			return GrantProjection{}, storageError(ctx, err)
		}
		projection.Actions = append(projection.Actions, action)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return GrantProjection{}, storageError(ctx, err)
	}
	sealedActions := projection.Actions
	rows.Close()
	if requireLive {
		projection.Targets, err = targetsForGrant(ctx, tx, binding, sealedActions)
		if err != nil {
			return GrantProjection{}, err
		}
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
	projection, err := loadGrantProjectionTx(ctx, authz.tx, authz.principal, request.GrantID, 0, true)
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
	kind, session, apiKey, ok := credentialColumns(authz.principal)
	if !ok {
		return GrantProjection{}, domainError(ErrForbidden, CodeCredentialRevoked)
	}
	var projectID int64
	if err := authz.tx.QueryRowContext(ctx, `SELECT project_id FROM control_capability_grants
		WHERE grant_id=? AND revision=? AND user_id=? AND principal_kind=?
		 AND actor_session_credential_id IS ? AND actor_api_key_id IS ?`, request.GrantID, request.Revision,
		authz.principal.UserID(), kind, session, apiKey).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GrantProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
		}
		return GrantProjection{}, storageError(ctx, err)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, projectID); err != nil {
		return GrantProjection{}, err
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "grant.revoke", keyDigest, requestDigest)
	if err != nil {
		return GrantProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadGrantProjectionTx(ctx, authz.tx, authz.principal, request.GrantID, request.Revision, false)
		if loadErr != nil {
			return GrantProjection{}, loadErr
		}
		if err := s.finishRead(authz); err != nil {
			return GrantProjection{}, err
		}
		return projection, nil
	}
	projection, err := loadGrantProjectionTx(ctx, authz.tx, authz.principal, request.GrantID, request.Revision, true)
	if err != nil {
		return GrantProjection{}, err
	}
	if projection.Revision != request.Revision {
		return GrantProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if err := revokeGrantRow(ctx, authz.tx, request.GrantID, request.Revision, "grant_revoked", "capability_revoked"); err != nil {
		return GrantProjection{}, err
	}
	projection.Targets = nil
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
