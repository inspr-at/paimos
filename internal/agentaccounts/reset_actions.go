// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type resetAction struct {
	ResetRequest
	By           string
	ExpiredAt    time.Time
	Confirmed    bool
	Plan         *capacity.ResetPlan
	ReportReadAt time.Time
	Window       capacity.Reading
}

func (m *Module) useReset(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Count     *int   `json:"expected_count"`
		Binding   *int64 `json:"binding_revision"`
		Confirmed bool   `json:"confirmed"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	if in.Count == nil || *in.Count < 1 || *in.Count > capacity.MaxResetCredits || in.Binding == nil || *in.Binding < 0 {
		writeErr(w, fail(400, "expected_count and binding_revision required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	action, err := m.prepareReset(ctx, p, id, *in.Binding, *in.Count, in.Confirmed, "person")
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := m.executeReset(ctx, p, action, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, result)
}

func (m *Module) prepareReset(ctx context.Context, p tenant.Principal, id string, binding int64, count int, confirmed bool, by string) (resetAction, error) {
	out := resetAction{By: by, Confirmed: confirmed}
	err := m.inReadinessWrite(ctx, p, func(tx pgx.Tx) error {
		a, err := resetOwner(ctx, tx, p, id, binding)
		if err != nil {
			return err
		}
		if err := resetExecutionAllowed(ctx, tx, p, a); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		state, err := loadResetState(ctx, tx, a, now)
		if err != nil {
			return err
		}
		if state.Credits == nil || state.Credits.Count != count || state.report == nil || state.reading == nil || now.Sub(state.reading.ReadAt) > ProbeFreshness || !state.reading.ResetsAt.After(now) {
			return &httpError{status: 409, code: "reset_state_changed", msg: "fresh vendor reset count and window required"}
		}
		if !state.UndoSupported && !confirmed && by == "person" {
			return &httpError{status: 409, code: "reset_confirmation_required", msg: "vendor cannot undo this reset; confirm inline"}
		}
		if by == "auto" && (state.Policy != "auto_before_expiry" || state.Plan == nil || state.Plan.PlannedAt.After(now) || state.reading.UsedPercent < 90) {
			return fail(409, "automatic reset is not due")
		}
		if m.resetVendor == nil {
			return &httpError{status: 422, code: "reset_unsupported", msg: "vendor reset execution is unavailable"}
		}
		out.ResetRequest = ResetRequest{AccountID: id, Harness: a.Harness, Provider: a.Provider, DaemonID: a.DaemonID, BindingRevision: binding, ExpectedCount: count}
		out.ExpiredAt, out.Plan = state.Credits.ExpiresAt[0], state.Plan
		out.ReportReadAt, out.Window = state.report.ReadAt, *state.reading
		return tx.QueryRow(ctx, `INSERT INTO account_reset_actions(tenant_id,account_id,person_id,binding_revision,policy_revision,state,by_kind,expected_count,expired_at,requested_at)
 VALUES($1,$2,$3,$4,$5,'pending',$6,$7,$8,$9) RETURNING id::text`, p.TenantID, id, p.ID, binding, state.Revision, by, count, out.ExpiredAt, now).Scan(&out.ActionID)
	})
	return out, err
}

func (m *Module) executeReset(ctx context.Context, p tenant.Principal, action resetAction, undo bool) (ResetResult, error) {
	var out ResetResult
	var outcome error
	called := false
	err := m.inReadinessWrite(ctx, p, func(tx pgx.Tx) error {
		// A rejection before the vendor call must not leave pending or
		// undo_pending: that row is the account's only block, and Undo has
		// already replaced succeeded. Commit the terminal state with the error.
		settle := func(reason error) error {
			if _, err := tx.Exec(ctx, `UPDATE account_reset_actions SET state=CASE WHEN state='undo_pending' THEN 'succeeded' ELSE 'cancelled' END WHERE id=$1 AND state IN ('pending','undo_pending')`, action.ActionID); err != nil {
				return err
			}
			outcome = reason
			return nil
		}
		// The vendor may have spent the credit. Unknown stays committed even
		// when evidence or audit fails; the savepoint drops that partial write.
		markUnknown := func(completed time.Time) error {
			unknown := "unknown"
			if undo {
				unknown = "undo_unknown"
			}
			if _, writeErr := tx.Exec(ctx, `UPDATE account_reset_actions SET state=$2,completed_at=$3 WHERE id=$1`, action.ActionID, unknown, completed); writeErr != nil {
				return writeErr
			}
			outcome = &httpError{status: 502, code: "reset_outcome_unknown", msg: "vendor reset outcome is unknown; do not retry before reconciliation"}
			return nil
		}
		// Fence authorization throughout the bounded vendor call, then its final
		// write. A revocation cannot slip between spending and persisting evidence.
		a, err := resetOwner(ctx, tx, p, action.AccountID, action.BindingRevision)
		if err != nil {
			return settle(err)
		}
		if err := resetExecutionAllowed(ctx, tx, p, a); err != nil {
			return settle(err)
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return settle(err)
		}
		var status string
		var until *time.Time
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT state,undo_until,policy_revision FROM account_reset_actions WHERE id=$1 AND account_id=$2 AND person_id=$3 FOR UPDATE`, action.ActionID, a.ID, p.ID).Scan(&status, &until, &revision); err != nil {
			return settle(err)
		}
		var currentRevision int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(usage_revision,0) FROM agent_accounts WHERE id=$1`, a.ID).Scan(&currentRevision); err != nil {
			return settle(err)
		}
		if revision != currentRevision {
			return settle(fail(409, "account policy changed before reset execution"))
		}
		wanted := "pending"
		if undo {
			wanted = "undo_pending"
		}
		if status != wanted || undo && (until == nil || !now.Before(*until)) {
			return settle(fail(409, "reset action changed or Undo expired"))
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT reset_report FROM agent_accounts WHERE id=$1`, a.ID).Scan(&raw); err != nil {
			return settle(err)
		}
		var report capacity.ResetReport
		if json.Unmarshal(raw, &report) != nil || report.Validate(now) != nil || report.BindingRevision != a.LinkRevision || now.Sub(report.ReadAt) > ProbeFreshness || report.Credits(now).Count != action.ExpectedCount || !report.ReadAt.Equal(action.ReportReadAt) {
			return settle(fail(409, "reset report changed before execution"))
		}
		if !undo && !report.UndoSupported && !action.Confirmed && action.By == "person" {
			return settle(fail(409, "reset confirmation changed"))
		}
		if !undo && action.By == "auto" {
			// loadResetState suppresses pending credit advice, so inspect the live,
			// binding-scoped opt-in directly while holding the same access fence.
			var opted bool
			if err := tx.QueryRow(ctx, `SELECT daily_reset_policy='auto_before_expiry' AND reset_policy_person_id=$2::uuid AND reset_policy_link_revision=link_revision FROM agent_accounts WHERE id=$1`, a.ID, p.ID).Scan(&opted); err != nil {
				return settle(err)
			}
			if !opted {
				return settle(fail(409, "automatic reset consent changed"))
			}
		}
		if m.resetVendor == nil {
			return settle(fail(422, "vendor reset execution unavailable"))
		}
		vendorCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		called = true
		if undo {
			out, err = m.resetVendor.Undo(vendorCtx, action.ResetRequest)
		} else {
			out, err = m.resetVendor.Use(vendorCtx, action.ResetRequest)
		}
		cancel()
		out.ActionID, out.AccountID = action.ActionID, a.ID
		// Use wall time after I/O in production; tests carry an injected clock.
		completed := time.Now().UTC()
		if _, ok := ctx.Value(clockKey{}).(time.Time); ok {
			completed = now
		}
		if err == nil {
			err = validResetResult(out, action, report, completed, undo)
		}
		if err != nil {
			return markUnknown(completed)
		}
		// Evidence and the audit event sit in a savepoint. A failure there must
		// not roll the spent credit back to pending. The event counter stays last
		// on the success path; the savepoint releases it before unknown is written.
		if _, err := tx.Exec(ctx, `SAVEPOINT reset_evidence`); err != nil {
			return err
		}
		evidenceErr := func() error {
			// Final evidence uses the already locked account, rather than invoking an
			// ingestion helper that would reacquire pairing/resource fences after rows.
			if err := persistResetResult(ctx, tx, a, out, completed); err != nil {
				return err
			}
			state := "succeeded"
			event := "account.reset_used"
			points := 0.0
			var paceUntil *time.Time
			if undo {
				state, event = "undone", "account.reset_undone"
			} else if action.By == "auto" && action.Plan != nil {
				days := math.Max(out.Window.ResetsAt.Sub(completed).Hours()/24, 1)
				points = math.Min(50, action.Plan.RaisedPacePoints)
				// A vendor may return an earlier fresh reset than the planning sample.
				points = math.Min(50, math.Max(points, action.Window.UsedPercent/days))
				paceUntil = &out.Window.ResetsAt
			}
			raw, err := json.Marshal(out)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE account_reset_actions SET state=$2,completed_at=$3,undo_until=$4,raised_pace_points=$5,raised_pace_until=$6,result=$7 WHERE id=$1`, action.ActionID, state, completed, out.UndoUntil, points, paceUntil, raw); err != nil {
				return err
			}
			// Event counter is last; no later resource locks or writes.
			return writeEvent(ctx, tx, p, event, nil, map[string]any{"account_id": a.ID, "action_id": action.ActionID, "expired_at": action.ExpiredAt, "by": action.By, "raised_pace_points": points})
		}()
		if evidenceErr != nil {
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT reset_evidence`); err != nil {
				return err
			}
			return markUnknown(completed)
		}
		_, err = tx.Exec(ctx, `RELEASE SAVEPOINT reset_evidence`)
		return err
	})
	if err != nil && called {
		return out, &httpError{status: 502, code: "reset_outcome_unknown", msg: "vendor may have applied the reset; read current state before reconciliation"}
	}
	if err != nil {
		return out, err
	}
	return out, outcome
}

func validResetResult(out ResetResult, action resetAction, before capacity.ResetReport, now time.Time, undo bool) error {
	expected := action.ExpectedCount - 1
	if undo {
		expected = action.ExpectedCount + 1
	}
	if out.Report.Validate(now) != nil || out.Report.BindingRevision != action.BindingRevision || !out.Report.ReadAt.After(before.ReadAt) || out.Report.Count != expected || out.Window.Validate(now) != nil || out.Window.Source == "estimate" || out.Window.WindowKind != action.Window.WindowKind || out.Window.Bucket != action.Window.Bucket || out.Window.OrdinaryUsageAllowed == nil || !*out.Window.OrdinaryUsageAllowed || out.Window.ReadAt.Before(out.Report.ReadAt) || out.Window.ReadAt.After(now) || out.Window.RunID != "" || out.Window.Phase != "" || looksLikeCredential(out.Window.Bucket) || looksLikeCredential(out.Window.Plan) {
		return errors.New("invalid reset result")
	}
	if !undo && out.UndoUntil != nil && (!out.UndoUntil.After(now) || out.UndoUntil.After(now.Add(24*time.Hour))) {
		return errors.New("invalid vendor Undo deadline")
	}
	if !undo && !action.Confirmed && action.By == "person" && out.UndoUntil == nil {
		return errors.New("vendor did not honor advertised Undo")
	}
	return nil
}

func persistResetResult(ctx context.Context, tx pgx.Tx, a Account, out ResetResult, now time.Time) error {
	var tenantID string
	if err := tx.QueryRow(ctx, `SELECT tenant_id::text FROM agent_accounts WHERE id=$1`, a.ID).Scan(&tenantID); err != nil {
		return err
	}
	if err := ingestLockedReadings(ctx, tx, tenantID, a, []capacity.Reading{out.Window}, false); err != nil {
		return err
	}
	return storeResetReport(ctx, tx, a, out.Report, now)
}

func (m *Module) undoReset(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, actionID := r.PathValue("accountId"), r.PathValue("actionId")
	if !uuidRE.MatchString(id) || !uuidRE.MatchString(actionID) {
		writeErr(w, fail(404, "reset action not found"))
		return
	}
	var in struct {
		Binding *int64 `json:"binding_revision"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Binding == nil || *in.Binding < 0 {
		writeErr(w, fail(400, "binding_revision required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	action := resetAction{By: "person", Confirmed: true}
	err := m.inReadinessWrite(ctx, p, func(tx pgx.Tx) error {
		a, err := resetOwner(ctx, tx, p, id, *in.Binding)
		if err != nil {
			return err
		}
		if err := resetExecutionAllowed(ctx, tx, p, a); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		var raw []byte
		var until *time.Time
		var state string
		if err := tx.QueryRow(ctx, `SELECT state,result,undo_until,expired_at FROM account_reset_actions WHERE id=$1 AND account_id=$2 AND person_id=$3 AND binding_revision=$4 FOR UPDATE`, actionID, id, p.ID, *in.Binding).Scan(&state, &raw, &until, &action.ExpiredAt); err != nil {
			if isNoRows(err) {
				return fail(404, "reset action not found")
			}
			return err
		}
		if state != "succeeded" || until == nil || !now.Before(*until) {
			return fail(409, "vendor Undo unavailable or expired")
		}
		if m.resetVendor == nil {
			return fail(422, "vendor reset execution unavailable")
		}
		var result ResetResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		action.ResetRequest = ResetRequest{ActionID: actionID, AccountID: id, Harness: a.Harness, Provider: a.Provider, DaemonID: a.DaemonID, BindingRevision: a.LinkRevision, ExpectedCount: result.Report.Count}
		var reportRaw []byte
		if err := tx.QueryRow(ctx, `SELECT reset_report FROM agent_accounts WHERE id=$1`, id).Scan(&reportRaw); err != nil {
			return err
		}
		var currentReport capacity.ResetReport
		if json.Unmarshal(reportRaw, &currentReport) != nil || currentReport.Validate(now) != nil || currentReport.BindingRevision != a.LinkRevision || now.Sub(currentReport.ReadAt) > ProbeFreshness || currentReport.Credits(now).Count != result.Report.Count {
			return fail(409, "fresh reset count changed before Undo")
		}
		action.ReportReadAt, action.Window = currentReport.ReadAt, result.Window
		_, err = tx.Exec(ctx, `UPDATE account_reset_actions SET state='undo_pending',policy_revision=(SELECT COALESCE(usage_revision,0) FROM agent_accounts WHERE id=$2) WHERE id=$1`, actionID, id)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := m.executeReset(ctx, p, action, true)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// Fresh daemon captures drive automatic use. No API read can spend a credit.
// The persisted person opt-in delegates only this bound account and is checked
// again inside prepareReset and the final vendor/write transaction.
func (m *Module) automaticReset(ctx context.Context, tenantID, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if m.resetVendor == nil {
		return nil
	}
	var owner tenant.Principal
	var state resetState
	err := m.in(ctx, tenantID, func(tx pgx.Tx) error {
		a, err := getAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		if a.OwnerPersonID == nil {
			return nil
		}
		var person string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(linked_to,id)::text FROM principals WHERE id=$1 AND kind='person'`, *a.OwnerPersonID).Scan(&person); err != nil {
			return err
		}
		owner = tenant.Principal{ID: person, TenantID: tenantID, Kind: tenant.Person}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		state, err = loadResetState(ctx, tx, a, now)
		return err
	})
	if err != nil || state.Policy != "auto_before_expiry" || state.Plan == nil || state.Credits == nil {
		return err
	}
	now := time.Now().UTC()
	if at, ok := ctx.Value(clockKey{}).(time.Time); ok {
		now = at
	}
	if state.Plan.PlannedAt.After(now) || state.reading == nil || state.reading.UsedPercent < 90 {
		return nil
	}
	action, err := m.prepareReset(ctx, owner, id, state.BindingRevision, state.Credits.Count, true, "auto")
	if err != nil {
		return err
	}
	_, err = m.executeReset(ctx, owner, action, false)
	return err
}
