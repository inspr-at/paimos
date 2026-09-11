// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/targetidentity"
)

const delegatedLaunchWorkflow = "deploy-production"

func encodeDelegatedLaunch(selection *DelegatedLaunchSelection) string {
	if selection == nil {
		return ""
	}
	raw, _ := json.Marshal(selection)
	return string(raw)
}

func decodeDelegatedLaunch(raw string, target **DelegatedLaunchSelection) {
	if strings.TrimSpace(raw) == "" {
		*target = nil
		return
	}
	var selection DelegatedLaunchSelection
	if json.Unmarshal([]byte(raw), &selection) == nil {
		*target = &selection
	}
}

func (s *Service) listDelegatedLaunchTargets(ctx context.Context, tx *sql.Tx, projectID int64) ([]DelegatedLaunchTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM project_environments WHERE project_id=? ORDER BY sort_order,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DelegatedLaunchTarget, 0, len(ids))
	for _, id := range ids {
		identity, err := targetidentity.LoadProjectEnvironment(ctx, tx, projectID, id)
		if err != nil {
			continue
		}
		if !targetidentity.IsSymbol(identity.EnvironmentSymbol) {
			continue
		}
		out = append(out, DelegatedLaunchTarget{
			TargetRef: targetidentity.Digest(identity), Environment: identity.EnvironmentSymbol,
			Label: fmt.Sprintf("Project environment %s (#%d)", identity.EnvironmentSymbol, id),
		})
	}
	return out, nil
}

func (s *Service) validateDelegatedLaunch(ctx context.Context, tx *sql.Tx, projectID int64, mode string, selection *DelegatedLaunchSelection) error {
	if selection == nil {
		return nil
	}
	if mode != ModeAutomatic || selection.Workflow != delegatedLaunchWorkflow || selection.MaxLaunches != 1 ||
		!targetidentity.IsDigest(selection.TargetRef) {
		return fmt.Errorf("%w: delegated_launch requires automatic deploy-production with max_launches 1", ErrInvalid)
	}
	expires, err := time.Parse(time.RFC3339Nano, selection.ExpiresAt)
	now := s.now().UTC()
	if err != nil || !expires.After(now) || expires.After(now.Add(24*time.Hour)) {
		return fmt.Errorf("%w: delegated_launch expiry must be in the next 24 hours", ErrInvalid)
	}
	selection.ExpiresAt = expires.UTC().Format(time.RFC3339Nano)
	targets, err := s.listDelegatedLaunchTargets(ctx, tx, projectID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.TargetRef == selection.TargetRef && target.Environment == selection.Environment {
			return nil
		}
	}
	return fmt.Errorf("%w: delegated_launch target is not a current server-listed project environment", ErrStale)
}

func sameDelegatedLaunch(left, right *DelegatedLaunchSelection) bool {
	return encodeDelegatedLaunch(left) == encodeDelegatedLaunch(right)
}

func (s *Service) mintLaunchGrantTx(ctx context.Context, tx *sql.Tx, actor Actor, draft Draft, batchID int64,
	attemptID, deliveryID, attemptNumber, planRevision int64, selection *DelegatedLaunchSelection,
) error {
	if selection == nil {
		return nil
	}
	issued := s.now().UTC().Format(time.RFC3339Nano)
	grantID := uuid.NewString()
	scopeRaw := encodeJSON(draft.Selected)
	workerRaw := encodeJSON(draft.Worker)
	scopeDigest := sha256.Sum256([]byte(scopeRaw))
	workerDigest := sha256.Sum256([]byte(workerRaw))
	identity := struct {
		Domain         string `json:"domain"`
		GrantID        string `json:"grant_id"`
		Revision       int    `json:"revision"`
		ProjectID      int64  `json:"project_id"`
		DraftID        int64  `json:"draft_id"`
		DraftRevision  int64  `json:"draft_revision"`
		ReviewID       int64  `json:"review_id"`
		BatchID        int64  `json:"batch_id"`
		DeliveryID     int64  `json:"delivery_id"`
		AttemptID      int64  `json:"attempt_id"`
		AttemptNumber  int64  `json:"attempt_number"`
		PlanRevision   int64  `json:"plan_revision"`
		BaselineRef    string `json:"baseline_ref"`
		ContentDigest  string `json:"content_digest"`
		RevisionSeal   string `json:"revision_seal"`
		ScopeDigest    string `json:"scope_digest"`
		WorkerDigest   string `json:"worker_digest"`
		TargetRef      string `json:"target_ref"`
		Workflow       string `json:"workflow"`
		Environment    string `json:"environment"`
		MaxLaunches    int    `json:"max_launches"`
		HumanUserID    int64  `json:"human_user_id"`
		HumanSessionID string `json:"human_session_credential_id"`
		IssuedAt       string `json:"issued_at"`
		ExpiresAt      string `json:"expires_at"`
	}{"paimos.baseline-batch.launch-grant.v1", grantID, 1, draft.ProjectID, draft.ID, draft.Revision,
		*draft.ReviewID, batchID, deliveryID, attemptID, attemptNumber, planRevision, draft.Baseline.BaselineRef,
		draft.Baseline.ContentDigest, draft.Baseline.RevisionSeal, hex.EncodeToString(scopeDigest[:]),
		hex.EncodeToString(workerDigest[:]), selection.TargetRef, selection.Workflow, selection.Environment,
		selection.MaxLaunches, actor.UserID, actor.SessionCredentialID, issued, selection.ExpiresAt}
	raw, _ := json.Marshal(identity)
	grantDigest := sha256.Sum256(raw)
	_, err := tx.ExecContext(ctx, `INSERT INTO baseline_batch_launch_grants(
		grant_id,revision,grant_digest,project_id,draft_id,draft_revision,review_id,batch_id,delivery_id,attempt_id,
		attempt_number,plan_revision,baseline_ref,content_digest,revision_seal,scope_digest,worker_digest,target_ref,
		workflow_symbol,environment_symbol,max_launches,human_user_id,human_session_credential_id,issued_at,expires_at)
		VALUES(?,1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, grantID, grantDigest[:], draft.ProjectID, draft.ID,
		draft.Revision, *draft.ReviewID, batchID, deliveryID, attemptID, attemptNumber, planRevision, draft.Baseline.BaselineRef,
		draft.Baseline.ContentDigest, draft.Baseline.RevisionSeal, scopeDigest[:], workerDigest[:], selection.TargetRef,
		selection.Workflow, selection.Environment, selection.MaxLaunches, actor.UserID, actor.SessionCredentialID, issued, selection.ExpiresAt)
	return err
}

func loadLaunchGrantView(ctx context.Context, tx *sql.Tx, batchID int64, now time.Time) (*LaunchGrantView, error) {
	var view LaunchGrantView
	var digest []byte
	var revoked sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT grant_id,revision,grant_digest,target_ref,workflow_symbol,environment_symbol,
		max_launches,issued_at,expires_at,revoked_at FROM baseline_batch_launch_grants WHERE batch_id=?`, batchID).
		Scan(&view.GrantID, &view.Revision, &digest, &view.TargetRef, &view.Workflow, &view.Environment,
			&view.MaxLaunches, &view.IssuedAt, &view.ExpiresAt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	view.GrantDigest = "sha256:" + hex.EncodeToString(digest)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_stage_launch_admissions WHERE grant_id=? AND state='consumed'`, view.GrantID).Scan(&view.UsedLaunches)
	view.State = "active"
	if revoked.Valid {
		view.State = "revoked"
	} else if view.UsedLaunches > 0 {
		view.State = "consumed"
	} else if expires, err := time.Parse(time.RFC3339Nano, view.ExpiresAt); err != nil || !expires.After(now.UTC()) {
		view.State = "expired"
	}
	return &view, nil
}
