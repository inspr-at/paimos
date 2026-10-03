// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Only terminal failed attempts may hand off: never mid-turn, after a cancel,
// or when ownership was lost. Replayed telemetry cannot arm a second retry.
func armVendorRetry(ctx context.Context, tx pgx.Tx, v Run) error {
	if v.Status != "failed" || v.Purpose != "managed" || v.AccountID == nil || v.ReadOnlyReview {
		return nil
	}
	var stopped bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM run_telemetry WHERE run_id=$1 AND error_code='vendor_limit')`, v.ID).Scan(&stopped); err != nil {
		return err
	}
	if !stopped {
		return nil
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return err
	}
	at, known, err := agentaccounts.VendorRetryAt(ctx, tx, *v.AccountID, v.ID, now)
	if err != nil {
		return err
	}
	same := known && !at.After(now.Add(20*time.Minute))
	_, err = tx.Exec(ctx, `UPDATE agent_runs SET vendor_retry_pending=true,vendor_retry_at=$2,vendor_retry_same_account=$3 WHERE id=$1`, v.ID, at, same)
	return err
}

// The queue poll is already serialized by the pairing lock. Work-order then
// run locks preserve the normal writer order; insertion and log are atomic.
func retryVendorStops(ctx context.Context, tx pgx.Tx, p tenant.Principal, pending *[]events.Change) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM agent_runs WHERE agent_principal_id=$1 AND vendor_retry_pending AND status='failed' ORDER BY created_at,id LIMIT 100`, p.ID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return err
	}
	for _, id := range ids {
		v, o, err := lockRun(ctx, tx, id)
		if err != nil {
			return err
		}
		var retryPending, same, child bool
		var at *time.Time
		if err = tx.QueryRow(ctx, `SELECT vendor_retry_pending,vendor_retry_same_account,vendor_retry_at,EXISTS(SELECT 1 FROM agent_runs WHERE retry_of_run_id=$1) FROM agent_runs WHERE id=$1`, id).Scan(&retryPending, &same, &at, &child); err != nil {
			return err
		}
		if !retryPending {
			continue
		}
		if child || v.Purpose != "managed" || v.AccountID == nil || v.DaemonID == nil || v.ProfileID == nil || o.Status != "ready" && o.Status != "running" || o.Assignee != nil && *o.Assignee != p.ID {
			if _, err = tx.Exec(ctx, `UPDATE agent_runs SET vendor_retry_pending=false WHERE id=$1`, id); err != nil {
				return err
			}
			continue
		}
		if same && at != nil && now.Before(*at) {
			continue
		}
		if err = dispatchable(ctx, tx, o); err != nil {
			var blocked *workorders.Error
			if errors.As(err, &blocked) {
				continue
			}
			return err
		}
		only, exclude := "", *v.AccountID
		if same {
			only, exclude = *v.AccountID, ""
		}
		policy, err := modelprefs.RunRequirement(ctx, tx, id)
		if err != nil {
			return err
		}
		advice, err := agentaccounts.NextForRun(ctx, tx, id, *v.DaemonID, only, exclude, now, policy.Residency)
		if err != nil {
			return err
		}
		// If the rest of the pool stays dry, retry the original account once
		// its reset/backoff has passed, subject to the same admission checks.
		if len(advice.Accounts) == 0 && !same && at != nil && !now.Before(*at) {
			advice, err = agentaccounts.NextForRun(ctx, tx, id, *v.DaemonID, *v.AccountID, "", now, policy.Residency)
			if err != nil {
				return err
			}
			same = true
		}
		if len(advice.Accounts) == 0 {
			continue
		}
		next := advice.Accounts[0]
		retry, err := scan(tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,requested_account_id,retry_of_run_id,retry_account_id,residency,prefs_person_id,trace) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+columns, p.TenantID, o.NodeID, v.AgentID, v.ProfileID, v.RequestedModel, v.RequestedAccountID, id, next.AccountID, modelprefs.Stamp(policy.Residency), policy.PersonID, v.Trace))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO account_run_targets(tenant_id,run_id,group_id)
 SELECT tenant_id,$2,group_id FROM account_run_targets WHERE run_id=$1`, id, retry.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_runs SET vendor_retry_pending=false WHERE id=$1`, id); err != nil {
			return err
		}
		var label string
		if err = tx.QueryRow(ctx, `SELECT label FROM agent_accounts WHERE id=$1`, *v.AccountID).Scan(&label); err != nil {
			return err
		}
		note := "Continues on " + logLabel(next.AccountLabel) + " after the vendor reset"
		if !same {
			note = fmt.Sprintf("Moved from %s to %s", logLabel(label), logLabel(next.AccountLabel))
			if at != nil {
				note += fmt.Sprintf(" (%s empty until %s UTC)", logLabel(label), at.UTC().Format("15:04"))
			}
		}
		nodeID := o.NodeID
		*pending = append(*pending, events.Change{NodeID: &nodeID, Type: "run.capacity_handoff", Before: v, After: struct {
			Run  Run    `json:"run"`
			Note string `json:"note"`
		}{retry, note}})
		// Append the same bounded line to the original session's activity history.
		if _, err = tx.Exec(ctx, `INSERT INTO harness_activity_notes(tenant_id,session_id,note) SELECT tenant_id,id,$2 FROM harness_sessions WHERE run_id=$1`, id, note); err != nil {
			return err
		}
	}
	return nil
}
func logLabel(s string) string {
	out := []rune{}
	for _, r := range s {
		if !unicode.IsControl(r) {
			out = append(out, r)
		}
		if len(out) == 22 {
			break
		}
	}
	return string(out)
}
func vendorWait(ctx context.Context, tx pgx.Tx, v Run) (*agentaccounts.CapacityWait, error) {
	var pending bool
	var at *time.Time
	if err := tx.QueryRow(ctx, `SELECT vendor_retry_pending,vendor_retry_at FROM agent_runs WHERE id=$1`, v.ID).Scan(&pending, &at); err != nil {
		return nil, err
	}
	if !pending {
		return nil, nil
	}
	return &agentaccounts.CapacityWait{Code: "vendor", Until: at}, nil
}
