// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/models"
)

const RetirementKind = "retire_after_work"

const retirementColumns = `id,project_id,harness_session_id,harness_session_revision,requested_activity_sequence,
	runtime_id,runtime_generation,session_generation,ticket_id,parent_harness_session_id,work_shape,
	request_key,request_digest,requested_by_user_id,requested_session_credential_id,state,reason,
	requested_at,COALESCE(claimed_at,''),COALESCE(stopping_at,''),COALESCE(completed_at,'')`

type retirementRecord struct {
	ID, SessionID, RuntimeID, RuntimeGeneration, SessionGeneration string
	ParentSessionID                                                *string
	TicketID                                                       *int64
	ProjectID, SessionRevision, ActivitySequence, RequestedBy      int64
	WorkShape, RequestKey, SessionCredential, State, Reason        string
	RequestDigest                                                  []byte
	RequestedAt, ClaimedAt, StoppingAt, CompletedAt                string
}

type BrowserRetirementResponse struct {
	SchemaVersion int                             `json:"schema_version"`
	Retirement    models.HarnessRetirementOutcome `json:"retirement"`
}

func scanRetirement(row interface{ Scan(...any) error }) (retirementRecord, error) {
	var out retirementRecord
	err := row.Scan(&out.ID, &out.ProjectID, &out.SessionID, &out.SessionRevision, &out.ActivitySequence,
		&out.RuntimeID, &out.RuntimeGeneration, &out.SessionGeneration, &out.TicketID, &out.ParentSessionID,
		&out.WorkShape, &out.RequestKey, &out.RequestDigest, &out.RequestedBy, &out.SessionCredential,
		&out.State, &out.Reason, &out.RequestedAt, &out.ClaimedAt, &out.StoppingAt, &out.CompletedAt)
	return out, err
}

func retirementDigest(user, project int64, credential, session string, revision int64, requestKey string) [sha256.Size]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("paimos-harness-retirement-v1:%d:%s:%d:%s:%d:%s", user, credential, project, session, revision, requestKey)))
}

func retirementID(digest [sha256.Size]byte) string {
	digest[6] = (digest[6] & 0x0f) | 0x40
	digest[8] = (digest[8] & 0x3f) | 0x80
	value, _ := uuid.FromBytes(digest[:16])
	return value.String()
}

func retirementOutcome(record retirementRecord, sessionPhase, closedReason string) models.HarnessRetirementOutcome {
	state, reason := "requested", record.Reason
	stopReceipt, stoppedProof := record.State == ControlApplied, sessionPhase == PhaseStopped && closedReason == ClosedStopped
	switch record.State {
	case ControlClaimed:
		state = "finishing"
	case "stopping":
		state = "stopping"
	case ControlApplied:
		state = "stopping"
		if stoppedProof {
			state = "completed"
		}
	case ControlRejected:
		state = "failed"
		if reason == "outcome_unknown" {
			state = "outcome_unknown"
		}
	}
	return models.HarnessRetirementOutcome{ID: record.ID, ProjectID: record.ProjectID,
		HarnessSessionID: record.SessionID, CorrelationID: record.ID, Kind: RetirementKind,
		RequestedRevision: record.SessionRevision, State: state, Reason: reason,
		RequestedAt: record.RequestedAt, ClaimedAt: record.ClaimedAt, StoppingAt: record.StoppingAt,
		CompletedAt: record.CompletedAt, OwnedStopReceipt: stopReceipt, StoppedGenerationProof: stoppedProof}
}

// RequestRetirementCAS atomically reauthorizes the current human and project,
// binds one request to the exact public/private generation and revision, and
// installs the admission fence. Exact replays return the original durable row
// even after later heartbeats advance the session revision.
func (s *Service) RequestRetirementCAS(ctx context.Context, p auth.Principal, project int64, sessionID string, request BrowserControlRequest) (BrowserRetirementResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BrowserRetirementResponse{}, ErrBrowserStorage
	}
	defer tx.Rollback()
	if err = browserAuthority(ctx, tx, p, project, true); err != nil {
		return BrowserRetirementResponse{}, err
	}
	if request.ExpectedRevision < 1 || uuid.Validate(request.RequestKey) != nil || uuid.Validate(sessionID) != nil {
		return BrowserRetirementResponse{}, ErrBrowserInvalid
	}
	digest := retirementDigest(p.UserID(), project, p.SessionCredentialID(), sessionID, request.ExpectedRevision, request.RequestKey)
	prior, priorErr := scanRetirement(tx.QueryRowContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements
		WHERE project_id=? AND requested_by_user_id=? AND request_key=?`, project, p.UserID(), request.RequestKey))
	if priorErr == nil {
		if prior.SessionID != sessionID || subtle.ConstantTimeCompare(prior.RequestDigest, digest[:]) != 1 {
			return BrowserRetirementResponse{}, ErrBrowserConflict
		}
		var phase, closed string
		if tx.QueryRowContext(ctx, `SELECT phase,closed_reason FROM harness_sessions WHERE id=? AND project_id=?`, sessionID, project).Scan(&phase, &closed) != nil || tx.Commit() != nil {
			return BrowserRetirementResponse{}, ErrBrowserStorage
		}
		return BrowserRetirementResponse{SchemaVersion: 1, Retirement: retirementOutcome(prior, phase, closed)}, nil
	}
	if !errors.Is(priorErr, sql.ErrNoRows) {
		return BrowserRetirementResponse{}, ErrBrowserStorage
	}
	var revision, activity int64
	var runtimeID, runtimeGeneration, sessionGeneration, phase, closed, mode, shape string
	var stop int
	var ticket *int64
	var parent *string
	err = tx.QueryRowContext(ctx, `SELECT session.revision,session.activity_sequence,runtime.id,runtime.generation,owned.generation,
		session.phase,session.closed_reason,session.management_mode,session.advertised_stop,session.ticket_id,
		session.parent_harness_session_id,COALESCE(session.work_shape,'unknown')
		FROM harness_sessions session
		JOIN lifecycle_runtime_sessions owned ON owned.session_id=session.id
		JOIN lifecycle_runtimes runtime ON runtime.id=owned.runtime_id
		WHERE session.id=? AND session.project_id=? AND runtime.project_id=? AND runtime.machine_id=session.host
		 AND runtime.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`, sessionID, project, project).
		Scan(&revision, &activity, &runtimeID, &runtimeGeneration, &sessionGeneration, &phase, &closed, &mode, &stop, &ticket, &parent, &shape)
	if err != nil || phase == PhaseStopped || mode != ManagementManaged || stop != 1 || revision != request.ExpectedRevision {
		if err == nil && revision != request.ExpectedRevision {
			return BrowserRetirementResponse{}, ErrBrowserConflict
		}
		return BrowserRetirementResponse{}, ErrBrowserUnavailable
	}
	var competing int
	if tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM harness_session_controls WHERE harness_session_id=? AND state IN ('pending','claimed'))+
		(SELECT COUNT(*) FROM harness_session_retirements WHERE harness_session_id=? AND (state<>'rejected' OR reason='outcome_unknown'))`,
		sessionID, sessionID).Scan(&competing) != nil {
		return BrowserRetirementResponse{}, ErrBrowserStorage
	}
	if competing != 0 {
		return BrowserRetirementResponse{}, ErrBrowserConflict
	}
	id := retirementID(digest)
	_, err = tx.ExecContext(ctx, `INSERT INTO harness_session_retirements(
		id,project_id,harness_session_id,harness_session_revision,requested_activity_sequence,
		runtime_id,runtime_generation,session_generation,ticket_id,parent_harness_session_id,work_shape,
		request_key,request_digest,requested_by_user_id,requested_session_credential_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, project, sessionID, revision, activity, runtimeID,
		runtimeGeneration, sessionGeneration, ticket, parent, shape, request.RequestKey, digest[:], p.UserID(), p.SessionCredentialID())
	if err != nil {
		return BrowserRetirementResponse{}, ErrBrowserStorage
	}
	record, err := scanRetirement(tx.QueryRowContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements WHERE id=?`, id))
	if err != nil || tx.Commit() != nil {
		return BrowserRetirementResponse{}, ErrBrowserStorage
	}
	return BrowserRetirementResponse{SchemaVersion: 1, Retirement: retirementOutcome(record, phase, closed)}, nil
}

func retirementClaim(record retirementRecord) models.HarnessRetirementClaim {
	return models.HarnessRetirementClaim{ID: record.ID, HarnessSessionID: record.SessionID,
		ProjectID: record.ProjectID, HarnessSessionRevision: record.SessionRevision, RequestedActivitySequence: record.ActivitySequence,
		RuntimeID: record.RuntimeID, RuntimeGeneration: record.RuntimeGeneration, SessionGeneration: record.SessionGeneration,
		State: record.State, RequestedAt: record.RequestedAt, ClaimedAt: record.ClaimedAt, StoppingAt: record.StoppingAt}
}

func claimRetirementsTx(ctx context.Context, tx *sql.Tx, sessionID string) ([]models.HarnessRetirementClaim, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE harness_session_retirements SET state='claimed',claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE harness_session_id=? AND state='pending'`, sessionID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements
		WHERE harness_session_id=? AND state IN ('claimed','stopping') ORDER BY requested_at,id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.HarnessRetirementClaim
	for rows.Next() {
		record, scanErr := scanRetirement(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, retirementClaim(record))
	}
	return out, rows.Err()
}

func retirementAuthorityCurrent(ctx context.Context, tx *sql.Tx, record retirementRecord, apiKeyID int64) (bool, error) {
	human, err := auth.NewSessionPrincipal(record.SessionCredential, record.RequestedBy, record.RequestedBy, false)
	if err != nil || browserAuthority(ctx, tx, human, record.ProjectID, true) != nil {
		return false, nil
	}
	var current int
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lifecycle_runtimes runtime
		JOIN lifecycle_runtime_sessions owned ON owned.runtime_id=runtime.id
		WHERE runtime.id=? AND runtime.project_id=? AND runtime.generation=? AND runtime.api_key_id=?
		 AND owned.session_id=? AND owned.generation=?
		 AND runtime.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		record.RuntimeID, record.ProjectID, record.RuntimeGeneration, apiKeyID,
		record.SessionID, record.SessionGeneration).Scan(&current)
	return current == 1, err
}

// PrepareRetirement is worker-lease-only. It closes the crash-safe boundary
// before the local stop effect, after the authenticated reporter has published
// a turn-completed event and every already accepted delivery has settled.
func (s *Service) PrepareRetirement(ctx context.Context, project int64, sessionID, retirementID string, apiKeyID int64) (models.HarnessRetirementClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.HarnessRetirementClaim{}, err
	}
	defer tx.Rollback()
	record, err := scanRetirement(tx.QueryRowContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements WHERE id=? AND project_id=? AND harness_session_id=?`, retirementID, project, sessionID))
	if err != nil {
		return models.HarnessRetirementClaim{}, coded(CodeNotFound, "harness retirement not found")
	}
	current, err := retirementAuthorityCurrent(ctx, tx, record, apiKeyID)
	if err != nil {
		return models.HarnessRetirementClaim{}, err
	}
	if !current {
		return models.HarnessRetirementClaim{}, coded(CodeConflict, "retirement runtime ownership changed")
	}
	if record.State == "stopping" {
		return retirementClaim(record), tx.Commit()
	}
	if record.State != ControlClaimed {
		return models.HarnessRetirementClaim{}, coded(CodeConflict, "harness retirement is not claimable")
	}
	var activity int64
	var event, phase, target string
	if err := tx.QueryRowContext(ctx, `SELECT activity_sequence,activity_event_kind,phase,COALESCE(message_target_id,'') FROM harness_sessions WHERE id=? AND project_id=?`, sessionID, project).Scan(&activity, &event, &phase, &target); err != nil || phase == PhaseStopped || event != ActivityCompleted || activity < record.ActivitySequence {
		return models.HarnessRetirementClaim{}, coded(CodeRetirementNotReady, "owned turn completion is not established")
	}
	var accepted int
	if err := tx.QueryRowContext(ctx, `SELECT
		 (SELECT COUNT(*) FROM agent_message_deliveries WHERE state='leased' AND (?<>'' AND (primary_target_id=? OR fallback_target_id=?)))+
		 (SELECT COUNT(*) FROM agent_consumer_attempts attempt JOIN agent_consumer_streams stream ON stream.id=attempt.stream_id
		   WHERE stream.session_id=? AND attempt.state IN ('claimed','executing','outcome_unknown'))`, target, target, target, sessionID).Scan(&accepted); err != nil {
		return models.HarnessRetirementClaim{}, err
	}
	if accepted != 0 {
		return models.HarnessRetirementClaim{}, coded(CodeRetirementNotReady, "accepted worker delivery is not settled")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE harness_session_retirements SET state='stopping',stopping_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND state='claimed'`, retirementID); err != nil {
		return models.HarnessRetirementClaim{}, err
	}
	record, err = scanRetirement(tx.QueryRowContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements WHERE id=?`, retirementID))
	if err != nil {
		return models.HarnessRetirementClaim{}, err
	}
	if err = tx.Commit(); err != nil {
		return models.HarnessRetirementClaim{}, err
	}
	return retirementClaim(record), nil
}

func (s *Service) CompleteRetirement(ctx context.Context, project int64, sessionID, retirementID, outcome, reason string, apiKeyID int64) (models.HarnessRetirementOutcome, error) {
	outcome, reason = strings.ToLower(strings.TrimSpace(outcome)), strings.ToLower(strings.TrimSpace(reason))
	if outcome != ControlApplied && outcome != ControlRejected {
		return models.HarnessRetirementOutcome{}, coded(CodeInvalid, "retirement outcome must be applied or rejected")
	}
	validReason := map[string]bool{ReasonApplied: true, ReasonNotRunning: true, ReasonUnsupported: true, ReasonOwnershipLost: true, ReasonFailed: true, "outcome_unknown": true}
	if !validReason[reason] || (outcome == ControlApplied) != (reason == ReasonApplied) {
		return models.HarnessRetirementOutcome{}, coded(CodeInvalid, "retirement outcome and reason do not match")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	defer tx.Rollback()
	record, err := scanRetirement(tx.QueryRowContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements WHERE id=? AND project_id=? AND harness_session_id=?`, retirementID, project, sessionID))
	if err != nil {
		return models.HarnessRetirementOutcome{}, coded(CodeNotFound, "harness retirement not found")
	}
	current, err := retirementAuthorityCurrent(ctx, tx, record, apiKeyID)
	if err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	if !current {
		return models.HarnessRetirementOutcome{}, coded(CodeConflict, "retirement runtime ownership changed")
	}
	if record.State == outcome && record.Reason == reason {
		var phase, closed string
		if err = tx.QueryRowContext(ctx, `SELECT phase,closed_reason FROM harness_sessions WHERE id=?`, sessionID).Scan(&phase, &closed); err != nil {
			return models.HarnessRetirementOutcome{}, err
		}
		if err = tx.Commit(); err != nil {
			return models.HarnessRetirementOutcome{}, err
		}
		return retirementOutcome(record, phase, closed), nil
	}
	if outcome == ControlApplied && record.State != "stopping" || outcome == ControlRejected && record.State != ControlClaimed && record.State != "stopping" {
		return models.HarnessRetirementOutcome{}, coded(CodeConflict, "harness retirement transition conflicts")
	}
	result, err := tx.ExecContext(ctx, `UPDATE harness_session_retirements SET state=?,reason=?,completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND state=?`, outcome, reason, retirementID, record.State)
	if err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return models.HarnessRetirementOutcome{}, coded(CodeConflict, "harness retirement changed concurrently")
	}
	record, err = scanRetirement(tx.QueryRowContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements WHERE id=?`, retirementID))
	var phase, closed string
	if err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT phase,closed_reason FROM harness_sessions WHERE id=?`, sessionID).Scan(&phase, &closed); err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	if err = tx.Commit(); err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	return retirementOutcome(record, phase, closed), nil
}

func (s *Service) GetRetirement(ctx context.Context, project int64, sessionID, retirementID string) (models.HarnessRetirementOutcome, error) {
	record, err := scanRetirement(s.db.QueryRowContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements WHERE id=? AND project_id=? AND harness_session_id=?`, retirementID, project, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.HarnessRetirementOutcome{}, coded(CodeNotFound, "harness retirement not found")
	}
	if err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	var phase, closed string
	if err := s.db.QueryRowContext(ctx, `SELECT phase,closed_reason FROM harness_sessions WHERE id=? AND project_id=?`, sessionID, project).Scan(&phase, &closed); err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	return retirementOutcome(record, phase, closed), nil
}

func (s *Service) GetRetirementBrowser(ctx context.Context, p auth.Principal, project int64, sessionID, retirementID string) (models.HarnessRetirementOutcome, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.HarnessRetirementOutcome{}, ErrBrowserStorage
	}
	defer tx.Rollback()
	if err := browserAuthority(ctx, tx, p, project, false); err != nil {
		return models.HarnessRetirementOutcome{}, err
	}
	record, err := scanRetirement(tx.QueryRowContext(ctx, `SELECT `+retirementColumns+` FROM harness_session_retirements WHERE id=? AND project_id=? AND harness_session_id=?`, retirementID, project, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.HarnessRetirementOutcome{}, ErrBrowserUnavailable
	}
	if err != nil {
		return models.HarnessRetirementOutcome{}, ErrBrowserStorage
	}
	var phase, closed string
	if tx.QueryRowContext(ctx, `SELECT phase,closed_reason FROM harness_sessions WHERE id=? AND project_id=?`, sessionID, project).Scan(&phase, &closed) != nil || tx.Commit() != nil {
		return models.HarnessRetirementOutcome{}, ErrBrowserStorage
	}
	return retirementOutcome(record, phase, closed), nil
}

func (s *Service) RetirementAdmissionBlocked(ctx context.Context, sessionID string) (bool, error) {
	var blocked int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM harness_session_retirements WHERE harness_session_id=? AND (state<>'rejected' OR reason='outcome_unknown'))`, sessionID).Scan(&blocked)
	return blocked == 1, err
}

func rejectRetirementForStopTx(ctx context.Context, tx *sql.Tx, sessionID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE harness_session_retirements SET state='rejected',reason='ownership_lost',
		claimed_at=COALESCE(claimed_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE harness_session_id=? AND state IN ('pending','claimed','stopping')`, sessionID)
	return err
}
