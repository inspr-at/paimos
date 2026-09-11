// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/managedharness"
)

const (
	effectHarnessControl    = "harness_control"
	effectLifecycleCancel   = "lifecycle_cancel"
	effectLifecycleResubmit = "lifecycle_resubmit"
	effectManualHold        = "manual_hold"
	effectManualRelease     = "manual_release"
	effectManualCancel      = "manual_cancel"
)

// ControlRequest is one human control action on a started batch. RequestKey
// makes a repeated click idempotent all the way down to the owned control row.
type ControlRequest struct {
	Action     string `json:"action"`
	RequestKey string `json:"request_key"`
}

// plannedControl is the single durable owned effect an action resolves to.
// Deciding it before performing it is what keeps the button honest: if there
// is no effect, the action is refused instead of moving a status column.
type plannedControl struct {
	effect     string
	nextState  string
	nextReason string
	kind       string // harness control kind
	reason     string // refusal reason when effect is empty
}

// Control performs a human control action against the batch's actual owned
// execution. Pause and cancel reach the running worker through the PAI-903
// harness control ledger, or release the not-yet-claimed start intent through
// the lifecycle authority's revision CAS. Nothing here edits a status column
// as a substitute for an effect.
func (s *Service) Control(ctx context.Context, actor Actor, projectID, batchID int64, req ControlRequest) (Batch, error) {
	if err := requireHuman(actor); err != nil {
		return Batch{}, err
	}
	if req.Action != "pause" && req.Action != "resume" && req.Action != "cancel" && req.Action != "revoke_launch" {
		return Batch{}, fmt.Errorf("%w: action", ErrInvalid)
	}
	if req.RequestKey != "" && uuid.Validate(req.RequestKey) != nil {
		return Batch{}, fmt.Errorf("%w: request_key", ErrInvalid)
	}
	if req.Action == "revoke_launch" {
		return s.revokeLaunchGrant(ctx, actor, projectID, batchID, req.RequestKey)
	}
	stored, state, execution, err := s.controlContext(ctx, actor, projectID, batchID)
	if err != nil {
		return Batch{}, err
	}
	if done, err := s.replayedControl(ctx, actor, projectID, stored, req); err == nil && done {
		return s.GetBatch(ctx, actor, projectID, batchID)
	} else if err != nil {
		return Batch{}, err
	}
	plan := planControl(stored, state, execution, req.Action)
	if plan.effect == "" {
		return Batch{}, fmt.Errorf("%w: %s", ErrConflict, plan.reason)
	}
	key := req.RequestKey
	if key == "" {
		key = requestKey(projectID, fmt.Sprintf("control:%d:%s:%s", batchID, req.Action, state), stored.ControlState)
	}
	effectRef := ""
	switch plan.effect {
	case effectHarnessControl:
		effectRef, err = s.requestHarnessControl(ctx, actor, projectID, execution, plan.kind, key)
	case effectLifecycleCancel:
		effectRef, err = s.cancelStartIntent(ctx, actor, projectID, stored, execution)
	case effectLifecycleResubmit:
		effectRef, err = s.resubmitStartIntent(ctx, actor, projectID, stored, key)
	}
	if err != nil {
		return Batch{}, err
	}
	if err := s.recordControl(ctx, actor, stored, req.Action, plan, effectRef, key); err != nil {
		return Batch{}, err
	}
	return s.GetBatch(ctx, actor, projectID, batchID)
}

func (s *Service) revokeLaunchGrant(ctx context.Context, actor Actor, projectID, batchID int64, requestKeyValue string) (Batch, error) {
	if requestKeyValue == "" {
		requestKeyValue = uuid.NewString()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Batch{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Batch{}, err
	}
	var grantID string
	var revoked sql.NullString
	var consumed int
	if err := tx.QueryRowContext(ctx, `SELECT grant.grant_id,grant.revoked_at,
		EXISTS(SELECT 1 FROM external_stage_launch_admissions admission WHERE admission.grant_id=grant.grant_id AND admission.state='consumed')
		FROM baseline_batch_launch_grants grant WHERE grant.project_id=? AND grant.batch_id=?`, projectID, batchID).
		Scan(&grantID, &revoked, &consumed); errors.Is(err, sql.ErrNoRows) {
		return Batch{}, fmt.Errorf("%w: launch grant", ErrNotFound)
	} else if err != nil {
		return Batch{}, err
	}
	if revoked.Valid {
		if err := tx.Commit(); err != nil {
			return Batch{}, err
		}
		return s.GetBatch(ctx, actor, projectID, batchID)
	}
	if consumed != 0 {
		return Batch{}, fmt.Errorf("%w: consumed launch grant cannot be revoked", ErrConflict)
	}
	idem := sha256.Sum256([]byte(requestKeyValue))
	now := s.stamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO baseline_batch_launch_grant_revocations(
		grant_id,batch_id,actor_user_id,actor_session_credential_id,idempotency_digest,revoked_at) VALUES(?,?,?,?,?,?)`,
		grantID, batchID, actor.UserID, actor.SessionCredentialID, idem[:], now); err != nil {
		return Batch{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_launch_grants SET revoked_at=?,revoked_by=?,
		revoked_session_credential_id=? WHERE grant_id=? AND revoked_at IS NULL`, now, actor.UserID, actor.SessionCredentialID, grantID); err != nil {
		return Batch{}, err
	}
	if err := tx.Commit(); err != nil {
		return Batch{}, err
	}
	return s.GetBatch(ctx, actor, projectID, batchID)
}

func (s *Service) controlContext(ctx context.Context, actor Actor, projectID, batchID int64) (storedBatch, string, ownedExecution, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return storedBatch{}, "", ownedExecution{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return storedBatch{}, "", ownedExecution{}, err
	}
	stored, err := loadBatchByID(ctx, tx, projectID, batchID)
	if err != nil {
		return storedBatch{}, "", ownedExecution{}, err
	}
	state, _, _, err := s.batchState(ctx, tx, stored)
	if err != nil {
		return storedBatch{}, "", ownedExecution{}, err
	}
	execution, err := loadOwnedExecution(ctx, tx, projectID, stored.LifecycleIntentID)
	if err != nil {
		return storedBatch{}, "", ownedExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return storedBatch{}, "", ownedExecution{}, err
	}
	return stored, state, execution, nil
}

// replayedControl treats an already-recorded request key as done. The owned
// effect behind it was idempotent too, so a retried click never produces a
// second interrupt, stop or start intent.
func (s *Service) replayedControl(ctx context.Context, actor Actor, projectID int64, stored storedBatch, req ControlRequest) (bool, error) {
	if req.RequestKey == "" {
		return false, nil
	}
	var action string
	err := s.DB.QueryRowContext(ctx, `SELECT action FROM baseline_batch_controls WHERE batch_id=? AND request_key=?`,
		stored.ID, req.RequestKey).Scan(&action)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if action != req.Action {
		return false, fmt.Errorf("%w: request_key is bound to a different action", ErrConflict)
	}
	return true, nil
}

// planControl maps the derived state and the live owned execution onto the one
// effect that action can actually have right now.
func planControl(stored storedBatch, state string, execution ownedExecution, action string) plannedControl {
	manual := stored.ExecutionMode == ModeManual
	sessionAlive := execution.SessionID != "" && execution.SessionPhase != "" && execution.SessionPhase != "stopped"
	intentPending := execution.IntentState == "requested" || execution.IntentState == "claimed"
	switch action {
	case "pause":
		switch {
		case state == BatchCompleted || state == BatchCancelled:
			return plannedControl{reason: "batch is " + state}
		case stored.ControlState == ControlPaused:
			return plannedControl{reason: "batch is already paused"}
		case manual:
			return plannedControl{effect: effectManualHold, nextState: ControlPaused, nextReason: "human_hold"}
		case sessionAlive && execution.CanInterrupt:
			return plannedControl{effect: effectHarnessControl, kind: managedharness.ControlInterrupt,
				nextState: ControlPaused, nextReason: "owned_session_interrupted"}
		case sessionAlive:
			return plannedControl{reason: "owned session does not advertise interrupt"}
		case intentPending:
			return plannedControl{effect: effectLifecycleCancel, nextState: ControlPaused,
				nextReason: "pending_start_released"}
		default:
			return plannedControl{reason: "no owned execution to pause"}
		}
	case "resume":
		switch {
		case stored.ControlState != ControlPaused:
			return plannedControl{reason: "batch is not paused"}
		case manual:
			return plannedControl{effect: effectManualRelease, nextState: ControlStarted}
		case stored.ControlReason == "pending_start_released":
			return plannedControl{effect: effectLifecycleResubmit, nextState: ControlStarted}
		case sessionAlive:
			// An interrupted owned child resumes through its own harness
			// conversation, not by rewriting this batch. Saying so is the honest
			// outcome; a decorative resume button would not restart anything.
			return plannedControl{reason: "owned_session_resume_unavailable"}
		default:
			return plannedControl{reason: "no owned execution to resume"}
		}
	default:
		switch {
		case state == BatchCancelled:
			return plannedControl{reason: "batch is already cancelled"}
		case state == BatchCompleted:
			return plannedControl{reason: "a completed batch cannot be cancelled"}
		case manual:
			return plannedControl{effect: effectManualCancel, nextState: ControlCancelled, nextReason: "human_cancelled"}
		case sessionAlive && execution.CanStop:
			return plannedControl{effect: effectHarnessControl, kind: managedharness.ControlStop,
				nextState: ControlCancelled, nextReason: "owned_session_stopped"}
		case sessionAlive:
			return plannedControl{reason: "owned session does not advertise stop"}
		case intentPending:
			return plannedControl{effect: effectLifecycleCancel, nextState: ControlCancelled,
				nextReason: "pending_start_cancelled"}
		default:
			return plannedControl{effect: effectManualCancel, nextState: ControlCancelled,
				nextReason: "no_owned_execution_remaining"}
		}
	}
}

// requestHarnessControl writes the real PAI-903 control row through the
// existing browser control path, with its own capability, phase and
// session-revision CAS, attributed to the acting human.
func (s *Service) requestHarnessControl(ctx context.Context, actor Actor, projectID int64, execution ownedExecution, kind, key string) (string, error) {
	if s.Harness == nil {
		return "", fmt.Errorf("%w: harness control service", ErrUnavailable)
	}
	principal, err := actor.principal()
	if err != nil {
		return "", err
	}
	out, err := s.Harness.RequestControlCAS(ctx, principal, projectID, execution.SessionID, kind,
		managedharness.BrowserControlRequest{ExpectedRevision: execution.SessionRevision, RequestKey: key})
	switch {
	case errors.Is(err, managedharness.ErrBrowserConflict):
		return "", fmt.Errorf("%w: owned session changed while the control was prepared", ErrConflict)
	case errors.Is(err, managedharness.ErrBrowserUnavailable):
		return "", fmt.Errorf("%w: owned_session_control_unavailable", ErrBlocked)
	case errors.Is(err, managedharness.ErrBrowserInvalid):
		return "", fmt.Errorf("%w: control request", ErrInvalid)
	case err != nil:
		return "", fmt.Errorf("%w: owned control", ErrUnavailable)
	}
	return out.Control.ID, nil
}

// cancelStartIntent releases a start the daemon has not executed yet, through
// the lifecycle authority's own revision CAS, so the change is attributed to
// this human and can never touch an already executing worker.
func (s *Service) cancelStartIntent(ctx context.Context, actor Actor, projectID int64, stored storedBatch, execution ownedExecution) (string, error) {
	if s.Lifecycle == nil {
		return "", fmt.Errorf("%w: lifecycle authority", ErrUnavailable)
	}
	principal, err := actor.principal()
	if err != nil {
		return "", err
	}
	lifecycleintents.LockMutations()
	defer lifecycleintents.UnlockMutations()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	intent, conflict, err := s.Lifecycle.ReadOrCancelTx(ctx, tx, principal, projectID, stored.LifecycleIntentID, execution.IntentRevision)
	if err != nil {
		return "", lifecycleFailure("cancel", err)
	}
	if conflict {
		if commitErr := tx.Commit(); commitErr != nil {
			return "", commitErr
		}
		return "", fmt.Errorf("%w: start intent moved on before the control was applied", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return intent.ID, nil
}

// resubmitStartIntent re-arms a batch whose pending start this human released.
// It re-verifies current readiness and goes through the same single submission
// path as the original start, so a stale observation blocks the resume.
func (s *Service) resubmitStartIntent(ctx context.Context, actor Actor, projectID int64, stored storedBatch, key string) (string, error) {
	if s.Lifecycle == nil {
		return "", fmt.Errorf("%w: lifecycle authority", ErrUnavailable)
	}
	lifecycleintents.LockMutations()
	defer lifecycleintents.UnlockMutations()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return "", err
	}
	evidence, err := s.Verifier.Verify(ctx, tx, projectID, stored.Worker, stored.Baseline.ContentDigest, s.now())
	if err != nil {
		return "", err
	}
	if err := requireAgentReadiness(evidence); err != nil {
		return "", err
	}
	intentID, err := s.submitStartIntent(ctx, tx, actor, projectID, stored.IssueID, stored.Worker,
		requestKey(projectID, "resume", key))
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_batches SET lifecycle_intent_id=? WHERE id=? AND project_id=?`,
		intentID, stored.ID, projectID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return intentID, nil
}

func (s *Service) recordControl(ctx context.Context, actor Actor, stored storedBatch, action string, plan plannedControl, effectRef, key string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, stored.ProjectID, true); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO baseline_batch_controls(
		batch_id,request_key,action,effect,effect_ref,actor_user_id,actor_credential_id,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, stored.ID, key, action, plan.effect, effectRef, actor.UserID,
		actor.SessionCredentialID, s.stamp()); err != nil {
		if isUniqueConstraint(err) {
			return nil
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_batches SET control_state=?,control_reason=? WHERE id=? AND project_id=?`,
		plan.nextState, plan.nextReason, stored.ID, stored.ProjectID); err != nil {
		return err
	}
	return tx.Commit()
}

// controlOptions reports which controls the current state actually supports,
// with the reason when it does not. The browser renders only available ones.
func controlOptions(stored storedBatch, state string, execution ownedExecution) []ControlOption {
	out := []ControlOption{}
	for _, action := range []string{"pause", "resume", "cancel"} {
		plan := planControl(stored, state, execution, action)
		option := ControlOption{Action: action, Available: plan.effect != "", Effect: plan.effect, Reason: plan.reason}
		out = append(out, option)
	}
	return out
}
