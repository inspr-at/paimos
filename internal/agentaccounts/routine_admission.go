// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// RoutineGateContract comes from server-owned accepted qualification evidence,
// bound to the exact capability digest. There is no bootstrap/default age.
// Qualification can tighten, never relax the existing vendor/host safety ages.
type RoutineGateContract struct {
	QualificationID  string
	CapabilityDigest string
	AccountMaxAgeMS  int64
	HostMaxAgeMS     int64
	// PaidAccountIDs is a separately accepted exact-account API execution
	// context. Nil preserves the subscription-only execution restriction.
	PaidAccountIDs []string
}

func (g RoutineGateContract) Valid() bool {
	if len(g.PaidAccountIDs) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range g.PaidAccountIDs {
		if !workorders.UUID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return workorders.UUID(g.QualificationID) && len(g.CapabilityDigest) == 64 && g.AccountMaxAgeMS > 0 && g.AccountMaxAgeMS <= 600000 && g.HostMaxAgeMS > 0 && g.HostMaxAgeMS <= 60000
}

// RequireRoutineCapacityTx shares the exact reserved-account daily, dial,
// harness, pacing, quota reserve, context and hard-stop predicates. Callers hold
// tenant/tree/pairing fences first; no remote observation occurs inside tx.
// Strict freshness is added only for routines, preserving ordinary recovery.
func RequireRoutineCapacityTx(ctx context.Context, tx pgx.Tx, owner, runID, accountID string, g RoutineGateContract, now time.Time) (string, string, string, error) {
	if !g.Valid() {
		return "", "", "", workorders.Fail(409, "admission_contract_unqualified")
	}
	// Exact owned-account inputs and occupancy must include sibling projects.
	// This trusted internal read returns only admitted target pins, never rows.
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return "", "", "", err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return "", "", "", err
	}
	harness, billing, host, err := requireRoutineCapacityInputs(ctx, tx, owner, runID, accountID, g, now)
	if err != nil {
		return "", "", "", err
	} // caller rolls back, including visibility
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return harness, billing, host, err
}

func requireRoutineCapacityInputs(ctx context.Context, tx pgx.Tx, owner, runID, accountID string, g RoutineGateContract, now time.Time) (string, string, string, error) {
	for _, bound := range []struct {
		query   string
		maximum int
	}{
		{`SELECT count(*) FROM (SELECT 1 FROM agent_accounts LIMIT 1025) x`, 1024},
		// Match the complete readAccountWindows/lockAccountWindows projection:
		// retired history still carries holds, and live rules retain their
		// removed source windows. Refuse before materializing or locking rows.
		{`SELECT count(*) FROM (SELECT 1 FROM account_allowance_windows w WHERE w.removed_at IS NULL OR EXISTS (SELECT 1 FROM account_limit_rules l WHERE l.tenant_id=w.tenant_id AND l.from_window_id=w.id AND l.removed_at IS NULL) LIMIT 4097) x`, 4096},
	} {
		var count int
		if err := tx.QueryRow(ctx, bound.query).Scan(&count); err != nil {
			return "", "", "", err
		}
		if count > bound.maximum {
			return "", "", "", workorders.Fail(409, "account_inputs_unreadable")
		}
	}
	run, err := lockRun(ctx, tx, runID)
	if err != nil {
		return "", "", "", err
	}
	if run.AccountID == nil || *run.AccountID != accountID || run.Purpose != "managed" || run.Status != "queued" {
		return "", "", "", workorders.Fail(409, "routine_account_unbound")
	}
	a, err := getAccount(ctx, tx, accountID)
	if err != nil {
		return "", "", "", err
	}
	var own bool
	err = tx.QueryRow(ctx, `SELECT coalesce(`+modelprefs.CanonicalPersonSQL("a.owner_person_id")+`=$2::uuid,false) AND a.registered_by_principal_id=$3::uuid FROM agent_accounts a WHERE a.id=$1`, accountID, owner, run.AgentID).Scan(&own)
	if err != nil {
		return "", "", "", err
	}
	if !own {
		return "", "", "", workorders.Fail(409, "own_account_required")
	}
	if a.LastProbeAt == nil || a.LastProbeAt.After(now) || now.Sub(*a.LastProbeAt) > time.Duration(g.AccountMaxAgeMS)*time.Millisecond {
		return "", "", "", workorders.Fail(409, "account_inputs_unreadable")
	}
	all, err := sharedQuotaWindows(ctx, tx, a, a.Windows, now)
	if err != nil {
		return "", "", "", err
	}
	known := false
	for _, w := range all {
		if w.capacityRetired || w.pairingVerification || !now.Before(w.EndsAt) || w.StartsAt.After(now) {
			continue
		}
		if synthetic(w) {
			continue
		}
		if w.capacityReadAt == nil {
			continue
		} // manual caps never prove vendor room
		if w.capacitySource == "estimate" || w.capacityReadAt.After(now) || now.Sub(*w.capacityReadAt) > time.Duration(g.AccountMaxAgeMS)*time.Millisecond {
			return "", "", "", workorders.Fail(409, "account_inputs_unreadable")
		}
		known = true
	}
	if !known {
		return "", "", "", workorders.Fail(409, "account_inputs_unreadable")
	}
	// Reuse existing reserved-account eligibility, resource recovery and quota
	// floor checks. It also rejects stale daily facts instead of guessing zero.
	if err := validateReservedAccount(ctx, tx, run, accountID); err != nil {
		return "", "", "", err
	}
	var raw, policyRaw []byte
	var at *time.Time
	var host string
	var running int
	err = tx.QueryRow(ctx, `SELECT c.id::text,c.capacity_signals,c.capacity_policy,c.capacity_reported_at,
 (SELECT count(*) FROM agent_runs r WHERE r.agent_principal_id=c.principal_id AND r.status IN ('starting','running','waiting'))
 FROM agent_pairing_enrollments e JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE e.account_id=$1 AND c.principal_id=$3 AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL
 AND c.state='connected' AND q.state='redeemed' AND `+modelprefs.CanonicalPersonSQL("q.approved_by")+`=$2::uuid`, accountID, owner, run.AgentID).Scan(&host, &raw, &policyRaw, &at, &running)
	if isNoRows(err) {
		return "", "", "", workorders.Fail(409, "host_inputs_unreadable")
	}
	if err != nil {
		return "", "", "", err
	}
	var signals *hostcapacity.Signals
	var policy hostcapacity.Policy
	if len(raw) > 8192 || len(policyRaw) > 8192 || json.Unmarshal(raw, &signals) != nil || signals == nil || signals.Validate() != nil || json.Unmarshal(policyRaw, &policy) != nil || policy.Validate() != nil || at == nil || at.After(now) || now.Sub(*at) > time.Duration(g.HostMaxAgeMS)*time.Millisecond || signals.Load == nil {
		return "", "", "", workorders.Fail(409, "host_inputs_unreadable")
	}
	if *signals.Load >= 30 {
		return "", "", "", workorders.Fail(409, "host_load")
	}
	if reason, _ := hostcapacity.Evaluate(policy, signals, at, running, now); reason != "" {
		return "", "", "", workorders.Fail(409, "host_"+reason)
	}
	return a.Harness, a.BillingMode, host, nil
}
