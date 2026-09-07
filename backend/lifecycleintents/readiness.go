// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// recordReadinessTx stores one accepted owned observation. Everything the
// observation is bound to comes from the authorized request and the current
// server-held runtime registration; only the outcome, its closed check codes
// and the daemon's own observation time come from the report. A report that
// disagrees with the authorized target is refused, never reinterpreted.
func (s *Service) recordReadinessTx(ctx context.Context, tx *sql.Tx, in Intent, runtime Runtime, report *ReadinessReport) error {
	if err := validateReadinessReport(report); err != nil {
		return err
	}
	r := in.Request
	workspace := workspaceIdentity(runtime, r.WorkspaceHandle)
	if workspace == "" || report.WorkspaceIdentity != workspace {
		return ErrUnavailable
	}
	if report.AccountKey != r.AccountKey || report.BaselineDigest != r.BaselineDigest {
		return ErrUnavailable
	}
	observed, err := time.Parse(time.RFC3339Nano, report.ObservedAt)
	if err != nil {
		return ErrInvalid
	}
	now := s.now()
	created, err := time.Parse(time.RFC3339Nano, in.CreatedAt)
	if err != nil {
		created = now.Add(-time.Duration(ReadinessTTLSeconds) * time.Second)
	}
	// The daemon's clock is trusted only inside the window this authority
	// already owns: after the intent it answers, and not in the future.
	if observed.After(now.Add(5*time.Second)) || observed.Before(created.Add(-5*time.Second)) {
		return ErrUnavailable
	}
	deadline := observed.Add(time.Duration(report.TTLSeconds) * time.Second)
	deadline = earliest(deadline, runtime.ExpiresAt)
	deadline = earliest(deadline, in.ExpiresAt)
	if !deadline.After(now) {
		return ErrUnavailable
	}
	checks, err := json.Marshal(report.Checks)
	if err != nil {
		return ErrStorage
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lifecycle_readiness_observations(
		project_id,intent_id,runtime_id,runtime_generation,account_label,account_key,dispatch_profile_id,
		dispatch_profile_version,workspace_handle,workspace_identity,baseline_digest,contract_version,status,
		next_action,host_kind,checks_json,observed_at,expires_at,recorded_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.ProjectID, in.ID, r.RuntimeID, r.RuntimeGeneration, r.AccountLabel, r.AccountKey, r.DispatchProfileID,
		r.DispatchProfileVersion, r.WorkspaceHandle, workspace, r.BaselineDigest, report.ContractVersion, report.Status,
		report.NextAction, report.HostKind, string(checks), stamp(observed), stamp(deadline), stamp(now))
	if err != nil {
		return ErrConflict
	}
	return nil
}

// earliest clamps a deadline to a stored RFC3339 boundary. Unparseable input
// keeps the smaller of the two by failing to the existing value.
func earliest(deadline time.Time, boundary string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, boundary)
	if err != nil || !parsed.Before(deadline) {
		return deadline
	}
	return parsed
}

const readinessColumns = `intent_id,project_id,runtime_id,runtime_generation,account_label,account_key,
	dispatch_profile_id,dispatch_profile_version,workspace_handle,workspace_identity,baseline_digest,
	host_kind,status,next_action,observed_at,expires_at,checks_json`

// CurrentReadinessTx returns the newest unexpired observation for exactly this
// runtime generation, account, profile, workspace and baseline. Anything less
// specific is not evidence for this start, so callers get sql.ErrNoRows and
// must block rather than fall back to a weaker match.
func CurrentReadinessTx(ctx context.Context, tx *sql.Tx, projectID int64, target ReadinessTarget, now time.Time) (ReadinessObservation, error) {
	var out ReadinessObservation
	var checks string
	err := tx.QueryRowContext(ctx, `SELECT `+readinessColumns+` FROM lifecycle_readiness_observations
		WHERE project_id=? AND runtime_id=? AND runtime_generation=? AND account_label=? AND account_key=?
		 AND dispatch_profile_id=? AND dispatch_profile_version=? AND workspace_handle=? AND baseline_digest=?
		 AND expires_at>? ORDER BY observed_at DESC, id DESC LIMIT 1`,
		projectID, target.RuntimeID, target.RuntimeGeneration, target.AccountLabel, target.AccountKey,
		target.ProfileID, target.ProfileVersion, target.WorkspaceHandle, target.BaselineDigest, stamp(now)).
		Scan(&out.IntentID, &out.ProjectID, &out.RuntimeID, &out.RuntimeGeneration, &out.AccountLabel, &out.AccountKey,
			&out.ProfileID, &out.ProfileVersion, &out.WorkspaceHandle, &out.WorkspaceIdentity, &out.BaselineDigest,
			&out.HostKind, &out.Status, &out.NextAction, &out.ObservedAt, &out.ExpiresAt, &checks)
	if err != nil {
		return ReadinessObservation{}, err
	}
	if json.Unmarshal([]byte(checks), &out.Checks) != nil {
		return ReadinessObservation{}, ErrStorage
	}
	return out, nil
}

// ReadinessTarget is the exact tuple an observation must cover to authorize a
// start. It is server-derived from the reviewed selection, never client input.
type ReadinessTarget struct {
	RuntimeID         string
	RuntimeGeneration string
	AccountLabel      string
	AccountKey        string
	ProfileID         string
	ProfileVersion    string
	WorkspaceHandle   string
	BaselineDigest    string
}
