// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/supervision"
)

type controlSynchronousMutator struct{}

func (controlSynchronousMutator) SetIssuePriorityTx(ctx context.Context, tx *sql.Tx, mutation supervision.PriorityMutation) error {
	if tx == nil || mutation.IssueID <= 0 || mutation.ExpectedRevision <= 0 || mutation.ActorUserID <= 0 ||
		mutation.CommandID == "" || (mutation.Priority != "low" && mutation.Priority != "medium" && mutation.Priority != "high") {
		return errors.New("invalid priority mutation")
	}
	before, err := fetchIssueMutationSnapshotTx(tx, mutation.IssueID)
	if err != nil {
		return err
	}
	var revision int64
	var updatedAt string
	if err := tx.QueryRowContext(ctx, `SELECT control.revision,issue.updated_at
		FROM issues issue JOIN issue_control_revisions control ON control.issue_id=issue.id
		WHERE issue.id=? AND issue.deleted_at IS NULL`, mutation.IssueID).Scan(&revision, &updatedAt); err != nil {
		return err
	}
	etag := issueETag(mutation.IssueID, updatedAt)
	digest := sha256.Sum256([]byte(etag))
	if revision != mutation.ExpectedRevision || !bytes.Equal(digest[:], mutation.ExpectedETagDigest[:]) {
		return errors.New("stale issue revision")
	}
	now := nextIssueMutationTimestamp(updatedAt)
	result, err := tx.ExecContext(ctx, `UPDATE issues SET priority=?,updated_at=? WHERE id=? AND updated_at=?`,
		mutation.Priority, now, mutation.IssueID, updatedAt)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("stale issue revision")
	}
	after, err := fetchIssueMutationSnapshotTx(tx, mutation.IssueID)
	if err != nil {
		return err
	}
	if err := recordControlPriorityMutationTx(ctx, tx, mutation, before, after); err != nil {
		return err
	}
	return insertControlIssueHistoryTx(ctx, tx, mutation, after, now)
}

func nextIssueMutationTimestamp(previous string) string {
	now := time.Now().UTC().Truncate(time.Second)
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, previous); err == nil && !now.After(parsed.UTC()) {
			now = parsed.UTC().Add(time.Second)
			break
		}
	}
	return now.Format("2006-01-02 15:04:05")
}

func recordControlPriorityMutationTx(ctx context.Context, tx *sql.Tx, mutation supervision.PriorityMutation,
	before, after issueMutationSnapshot) error {
	beforeJSON, beforeHash, err := canonicalState(before)
	if err != nil {
		return err
	}
	afterJSON, afterHash, err := canonicalState(after)
	if err != nil {
		return err
	}
	inverseJSON, err := json.Marshal(InverseOp{Method: http.MethodPut,
		Path: fmt.Sprintf("/issues/%d", mutation.IssueID), Body: before})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO mutation_log(
		request_id,user_id,mutation_type,subject_type,subject_id,inverse_op,before_state,after_state,
		before_hash,after_hash,undoable,on_user_stack)
		VALUES(?,?,'control.issue.priority.set','issue',?,?,?,?,?,?,1,1)`, mutation.CommandID,
		mutation.ActorUserID, mutation.IssueID, string(inverseJSON), string(beforeJSON), string(afterJSON), beforeHash, afterHash)
	if err != nil {
		return err
	}
	if _, err := result.LastInsertId(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mutation_log SET redoable=0 WHERE user_id=? AND redoable=1`, mutation.ActorUserID); err != nil {
		return err
	}
	return enforceUndoStackDepth(ctx, tx, mutation.ActorUserID)
}

func insertControlIssueHistoryTx(ctx context.Context, tx *sql.Tx, mutation supervision.PriorityMutation,
	after issueMutationSnapshot, changedAt string) error {
	blob, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_history(issue_id,changed_by,snapshot,changed_at)
		VALUES(?,?,?,?)`, mutation.IssueID, mutation.ActorUserID, string(blob), changedAt)
	return err
}

func (controlSynchronousMutator) CancelQueuedRunTx(ctx context.Context, tx *sql.Tx, mutation supervision.RunCancellationMutation) error {
	return cancelControlledRunTx(ctx, tx, mutation, "queued")
}

func (controlSynchronousMutator) CancelRunningRunTx(ctx context.Context, tx *sql.Tx, mutation supervision.RunCancellationMutation) error {
	return cancelControlledRunTx(ctx, tx, mutation, "running")
}

func cancelControlledRunTx(ctx context.Context, tx *sql.Tx, mutation supervision.RunCancellationMutation, state string) error {
	if tx == nil || mutation.IssueID <= 0 || mutation.RunID <= 0 || mutation.ActorUserID <= 0 ||
		mutation.CommandID == "" || mutation.ExpectedState != state {
		return errors.New("invalid run cancellation mutation")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='cancelled',finished_at=datetime('now')
		WHERE id=? AND issue_id=? AND status=?`, mutation.RunID, mutation.IssueID, state)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("stale run state")
	}
	store := delivery.NewStore(nil, delivery.Options{})
	effects := delivery.NewEffects(nil)
	if err := store.NormalizeRunTx(ctx, tx, effects, delivery.RunNormalization{RunID: mutation.RunID,
		Status: "cancelled", IdempotencyKey: "control:" + mutation.CommandID}); err != nil {
		return err
	}
	if len(effects.Hints()) != 1 {
		return errors.New("controlled cancellation produced an invalid change count")
	}
	return nil
}

type controlChangeRecorder struct {
	store *delivery.Store
}

func (recorder controlChangeRecorder) RecordControlChangeTx(ctx context.Context, tx *sql.Tx,
	change supervision.ControlChange) (supervision.CommitWake, error) {
	if recorder.store == nil || change.CommandID == "" {
		return supervision.CommitWake{}, errors.New("control change recorder unavailable")
	}
	hint, err := recorder.store.RecordControlChangeTx(ctx, tx, delivery.ControlChangeRequest{
		IssueID: change.IssueID, RunID: change.RunID, Action: string(change.Action),
	})
	if err != nil {
		return supervision.CommitWake{}, err
	}
	return supervision.CommitWake{ID: hint.InternalID}, nil
}

func (recorder controlChangeRecorder) WakeControlChange(ctx context.Context, wake supervision.CommitWake) {
	if recorder.store == nil || wake.ID <= 0 {
		return
	}
	hint, err := recorder.store.LoadChangeHint(ctx, wake.ID)
	if err == nil {
		agentmode.NotifyChange(ctx, hint)
	}
}

func newControlService(database *sql.DB) *supervision.Service {
	store := delivery.NewStore(database, delivery.Options{})
	return supervision.NewService(database, supervision.Options{
		Mutator: controlSynchronousMutator{}, Changes: controlChangeRecorder{store: store},
	})
}

func agentRunCancellationCauseIs(runID int64, cause string) bool {
	if runID <= 0 || cause == "" {
		return false
	}
	var exists int
	err := db.DB.QueryRow(`SELECT 1 FROM agent_run_cancellation_facts WHERE run_id=? AND cancellation_cause=?`,
		runID, cause).Scan(&exists)
	return err == nil && exists == 1
}

func canRecordNonOperatorCancellation(r *http.Request, run *AgentRun, deviceID string) bool {
	principal, ok := auth.GetPrincipal(r)
	if !ok || principal.Kind() != auth.PrincipalAPIKey || run == nil || run.ID <= 0 || deviceID == "" ||
		run.DeviceID == "" || deviceID != run.DeviceID {
		return false
	}
	var leaseCount int
	if err := db.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM control_capability_leases WHERE agent_run_id=?`,
		run.ID).Scan(&leaseCount); err != nil {
		return false
	}
	if leaseCount == 0 {
		return true
	}
	var exact int
	err := db.DB.QueryRowContext(r.Context(), `SELECT 1 FROM control_capability_leases lease
		WHERE lease.agent_run_id=? AND lease.user_id=? AND lease.actor_api_key_id=? AND lease.device_id=?
		 AND lease.revision=(SELECT MAX(current.revision) FROM control_capability_leases current
		                     WHERE current.lease_id=lease.lease_id)
		 AND lease.revoked_at IS NULL AND lease.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')
		LIMIT 1`, run.ID, principal.UserID(), principal.APIKeyID(), deviceID).Scan(&exact)
	return err == nil && exact == 1
}

// recordNonOperatorCancellationTx appends the immutable lifecycle fact and its
// sparse control event after the exact running→cancelled CAS, in the same
// transaction as delivery normalization. The SQL clock expression is the
// M147-owned server timestamp; no runner-provided time crosses this seam.
func recordNonOperatorCancellationTx(ctx context.Context, tx *sql.Tx, runID int64, cause string) error {
	if tx == nil || runID <= 0 || (cause != "execution_timeout" && cause != "silence_timeout" &&
		cause != "runner_shutdown" && cause != "server_cancel") {
		return errors.New("invalid nonoperator cancellation fact")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_run_cancellation_facts(
		run_id,cancellation_cause,command_id,recorded_at)
		VALUES(?,?,NULL,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, runID, cause); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_events(
		sequence,event_kind,cancellation_run_id,cancellation_command_id,cancellation_cause,agent_run_id)
		VALUES(1,'cancellation_recorded',?,NULL,?,?)`, runID, cause, runID)
	return err
}
