// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package externalstage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/delivery"
)

var (
	ErrNotFound    = errors.New("external stage handoff not found")
	ErrInvalid     = errors.New("invalid external stage request")
	ErrConflict    = errors.New("external stage conflict")
	ErrUnavailable = errors.New("external stage dependency unavailable")

	handoffIDPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	symbolPattern    = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	versionPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	commitPattern    = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	ulidEncoding     = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)
)

const secretDomain = "paimos.external-stage.secret.v1\x00"
const MaxReporterFutureSkew = 2 * time.Minute
const ActiveReporterStaleAfter = 2 * time.Minute

type Clock interface{ Now() time.Time }
type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

// Principal is the immutable, non-secret identity installed by authentication.
// SessionCredentialID is the safe database credential UUID, never the cookie.
type Principal struct {
	UserID              int64
	Kind                string
	SessionCredentialID string
	APIKeyID            int64
}

type Options struct {
	FixtureDigest    [sha256.Size]byte
	Random           io.Reader
	Clock            Clock
	Observer         delivery.CommitObserver
	ActiveStaleAfter time.Duration
}

type Service struct {
	db               *sql.DB
	random           io.Reader
	clock            Clock
	observer         delivery.CommitObserver
	activeStaleAfter time.Duration
	beforeCommit     func(string) error
	fixture          [sha256.Size]byte
}

func NewService(database *sql.DB, opts Options) (*Service, error) {
	want := contracts.ExternalStageV1FixtureDigest()
	if database == nil || opts.FixtureDigest != want {
		return nil, fmt.Errorf("%w: fixture digest is not the frozen v1 digest", ErrInvalid)
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if opts.Clock == nil {
		opts.Clock = clockFunc(time.Now)
	}
	if opts.ActiveStaleAfter <= 0 {
		opts.ActiveStaleAfter = ActiveReporterStaleAfter
	}
	return &Service{db: database, random: opts.Random, clock: opts.Clock, observer: opts.Observer,
		activeStaleAfter: opts.ActiveStaleAfter, fixture: opts.FixtureDigest}, nil
}

func canonicalDigest(value any) ([sha256.Size]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func secretDigest(secret []byte) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write([]byte(secretDomain))
	_, _ = h.Write(secret)
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

func mintIdempotencyDigest(requestDigest [sha256.Size]byte, expected int64) [sha256.Size]byte {
	var encodedEpoch [8]byte
	_, _ = binary.Encode(encodedEpoch[:], binary.BigEndian, expected)
	hash := sha256.New()
	_, _ = hash.Write(requestDigest[:])
	_, _ = hash.Write(encodedEpoch[:])
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func (s *Service) newHandoffID() (string, error) {
	var raw [16]byte
	millis := uint64(s.clock.Now().UTC().UnixMilli())
	for i := 5; i >= 0; i-- {
		raw[i] = byte(millis)
		millis >>= 8
	}
	if _, err := io.ReadFull(s.random, raw[6:]); err != nil {
		return "", err
	}
	id := ulidEncoding.EncodeToString(raw[:])
	if !handoffIDPattern.MatchString(id) {
		return "", errors.New("generated invalid handoff locator")
	}
	return id, nil
}

func principalColumns(p Principal) (kind string, session any, key any, err error) {
	if p.UserID <= 0 {
		return "", nil, nil, ErrNotFound
	}
	switch p.Kind {
	case "session":
		if p.SessionCredentialID == "" || p.APIKeyID != 0 {
			return "", nil, nil, ErrNotFound
		}
		return p.Kind, p.SessionCredentialID, nil, nil
	case "api_key":
		if p.APIKeyID <= 0 || p.SessionCredentialID != "" {
			return "", nil, nil, ErrNotFound
		}
		return p.Kind, nil, p.APIKeyID, nil
	default:
		return "", nil, nil, ErrNotFound
	}
}

func validStageKey(stage string) bool {
	switch stage {
	case "specification", "implementation", "qa", "deployment", "verification":
		return true
	default:
		return false
	}
}

func validReporterStage(role ReporterRole, stage string) bool {
	return role != ReporterRoleOwner || stage == "deployment" || stage == "verification"
}

type handoffRow struct {
	rowID, deliveryID, issueID, projectID, attemptID, attemptNumber, planRevision  int64
	executionNumber, startEventID, authorityEpoch, authorityEventID                int64
	registrationID, reporterID, apiKeyID, credentialEpoch, lastSequence            int64
	handoffID, deliveryKey, issueKey, planDigest, predecessorDigest, contextDigest string
	stageKey, class, role, dependency, workflow, environment                       string
	allowDeployment, allowVerification, allowAuthorization, allowCredential        bool
	expiresAt, state, createdAt, revokedAt, terminalAt                             string
	secretDigest                                                                   []byte
}

const handoffSelect = `SELECT handoff.id,handoff.delivery_id,handoff.root_issue_id,handoff.project_id,handoff.attempt_id,
	handoff.attempt_number,handoff.plan_revision,handoff.execution_number,handoff.execution_start_stage_event_id,
	handoff.authority_epoch,handoff.authority_stage_event_id,handoff.reporter_registration_id,handoff.reporter_id,
	handoff.api_key_id,handoff.credential_epoch,handoff.last_sequence,handoff.handoff_id,handoff.delivery_key,
	project.key||'-'||issue.issue_number,
	hex(handoff.plan_digest),hex(handoff.predecessor_digest),hex(handoff.context_digest),handoff.stage_key,
	handoff.reporter_class,handoff.reporter_role,COALESCE(handoff.dependency_key,''),COALESCE(handoff.workflow_symbol,''),
	COALESCE(handoff.environment_symbol,''),handoff.allow_deployment,handoff.allow_verification,
	handoff.allow_authorization,handoff.allow_credential_handoff,handoff.expires_at,handoff.lifecycle_state,
	handoff.created_at,COALESCE(handoff.revoked_at,''),COALESCE(handoff.terminal_at,''),handoff.secret_digest
	FROM external_stage_handoffs handoff JOIN issues issue ON issue.id=handoff.root_issue_id
	JOIN projects project ON project.id=issue.project_id `

func scanHandoff(row *sql.Row) (handoffRow, error) {
	var h handoffRow
	var ad, av, aa, ac int
	err := row.Scan(&h.rowID, &h.deliveryID, &h.issueID, &h.projectID, &h.attemptID, &h.attemptNumber, &h.planRevision,
		&h.executionNumber, &h.startEventID, &h.authorityEpoch, &h.authorityEventID, &h.registrationID, &h.reporterID,
		&h.apiKeyID, &h.credentialEpoch, &h.lastSequence, &h.handoffID, &h.deliveryKey, &h.issueKey, &h.planDigest,
		&h.predecessorDigest, &h.contextDigest, &h.stageKey, &h.class, &h.role, &h.dependency, &h.workflow, &h.environment,
		&ad, &av, &aa, &ac, &h.expiresAt, &h.state, &h.createdAt, &h.revokedAt, &h.terminalAt, &h.secretDigest)
	h.allowDeployment, h.allowVerification, h.allowAuthorization, h.allowCredential = ad == 1, av == 1, aa == 1, ac == 1
	return h, err
}

func (h handoffRow) ceiling() []EvidenceKind {
	out := make([]EvidenceKind, 0, 2)
	if h.allowDeployment {
		out = append(out, EvidenceKindDeployment)
	}
	if h.allowVerification {
		out = append(out, EvidenceKindVerification)
	}
	if h.allowAuthorization {
		out = append(out, EvidenceKindAuthorization)
	}
	if h.allowCredential {
		out = append(out, EvidenceKindCredentialHandoff)
	}
	return out
}

func (h handoffRow) metadata(fixture [sha256.Size]byte) HandoffMetadata {
	return HandoffMetadata{HandoffID: h.handoffID, DeliveryKey: h.deliveryKey, IssueKey: h.issueKey,
		AttemptNumber: h.attemptNumber, PlanRevision: h.planRevision, PlanDigest: "sha256:" + strings.ToLower(h.planDigest),
		StageKey: h.stageKey, ExecutionNumber: h.executionNumber, ExecutionStartStageEventID: h.startEventID,
		PredecessorDigest: "sha256:" + strings.ToLower(h.predecessorDigest), AuthorityEpoch: h.authorityEpoch,
		AuthorityStageEventID: h.authorityEventID, ReporterRegistrationID: h.registrationID,
		ReporterClass: ReporterClass(h.class), ReporterRole: ReporterRole(h.role), DependencyKey: h.dependency,
		EvidenceCeiling: h.ceiling(), ContractMajor: ContractMajor, FixtureDigest: "sha256:" + hex.EncodeToString(fixture[:]),
		CredentialEpoch: h.credentialEpoch, ExpiresAt: h.expiresAt, ContextDigest: "sha256:" + strings.ToLower(h.contextDigest),
		State: HandoffState(h.state), CreatedAt: h.createdAt, RevokedAt: h.revokedAt}
}

func (h handoffRow) pull(fixture [sha256.Size]byte) PullResponse {
	m := h.metadata(fixture)
	return PullResponse{HandoffID: m.HandoffID, ContractMajor: m.ContractMajor, FixtureDigest: m.FixtureDigest,
		CredentialEpoch: m.CredentialEpoch, ExpiresAt: m.ExpiresAt, State: m.State, ReporterClass: m.ReporterClass,
		ReporterRole: m.ReporterRole, DependencyKey: m.DependencyKey, EvidenceCeiling: m.EvidenceCeiling,
		StageKey: m.StageKey, ExecutionNumber: m.ExecutionNumber, PlanDigest: m.PlanDigest,
		PredecessorDigest: m.PredecessorDigest, AuthorityEpoch: m.AuthorityEpoch, ContextDigest: m.ContextDigest}
}

func (s *Service) loadHandoffTx(ctx context.Context, tx *sql.Tx, handoffID string) (handoffRow, error) {
	if !handoffIDPattern.MatchString(handoffID) {
		return handoffRow{}, ErrNotFound
	}
	h, err := scanHandoff(tx.QueryRowContext(ctx, handoffSelect+`WHERE handoff.handoff_id=?`, handoffID))
	if errors.Is(err, sql.ErrNoRows) {
		return handoffRow{}, ErrNotFound
	}
	return h, err
}

func (s *Service) CreateHandoff(ctx context.Context, principal Principal, deliveryKey, idempotencyKey string, req CreateHandoffRequest) (CreateHandoffResult, error) {
	kind, session, key, err := principalColumns(principal)
	if err != nil {
		return CreateHandoffResult{}, err
	}
	if deliveryKey == "" || !validStageKey(req.StageKey) || req.ExecutionNumber <= 0 || req.ExpectedPlanRevision <= 0 ||
		req.ExpectedAuthorityEpoch <= 0 || req.ReporterRegistrationID <= 0 || idempotencyKey == "" {
		return CreateHandoffResult{}, ErrInvalid
	}
	expires, err := time.Parse(time.RFC3339Nano, req.ExpiresAt)
	if err != nil || !expires.After(s.clock.Now().UTC()) || expires.After(s.clock.Now().UTC().Add(30*24*time.Hour)) {
		return CreateHandoffResult{}, ErrInvalid
	}
	reqDigest, err := canonicalDigest(struct {
		Delivery string
		Request  CreateHandoffRequest
	}{deliveryKey, req})
	if err != nil {
		return CreateHandoffResult{}, err
	}
	idemDigest := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateHandoffResult{}, err
	}
	defer tx.Rollback()
	if _, _, _, err := authorizeInternalTx(ctx, tx, principal, deliveryKey); err != nil {
		return CreateHandoffResult{}, err
	}
	if prior, found, err := lookupInternalOperationReplay(ctx, tx, principal, "created", idemDigest, reqDigest); err != nil {
		return CreateHandoffResult{}, err
	} else if found {
		h, err := s.loadHandoffTx(ctx, tx, prior)
		if err != nil {
			return CreateHandoffResult{}, err
		}
		h.credentialEpoch, h.state, h.revokedAt = 0, "issued", ""
		return CreateHandoffResult{HandoffMetadata: h.metadata(s.fixture), Duplicate: true}, nil
	}
	var h handoffRow
	var planSeed, predecessorSeed string
	err = tx.QueryRowContext(ctx, `SELECT delivery.id,delivery.issue_id,issue.project_id,attempt.id,attempt.attempt_number,
		attempt.plan_revision,latest.execution_number,latest.execution_start_stage_event_id,latest.authority_epoch,
		latest.authority_stage_event_id,registration.reporter_id,registration.api_key_id,registration.reporter_class,
		registration.reporter_role,COALESCE(registration.dependency_key,''),COALESCE(registration.workflow_symbol,''),
		COALESCE(registration.environment_symbol,''),registration.allow_deployment,registration.allow_verification,
		registration.allow_authorization,registration.allow_credential_handoff,
		printf('%d:%d:%d',attempt.id,attempt.plan_revision,attempt.start_delivery_event_id),
		printf('%d:%d:%d',latest.execution_start_stage_event_id,latest.authority_stage_event_id,COALESCE(latest.semantic_stage_event_id,0))
		FROM deliveries delivery JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN delivery_attempts attempt ON attempt.delivery_id=delivery.id AND attempt.attempt_number=(SELECT MAX(a.attempt_number) FROM delivery_attempts a WHERE a.delivery_id=delivery.id)
		JOIN delivery_stage_latest latest ON latest.delivery_id=delivery.id AND latest.attempt_id=attempt.id AND latest.stage_key=?
		JOIN external_stage_reporter_registrations registration ON registration.id=? AND registration.delivery_id=delivery.id AND registration.revoked_at IS NULL
		WHERE delivery.delivery_key=? AND attempt.plan_revision=? AND latest.execution_number=? AND latest.authority_epoch=?
		AND NOT EXISTS(SELECT 1 FROM delivery_stage_events terminal WHERE terminal.id=latest.semantic_stage_event_id
		 AND terminal.semantic_state IN ('succeeded','failed','cancelled','draft_ready'))`,
		req.StageKey, req.ReporterRegistrationID, deliveryKey, req.ExpectedPlanRevision, req.ExecutionNumber, req.ExpectedAuthorityEpoch).
		Scan(&h.deliveryID, &h.issueID, &h.projectID, &h.attemptID, &h.attemptNumber, &h.planRevision, &h.executionNumber,
			&h.startEventID, &h.authorityEpoch, &h.authorityEventID, &h.reporterID, &h.apiKeyID, &h.class, &h.role, &h.dependency,
			&h.workflow, &h.environment, &h.allowDeployment, &h.allowVerification, &h.allowAuthorization, &h.allowCredential,
			&planSeed, &predecessorSeed)
	if errors.Is(err, sql.ErrNoRows) {
		return CreateHandoffResult{}, ErrNotFound
	}
	if err != nil {
		return CreateHandoffResult{}, err
	}
	h.stageKey = req.StageKey
	if !validReporterStage(ReporterRole(h.role), h.stageKey) {
		return CreateHandoffResult{}, ErrInvalid
	}
	h.handoffID, err = s.newHandoffID()
	if err != nil {
		return CreateHandoffResult{}, err
	}
	h.deliveryKey = deliveryKey
	h.registrationID = req.ReporterRegistrationID
	planDigest := sha256.Sum256([]byte("paimos.external-stage.plan.v1\x00" + planSeed))
	predecessorDigest := sha256.Sum256([]byte("paimos.external-stage.predecessor.v1\x00" + predecessorSeed))
	contextDigest := sha256.Sum256([]byte(fmt.Sprintf("paimos.external-stage.context.v1\x00%s\x00%d\x00%s\x00%d\x00%d\x00%d", deliveryKey, h.attemptID, req.StageKey, h.executionNumber, h.authorityEpoch, h.registrationID)))
	result, err := tx.ExecContext(ctx, `INSERT INTO external_stage_handoffs(handoff_id,delivery_id,delivery_key,root_issue_id,project_id,
		attempt_id,attempt_number,plan_revision,plan_digest,stage_key,execution_number,execution_start_stage_event_id,
		predecessor_digest,authority_epoch,authority_stage_event_id,reporter_registration_id,reporter_id,api_key_id,
		reporter_class,reporter_role,dependency_key,workflow_symbol,environment_symbol,allow_deployment,allow_verification,
		allow_authorization,allow_credential_handoff,contract_major,fixture_digest,expires_at,context_digest)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?,?,1,?,?,?)`, h.handoffID, h.deliveryID, deliveryKey, h.issueID, h.projectID,
		h.attemptID, h.attemptNumber, h.planRevision, planDigest[:], h.stageKey, h.executionNumber, h.startEventID, predecessorDigest[:], h.authorityEpoch, h.authorityEventID,
		h.registrationID, h.reporterID, h.apiKeyID, h.class, h.role, h.dependency, h.workflow, h.environment, h.allowDeployment, h.allowVerification, h.allowAuthorization, h.allowCredential,
		s.fixture[:], expires.UTC().Format(time.RFC3339Nano), contextDigest[:])
	if err != nil {
		return CreateHandoffResult{}, mapConflict(err)
	}
	h.rowID, _ = result.LastInsertId()
	op, received, err := insertOperation(ctx, tx, h, "created", reqDigest, idemDigest, principal, kind, session, key, 0, nil)
	if err != nil {
		return CreateHandoffResult{}, mapConflict(err)
	}
	if err := insertOperationAudit(ctx, tx, h, op, "created", 0, nil, key, received); err != nil {
		return CreateHandoffResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateHandoffResult{}, err
	}
	h, err = s.loadHandoff(ctx, h.handoffID)
	if err != nil {
		return CreateHandoffResult{}, err
	}
	return CreateHandoffResult{HandoffMetadata: h.metadata(s.fixture)}, nil
}

func lookupInternalOperationReplay(ctx context.Context, tx *sql.Tx, p Principal, kind string, idem, request [32]byte) (string, bool, error) {
	var id string
	var prior []byte
	err := tx.QueryRowContext(ctx, `SELECT handoff.handoff_id,operation.request_digest FROM external_stage_operation_events operation
		JOIN external_stage_handoffs handoff ON handoff.id=operation.handoff_row_id WHERE operation.actor_user_id=?
		AND operation.actor_principal_kind=? AND operation.actor_session_id IS ? AND operation.actor_api_key_id IS ?
		AND operation.operation_kind=? AND operation.idempotency_digest=? ORDER BY operation.id DESC LIMIT 1`, p.UserID, p.Kind,
		nullString(p.SessionCredentialID), nullInt(p.APIKeyID), kind, idem[:]).Scan(&id, &prior)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if subtle.ConstantTimeCompare(prior, request[:]) != 1 {
		return "", false, ErrConflict
	}
	return id, true, nil
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func insertOperation(ctx context.Context, tx *sql.Tx, h handoffRow, operation string, request, idem [32]byte, p Principal, kind string, session, key any, epoch int64, sequence any) (int64, string, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO external_stage_operation_events(handoff_row_id,operation_kind,request_digest,
		idempotency_digest,actor_user_id,actor_principal_kind,actor_session_id,actor_api_key_id,credential_epoch,sequence)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, h.rowID, operation, request[:], idem[:], p.UserID, kind, session, key, epoch, sequence)
	if err != nil {
		return 0, "", err
	}
	id, _ := res.LastInsertId()
	var received string
	err = tx.QueryRowContext(ctx, `SELECT server_received_at FROM external_stage_operation_events WHERE id=?`, id).Scan(&received)
	return id, received, err
}

func insertOperationAudit(ctx context.Context, tx *sql.Tx, h handoffRow, op int64, kind string, epoch int64, sequence any, apiKey any, received string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO external_stage_audit_events(event_kind,handoff_row_id,operation_event_id,
		api_key_id,credential_epoch,sequence,outcome,server_received_at) VALUES(?,?,?,?,?,?,'committed',?)`, kind, h.rowID, op, apiKey, epoch, sequence, received)
	return err
}

func mapConflict(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint") || strings.Contains(msg, "stale") || strings.Contains(msg, "constraint failed") {
		return fmt.Errorf("%w", ErrConflict)
	}
	return err
}

func (s *Service) loadHandoff(ctx context.Context, id string) (handoffRow, error) {
	h, err := scanHandoff(s.db.QueryRowContext(ctx, handoffSelect+`WHERE handoff.handoff_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return handoffRow{}, ErrNotFound
	}
	return h, err
}

func (s *Service) Mint(ctx context.Context, p Principal, handoffID string, expected int64, rotate bool) ([]byte, error) {
	kind, session, key, err := principalColumns(p)
	if err != nil {
		return nil, err
	}
	if expected < 0 || expected == math.MaxInt64 {
		return nil, ErrInvalid
	}
	opKind := "secret_minted"
	if rotate {
		opKind = "secret_rotated"
	}
	reqDigest, _ := canonicalDigest(struct {
		ID        string
		Epoch     int64
		Operation string
	}{handoffID, expected, opKind})
	idem := mintIdempotencyDigest(reqDigest, expected)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	h, err := s.loadHandoffTx(ctx, tx, handoffID)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := authorizeInternalTx(ctx, tx, p, h.deliveryKey); err != nil {
		return nil, err
	}
	expires, expiryErr := time.Parse(time.RFC3339Nano, h.expiresAt)
	if expiryErr != nil || !expires.After(s.clock.Now().UTC()) || h.revokedAt != "" || h.terminalAt != "" {
		return nil, ErrConflict
	}
	if h.credentialEpoch != expected || (rotate && expected == 0) || (!rotate && expected != 0) {
		return nil, ErrConflict
	}
	secret := make([]byte, OneTimeSecretBytes)
	committed := false
	defer func() {
		if !committed {
			for i := range secret {
				secret[i] = 0
			}
		}
	}()
	if _, err := io.ReadFull(s.random, secret); err != nil {
		return nil, err
	}
	digest := secretDigest(secret)
	next := expected + 1
	op, received, err := insertOperation(ctx, tx, h, opKind, reqDigest, idem, p, kind, session, key, next, nil)
	if err != nil {
		return nil, mapConflict(err)
	}
	if err := insertOperationAudit(ctx, tx, h, op, opKind, next, nil, key, received); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE external_stage_handoffs SET credential_epoch=?,secret_digest=? WHERE id=?`, next, digest[:], h.rowID); err != nil {
		return nil, mapConflict(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return secret, nil
}

func (s *Service) Revoke(ctx context.Context, p Principal, handoffID, idempotencyKey string, expected int64) (HandoffMetadata, error) {
	kind, session, key, err := principalColumns(p)
	if err != nil {
		return HandoffMetadata{}, err
	}
	if expected < 0 || idempotencyKey == "" {
		return HandoffMetadata{}, ErrInvalid
	}
	reqDigest, _ := canonicalDigest(struct {
		ID    string
		Epoch int64
	}{handoffID, expected})
	idem := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HandoffMetadata{}, err
	}
	defer tx.Rollback()
	h, err := s.loadHandoffTx(ctx, tx, handoffID)
	if err != nil {
		return HandoffMetadata{}, err
	}
	if _, _, _, err := authorizeInternalTx(ctx, tx, p, h.deliveryKey); err != nil {
		return HandoffMetadata{}, err
	}
	if prior, found, err := lookupInternalOperationReplay(ctx, tx, p, "revoked", idem, reqDigest); err != nil {
		return HandoffMetadata{}, err
	} else if found {
		priorHandoff, err := s.loadHandoffTx(ctx, tx, prior)
		if err != nil {
			return HandoffMetadata{}, err
		}
		return priorHandoff.metadata(s.fixture), nil
	}
	if h.credentialEpoch != expected || h.revokedAt != "" {
		return HandoffMetadata{}, ErrConflict
	}
	op, received, err := insertOperation(ctx, tx, h, "revoked", reqDigest, idem, p, kind, session, key, expected, nil)
	if err != nil {
		return HandoffMetadata{}, mapConflict(err)
	}
	if err := insertOperationAudit(ctx, tx, h, op, "revoked", expected, nil, key, received); err != nil {
		return HandoffMetadata{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE external_stage_handoffs SET revoked_at=? WHERE id=?`, received, h.rowID); err != nil {
		return HandoffMetadata{}, mapConflict(err)
	}
	if err := tx.Commit(); err != nil {
		return HandoffMetadata{}, err
	}
	h, err = s.loadHandoff(ctx, handoffID)
	return h.metadata(s.fixture), err
}

func (s *Service) authenticateExternalTx(ctx context.Context, tx *sql.Tx, h handoffRow, p Principal, secret []byte) error {
	digest := secretDigest(secret)
	expected := make([]byte, sha256.Size)
	copy(expected, h.secretDigest)
	secretOK := len(secret) == OneTimeSecretBytes && len(h.secretDigest) == sha256.Size && subtle.ConstantTimeCompare(digest[:], expected) == 1
	var current int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM external_stage_handoffs handoff
		JOIN external_stage_reporter_registrations registration ON registration.id=handoff.reporter_registration_id
		 AND registration.delivery_id=handoff.delivery_id AND registration.api_key_id=handoff.api_key_id
		 AND registration.user_id=? AND registration.revoked_at IS NULL
		JOIN api_keys api_key ON api_key.id=handoff.api_key_id AND api_key.user_id=registration.user_id
		 AND api_key.disabled_at IS NULL AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
		JOIN external_stage_user_roles reporter_user ON reporter_user.id=registration.user_id AND reporter_user.status='active'
		JOIN issues issue ON issue.id=handoff.root_issue_id AND issue.project_id=handoff.project_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=handoff.project_id AND project.status IN ('active','frozen')
		JOIN delivery_attempts attempt ON attempt.id=handoff.attempt_id AND attempt.delivery_id=handoff.delivery_id
		 AND attempt.attempt_number=handoff.attempt_number AND attempt.plan_revision=handoff.plan_revision
		 AND attempt.attempt_number=(SELECT MAX(current_attempt.attempt_number) FROM delivery_attempts current_attempt WHERE current_attempt.delivery_id=handoff.delivery_id)
		JOIN delivery_stage_latest latest ON latest.delivery_id=handoff.delivery_id AND latest.attempt_id=handoff.attempt_id
		 AND latest.stage_key=handoff.stage_key AND latest.execution_number=handoff.execution_number
		 AND latest.execution_start_stage_event_id=handoff.execution_start_stage_event_id
		 AND latest.authority_epoch=handoff.authority_epoch AND latest.authority_stage_event_id=handoff.authority_stage_event_id
		WHERE handoff.id=? AND handoff.api_key_id=? AND reporter_user.effective_role<>'external' AND
		 (reporter_user.effective_role IN ('admin','super_admin') OR
		 EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=reporter_user.id AND membership.project_id=handoff.project_id AND membership.access_level IN ('viewer','editor')) OR
		 (reporter_user.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=reporter_user.id AND membership.project_id=handoff.project_id)))
		AND ((handoff.reporter_role='owner' AND latest.current_reporter_id=handoff.reporter_id) OR
		 (handoff.reporter_role='dependency' AND EXISTS(SELECT 1 FROM external_stage_prerequisites prerequisite
		  JOIN external_stage_prerequisite_sets prerequisite_set ON prerequisite_set.attempt_id=prerequisite.attempt_id
		   AND prerequisite_set.stage_key=prerequisite.stage_key AND prerequisite_set.execution_number=prerequisite.execution_number
		   AND prerequisite_set.authority_epoch=prerequisite.authority_epoch AND prerequisite_set.sealed_at IS NOT NULL
		  WHERE prerequisite.attempt_id=handoff.attempt_id AND prerequisite.stage_key=handoff.stage_key
		   AND prerequisite.execution_number=handoff.execution_number AND prerequisite.authority_epoch=handoff.authority_epoch
		   AND prerequisite.dependency_key=handoff.dependency_key AND prerequisite.registration_id=handoff.reporter_registration_id)))`,
		p.UserID, h.rowID, p.APIKeyID).Scan(&current)
	if !secretOK || p.Kind != "api_key" || p.UserID <= 0 || p.APIKeyID != h.apiKeyID || err != nil || current != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Pull(ctx context.Context, p Principal, handoffID string, secret []byte) (PullResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PullResponse{}, err
	}
	defer tx.Rollback()
	h, err := s.loadHandoffTx(ctx, tx, handoffID)
	if err != nil || s.authenticateExternalTx(ctx, tx, h, p, secret) != nil {
		return PullResponse{}, ErrNotFound
	}
	expires, err := time.Parse(time.RFC3339Nano, h.expiresAt)
	if err != nil || !expires.After(s.clock.Now().UTC()) || h.revokedAt != "" || h.terminalAt != "" {
		return PullResponse{}, ErrNotFound
	}
	return h.pull(s.fixture), nil
}

func (s *Service) Accept(ctx context.Context, p Principal, handoffID, idempotencyKey string, secret []byte, req AcceptRequest) (ReportReceipt, error) {
	if req.Sequence <= 0 || idempotencyKey == "" {
		return ReportReceipt{}, ErrInvalid
	}
	observed, err := time.Parse(time.RFC3339Nano, req.ObservedAt)
	if err != nil || observed.After(s.clock.Now().UTC().Add(MaxReporterFutureSkew)) {
		return ReportReceipt{}, ErrInvalid
	}
	req.ObservedAt = observed.UTC().Format(time.RFC3339Nano)
	reqDigest, _ := canonicalDigest(req)
	idem := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReportReceipt{}, err
	}
	defer tx.Rollback()
	h, err := s.loadHandoffTx(ctx, tx, handoffID)
	if err != nil || s.authenticateExternalTx(ctx, tx, h, p, secret) != nil {
		return ReportReceipt{}, ErrNotFound
	}
	if receipt, found, err := lookupOperationReceipt(ctx, tx, h, "accepted", idem, reqDigest); err != nil {
		return ReportReceipt{}, err
	} else if found {
		return receipt, nil
	}
	expires, expiryErr := time.Parse(time.RFC3339Nano, h.expiresAt)
	if expiryErr != nil || !expires.After(s.clock.Now().UTC()) || h.revokedAt != "" || h.terminalAt != "" {
		return ReportReceipt{}, ErrConflict
	}
	if h.state != "issued" || req.Sequence != h.lastSequence+1 {
		return ReportReceipt{}, ErrConflict
	}
	op, received, err := insertOperation(ctx, tx, h, "accepted", reqDigest, idem, p, "api_key", nil, p.APIKeyID, h.credentialEpoch, req.Sequence)
	if err != nil {
		return ReportReceipt{}, mapConflict(err)
	}
	if err := insertOperationAudit(ctx, tx, h, op, "accepted", h.credentialEpoch, req.Sequence, p.APIKeyID, received); err != nil {
		return ReportReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE external_stage_handoffs SET lifecycle_state='accepted',last_sequence=?,accepted_at=? WHERE id=?`, req.Sequence, received, h.rowID); err != nil {
		return ReportReceipt{}, mapConflict(err)
	}
	if err := tx.Commit(); err != nil {
		return ReportReceipt{}, err
	}
	return ReportReceipt{HandoffID: h.handoffID, Sequence: req.Sequence, State: HandoffStateAccepted, CredentialEpoch: h.credentialEpoch, ServerReceivedAt: received}, nil
}

func lookupOperationReceipt(ctx context.Context, tx *sql.Tx, h handoffRow, kind string, idem, request [32]byte) (ReportReceipt, bool, error) {
	var r ReportReceipt
	var prior []byte
	err := tx.QueryRowContext(ctx, `SELECT operation.sequence,'accepted',operation.credential_epoch,
		operation.server_received_at,operation.request_digest FROM external_stage_operation_events operation
		JOIN external_stage_handoffs handoff ON handoff.id=operation.handoff_row_id WHERE operation.handoff_row_id=?
		AND operation.operation_kind=? AND operation.idempotency_digest=?`, h.rowID, kind, idem[:]).Scan(&r.Sequence, &r.State, &r.CredentialEpoch, &r.ServerReceivedAt, &prior)
	if errors.Is(err, sql.ErrNoRows) {
		return ReportReceipt{}, false, nil
	}
	if err != nil {
		return ReportReceipt{}, false, err
	}
	if subtle.ConstantTimeCompare(prior, request[:]) != 1 {
		return ReportReceipt{}, false, ErrConflict
	}
	r.HandoffID = h.handoffID
	r.Duplicate = true
	return r, true, nil
}

func (s *Service) Report(ctx context.Context, p Principal, handoffID, idempotencyKey string, secret []byte, req ReportRequest) (ReportReceipt, error) {
	if req.Sequence <= 0 || idempotencyKey == "" || !validReportState(req.State) {
		return ReportReceipt{}, ErrInvalid
	}
	observed, err := time.Parse(time.RFC3339Nano, req.ObservedAt)
	if err != nil || observed.After(s.clock.Now().UTC().Add(MaxReporterFutureSkew)) {
		return ReportReceipt{}, ErrInvalid
	}
	req.ObservedAt = observed.UTC().Format(time.RFC3339Nano)
	if req.PharosEvidence != nil {
		evidence := *req.PharosEvidence
		at, parseErr := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
		if parseErr != nil || at.After(s.clock.Now().UTC().Add(MaxReporterFutureSkew)) {
			return ReportReceipt{}, ErrInvalid
		}
		evidence.ObservedAt = at.UTC().Format(time.RFC3339Nano)
		req.PharosEvidence = &evidence
	}
	if req.JanusEvidence != nil {
		evidence := *req.JanusEvidence
		at, parseErr := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
		if parseErr != nil || at.After(s.clock.Now().UTC().Add(MaxReporterFutureSkew)) {
			return ReportReceipt{}, ErrInvalid
		}
		evidence.ObservedAt = at.UTC().Format(time.RFC3339Nano)
		req.JanusEvidence = &evidence
	}
	requestDigest, err := canonicalDigest(req)
	if err != nil {
		return ReportReceipt{}, err
	}
	idemDigest := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReportReceipt{}, err
	}
	defer tx.Rollback()
	h, err := s.loadHandoffTx(ctx, tx, handoffID)
	if err != nil || s.authenticateExternalTx(ctx, tx, h, p, secret) != nil {
		return ReportReceipt{}, ErrNotFound
	}
	if receipt, found, err := lookupReportReceipt(ctx, tx, h, idemDigest, requestDigest); err != nil {
		return ReportReceipt{}, err
	} else if found {
		return receipt, nil
	}
	expires, expiryErr := time.Parse(time.RFC3339Nano, h.expiresAt)
	if expiryErr != nil || !expires.After(s.clock.Now().UTC()) {
		return ReportReceipt{}, ErrConflict
	}
	if h.terminalAt != "" || h.revokedAt != "" || (h.state != "accepted" && h.state != "active" && h.state != "waiting" && h.state != "blocked") {
		return ReportReceipt{}, ErrConflict
	}
	if req.Sequence != h.lastSequence+1 {
		return ReportReceipt{}, ErrConflict
	}
	if !req.Heartbeat && (h.state == "active" || h.state == "waiting" || h.state == "blocked") {
		stale, err := activeReporterStaleTx(ctx, tx, h, s.activeStaleAfter)
		if err != nil {
			return ReportReceipt{}, err
		}
		if stale {
			return ReportReceipt{}, ErrConflict
		}
	}
	if req.Heartbeat {
		return s.reportHeartbeat(ctx, tx, h, p, requestDigest, idemDigest, req)
	}
	if err := validateSemanticReport(h, req, secret); err != nil {
		return ReportReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO external_stage_report_events(handoff_row_id,actor_api_key_id,
		sequence,credential_epoch,request_digest,idempotency_digest,lifecycle_state,observed_at,heartbeat,
		declared_blockers,evidence_kind) VALUES(?,?,?,?,?,?,?,?,0,?,?)`, h.rowID, p.APIKeyID, req.Sequence,
		h.credentialEpoch, requestDigest[:], idemDigest[:], req.State, req.ObservedAt, len(req.BlockerCodes), reportEvidenceKind(req))
	if err != nil {
		return ReportReceipt{}, mapConflict(err)
	}
	reportID, _ := result.LastInsertId()
	var received string
	if err := tx.QueryRowContext(ctx, `SELECT server_received_at FROM external_stage_report_events WHERE id=?`, reportID).Scan(&received); err != nil {
		return ReportReceipt{}, err
	}
	for i, blocker := range req.BlockerCodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO external_stage_report_blockers(report_event_id,ordinal,blocker_code) VALUES(?,?,?)`, reportID, i, blocker); err != nil {
			return ReportReceipt{}, ErrInvalid
		}
	}
	if err := insertTypedEvidence(ctx, tx, h, reportID, received, req); err != nil {
		return ReportReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO external_stage_audit_events(event_kind,handoff_row_id,report_event_id,
		api_key_id,credential_epoch,sequence,outcome,server_received_at) VALUES('reported',?,?,?,?,?,'committed',?)`,
		h.rowID, reportID, p.APIKeyID, h.credentialEpoch, req.Sequence, received); err != nil {
		return ReportReceipt{}, err
	}

	deliveryStore := delivery.NewStore(nil, delivery.Options{Clock: deliveryClock{s.clock}, Observer: s.observer})
	effects := delivery.NewEffects(s.observer)
	var dependencyHint *delivery.ChangeHint
	if h.role == string(ReporterRoleOwner) {
		streamSequence, err := nextOwnerStreamSequence(ctx, tx, h)
		if err != nil {
			return ReportReceipt{}, err
		}
		ownerEvent, err := tx.ExecContext(ctx, `INSERT INTO external_stage_owner_events(delivery_id,attempt_id,stage_key,
			execution_number,authority_epoch,handoff_row_id,report_event_id,sequence,stream_sequence,lifecycle_state,server_received_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, h.deliveryID, h.attemptID, h.stageKey, h.executionNumber, h.authorityEpoch, h.rowID, reportID, req.Sequence, streamSequence, req.State, received)
		if err != nil {
			return ReportReceipt{}, err
		}
		ownerID, _ := ownerEvent.LastInsertId()
		if _, err := tx.ExecContext(ctx, `INSERT INTO external_stage_owner_latest(delivery_id,attempt_id,stage_key,execution_number,
			authority_epoch,owner_event_id,handoff_row_id,report_event_id,sequence,stream_sequence,lifecycle_state,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(attempt_id,stage_key,execution_number,authority_epoch) DO UPDATE SET
			owner_event_id=excluded.owner_event_id,handoff_row_id=excluded.handoff_row_id,report_event_id=excluded.report_event_id,
			sequence=excluded.sequence,stream_sequence=excluded.stream_sequence,lifecycle_state=excluded.lifecycle_state,
			updated_at=excluded.updated_at`, h.deliveryID, h.attemptID, h.stageKey, h.executionNumber, h.authorityEpoch,
			ownerID, h.rowID, reportID, req.Sequence, streamSequence, req.State, received); err != nil {
			return ReportReceipt{}, err
		}
		var opaque string
		if err := tx.QueryRowContext(ctx, `SELECT opaque_key FROM delivery_reporters WHERE delivery_id=? AND id=?`, h.deliveryID, h.reporterID).Scan(&opaque); err != nil {
			return ReportReceipt{}, err
		}
		canonical, err := canonicalStageReport(h, opaque, idempotencyKey, streamSequence, req)
		if err != nil {
			return ReportReceipt{}, err
		}
		if _, err := deliveryStore.ReportStageTx(ctx, tx, effects, canonical); err != nil {
			return ReportReceipt{}, mapDeliveryError(err)
		}
		if len(effects.Hints()) != 1 {
			return ReportReceipt{}, fmt.Errorf("external stage owner report produced %d canonical change hints", len(effects.Hints()))
		}
	} else {
		streamSequence, err := nextDependencyStreamSequence(ctx, tx, h)
		if err != nil {
			return ReportReceipt{}, err
		}
		dependencyEvent, err := tx.ExecContext(ctx, `INSERT INTO external_stage_dependency_events(delivery_id,attempt_id,stage_key,
			execution_number,authority_epoch,dependency_key,registration_id,handoff_row_id,report_event_id,credential_epoch,
			sequence,stream_sequence,lifecycle_state,server_received_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, h.deliveryID, h.attemptID, h.stageKey,
			h.executionNumber, h.authorityEpoch, h.dependency, h.registrationID, h.rowID, reportID, h.credentialEpoch,
			req.Sequence, streamSequence, req.State, received)
		if err != nil {
			return ReportReceipt{}, err
		}
		dependencyID, _ := dependencyEvent.LastInsertId()
		if _, err := tx.ExecContext(ctx, `INSERT INTO external_stage_dependency_latest(delivery_id,attempt_id,stage_key,execution_number,
			authority_epoch,dependency_key,registration_id,credential_epoch,dependency_event_id,handoff_row_id,report_event_id,
			sequence,stream_sequence,lifecycle_state,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id)
			DO UPDATE SET dependency_event_id=excluded.dependency_event_id,handoff_row_id=excluded.handoff_row_id,
			report_event_id=excluded.report_event_id,credential_epoch=excluded.credential_epoch,sequence=excluded.sequence,
			stream_sequence=excluded.stream_sequence,lifecycle_state=excluded.lifecycle_state,
			updated_at=excluded.updated_at`, h.deliveryID, h.attemptID, h.stageKey, h.executionNumber, h.authorityEpoch, h.dependency,
			h.registrationID, h.credentialEpoch, dependencyID, h.rowID, reportID, req.Sequence, streamSequence, req.State, received); err != nil {
			return ReportReceipt{}, err
		}
		hint, err := deliveryStore.RecordExternalDependencyChangeTx(ctx, tx, h.issueID, streamSequence)
		if err != nil {
			return ReportReceipt{}, err
		}
		dependencyHint = &hint
	}
	terminal := any(nil)
	if req.State == HandoffStateSucceeded || req.State == HandoffStateFailed {
		terminal = received
	}
	if _, err := tx.ExecContext(ctx, `UPDATE external_stage_handoffs SET lifecycle_state=?,last_sequence=?,terminal_at=? WHERE id=?`, req.State, req.Sequence, terminal, h.rowID); err != nil {
		return ReportReceipt{}, mapConflict(err)
	}
	if s.beforeCommit != nil {
		if err := s.beforeCommit("report"); err != nil {
			return ReportReceipt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ReportReceipt{}, err
	}
	if dependencyHint != nil {
		if s.observer != nil {
			s.observer(ctx, *dependencyHint)
		}
	} else {
		effects.Dispatch(ctx)
	}
	return ReportReceipt{HandoffID: h.handoffID, Sequence: req.Sequence, State: req.State, CredentialEpoch: h.credentialEpoch, ServerReceivedAt: received}, nil
}

func activeReporterStaleTx(ctx context.Context, tx *sql.Tx, h handoffRow, after time.Duration) (bool, error) {
	var stale bool
	err := tx.QueryRowContext(ctx, `SELECT julianday(strftime('%Y-%m-%dT%H:%M:%fZ','now'))-julianday(MAX(server_received_at))>?/86400.0 FROM (
		SELECT server_received_at FROM external_stage_operation_events WHERE handoff_row_id=? AND operation_kind='accepted'
		UNION ALL SELECT server_received_at FROM external_stage_report_events WHERE handoff_row_id=?
		UNION ALL SELECT last_received_at AS server_received_at FROM external_stage_heartbeat_windows WHERE handoff_row_id=?)`,
		after.Seconds(), h.rowID, h.rowID, h.rowID).Scan(&stale)
	return stale, err
}

type deliveryClock struct{ Clock }

func (c deliveryClock) Now() time.Time { return c.Clock.Now() }

func validReportState(state HandoffState) bool {
	return state == HandoffStateActive || state == HandoffStateWaiting || state == HandoffStateBlocked || state == HandoffStateSucceeded || state == HandoffStateFailed
}

func lookupReportReceipt(ctx context.Context, tx *sql.Tx, h handoffRow, idem, request [32]byte) (ReportReceipt, bool, error) {
	var r ReportReceipt
	var prior []byte
	err := tx.QueryRowContext(ctx, `SELECT sequence,lifecycle_state,credential_epoch,server_received_at,request_digest
		FROM external_stage_report_events WHERE handoff_row_id=? AND idempotency_digest=?`, h.rowID, idem[:]).Scan(&r.Sequence, &r.State, &r.CredentialEpoch, &r.ServerReceivedAt, &prior)
	if errors.Is(err, sql.ErrNoRows) {
		var requestHex string
		err = tx.QueryRowContext(ctx, `SELECT CAST(json_extract(replay.value,'$.sequence') AS INTEGER),window.lifecycle_state,
			window.credential_epoch,json_extract(replay.value,'$.server_received_at'),json_extract(replay.value,'$.request_digest')
			FROM external_stage_heartbeat_windows window,json_each(window.replay_json) replay
			WHERE window.handoff_row_id=? AND json_extract(replay.value,'$.idempotency_digest')=? LIMIT 1`, h.rowID, hex.EncodeToString(idem[:])).Scan(&r.Sequence, &r.State, &r.CredentialEpoch, &r.ServerReceivedAt, &requestHex)
		if errors.Is(err, sql.ErrNoRows) {
			return ReportReceipt{}, false, nil
		}
		if err != nil {
			return ReportReceipt{}, false, err
		}
		prior, err = hex.DecodeString(requestHex)
		if err != nil {
			return ReportReceipt{}, false, err
		}
	} else if err != nil {
		return ReportReceipt{}, false, err
	}
	if subtle.ConstantTimeCompare(prior, request[:]) != 1 {
		return ReportReceipt{}, false, ErrConflict
	}
	r.HandoffID = h.handoffID
	r.Duplicate = true
	return r, true, nil
}

func (s *Service) reportHeartbeat(ctx context.Context, tx *sql.Tx, h handoffRow, p Principal, request, idem [32]byte, req ReportRequest) (ReportReceipt, error) {
	if len(req.BlockerCodes) != 0 || req.PharosEvidence != nil || req.JanusEvidence != nil || req.State != HandoffState(h.state) || (h.state != "active" && h.state != "waiting" && h.state != "blocked") {
		return ReportReceipt{}, ErrInvalid
	}
	requestHex, idemHex := hex.EncodeToString(request[:]), hex.EncodeToString(idem[:])
	result, err := tx.ExecContext(ctx, `UPDATE external_stage_heartbeat_windows SET last_sequence=?,heartbeat_count=heartbeat_count+1,
		replay_json=json_insert(replay_json,'$[#]',json_object('sequence',?,'request_digest',?,'idempotency_digest',?,
		 'server_received_at',strftime('%Y-%m-%dT%H:%M:%fZ','now'))),
		last_observed_at=?,last_received_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=(SELECT id FROM external_stage_heartbeat_windows
		WHERE handoff_row_id=? AND credential_epoch=? AND lifecycle_state=? AND heartbeat_count<64
		AND julianday(strftime('%Y-%m-%dT%H:%M:%fZ','now'))-julianday(window_started_at)<=30.0/86400.0 ORDER BY window_number DESC LIMIT 1)`,
		req.Sequence, req.Sequence, requestHex, idemHex, req.ObservedAt, h.rowID, h.credentialEpoch, h.state)
	if err != nil {
		return ReportReceipt{}, mapConflict(err)
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		var window int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(window_number),0)+1 FROM external_stage_heartbeat_windows WHERE handoff_row_id=? AND credential_epoch=?`, h.rowID, h.credentialEpoch).Scan(&window); err != nil {
			return ReportReceipt{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO external_stage_heartbeat_windows(handoff_row_id,actor_api_key_id,credential_epoch,
			window_number,first_sequence,last_sequence,heartbeat_count,lifecycle_state,replay_json,last_observed_at)
			VALUES(?,?,?,?,?,?,1,?,json_array(json_object('sequence',?,'request_digest',?,'idempotency_digest',?,
			 'server_received_at',strftime('%Y-%m-%dT%H:%M:%fZ','now'))),?)`, h.rowID, p.APIKeyID,
			h.credentialEpoch, window, req.Sequence, req.Sequence, req.State, req.Sequence, requestHex, idemHex, req.ObservedAt); err != nil {
			return ReportReceipt{}, mapConflict(err)
		}
	}
	var received string
	if err := tx.QueryRowContext(ctx, `SELECT last_received_at FROM external_stage_heartbeat_windows WHERE handoff_row_id=? AND credential_epoch=? ORDER BY window_number DESC LIMIT 1`, h.rowID, h.credentialEpoch).Scan(&received); err != nil {
		return ReportReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE external_stage_handoffs SET last_sequence=? WHERE id=?`, req.Sequence, h.rowID); err != nil {
		return ReportReceipt{}, mapConflict(err)
	}
	if err := tx.Commit(); err != nil {
		return ReportReceipt{}, err
	}
	return ReportReceipt{HandoffID: h.handoffID, Sequence: req.Sequence, State: req.State, CredentialEpoch: h.credentialEpoch, ServerReceivedAt: received}, nil
}

func reportEvidenceKind(req ReportRequest) any {
	if req.PharosEvidence != nil {
		return req.PharosEvidence.Kind
	}
	if req.JanusEvidence != nil {
		return req.JanusEvidence.Kind
	}
	return nil
}

func validateSemanticReport(h handoffRow, req ReportRequest, secret []byte) error {
	seen := map[BlockerCode]bool{}
	for _, b := range req.BlockerCodes {
		if seen[b] || !(b == BlockerDependencyPending || b == BlockerDependencyFailed || b == BlockerReporterStale || b == BlockerExternalWaiting) {
			return ErrInvalid
		}
		seen[b] = true
	}
	if req.State == HandoffStateBlocked && len(req.BlockerCodes) == 0 {
		return ErrInvalid
	}
	if req.State != HandoffStateBlocked && len(req.BlockerCodes) > 0 {
		return ErrInvalid
	}
	if h.class == string(ReporterClassPharos) {
		if req.JanusEvidence != nil {
			return ErrInvalid
		}
		if req.PharosEvidence != nil {
			e := req.PharosEvidence
			if e.ObservedAt != req.ObservedAt || e.Workflow != h.workflow || e.Environment != h.environment || !symbolPattern.MatchString(e.Workflow) || !symbolPattern.MatchString(e.Environment) ||
				secretEcho(e.Artifact.Version, secret) || secretEcho(e.Artifact.Digest, secret) || secretEcho(e.Artifact.CommitDigest, secret) ||
				!versionPattern.MatchString(e.Artifact.Version) || !commitPattern.MatchString(e.Artifact.CommitDigest) {
				return ErrInvalid
			}
			if _, err := decodeWireDigest(e.Artifact.Digest); err != nil {
				return ErrInvalid
			}
			if !((e.Kind == EvidenceKindDeployment && h.allowDeployment && h.stageKey == "deployment") || (e.Kind == EvidenceKindVerification && h.allowVerification && h.stageKey == "verification")) {
				return ErrInvalid
			}
			if !((e.Result == EvidenceResultSucceeded && req.State == HandoffStateSucceeded) || (e.Result == EvidenceResultFailed && req.State == HandoffStateFailed)) {
				return ErrInvalid
			}
		}
	}
	if h.class == string(ReporterClassJanus) {
		if req.PharosEvidence != nil {
			return ErrInvalid
		}
		if req.State == HandoffStateBlocked && req.JanusEvidence == nil {
			return ErrInvalid
		}
		if req.JanusEvidence != nil {
			e := req.JanusEvidence
			if e.ObservedAt != req.ObservedAt {
				return ErrInvalid
			}
			if !((e.Kind == EvidenceKindAuthorization && h.allowAuthorization && e.Authorized != nil && e.CredentialReady == nil) || (e.Kind == EvidenceKindCredentialHandoff && h.allowCredential && e.CredentialReady != nil && e.Authorized == nil)) {
				return ErrInvalid
			}
			value := false
			if e.Authorized != nil {
				value = *e.Authorized
			} else {
				value = *e.CredentialReady
			}
			if !((e.Result == EvidenceResultSatisfied && value && req.State == HandoffStateSucceeded) || (e.Result == EvidenceResultBlocked && !value && req.State == HandoffStateBlocked)) {
				return ErrInvalid
			}
		}
	}
	if (req.State == HandoffStateSucceeded || req.State == HandoffStateFailed) && req.PharosEvidence == nil && req.JanusEvidence == nil {
		return ErrInvalid
	}
	return nil
}

func secretEcho(value string, secret []byte) bool {
	if len(secret) != OneTimeSecretBytes {
		return false
	}
	hexValue := hex.EncodeToString(secret)
	candidates := []string{
		hexValue,
		"sha256:" + hexValue,
		base64.RawURLEncoding.EncodeToString(secret),
		base64.URLEncoding.EncodeToString(secret),
		base64.RawStdEncoding.EncodeToString(secret),
		base64.StdEncoding.EncodeToString(secret),
	}
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func decodeWireDigest(value string) ([]byte, error) {
	if !strings.HasPrefix(value, "sha256:") {
		return nil, ErrInvalid
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	if err != nil || len(raw) != 32 || value != "sha256:"+hex.EncodeToString(raw) {
		return nil, ErrInvalid
	}
	return raw, nil
}

func insertTypedEvidence(ctx context.Context, tx *sql.Tx, h handoffRow, reportID int64, received string, req ReportRequest) error {
	if req.PharosEvidence != nil {
		e := req.PharosEvidence
		digest, _ := decodeWireDigest(e.Artifact.Digest)
		_, err := tx.ExecContext(ctx, `INSERT INTO external_stage_pharos_evidence(report_event_id,evidence_kind,workflow_symbol,
		environment_symbol,artifact_version,artifact_digest,commit_digest,result,observed_at,server_received_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, reportID, e.Kind, e.Workflow, e.Environment, e.Artifact.Version, digest, e.Artifact.CommitDigest, e.Result, e.ObservedAt, received)
		return mapInvalid(err)
	}
	if req.JanusEvidence != nil {
		e := req.JanusEvidence
		var value bool
		if e.Authorized != nil {
			value = *e.Authorized
		} else {
			value = *e.CredentialReady
		}
		var authorized, credentialReady any
		if e.Kind == EvidenceKindAuthorization {
			authorized = boolInt(value)
		} else {
			credentialReady = boolInt(value)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO external_stage_janus_evidence(report_event_id,evidence_kind,result,
			authorized,credential_ready,observed_at,server_received_at) VALUES(?,?,?,?,?,?,?)`, reportID, e.Kind,
			e.Result, authorized, credentialReady, e.ObservedAt, received)
		return mapInvalid(err)
	}
	return nil
}

func mapInvalid(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "constraint") {
		return ErrInvalid
	}
	return err
}

func nextOwnerStreamSequence(ctx context.Context, tx *sql.Tx, h handoffRow) (int64, error) {
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT 1+MAX(
		COALESCE((SELECT authority_source_sequence_cutoff FROM delivery_stage_events WHERE id=?),0),
		COALESCE((SELECT MAX(source_sequence) FROM delivery_stage_events WHERE attempt_id=? AND stage_key=?
		 AND execution_number=? AND authority_epoch=?),0),
		COALESCE((SELECT MAX(stream_sequence) FROM external_stage_owner_events WHERE attempt_id=? AND stage_key=?
		 AND execution_number=? AND authority_epoch=?),0))`, h.authorityEventID, h.attemptID, h.stageKey,
		h.executionNumber, h.authorityEpoch, h.attemptID, h.stageKey, h.executionNumber, h.authorityEpoch).Scan(&sequence)
	return sequence, err
}

func nextDependencyStreamSequence(ctx context.Context, tx *sql.Tx, h handoffRow) (int64, error) {
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT 1+COALESCE(MAX(stream_sequence),0) FROM external_stage_dependency_events
		WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?
		 AND dependency_key=? AND registration_id=?`, h.attemptID, h.stageKey, h.executionNumber,
		h.authorityEpoch, h.dependency, h.registrationID).Scan(&sequence)
	return sequence, err
}

func canonicalStageReport(h handoffRow, opaque, idempotency string, streamSequence int64, req ReportRequest) (delivery.StageReport, error) {
	state := string(req.State)
	if req.State == HandoffStateBlocked {
		state = "waiting"
	}
	blockers := make([]delivery.Blocker, 0, len(req.BlockerCodes))
	for _, code := range req.BlockerCodes {
		class, summary := "external", "External reporter is waiting"
		if code == BlockerDependencyPending || code == BlockerDependencyFailed {
			class = "dependency"
			summary = "Declared external dependency is not satisfied"
		}
		blockers = append(blockers, delivery.Blocker{Key: "external-stage:" + string(code), Class: class, Summary: summary})
	}
	evidence := []delivery.Evidence{}
	if req.PharosEvidence != nil {
		e := req.PharosEvidence
		outcome := "failed"
		if e.Result == EvidenceResultSucceeded {
			outcome = "passed"
		}
		digest, _ := decodeWireDigest(e.Artifact.Digest)
		evidence = append(evidence, delivery.Evidence{Type: h.stageKey + "_result", Outcome: outcome, ReferenceKind: "digest", DigestSHA256: hex.EncodeToString(digest)})
	}
	return delivery.StageReport{IssueID: h.issueID, AttemptNumber: h.attemptNumber, StageKey: h.stageKey, ExecutionNumber: h.executionNumber, AuthorityEpoch: h.authorityEpoch, Reporter: delivery.Actor{Type: "external", OpaqueKey: opaque}, IdempotencyKey: "external-stage:" + h.handoffID + ":" + fmt.Sprint(req.Sequence), SourceSequence: &streamSequence, Kind: "semantic", State: state, Blockers: blockers, Evidence: evidence, ReasonCode: "external_stage_report"}, nil
}

func mapDeliveryError(err error) error {
	if errors.Is(err, delivery.ErrNotFound) || errors.Is(err, delivery.ErrUnauthorized) {
		return ErrNotFound
	}
	if errors.Is(err, delivery.ErrConflict) || errors.Is(err, delivery.ErrStaleAuthority) || errors.Is(err, delivery.ErrInvalid) {
		return ErrConflict
	}
	return err
}
