// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// EngineCapacity is a content-free advisory. No account or computer identity,
// measurement, reservation, recovery permit or execution authority leaves it.
type EngineCapacity struct {
	Reason string
	Until  *time.Time
}

// EngineCapacityTx reuses the overview's account admission/pacing/limit rules.
// It additionally requires fresh measurable capacity and a fresh enrolled host;
// the runtime's blind one-run recovery fallback never authorizes this shadow gate.
func EngineCapacityTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, harness string, hours float64, now time.Time, projectIDs ...string) (EngineCapacity, error) {
	projectID := ""
	if len(projectIDs) > 0 {
		projectID = projectIDs[0]
	}
	return engineCapacityTx(ctx, tx, p, owner, harness, hours, now, projectID, runRow{Purpose: "managed"})
}

// EngineRunCapacityTx checks only the account bound to this routine actor,
// including an exact claimed replay or settled completion. Its own slot and
// window holds are excluded from projections; sibling runs remain counted
// across projects and shared quotas. Callers retain the
// tenant/tree/pairing fences and the durable-intent check in RequireRoutineTx.
func EngineRunCapacityTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, harness string, hours float64, now time.Time, projectID, runID string) (EngineCapacity, error) {
	run, err := lockRun(ctx, tx, runID)
	if err != nil {
		return EngineCapacity{}, err
	}
	if run.AccountID == nil || run.ProfileID == nil || run.Purpose != "managed" {
		return EngineCapacity{}, workorders.Fail(409, "routine_account_unbound")
	}
	if err := requireRoutineAccountPhase(ctx, tx, run, true); err != nil {
		return EngineCapacity{}, err
	}
	return engineCapacityTx(ctx, tx, p, owner, harness, hours, now, projectID, run)
}

func engineCapacityTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, harness string, hours float64, now time.Time, projectID string, run runRow) (EngineCapacity, error) {
	permission := "account.read"
	if p.Kind == tenant.Agent {
		permission = "account.overview.read"
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{}); err != nil {
		return EngineCapacity{}, err
	}
	// Account authority is workspace-wide. Evaluate only owned, privacy-visible
	// candidates, but count their occupancy and hard limits across all projects.
	// Otherwise a project-only observer could miss a sibling run's reservation.
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return EngineCapacity{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return EngineCapacity{}, err
	}
	out, err := engineCapacityInputs(ctx, tx, p, owner, harness, hours, now, projectID, run)
	if err != nil {
		return EngineCapacity{}, err
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return out, err
}

func engineCapacityInputs(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, harness string, hours float64, now time.Time, projectID string, run runRow) (EngineCapacity, error) {

	for _, bound := range []struct {
		query   string
		maximum int
	}{
		{`SELECT count(*) FROM (SELECT 1 FROM agent_accounts LIMIT 1025) a`, 1024},
		{`SELECT count(*) FROM (SELECT 1 FROM account_allowance_windows WHERE NOT pairing_verification AND NOT capacity_retired AND removed_at IS NULL LIMIT 4097) w`, 4096},
	} {
		var n int
		if err := tx.QueryRow(ctx, bound.query).Scan(&n); err != nil {
			return EngineCapacity{}, err
		}
		if n > bound.maximum {
			return EngineCapacity{Reason: "account_inputs_unreadable"}, nil
		}
	}
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return EngineCapacity{}, err
	}
	ids := []string{}
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	privacy, err := accountprivacy.Load(ctx, tx, p, ids)
	if err != nil {
		return EngineCapacity{}, err
	}
	used, err := occupancyExcept(ctx, tx, run.ID)
	if err != nil {
		return EngineCapacity{}, err
	}
	quotaUsed, err := quotaOccupancyExcept(ctx, tx, run.ID)
	if err != nil {
		return EngineCapacity{}, err
	}
	out := EngineCapacity{Reason: "account_unavailable"}
	denied, allowedCandidate := false, false
	for _, a := range accounts {
		if a.Harness != harness || !privacy[a.ID] || run.AccountID != nil && (a.ID != *run.AccountID || a.RegisteredBy != run.AgentID) {
			continue
		}
		var own bool
		if err = tx.QueryRow(ctx, `SELECT coalesce(`+modelprefs.CanonicalPersonSQL("a.owner_person_id")+`=$2::uuid,false) FROM agent_accounts a WHERE a.id=$1`, a.ID, owner).Scan(&own); err != nil {
			return EngineCapacity{}, err
		}
		if !own {
			continue
		}
		allowed, err := accountuse.AllowedForProject(ctx, tx, a.ID, projectID)
		if err != nil {
			return EngineCapacity{}, err
		}
		if !allowed {
			denied = true
			continue
		}
		allowedCandidate = true
		var model bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE enabled AND harness=$1 AND aeon_account_allows_profile($1,$2::uuid[],id))`, harness, a.AllowedProfileIDs).Scan(&model); err != nil {
			return EngineCapacity{}, err
		}
		if !model {
			out = EngineCapacity{Reason: "account_models"}
			continue
		}
		if run.ProfileID != nil {
			allowed, err := AccountAllowsProfile(ctx, tx, a, *run.ProfileID)
			if err != nil {
				return EngineCapacity{}, err
			}
			if !allowed {
				return EngineCapacity{Reason: "account_models"}, nil
			}
		}
		windows, wait, err := admissionInputs(ctx, tx, a, a.Windows, now, slotCount(a, used, quotaUsed), run, false, nil, run.ID)
		if err != nil {
			return EngineCapacity{}, err
		}
		if wait != nil {
			out = EngineCapacity{Reason: "account_" + wait.Code, Until: wait.Until}
			continue
		}
		// All binding vendor windows must be fresh; missing windows and synthetic
		// grants represent unknown usage and cannot prove the floors are preserved.
		known := false
		stale := false
		for _, w := range a.Windows {
			if w.capacityReadAt != nil && now.Before(w.EndsAt) && !synthetic(w) && (w.capacityReadAt.After(now) || now.Sub(*w.capacityReadAt) > 10*time.Minute || w.capacitySource == "estimate") {
				stale = true
			}
		}
		for _, w := range windows {
			if w.capacityReadAt != nil && !synthetic(w) && !w.capacityReadAt.After(now) && now.Sub(*w.capacityReadAt) <= 10*time.Minute && w.capacitySource != "estimate" {
				known = true
			}
		}
		if !known || stale {
			out = EngineCapacity{Reason: "account_inputs_unreadable"}
			continue
		}
		learned, err := loadLearning(ctx, tx, a.ID)
		if err != nil {
			return EngineCapacity{}, err
		}
		schedule, err := routingSchedule(ctx, tx, a)
		if err != nil {
			return EngineCapacity{}, err
		}
		fitsAll := true
		for _, w := range windows {
			need := max(int64(1), windowEstimate(w, map[string]int64{"requests": 1, "tokens": 1, "cost_micros": 1}))
			if w.capacityReadAt != nil && !synthetic(w) {
				metric := learned.metric(capacity.Reading{WindowKind: w.capacityKind, Bucket: w.capacityBucket, WindowMinutes: int(w.EndsAt.Sub(w.StartsAt) / time.Minute)}, now, schedule, "")
				burn := metric.BurnRate * hours
				if math.IsNaN(burn) || math.IsInf(burn, 0) || burn > 1000000 {
					return EngineCapacity{Reason: "account_inputs_unreadable"}, nil
				}
				need = max(need, int64(math.Ceil(burn)))
			}
			if _, ok := fits(w, now, need); !ok {
				fitsAll = false
				out = EngineCapacity{Reason: "account_estimate", Until: &w.EndsAt}
				break
			}
		}
		if !fitsAll {
			continue
		}
		reason, err := engineHost(ctx, tx, a.ID, owner, now, run.ID)
		if err != nil {
			return EngineCapacity{}, err
		}
		if reason != "" {
			out = EngineCapacity{Reason: reason}
			continue
		}
		return EngineCapacity{Reason: "allowed"}, nil
	}
	if denied && !allowedCandidate {
		return EngineCapacity{Reason: "context"}, nil
	}
	return out, nil
}

func engineHost(ctx context.Context, tx pgx.Tx, account, owner string, now time.Time, excludeRun string) (string, error) {
	var raw, policyRaw []byte
	var at *time.Time
	var running int
	err := tx.QueryRow(ctx, `SELECT c.capacity_signals,c.capacity_policy,c.capacity_reported_at,
 (SELECT count(*) FROM agent_runs r WHERE r.agent_principal_id=c.principal_id AND r.account_id IS NOT NULL AND ($3::text='' OR r.id::text<>$3) AND (r.status IN ('starting','running','waiting')
  OR r.trace->'work_lifecycle_release'->>'exit_unconfirmed'='true'))
 FROM agent_pairing_enrollments e
 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE e.account_id=$1 AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL AND c.state='connected'
 AND q.state='redeemed' AND `+modelprefs.CanonicalPersonSQL("q.approved_by")+`=$2::uuid`, account, owner, excludeRun).Scan(&raw, &policyRaw, &at, &running)
	if isNoRows(err) {
		return "host_inputs_unreadable", nil
	}
	if err != nil {
		return "", err
	}
	var signals *hostcapacity.Signals
	var policy hostcapacity.Policy
	if json.Unmarshal(raw, &signals) != nil || signals == nil || signals.Validate() != nil || json.Unmarshal(policyRaw, &policy) != nil || policy.Validate() != nil || at == nil || at.After(now) || now.Sub(*at) > time.Minute || signals.Load == nil {
		return "host_inputs_unreadable", nil
	}
	// Match start-queue's global safety ceiling exactly, including equality.
	if *signals.Load >= 30 {
		return "host_load", nil
	}
	if reason, _ := hostcapacity.Evaluate(policy, signals, at, running, now); reason != "" {
		return "host_" + reason, nil
	}
	return "", nil
}

// Adjust projections only; never release the persisted ledger. Shared quota and
// fact windows carry overlapping historical holds, so match their resource key
// and interval. Independent account windows retain their exact window IDs.
func excludeWindowReservations(ctx context.Context, tx pgx.Tx, windows []Window, runID string, sharedQuota bool) error {
	if runID == "" {
		return nil
	}
	rows, err := tx.Query(ctx, `WITH holds AS (
 SELECT tenant_id,window_id,reserved_units FROM account_reservations WHERE run_id=$1 AND state='active'
 UNION ALL
 SELECT r.tenant_id,h.window_id,h.reserved_units FROM native_session_roots r,
 jsonb_to_recordset(r.budget_holds) AS h(window_id uuid,reserved_units bigint)
 WHERE r.owned_session_id=$1 AND r.run_id IS NULL AND r.released_at IS NULL)
 SELECT w.id::text,coalesce(w.capacity_kind,''),coalesce(w.capacity_bucket,''),w.starts_at,w.ends_at,h.reserved_units
 FROM holds h JOIN account_allowance_windows w ON w.tenant_id=h.tenant_id AND w.id=h.window_id
 ORDER BY w.id LIMIT 4097`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 4096 {
			return workorders.Fail(409, "account_inputs_unreadable")
		}
		var id, kind, bucket string
		var start, end time.Time
		var held int64
		if err := rows.Scan(&id, &kind, &bucket, &start, &end, &held); err != nil {
			return err
		}
		for i := range windows {
			w := &windows[i]
			match := w.ID == id
			if w.capacityKind == "fact" || sharedQuota && w.capacityReadAt != nil && !synthetic(*w) {
				match = kind == w.capacityKind && bucket == w.capacityBucket && start.Before(w.EndsAt) && w.StartsAt.Before(end)
			}
			if match {
				if held > w.Reserved {
					return workorders.Fail(409, "account_inputs_unreadable")
				}
				w.Reserved -= held
			}
		}
	}
	return rows.Err()
}
