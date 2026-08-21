// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

const (
	AdminRegistrationsPath      = "/api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations"
	AdminRegistrationRevokePath = "/api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations/{registrationID}/revoke"
	AdminPrerequisiteSetsPath   = "/api/agent-mode/deliveries/{deliveryKey}/external-prerequisite-sets"
)

type RegisterReporterRequest struct {
	APIKeyID      int64         `json:"api_key_id"`
	ReporterClass ReporterClass `json:"reporter_class"`
	ReporterRole  ReporterRole  `json:"reporter_role"`
	DependencyKey string        `json:"dependency_key,omitempty"`
	Workflow      string        `json:"workflow,omitempty"`
	Environment   string        `json:"environment,omitempty"`
}

type ReporterRegistration struct {
	RegistrationID  int64          `json:"registration_id"`
	ReporterID      int64          `json:"reporter_id"`
	APIKeyID        int64          `json:"api_key_id"`
	ReporterClass   ReporterClass  `json:"reporter_class"`
	ReporterRole    ReporterRole   `json:"reporter_role"`
	DependencyKey   string         `json:"dependency_key,omitempty"`
	Workflow        string         `json:"workflow,omitempty"`
	Environment     string         `json:"environment,omitempty"`
	EvidenceCeiling []EvidenceKind `json:"evidence_ceiling"`
	CreatedAt       string         `json:"created_at"`
	RevokedAt       string         `json:"revoked_at,omitempty"`
}

type ReporterRegistrationList struct {
	Registrations []ReporterRegistration `json:"registrations"`
}
type PrerequisiteRequirement string

const (
	PrerequisiteRequired PrerequisiteRequirement = "required"
	PrerequisiteOptional PrerequisiteRequirement = "optional"
)

type Prerequisite struct {
	DependencyKey          string                  `json:"dependency_key"`
	ReporterRegistrationID int64                   `json:"reporter_registration_id"`
	Requirement            PrerequisiteRequirement `json:"requirement"`
}
type SealPrerequisitesRequest struct {
	StageKey               string         `json:"stage_key"`
	ExecutionNumber        int64          `json:"execution_number"`
	ExpectedPlanRevision   int64          `json:"expected_plan_revision"`
	ExpectedAuthorityEpoch int64          `json:"expected_authority_epoch"`
	Prerequisites          []Prerequisite `json:"prerequisites"`
}
type PrerequisiteSet struct {
	DeliveryKey     string `json:"delivery_key"`
	StageKey        string `json:"stage_key"`
	ExecutionNumber int64  `json:"execution_number"`
	PlanRevision    int64  `json:"plan_revision"`
	AuthorityEpoch  int64  `json:"authority_epoch"`
	DeclaredCount   int    `json:"declared_count"`
	SealedAt        string `json:"sealed_at"`
}

func registrationCeiling(class ReporterClass) []EvidenceKind {
	if class == ReporterClassPharos {
		return []EvidenceKind{EvidenceKindDeployment, EvidenceKindVerification}
	}
	return []EvidenceKind{EvidenceKindAuthorization, EvidenceKindCredentialHandoff}
}

func validateRegistrationRequest(req RegisterReporterRequest) error {
	if req.APIKeyID <= 0 {
		return ErrInvalid
	}
	if req.ReporterClass == ReporterClassPharos && req.ReporterRole == ReporterRoleOwner && req.DependencyKey == "" && symbolPattern.MatchString(req.Workflow) && symbolPattern.MatchString(req.Environment) {
		return nil
	}
	if req.ReporterClass == ReporterClassJanus && req.ReporterRole == ReporterRoleDependency && symbolPattern.MatchString(req.DependencyKey) && req.Workflow == "" && req.Environment == "" {
		return nil
	}
	return ErrInvalid
}

func setupReplay(ctx context.Context, tx *sql.Tx, p Principal, event string, idem, request [32]byte) (int64, bool, error) {
	var subject int64
	var prior []byte
	column := "registration_id"
	if event == "prerequisites_sealed" {
		column = "attempt_id"
	}
	query := `SELECT COALESCE(` + column + `,0),request_digest FROM external_stage_setup_events WHERE actor_user_id=?
		AND actor_principal_kind=? AND actor_session_id IS ? AND actor_api_key_id IS ? AND event_kind=? AND idempotency_digest=?`
	err := tx.QueryRowContext(ctx, query, p.UserID, p.Kind, nullString(p.SessionCredentialID), nullInt(p.APIKeyID), event, idem[:]).Scan(&subject, &prior)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if string(prior) != string(request[:]) {
		return 0, false, ErrConflict
	}
	return subject, true, nil
}

func insertSetup(ctx context.Context, tx *sql.Tx, p Principal, event string, deliveryID, projectID int64, registration any, attempt any, stage any, execution any, epoch any, request, idem [32]byte) (int64, string, error) {
	kind, session, key, err := principalColumns(p)
	if err != nil {
		return 0, "", err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO external_stage_setup_events(event_kind,delivery_id,project_id,registration_id,
		attempt_id,stage_key,execution_number,authority_epoch,actor_user_id,actor_principal_kind,actor_session_id,
		actor_api_key_id,request_digest,idempotency_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event, deliveryID, projectID,
		registration, attempt, stage, execution, epoch, p.UserID, kind, session, key, request[:], idem[:])
	if err != nil {
		return 0, "", mapConflict(err)
	}
	id, _ := res.LastInsertId()
	var received string
	err = tx.QueryRowContext(ctx, `SELECT server_received_at FROM external_stage_setup_events WHERE id=?`, id).Scan(&received)
	return id, received, err
}

func authorizeInternalTx(ctx context.Context, tx *sql.Tx, p Principal, deliveryKey string) (int64, int64, int64, error) {
	if _, _, _, err := principalColumns(p); err != nil {
		return 0, 0, 0, ErrNotFound
	}
	var deliveryID, issueID, projectID int64
	err := tx.QueryRowContext(ctx, `SELECT delivery.id,delivery.issue_id,issue.project_id FROM deliveries delivery
		JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id AND project.status IN ('active','frozen')
		JOIN external_stage_user_roles actor ON actor.id=? AND actor.status='active' AND actor.effective_role<>'external'
		WHERE delivery.delivery_key=? AND (actor.effective_role IN ('admin','super_admin') OR
		 EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=actor.id AND membership.project_id=issue.project_id AND membership.access_level='editor') OR
		 (actor.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=actor.id AND membership.project_id=issue.project_id)))
		AND ((?='session' AND EXISTS(SELECT 1 FROM sessions session WHERE session.credential_id=?
		 AND COALESCE(session.acting_as_user_id,session.user_id)=actor.id AND session.expires_at>datetime('now'))) OR
		 (?='api_key' AND EXISTS(SELECT 1 FROM api_keys api_key WHERE api_key.id=? AND api_key.user_id=actor.id
		 AND api_key.disabled_at IS NULL AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
		 AND (api_key.scopes='*' OR (','||replace(api_key.scopes,' ','')||',') LIKE '%,agent-controls:write,%'))))`,
		p.UserID, deliveryKey, p.Kind, p.SessionCredentialID, p.Kind, p.APIKeyID).Scan(&deliveryID, &issueID, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, ErrNotFound
	}
	return deliveryID, issueID, projectID, err
}

func scanRegistration(row interface{ Scan(...any) error }) (ReporterRegistration, error) {
	var r ReporterRegistration
	var class, role string
	var ad, av, aa, ac int
	err := row.Scan(&r.RegistrationID, &r.ReporterID, &r.APIKeyID, &class, &role, &r.DependencyKey, &r.Workflow, &r.Environment, &ad, &av, &aa, &ac, &r.CreatedAt, &r.RevokedAt)
	r.ReporterClass, r.ReporterRole = ReporterClass(class), ReporterRole(role)
	r.EvidenceCeiling = registrationCeiling(r.ReporterClass)
	return r, err
}

const registrationSelect = `SELECT id,reporter_id,api_key_id,reporter_class,reporter_role,COALESCE(dependency_key,''),
	COALESCE(workflow_symbol,''),COALESCE(environment_symbol,''),allow_deployment,allow_verification,
	allow_authorization,allow_credential_handoff,created_at,COALESCE(revoked_at,'') FROM external_stage_reporter_registrations `

func (s *Service) RegisterReporter(ctx context.Context, p Principal, deliveryKey, idempotencyKey string, req RegisterReporterRequest) (ReporterRegistration, error) {
	if _, _, _, err := principalColumns(p); err != nil {
		return ReporterRegistration{}, err
	}
	if deliveryKey == "" || idempotencyKey == "" || validateRegistrationRequest(req) != nil {
		return ReporterRegistration{}, ErrInvalid
	}
	request, _ := canonicalDigest(struct {
		Delivery string
		Request  RegisterReporterRequest
	}{deliveryKey, req})
	idem := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReporterRegistration{}, err
	}
	defer tx.Rollback()
	deliveryID, _, projectID, err := authorizeInternalTx(ctx, tx, p, deliveryKey)
	if err != nil {
		return ReporterRegistration{}, err
	}
	if id, found, err := setupReplay(ctx, tx, p, "registration_created", idem, request); err != nil {
		return ReporterRegistration{}, err
	} else if found {
		return scanRegistration(tx.QueryRowContext(ctx, registrationSelect+`WHERE id=?`, id))
	}
	var userID int64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM api_keys WHERE id=?`, req.APIKeyID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ReporterRegistration{}, ErrNotFound
	}
	if err != nil {
		return ReporterRegistration{}, err
	}
	opaqueDigest := sha256.Sum256([]byte(fmt.Sprintf("external-reporter\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s", deliveryID, req.APIKeyID, req.ReporterClass, req.DependencyKey, req.Workflow, req.Environment)))
	opaque := "external:" + fmt.Sprintf("%x", opaqueDigest[:])
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at) VALUES(?,'external',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, deliveryID, opaque); err != nil {
		return ReporterRegistration{}, err
	}
	var reporterID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM delivery_reporters WHERE delivery_id=? AND reporter_type='external' AND opaque_key=?`, deliveryID, opaque).Scan(&reporterID); err != nil {
		return ReporterRegistration{}, err
	}
	ad, av, aa, ac := 0, 0, 0, 0
	if req.ReporterClass == ReporterClassPharos {
		ad, av = 1, 1
	} else {
		aa, ac = 1, 1
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO external_stage_reporter_registrations(delivery_id,project_id,user_id,api_key_id,
		reporter_id,reporter_class,reporter_role,dependency_key,workflow_symbol,environment_symbol,allow_deployment,
		allow_verification,allow_authorization,allow_credential_handoff) VALUES(?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?,?)`,
		deliveryID, projectID, userID, req.APIKeyID, reporterID, req.ReporterClass, req.ReporterRole, req.DependencyKey, req.Workflow, req.Environment, ad, av, aa, ac)
	if err != nil {
		return ReporterRegistration{}, mapConflict(err)
	}
	registrationID, _ := res.LastInsertId()
	if _, _, err := insertSetup(ctx, tx, p, "registration_created", deliveryID, projectID, registrationID, nil, nil, nil, nil, request, idem); err != nil {
		return ReporterRegistration{}, err
	}
	registration, err := scanRegistration(tx.QueryRowContext(ctx, registrationSelect+`WHERE id=?`, registrationID))
	if err != nil {
		return ReporterRegistration{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReporterRegistration{}, err
	}
	return registration, nil
}

func (s *Service) ListReporters(ctx context.Context, p Principal, deliveryKey string) (ReporterRegistrationList, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReporterRegistrationList{}, err
	}
	defer tx.Rollback()
	deliveryID, _, projectID, err := authorizeInternalTx(ctx, tx, p, deliveryKey)
	if err != nil {
		return ReporterRegistrationList{}, err
	}
	rows, err := tx.QueryContext(ctx, registrationSelect+`registration WHERE registration.delivery_id=? AND registration.project_id=?
		AND registration.revoked_at IS NULL AND EXISTS(SELECT 1 FROM api_keys api_key JOIN external_stage_user_roles reporter_user
		 ON reporter_user.id=api_key.user_id AND reporter_user.status='active' WHERE api_key.id=registration.api_key_id
		 AND api_key.user_id=registration.user_id AND api_key.disabled_at IS NULL
		 AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
		 AND (reporter_user.effective_role IN ('admin','super_admin') OR EXISTS(SELECT 1 FROM project_members membership
		  WHERE membership.user_id=reporter_user.id AND membership.project_id=registration.project_id AND membership.access_level IN ('viewer','editor')) OR
		  (reporter_user.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
		   WHERE membership.user_id=reporter_user.id AND membership.project_id=registration.project_id)))) ORDER BY registration.id`, deliveryID, projectID)
	if err != nil {
		return ReporterRegistrationList{}, err
	}
	defer rows.Close()
	out := ReporterRegistrationList{Registrations: []ReporterRegistration{}}
	for rows.Next() {
		r, err := scanRegistration(rows)
		if err != nil {
			return ReporterRegistrationList{}, err
		}
		out.Registrations = append(out.Registrations, r)
	}
	return out, rows.Err()
}

func (s *Service) RevokeReporter(ctx context.Context, p Principal, deliveryKey, idempotencyKey string, registrationID int64) (ReporterRegistration, error) {
	if deliveryKey == "" || idempotencyKey == "" || registrationID <= 0 {
		return ReporterRegistration{}, ErrInvalid
	}
	request, _ := canonicalDigest(struct {
		Delivery string
		ID       int64
	}{deliveryKey, registrationID})
	idem := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReporterRegistration{}, err
	}
	defer tx.Rollback()
	if _, _, _, err := authorizeInternalTx(ctx, tx, p, deliveryKey); err != nil {
		return ReporterRegistration{}, err
	}
	if id, found, err := setupReplay(ctx, tx, p, "registration_revoked", idem, request); err != nil {
		return ReporterRegistration{}, err
	} else if found {
		return scanRegistration(tx.QueryRowContext(ctx, registrationSelect+`WHERE id=?`, id))
	}
	var deliveryID, projectID int64
	if err := tx.QueryRowContext(ctx, `SELECT registration.delivery_id,registration.project_id FROM external_stage_reporter_registrations registration JOIN deliveries delivery ON delivery.id=registration.delivery_id WHERE registration.id=? AND delivery.delivery_key=? AND registration.revoked_at IS NULL`, registrationID, deliveryKey).Scan(&deliveryID, &projectID); errors.Is(err, sql.ErrNoRows) {
		return ReporterRegistration{}, ErrNotFound
	} else if err != nil {
		return ReporterRegistration{}, err
	}
	_, received, err := insertSetup(ctx, tx, p, "registration_revoked", deliveryID, projectID, registrationID, nil, nil, nil, nil, request, idem)
	if err != nil {
		return ReporterRegistration{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE external_stage_reporter_registrations SET revoked_at=? WHERE id=?`, received, registrationID); err != nil {
		return ReporterRegistration{}, mapConflict(err)
	}
	r, err := scanRegistration(tx.QueryRowContext(ctx, registrationSelect+`WHERE id=?`, registrationID))
	if err != nil {
		return ReporterRegistration{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReporterRegistration{}, err
	}
	return r, nil
}

func (s *Service) SealPrerequisites(ctx context.Context, p Principal, deliveryKey, idempotencyKey string, req SealPrerequisitesRequest) (PrerequisiteSet, error) {
	if deliveryKey == "" || idempotencyKey == "" || !validStageKey(req.StageKey) || req.ExecutionNumber <= 0 || req.ExpectedPlanRevision <= 0 ||
		req.ExpectedAuthorityEpoch <= 0 || req.Prerequisites == nil || len(req.Prerequisites) > 16 {
		return PrerequisiteSet{}, ErrInvalid
	}
	seenKeys := map[string]bool{}
	seenIDs := map[int64]bool{}
	for _, item := range req.Prerequisites {
		if !symbolPattern.MatchString(item.DependencyKey) || item.ReporterRegistrationID <= 0 ||
			(item.Requirement != PrerequisiteRequired && item.Requirement != PrerequisiteOptional) ||
			seenKeys[item.DependencyKey] || seenIDs[item.ReporterRegistrationID] {
			return PrerequisiteSet{}, ErrInvalid
		}
		seenKeys[item.DependencyKey] = true
		seenIDs[item.ReporterRegistrationID] = true
	}
	request, _ := canonicalDigest(struct {
		Delivery string
		Request  SealPrerequisitesRequest
	}{deliveryKey, req})
	idem := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PrerequisiteSet{}, err
	}
	defer tx.Rollback()
	if _, _, _, err := authorizeInternalTx(ctx, tx, p, deliveryKey); err != nil {
		return PrerequisiteSet{}, err
	}
	if attempt, found, err := setupReplay(ctx, tx, p, "prerequisites_sealed", idem, request); err != nil {
		return PrerequisiteSet{}, err
	} else if found {
		return loadPrerequisiteSet(ctx, tx, deliveryKey, attempt, req.StageKey, req.ExecutionNumber, req.ExpectedAuthorityEpoch)
	}
	var deliveryID, issueID, projectID, attemptID, startID, authorityID int64
	err = tx.QueryRowContext(ctx, `SELECT delivery.id,delivery.issue_id,issue.project_id,attempt.id,
		latest.execution_start_stage_event_id,latest.authority_stage_event_id
		FROM deliveries delivery JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN delivery_attempts attempt ON attempt.delivery_id=delivery.id
		 AND attempt.attempt_number=(SELECT MAX(a.attempt_number) FROM delivery_attempts a WHERE a.delivery_id=delivery.id)
		JOIN delivery_stage_latest latest ON latest.delivery_id=delivery.id AND latest.attempt_id=attempt.id
		 AND latest.stage_key=? AND latest.execution_number=? AND latest.authority_epoch=?
		WHERE delivery.delivery_key=? AND attempt.plan_revision=?
		 AND NOT EXISTS(SELECT 1 FROM delivery_stage_events terminal WHERE terminal.id=latest.semantic_stage_event_id
		  AND terminal.semantic_state IN ('succeeded','failed','cancelled','draft_ready'))`, req.StageKey, req.ExecutionNumber,
		req.ExpectedAuthorityEpoch, deliveryKey, req.ExpectedPlanRevision).
		Scan(&deliveryID, &issueID, &projectID, &attemptID, &startID, &authorityID)
	if errors.Is(err, sql.ErrNoRows) {
		return PrerequisiteSet{}, ErrNotFound
	}
	if err != nil {
		return PrerequisiteSet{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO external_stage_prerequisite_sets(delivery_id,attempt_id,stage_key,execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,declared_count) VALUES(?,?,?,?,?,?,?,?)`, deliveryID, attemptID, req.StageKey, req.ExecutionNumber, startID, req.ExpectedAuthorityEpoch, authorityID, len(req.Prerequisites)); err != nil {
		return PrerequisiteSet{}, mapConflict(err)
	}
	for i, item := range req.Prerequisites {
		if _, err := tx.ExecContext(ctx, `INSERT INTO external_stage_prerequisites(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id,requirement,ordinal) VALUES(?,?,?,?,?,?,?,?,?)`, deliveryID, attemptID, req.StageKey, req.ExecutionNumber, req.ExpectedAuthorityEpoch, item.DependencyKey, item.ReporterRegistrationID, item.Requirement, i); err != nil {
			return PrerequisiteSet{}, mapConflict(err)
		}
	}
	_, received, err := insertSetup(ctx, tx, p, "prerequisites_sealed", deliveryID, projectID, nil, attemptID, req.StageKey, req.ExecutionNumber, req.ExpectedAuthorityEpoch, request, idem)
	if err != nil {
		return PrerequisiteSet{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE external_stage_prerequisite_sets SET sealed_at=? WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?`, received, attemptID, req.StageKey, req.ExecutionNumber, req.ExpectedAuthorityEpoch); err != nil {
		return PrerequisiteSet{}, mapConflict(err)
	}
	out := PrerequisiteSet{DeliveryKey: deliveryKey, StageKey: req.StageKey, ExecutionNumber: req.ExecutionNumber, PlanRevision: req.ExpectedPlanRevision, AuthorityEpoch: req.ExpectedAuthorityEpoch, DeclaredCount: len(req.Prerequisites), SealedAt: received}
	if err := tx.Commit(); err != nil {
		return PrerequisiteSet{}, err
	}
	return out, nil
}

func loadPrerequisiteSet(ctx context.Context, tx *sql.Tx, deliveryKey string, attemptID int64, stage string, execution, epoch int64) (PrerequisiteSet, error) {
	var out PrerequisiteSet
	out.DeliveryKey = deliveryKey
	err := tx.QueryRowContext(ctx, `SELECT prerequisite_set.stage_key,prerequisite_set.execution_number,attempt.plan_revision,prerequisite_set.authority_epoch,prerequisite_set.declared_count,prerequisite_set.sealed_at FROM external_stage_prerequisite_sets prerequisite_set JOIN delivery_attempts attempt ON attempt.id=prerequisite_set.attempt_id WHERE prerequisite_set.attempt_id=? AND prerequisite_set.stage_key=? AND prerequisite_set.execution_number=? AND prerequisite_set.authority_epoch=?`, attemptID, stage, execution, epoch).Scan(&out.StageKey, &out.ExecutionNumber, &out.PlanRevision, &out.AuthorityEpoch, &out.DeclaredCount, &out.SealedAt)
	return out, err
}
