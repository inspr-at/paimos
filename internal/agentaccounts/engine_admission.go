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
	projectID := ""
	if len(projectIDs) > 0 {
		projectID = projectIDs[0]
	}
	out, err := engineCapacityInputs(ctx, tx, p, owner, harness, hours, now, projectID)
	if err != nil {
		return EngineCapacity{}, err
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return out, err
}

func engineCapacityInputs(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, harness string, hours float64, now time.Time, projectID string) (EngineCapacity, error) {

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
	used, err := occupancy(ctx, tx)
	if err != nil {
		return EngineCapacity{}, err
	}
	quotaUsed, err := quotaOccupancy(ctx, tx)
	if err != nil {
		return EngineCapacity{}, err
	}
	out := EngineCapacity{Reason: "account_unavailable"}
	denied, allowedCandidate := false, false
	for _, a := range accounts {
		if a.Harness != harness || !privacy[a.ID] {
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
		windows, wait, err := admission(ctx, tx, a, a.Windows, now, slotCount(a, used, quotaUsed), runRow{Purpose: "managed"}, false)
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
		reason, err := engineHost(ctx, tx, a.ID, owner, now)
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

func engineHost(ctx context.Context, tx pgx.Tx, account, owner string, now time.Time) (string, error) {
	var raw, policyRaw []byte
	var at *time.Time
	var running int
	err := tx.QueryRow(ctx, `SELECT c.capacity_signals,c.capacity_policy,c.capacity_reported_at,
 (SELECT count(*) FROM agent_runs r WHERE r.agent_principal_id=c.principal_id AND (r.status IN ('starting','running','waiting')
  OR r.trace->'work_lifecycle_release'->>'exit_unconfirmed'='true'))
 FROM agent_pairing_enrollments e
 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE e.account_id=$1 AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL AND c.state='connected'
 AND q.state='redeemed' AND `+modelprefs.CanonicalPersonSQL("q.approved_by")+`=$2::uuid`, account, owner).Scan(&raw, &policyRaw, &at, &running)
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
