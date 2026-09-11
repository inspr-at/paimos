// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

// startIntentTTLSeconds bounds how long an owned daemon has to claim a start
// the human just confirmed. An unclaimed intent expires instead of waiting.
const startIntentTTLSeconds = 300

func (s *Service) Start(ctx context.Context, actor Actor, projectID int64, req StartRequest) (Batch, error) {
	if err := requireHuman(actor); err != nil {
		return Batch{}, err
	}
	if req.ClientReady != nil || req.ClientAuthorized != nil {
		return Batch{}, fmt.Errorf("%w: client ready/authorized claims are not authority", ErrInvalid)
	}
	if !req.Confirm {
		return Batch{}, fmt.Errorf("%w: explicit confirm is required", ErrInvalid)
	}
	if !validIdempotency(req.IdempotencyKey) {
		return Batch{}, fmt.Errorf("%w: idempotency_key", ErrInvalid)
	}
	// Agent execution goes through the lifecycle authority inside this
	// transaction, so its mutation lock is taken first: mutex before SQLite
	// write lock, never the other way round.
	if req.ExecutionMode != ModeManual {
		if s.Lifecycle == nil {
			return Batch{}, fmt.Errorf("%w: lifecycle authority", ErrUnavailable)
		}
		lifecycleintents.LockMutations()
		defer lifecycleintents.UnlockMutations()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Batch{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Batch{}, err
	}
	if err := s.requireStreamEnabled(ctx, tx, projectID); err != nil {
		return Batch{}, err
	}
	if existing, err := loadBatchByIdempotency(ctx, tx, projectID, req.IdempotencyKey); err == nil {
		if req.DraftID != 0 && existing.DraftID != req.DraftID {
			return Batch{}, fmt.Errorf("%w: idempotency key is bound to a different draft", ErrConflict)
		}
		batch, err := s.projectBatch(ctx, tx, existing)
		if err != nil {
			return Batch{}, err
		}
		if err := tx.Commit(); err != nil {
			return Batch{}, err
		}
		return batch, nil
	} else if err != nil && !isNotFound(err) {
		return Batch{}, err
	}
	var reviewMode, reviewWorkerJSON, reviewSelectedJSON, reviewDelegatedJSON, binding, sessionCred string
	var reviewDraftID, reviewRevision, reviewUser int64
	var invalidated sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT draft_id,draft_revision,binding_hash,execution_mode,worker_json,selected_requirement_refs_json,
		human_user_id,session_credential_id,invalidated_at,delegated_launch_json FROM baseline_batch_reviews WHERE id=?`, req.ReviewID).
		Scan(&reviewDraftID, &reviewRevision, &binding, &reviewMode, &reviewWorkerJSON, &reviewSelectedJSON, &reviewUser, &sessionCred, &invalidated, &reviewDelegatedJSON)
	if err == sql.ErrNoRows {
		return Batch{}, fmt.Errorf("%w: review", ErrNotFound)
	}
	if err != nil {
		return Batch{}, err
	}
	if invalidated.Valid {
		return Batch{}, fmt.Errorf("%w: review is stale", ErrStale)
	}
	if reviewUser != actor.UserID || sessionCred != actor.SessionCredentialID {
		return Batch{}, fmt.Errorf("%w: review is bound to a different human session", ErrForbidden)
	}
	if req.DraftID != 0 && reviewDraftID != req.DraftID {
		return Batch{}, fmt.Errorf("%w: start path does not match reviewed draft", ErrNotFound)
	}
	if req.DraftRevision != reviewRevision {
		return Batch{}, fmt.Errorf("%w: draft revision", ErrStale)
	}
	draft, err := loadDraftByID(ctx, tx, projectID, reviewDraftID)
	if err != nil {
		return Batch{}, err
	}
	if draft.Status == DraftClosed {
		return Batch{}, fmt.Errorf("%w: draft is closed", ErrConflict)
	}
	if draft.Revision != reviewRevision {
		return Batch{}, fmt.Errorf("%w: draft changed after review", ErrStale)
	}
	if req.ExecutionMode != reviewMode || req.ContentDigest != draft.Baseline.ContentDigest || req.RevisionSeal != draft.Baseline.RevisionSeal {
		return Batch{}, fmt.Errorf("%w: confirmation does not bind the reviewed baseline", ErrStale)
	}
	var reviewWorker WorkerSelection
	_ = json.Unmarshal([]byte(reviewWorkerJSON), &reviewWorker)
	var reviewSelected []string
	_ = json.Unmarshal([]byte(reviewSelectedJSON), &reviewSelected)
	var reviewDelegated *DelegatedLaunchSelection
	decodeDelegatedLaunch(reviewDelegatedJSON, &reviewDelegated)
	// Scope is a set of whole refs. Click order must not mint a second
	// binding or refuse the reviewed membership. Compare the canonical
	// arrays directly; a delimiter-joined key would collapse distinct
	// selections whose refs contain commas.
	if !sameCanonicalScope(req.Selected, reviewSelected) {
		return Batch{}, fmt.Errorf("%w: confirmation does not bind reviewed mode, scope, or worker", ErrStale)
	}
	if !sameDelegatedLaunch(req.DelegatedLaunch, reviewDelegated) ||
		reviewBinding(draft, req.ExecutionMode, req.Selected, req.Worker, req.DelegatedLaunch) != binding {
		return Batch{}, fmt.Errorf("%w: confirmation does not bind reviewed mode, scope, or worker", ErrStale)
	}
	if err := s.validateDelegatedLaunch(ctx, tx, projectID, req.ExecutionMode, req.DelegatedLaunch); err != nil {
		return Batch{}, err
	}
	active, err := s.hasActiveBatch(ctx, tx, projectID)
	if err != nil {
		return Batch{}, err
	}
	if active {
		return Batch{}, fmt.Errorf("%w: an in-flight batch is immutable; later input stays in a draft", ErrConflict)
	}
	now := s.stamp()
	evidence := ReadinessEvidence{}
	if req.ExecutionMode != ModeManual {
		evidence, err = s.Verifier.Verify(ctx, tx, projectID, reviewWorker, draft.Baseline.ContentDigest, s.now())
		if err != nil {
			return Batch{}, err
		}
		if err := requireAgentReadiness(evidence); err != nil {
			return Batch{}, err
		}
		var agent int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, projectID, reviewWorker.WorkerName).Scan(&agent); err != nil {
			return Batch{}, fmt.Errorf("%w: named worker is not a project agent", ErrInvalid)
		}
	}

	var nextNum int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(issue_number),0)+1 FROM issues WHERE project_id=?`, projectID).Scan(&nextNum); err != nil {
		return Batch{}, err
	}
	title := fmt.Sprintf("Delivery batch %s r%d", draft.Baseline.BaselineRef, draft.Baseline.Revision)
	description := fmt.Sprintf("INSPR baseline batch bound to %s (%s). Imported approved_by %q is an untrusted claim.",
		draft.Baseline.ContentDigest, draft.Baseline.RevisionSeal, draft.Baseline.ImportedClaimedApprovedBy)
	res, err := tx.ExecContext(ctx, `INSERT INTO issues(project_id,issue_number,type,title,description,status,priority,created_by)
		VALUES(?,?,'ticket',?,?,'backlog','medium',?)`, projectID, nextNum, title, description, actor.UserID)
	if err != nil {
		return Batch{}, err
	}
	issueID, _ := res.LastInsertId()

	if s.Delivery == nil {
		return Batch{}, fmt.Errorf("%w: delivery store required", ErrUnavailable)
	}
	effects := s.Delivery.NewEffects()
	attempt, err := s.Delivery.StartAttemptTx(ctx, tx, effects, delivery.AttemptRequest{
		IssueID:        issueID,
		Actor:          delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", actor.UserID)},
		Policies:       delivery.DefaultPolicy(),
		ReasonCode:     "baseline_batch_start",
		ReasonText:     "Authorized human started an immutable baseline batch",
		IdempotencyKey: "batch:" + req.IdempotencyKey,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := s.recordSpecificationEvidenceTx(ctx, tx, effects, actor, issueID, attempt.AttemptNumber, "batch:"+req.IdempotencyKey); err != nil {
		return Batch{}, err
	}

	intentID := ""
	if req.ExecutionMode != ModeManual {
		intentID, err = s.submitStartIntent(ctx, tx, actor, projectID, issueID, reviewWorker, requestKey(projectID, "start", req.IdempotencyKey))
		if err != nil {
			return Batch{}, err
		}
	}

	batchKey := newBatchKey()
	scope := Scope{RequirementRefs: canonicalRequirementRefs(req.Selected), ConstraintRefs: draft.Selected.ConstraintRefs}
	confirm := map[string]any{
		"human_user_id":         actor.UserID,
		"session_credential_id": actor.SessionCredentialID,
		"content_digest":        draft.Baseline.ContentDigest,
		"revision_seal":         draft.Baseline.RevisionSeal,
		"draft_revision":        draft.Revision,
		"execution_mode":        req.ExecutionMode,
		"scope":                 scope,
		"worker":                reviewWorker,
		"issue_id":              issueID,
		"project_id":            projectID,
		"readiness_intent_id":   evidence.IntentID,
		"readiness_observed_at": evidence.ObservedAt,
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO baseline_batch_batches(
		project_id,batch_key,draft_id,draft_revision,review_id,baseline_ref,content_digest,revision_seal,execution_mode,
		scope_json,worker_json,issue_id,delivery_id,attempt_id,lifecycle_intent_id,readiness_intent_id,control_state,
		confirmation_json,idempotency_key,imported_claimed_approved_by,imported_claimed_approved_at,imported_authenticity,
		stream_ref,started_by,started_at,delegated_launch_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'started',?,?,?,?,?,?,?,?,?)`,
		projectID, batchKey, draft.ID, draft.Revision, req.ReviewID, draft.Baseline.BaselineRef, draft.Baseline.ContentDigest,
		draft.Baseline.RevisionSeal, req.ExecutionMode, encodeJSON(scope), encodeJSON(reviewWorker), issueID, attempt.DeliveryID,
		attempt.ID, intentID, evidence.IntentID, encodeJSON(confirm), req.IdempotencyKey,
		draft.Baseline.ImportedClaimedApprovedBy, draft.Baseline.ImportedClaimedApprovedAt, ImportedClaimAuthenticity,
		draft.Baseline.StreamRef, actor.UserID, now, encodeDelegatedLaunch(req.DelegatedLaunch))
	if err != nil {
		if isUniqueConstraint(err) {
			return Batch{}, fmt.Errorf("%w: a batch already exists for this confirmation", ErrConflict)
		}
		return Batch{}, err
	}
	batchID, _ := result.LastInsertId()
	draft.ReviewID = &req.ReviewID
	draft.ExecutionMode = req.ExecutionMode
	draft.Worker = reviewWorker
	draft.Selected.RequirementRefs = reviewSelected
	if err := s.mintLaunchGrantTx(ctx, tx, actor, draft, batchID, attempt.ID, attempt.DeliveryID,
		attempt.AttemptNumber, attempt.PlanRevision, req.DelegatedLaunch); err != nil {
		return Batch{}, err
	}
	eta := int64(len(scope.RequirementRefs) * 1800)
	guess := Forecast{Subject: "overall", Percent: 0, ETASeconds: &eta, Kind: ForecastGuess,
		Basis: "requirement count at confirmation; nothing has been observed yet", AsOf: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO baseline_batch_forecasts(batch_id,subject,percent,eta_seconds,kind,basis,as_of)
		VALUES(?,?,?,?,?,?,?)`, batchID, guess.Subject, guess.Percent, eta, guess.Kind, guess.Basis, guess.AsOf); err != nil {
		return Batch{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_drafts SET status='closed',updated_at=? WHERE id=?`, now, draft.ID); err != nil {
		return Batch{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_reviews SET confirmed=1 WHERE id=?`, req.ReviewID); err != nil {
		return Batch{}, err
	}
	stored, err := loadBatchByID(ctx, tx, projectID, batchID)
	if err != nil {
		return Batch{}, err
	}
	batch, err := s.projectBatch(ctx, tx, stored)
	if err != nil {
		return Batch{}, err
	}
	if err := tx.Commit(); err != nil {
		return Batch{}, err
	}
	effects.Dispatch(ctx)
	if stored.ExecutionMode != ModeManual {
		if reconciled, recErr := s.Reconcile(ctx, actor, projectID, batch.ID); recErr == nil {
			return reconciled, nil
		}
	}
	return batch, nil
}

// recordSpecificationEvidenceTx writes the only completion the human review
// actually established: specification. Implementation, QA, deployment and
// verification stay pending until their own scoped evidence arrives.
func (s *Service) recordSpecificationEvidenceTx(ctx context.Context, tx *sql.Tx, effects *delivery.Effects, actor Actor, issueID, attemptNumber int64, idempotencyKey string) error {
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", actor.UserID)}
	spec, err := s.Delivery.StartStageRetryTx(ctx, tx, effects, delivery.StageStartRequest{
		IssueID: issueID, AttemptNumber: attemptNumber, StageKey: delivery.StageSpecification, Reporter: reporter,
		ReasonCode: "baseline_review", ReasonText: "Human review of the immutable Aithema baseline",
		IdempotencyKey: idempotencyKey + ":spec:start",
	})
	if err != nil {
		return fmt.Errorf("%w: specification start: %v", ErrUnavailable, err)
	}
	digest, err := delivery.IssueSpecDigestTx(ctx, tx, issueID)
	if err != nil {
		return fmt.Errorf("%w: specification digest: %v", ErrUnavailable, err)
	}
	if _, err := s.Delivery.ReportStageTx(ctx, tx, effects, delivery.StageReport{
		IssueID: issueID, AttemptNumber: attemptNumber, StageKey: delivery.StageSpecification,
		ExecutionNumber: spec.ExecutionNumber, AuthorityEpoch: spec.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: idempotencyKey + ":spec:report", Kind: "semantic", State: "succeeded",
		Activity: "Reviewed baseline accepted as specification",
		Evidence: []delivery.Evidence{{
			Type: "spec_acceptance", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: digest,
		}},
		ReasonCode: "baseline_review",
	}); err != nil {
		return fmt.Errorf("%w: specification evidence: %v", ErrUnavailable, err)
	}
	return nil
}

// submitStartIntent routes the reviewed selection through the one lifecycle
// submission path. Request validation, catalog profile resolution, workspace
// provenance, hierarchy, the agent/workspace/coordinator reservation and the
// active-intent cap all apply; a refusal fails the whole start rather than
// leaving a batch waiting on an intent no daemon will ever execute.
func (s *Service) submitStartIntent(ctx context.Context, tx *sql.Tx, actor Actor, projectID, issueID int64, worker WorkerSelection, key string) (string, error) {
	principal, err := actor.principal()
	if err != nil {
		return "", err
	}
	ticket := issueID
	intent, _, err := s.Lifecycle.SubmitTx(ctx, tx, principal, projectID, lifecycleintents.Request{
		RequestKey:             key,
		Operation:              "start",
		RuntimeID:              worker.RuntimeID,
		RuntimeGeneration:      worker.RuntimeGeneration,
		AccountLabel:           worker.AccountLabel,
		AccountKey:             worker.AccountKey,
		AttachmentRevision:     worker.AttachmentRevision,
		TTLSeconds:             startIntentTTLSeconds,
		WorkspaceHandle:        worker.WorkspaceHandle,
		AgentName:              worker.WorkerName,
		DispatchProfileID:      worker.ProfileID,
		DispatchProfileVersion: worker.ProfileVersion,
		TicketID:               &ticket,
		WorkShape:              "ship",
		Role:                   "worker",
	})
	if err != nil {
		return "", lifecycleFailure("start", err)
	}
	return intent.ID, nil
}

// lifecycleFailure keeps the lifecycle authority's typed outcome visible to the
// browser instead of collapsing it into an opaque 500.
func lifecycleFailure(operation string, err error) error {
	switch {
	case errors.Is(err, lifecycleintents.ErrInvalid):
		return fmt.Errorf("%w: lifecycle %s request rejected", ErrInvalid, operation)
	case errors.Is(err, lifecycleintents.ErrConflict):
		return fmt.Errorf("%w: lifecycle %s conflicts with current owned work", ErrConflict, operation)
	case errors.Is(err, lifecycleintents.ErrUnavailable):
		return fmt.Errorf("%w: lifecycle_%s_unavailable", ErrBlocked, operation)
	}
	return fmt.Errorf("%w: lifecycle %s", ErrUnavailable, operation)
}

// requestKey derives one stable UUID per (project, purpose, opaque key) so an
// atomic retry of the same human confirmation reaches the same lifecycle
// intent instead of creating a second one.
func requestKey(projectID int64, purpose, key string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("paimos-baseline-batch-v1\x00%d\x00%s\x00%s", projectID, purpose, key)))
	digest[6] = (digest[6] & 0x0f) | 0x40
	digest[8] = (digest[8] & 0x3f) | 0x80
	value, _ := uuid.FromBytes(digest[:16])
	return value.String()
}

func (s *Service) GetBatch(ctx context.Context, actor Actor, projectID, batchID int64) (Batch, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Batch{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return Batch{}, err
	}
	stored, err := loadBatchByID(ctx, tx, projectID, batchID)
	if err != nil {
		return Batch{}, err
	}
	batch, err := s.projectBatch(ctx, tx, stored)
	if err != nil {
		return Batch{}, err
	}
	if err := tx.Commit(); err != nil {
		return Batch{}, err
	}
	return batch, nil
}

func loadBatchByIdempotency(ctx context.Context, tx *sql.Tx, projectID int64, key string) (storedBatch, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM baseline_batch_batches WHERE project_id=? AND idempotency_key=?`, projectID, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return storedBatch{}, fmt.Errorf("%w: batch", ErrNotFound)
	}
	if err != nil {
		return storedBatch{}, err
	}
	return loadBatchByID(ctx, tx, projectID, id)
}

// hasActiveBatch asks the derived state, not a stored status column: a batch is
// in flight while its human control is not terminal and its delivery attempt
// has not satisfied every required stage.
func (s *Service) hasActiveBatch(ctx context.Context, tx *sql.Tx, projectID int64) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM baseline_batch_batches WHERE project_id=? AND control_state<>'cancelled'`, projectID)
	if err != nil {
		return false, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		stored, err := loadBatchByID(ctx, tx, projectID, id)
		if err != nil {
			return false, err
		}
		state, _, _, err := s.batchState(ctx, tx, stored)
		if err != nil {
			return false, err
		}
		if state != BatchCompleted {
			return true, nil
		}
	}
	return false, nil
}

func isNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func (s *Service) now() time.Time { return s.Clock.Now().UTC() }

func (s *Service) stamp() string { return s.now().Format(time.RFC3339Nano) }
