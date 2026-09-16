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
func (s *Service) recordReadinessTx(ctx context.Context, tx *sql.Tx, in Intent, runtime Runtime, report *ReadinessReport) (ReadinessObservation, error) {
	if err := validateReadinessReport(report); err != nil {
		return ReadinessObservation{}, err
	}
	r := in.Request
	profile, err := resolveProfile(r.DispatchProfileID, r.DispatchProfileVersion)
	if err != nil {
		return ReadinessObservation{}, err
	}
	workspace := workspaceIdentity(runtime, r.WorkspaceHandle)
	if workspace == "" || report.WorkspaceIdentity != workspace {
		return ReadinessObservation{}, ErrUnavailable
	}
	if report.AccountKey != r.AccountKey || report.BaselineDigest != r.BaselineDigest {
		return ReadinessObservation{}, ErrUnavailable
	}
	observed, err := time.Parse(time.RFC3339Nano, report.ObservedAt)
	if err != nil {
		return ReadinessObservation{}, ErrInvalid
	}
	now := s.now()
	created, err := time.Parse(time.RFC3339Nano, in.CreatedAt)
	if err != nil {
		created = now.Add(-time.Duration(ReadinessTTLSeconds) * time.Second)
	}
	// The daemon's clock is trusted only inside the window this authority
	// already owns: after the intent it answers, and not in the future.
	if observed.After(now.Add(5*time.Second)) || observed.Before(created.Add(-5*time.Second)) {
		return ReadinessObservation{}, ErrUnavailable
	}
	deadline := observed.Add(time.Duration(report.TTLSeconds) * time.Second)
	deadline = earliest(deadline, runtime.ExpiresAt)
	deadline = earliest(deadline, in.ExpiresAt)
	if !deadline.After(now) {
		return ReadinessObservation{}, ErrUnavailable
	}
	// The daemon can only inspect its own sessions. Reconcile its observation
	// with this authority's same-machine reservations and registered sessions
	// before storing a ready result. This is still a point-in-time observation;
	// Start performs the final atomic reservation against the same ledger.
	occupied, err := readinessWorkspaceOccupiedTx(ctx, tx, in, runtime, workspace)
	if err != nil {
		return ReadinessObservation{}, err
	}
	stored := *report
	if occupied {
		stored.Checks = append([]ReadinessCheck(nil), report.Checks...)
		for i := range stored.Checks {
			if stored.Checks[i].ID == "workspace_isolation" {
				stored.Checks[i].Status = "fail"
				stored.Checks[i].Reason = "workspace_occupied"
				stored.Checks[i].Digest = ""
				break
			}
		}
		if stored.Status == "ready" {
			stored.Status = "needs_setup"
			stored.NextAction = "select_declared_workspace"
		}
	}
	checks, err := json.Marshal(stored.Checks)
	if err != nil {
		return ReadinessObservation{}, ErrStorage
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lifecycle_readiness_observations(
		project_id,intent_id,runtime_id,runtime_generation,account_label,account_key,dispatch_profile_id,
		dispatch_profile_version,workspace_handle,workspace_identity,baseline_digest,contract_version,status,
		next_action,host_kind,checks_json,observed_at,expires_at,recorded_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.ProjectID, in.ID, r.RuntimeID, r.RuntimeGeneration, r.AccountLabel, r.AccountKey, r.DispatchProfileID,
		r.DispatchProfileVersion, r.WorkspaceHandle, workspace, r.BaselineDigest, stored.ContractVersion, stored.Status,
		stored.NextAction, stored.HostKind, string(checks), stamp(observed), stamp(deadline), stamp(now))
	if err != nil {
		return ReadinessObservation{}, ErrConflict
	}
	return ReadinessObservation{
		ContractVersion: stored.ContractVersion, IntentID: in.ID, ProjectID: in.ProjectID,
		RuntimeID: r.RuntimeID, RuntimeGeneration: r.RuntimeGeneration, AccountLabel: r.AccountLabel,
		AccountKey: r.AccountKey, ProfileID: r.DispatchProfileID, ProfileVersion: r.DispatchProfileVersion,
		WorkspaceHandle: r.WorkspaceHandle, WorkspaceIdentity: workspace, WorkspaceMode: profile.WorkspaceMode, BaselineDigest: r.BaselineDigest,
		HostKind: stored.HostKind, Status: stored.Status, NextAction: stored.NextAction,
		ObservedAt: stamp(observed), ExpiresAt: stamp(deadline), Checks: append([]ReadinessCheck(nil), stored.Checks...),
	}, nil
}

// readinessWorkspaceOccupiedTx observes Paimos-owned same-machine claims and
// sessions. It does not reserve the workspace or make a claim about unmanaged
// processes. The final Start claim repeats the authority check atomically.
func readinessWorkspaceOccupiedTx(ctx context.Context, tx *sql.Tx, in Intent, runtime Runtime, workspace string) (bool, error) {
	profile, err := resolveProfile(in.Request.DispatchProfileID, in.Request.DispatchProfileVersion)
	if err != nil {
		return false, err
	}
	var reserved int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lifecycle_intents i
   JOIN lifecycle_runtimes owner ON owner.id=i.runtime_id
   LEFT JOIN json_each(owner.registration_json,'$.workspaces') w
    ON json_extract(w.value,'$.handle')=json_extract(i.request_json,'$.workspace_handle')
   WHERE i.state IN ('claimed','executing')
    AND json_extract(i.request_json,'$.operation') IN ('start','restart')
    AND owner.machine_id=? AND json_extract(w.value,'$.identity')=?`, runtime.MachineID, workspace).Scan(&reserved)
	if err != nil {
		return false, ErrStorage
	}
	if reserved != 0 {
		return true, nil
	}
	var sessions int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM harness_sessions
   WHERE host=? AND workspace_identity=? AND phase<>'stopped'
    AND (workspace_mode='exclusive' OR ?='exclusive')`, runtime.MachineID, workspace, profile.WorkspaceMode).Scan(&sessions)
	if err != nil {
		return false, ErrStorage
	}
	return sessions != 0, nil
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

const readinessColumns = `contract_version,intent_id,project_id,runtime_id,runtime_generation,account_label,account_key,
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
		Scan(&out.ContractVersion, &out.IntentID, &out.ProjectID, &out.RuntimeID, &out.RuntimeGeneration, &out.AccountLabel, &out.AccountKey,
			&out.ProfileID, &out.ProfileVersion, &out.WorkspaceHandle, &out.WorkspaceIdentity, &out.BaselineDigest,
			&out.HostKind, &out.Status, &out.NextAction, &out.ObservedAt, &out.ExpiresAt, &checks)
	if err != nil {
		return ReadinessObservation{}, err
	}
	if json.Unmarshal([]byte(checks), &out.Checks) != nil {
		return ReadinessObservation{}, ErrStorage
	}
	if profile, err := resolveProfile(out.ProfileID, out.ProfileVersion); err != nil {
		return ReadinessObservation{}, err
	} else {
		out.WorkspaceMode = profile.WorkspaceMode
	}
	return out, nil
}

// ReadinessReceiptForIntentTx returns the one observation that this terminal
// readiness intent accepted. Terminal replay must never substitute a newer
// tuple match, and it must remain replayable after the evidence has expired so
// a daemon can journal the accepted outcome without treating old evidence as
// current availability.
func ReadinessReceiptForIntentTx(ctx context.Context, tx *sql.Tx, projectID int64, intentID string) (ReadinessObservation, error) {
	var out ReadinessObservation
	var checks string
	err := tx.QueryRowContext(ctx, `SELECT `+readinessColumns+` FROM lifecycle_readiness_observations WHERE project_id=? AND intent_id=? LIMIT 1`, projectID, intentID).
		Scan(&out.ContractVersion, &out.IntentID, &out.ProjectID, &out.RuntimeID, &out.RuntimeGeneration, &out.AccountLabel, &out.AccountKey,
			&out.ProfileID, &out.ProfileVersion, &out.WorkspaceHandle, &out.WorkspaceIdentity, &out.BaselineDigest,
			&out.HostKind, &out.Status, &out.NextAction, &out.ObservedAt, &out.ExpiresAt, &checks)
	if err != nil {
		return ReadinessObservation{}, err
	}
	if json.Unmarshal([]byte(checks), &out.Checks) != nil {
		return ReadinessObservation{}, ErrStorage
	}
	if profile, err := resolveProfile(out.ProfileID, out.ProfileVersion); err != nil {
		return ReadinessObservation{}, err
	} else {
		out.WorkspaceMode = profile.WorkspaceMode
	}
	return out, nil
}

// ValidateReadinessReceipt verifies the bounded data a daemon may retain after
// its own authorized transition. It deliberately checks freshness and the
// closed report vocabulary again so a stale or malformed transport response
// cannot become local availability evidence.
func ValidateReadinessReceipt(in ReadinessObservation, now time.Time) error {
	if err := ValidateReadinessReceiptShape(in); err != nil {
		return err
	}
	observed, _ := time.Parse(time.RFC3339Nano, in.ObservedAt)
	expires, err := time.Parse(time.RFC3339Nano, in.ExpiresAt)
	if err != nil || !expires.After(now) || !expires.After(observed) {
		return ErrUnavailable
	}
	return nil
}

// ValidateReadinessReceiptShape checks an authority response without deciding
// whether its expiry is still current. Terminal replay uses this to retain its
// accepted outcome while callers still use ValidateReadinessReceipt before
// treating an observation as live availability evidence.
func ValidateReadinessReceiptShape(in ReadinessObservation) error {
	if in.ContractVersion != ReadinessContractVersion || !validID(in.IntentID) || in.ProjectID <= 0 || !validID(in.RuntimeID) || !validID(in.RuntimeGeneration) ||
		!label(in.AccountLabel, 128) || (in.AccountKey != "" && !validAccountKey(in.AccountKey)) || !label(in.ProfileID, 128) || !label(in.ProfileVersion, 128) ||
		!label(in.WorkspaceHandle, 128) || !identity.MatchString(in.WorkspaceIdentity) || (in.WorkspaceMode != "exclusive" && in.WorkspaceMode != "shared") || !ValidBaselineDigest(in.BaselineDigest) {
		return ErrInvalid
	}
	profile, err := resolveProfile(in.ProfileID, in.ProfileVersion)
	if err != nil || profile.WorkspaceMode != in.WorkspaceMode {
		return ErrInvalid
	}
	if err := validateReadinessReport(&ReadinessReport{ContractVersion: in.ContractVersion, Status: in.Status, TTLSeconds: 30, HostKind: in.HostKind, NextAction: in.NextAction, WorkspaceIdentity: in.WorkspaceIdentity, AccountKey: in.AccountKey, BaselineDigest: in.BaselineDigest, Checks: in.Checks}); err != nil {
		return err
	}
	observed, err := time.Parse(time.RFC3339Nano, in.ObservedAt)
	if err != nil || observed.IsZero() {
		return ErrInvalid
	}
	expires, err := time.Parse(time.RFC3339Nano, in.ExpiresAt)
	if err != nil || !expires.After(observed) {
		return ErrInvalid
	}
	return nil
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
