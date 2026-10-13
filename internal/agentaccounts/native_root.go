// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/jackc/pgx/v5"
)

// NativeAdmission is server-resolved, never a browser's account/home settings.
// The caller holds tenant -> tree -> pairing, has called RequireTx against
// the original actor, and appends its event only after these helpers finish.
type NativeAdmission struct {
	TenantID, ProjectID, OwnerPersonID, PrincipalID string
	OwnedSessionID                                  string
	AccountID, ComputerID, Harness, ModelProfileID  string
	DaemonID, DaemonGeneration, LedgerGeneration    string
	WorkID, RunID                                   *string
}

// NativeBudget carries the exact admitted windows and estimates to persistence
// in the same fenced transaction. It contains no browser-supplied quantities.
type NativeBudget struct {
	tenantID, accountID, sessionID string
	windows                        []Window
	estimates                      map[string]int64
	now                            time.Time
}

type nativeBudgetHold struct {
	WindowID      string `json:"window_id"`
	ReservedUnits int64  `json:"reserved_units"`
}

func nativeDenial(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

// NativeRootCountTx counts ALL unreleased roots, irrespective of heartbeat,
// daemon generation or display state. Unknown/idle/taken holds stay occupied.
// I1 adds this exact predicate to legacy account/host admission; workers are
// counted exclusively by their existing account-backed agent_runs predicate.
func NativeRootCountTx(ctx context.Context, tx pgx.Tx, account, computer, exceptSession string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM native_session_roots
 WHERE run_id IS NULL AND released_at IS NULL AND owned_session_id::text<>$3
 AND ($1='' OR account_id::text=$1) AND ($2='' OR computer_id::text=$2)`, account, computer, exceptSession).Scan(&n)
	return n, err
}

// RequireNativeCapacityTx reuses current context, model, daily, readiness,
// quota, pacing and account gates. It never creates a fake worker run.
func RequireNativeCapacityTx(ctx context.Context, tx pgx.Tx, in NativeAdmission, exceptSession string, now time.Time) error {
	return requireNativeCapacityTx(ctx, tx, in, exceptSession, now, nil)
}

// AdmitNativeCapacityTx retains the budget for a new coordinator claim. Replay
// only projects its own saved holds out; it never releases or renews them.
func AdmitNativeCapacityTx(ctx context.Context, tx pgx.Tx, in NativeAdmission, exceptSession string, now time.Time) (NativeBudget, error) {
	var budget NativeBudget
	err := requireNativeCapacityTx(ctx, tx, in, exceptSession, now, &budget)
	return budget, err
}

func requireNativeCapacityTx(ctx context.Context, tx pgx.Tx, in NativeAdmission, exceptSession string, now time.Time, budget *NativeBudget) error {
	// Existing window hydration is bounded before it can allocate/lock rows.
	for _, bound := range []struct {
		sql string
		max int
	}{
		{`SELECT count(*) FROM (SELECT 1 FROM agent_accounts LIMIT 1025) x`, 1024},
		{`SELECT count(*) FROM (SELECT 1 FROM account_allowance_windows WHERE removed_at IS NULL OR id IN (SELECT from_window_id FROM account_limit_rules WHERE removed_at IS NULL) LIMIT 4097) x`, 4096},
	} {
		var n int
		if err := tx.QueryRow(ctx, bound.sql).Scan(&n); err != nil {
			return err
		}
		if n > bound.max {
			return nativeDenial(nativecontract.ReasonBufferFull)
		}
	}
	var owner, computer, principal, daemon, generation, ledger string
	err := tx.QueryRow(ctx, `SELECT `+modelprefs.CanonicalPersonSQL("a.owner_person_id")+`::text,c.id::text,c.principal_id::text,c.daemon_id,
 coalesce(a.last_daemon_generation,''),coalesce(c.ledger_generation,'')
 FROM agent_accounts a JOIN agent_pairing_enrollments e ON e.tenant_id=a.tenant_id AND e.account_id=a.id
 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE a.id=$1 AND a.tenant_id=$2 AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL
 AND c.state='connected' AND q.state='redeemed'
 AND `+modelprefs.CanonicalPersonSQL("q.approved_by")+`=`+modelprefs.CanonicalPersonSQL("a.owner_person_id")+`
 FOR NO KEY UPDATE OF a`, in.AccountID, in.TenantID).Scan(&owner, &computer, &principal, &daemon, &generation, &ledger)
	if errors.Is(err, pgx.ErrNoRows) {
		return nativeDenial(nativecontract.ReasonGrantMissing)
	}
	if err != nil {
		return err
	}
	if owner != in.OwnerPersonID || principal != in.PrincipalID || computer != in.ComputerID || daemon != in.DaemonID {
		return nativeDenial(nativecontract.ReasonWrongPrincipal)
	}
	if generation != in.DaemonGeneration || ledger == "" || ledger != in.LedgerGeneration {
		return nativeDenial(nativecontract.ReasonWrongGeneration)
	}
	a, err := getAccount(ctx, tx, in.AccountID)
	if err != nil {
		return err
	}
	a.OwnerPersonID = &owner
	if a.Harness != in.Harness {
		return nativeDenial(nativecontract.ReasonIdentityMismatch)
	}
	if err = accountuse.RequireProject(ctx, tx, a.ID, in.ProjectID); err != nil {
		return nativeDenial(nativecontract.ReasonPermissionRevoked)
	}
	allowed, err := AccountAllowsProfile(ctx, tx, a, in.ModelProfileID)
	if err != nil {
		return err
	}
	if !allowed {
		return nativeDenial(nativecontract.ReasonQualificationMissing)
	}
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT enabled AND harness=$2 FROM model_profiles WHERE id=$1`, in.ModelProfileID, in.Harness).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return nativeDenial(nativecontract.ReasonQualificationMissing)
	}
	run := runRow{ID: in.OwnedSessionID, AgentID: in.PrincipalID, ProfileID: &in.ModelProfileID, Purpose: "native_root"}
	if in.RunID == nil {
		if !uuidRE.MatchString(in.OwnedSessionID) || exceptSession != "" && exceptSession != in.OwnedSessionID {
			return nativeDenial(nativecontract.ReasonIdentityMismatch)
		}
		if exceptSession != "" {
			err = tx.QueryRow(ctx, `SELECT account_limit_estimates FROM native_session_roots
 WHERE owned_session_id=$1 AND account_id=$2 AND run_id IS NULL AND released_at IS NULL`, exceptSession, in.AccountID).Scan(&run.LimitEstimates)
			if errors.Is(err, pgx.ErrNoRows) {
				return nativeDenial(nativecontract.ReasonOwnershipUnconfirmed)
			}
			if err != nil {
				return err
			}
		}
		if len(run.LimitEstimates) == 0 {
			run.LimitEstimates, err = learnedLimitEstimates(ctx, tx, a.ID, nil)
			if err != nil {
				return err
			}
		}
	}
	if in.RunID != nil {
		run, err = lockRun(ctx, tx, *in.RunID)
		if err != nil {
			return err
		}
		work, workErr := runTicketID(ctx, tx, *in.RunID)
		if workErr != nil {
			return workErr
		}
		phaseOK := run.Status == "queued" || exceptSession != "" && run.Status == "starting" && run.DaemonID != nil && *run.DaemonID == in.DaemonID && run.Generation != nil && *run.Generation == in.DaemonGeneration
		if run.AgentID != in.PrincipalID || run.ProfileID == nil || *run.ProfileID != in.ModelProfileID || run.AccountID == nil || *run.AccountID != in.AccountID || run.Purpose != "managed" || run.RoutineLaunchID != nil || !phaseOK || work == "" || in.WorkID == nil || work != *in.WorkID {
			return nativeDenial(nativecontract.ReasonIdentityMismatch)
		}
		if err = accountuse.RequireRun(ctx, tx, a.ID, run.ID); err != nil {
			return nativeDenial(nativecontract.ReasonPermissionRevoked)
		}
		if err = validateReservedAccount(ctx, tx, run, a.ID); err != nil {
			return err
		}
	}
	if wait, err := dailyStartAccountTx(ctx, tx, a, now); err != nil {
		return err
	} else if wait != nil {
		return nativeDenial(nativecontract.ReasonQuotaLimited)
	}
	// Account occupancy must include sibling projects, even for project-scoped
	// actors. These counts return no private account/session facts.
	used, err := occupancyExcept(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	quotaUsed, err := quotaOccupancyExcept(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT r.account_id::text,a.harness||':'||coalesce(a.quota_pool_fingerprint,''),count(*)
 FROM native_session_roots r JOIN agent_accounts a ON a.tenant_id=r.tenant_id AND a.id=r.account_id
 WHERE r.run_id IS NULL AND r.released_at IS NULL AND r.owned_session_id::text<>$1
	 GROUP BY r.account_id,a.harness,a.quota_pool_fingerprint LIMIT 1025`, exceptSession)
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		var id, pool string
		var count int
		if err = rows.Scan(&id, &pool, &count); err != nil {
			rows.Close()
			return err
		}
		n++
		if n > 1024 {
			rows.Close()
			return nativeDenial(nativecontract.ReasonBufferFull)
		}
		used[id] += count
		if pool != "" {
			quotaUsed[pool] += count
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	windows, wait, err := admissionInputs(ctx, tx, a, a.Windows, now, slotCount(a, used, quotaUsed), run, in.RunID != nil || exceptSession != "", nil, run.ID)
	if err != nil {
		return err
	}
	if wait != nil {
		switch wait.Code {
		case "capacity":
			return nativeDenial(nativecontract.ReasonAccountFull)
		case "sign_in":
			return nativeDenial(nativecontract.ReasonLoginRequired)
		case "offline", "reading":
			return nativeDenial(nativecontract.ReasonQuotaUnknown)
		default:
			return nativeDenial(nativecontract.ReasonQuotaLimited)
		}
	}
	if in.RunID == nil {
		for _, w := range windows {
			// Recovery permits belong to their existing worker-run journal. A
			// native root cannot consume them without that qualified adapter.
			if len(w.recoveryPermits) > 0 {
				return nativeDenial(nativecontract.ReasonQuotaUnknown)
			}
			if _, ok := fits(w, now, windowEstimate(w, run.LimitEstimates)); !ok {
				return nativeDenial(nativecontract.ReasonQuotaLimited)
			}
		}
	}
	if err = requireNativeHost(ctx, tx, in, exceptSession, now); err != nil {
		return err
	}
	if budget != nil {
		*budget = NativeBudget{tenantID: in.TenantID, accountID: in.AccountID, sessionID: in.OwnedSessionID, windows: windows, estimates: run.LimitEstimates, now: now}
	}
	return nil
}

func requireNativeHost(ctx context.Context, tx pgx.Tx, in NativeAdmission, exceptSession string, now time.Time) error {
	var raw, policyRaw []byte
	var at *time.Time
	var running int
	err := tx.QueryRow(ctx, `SELECT capacity_policy,capacity_signals,capacity_reported_at,
 (SELECT count(*) FROM agent_runs r WHERE r.agent_principal_id=c.principal_id AND r.account_id IS NOT NULL
 AND r.id::text<>$2 AND (r.status IN ('starting','running','waiting') OR r.trace->'work_lifecycle_release'->>'exit_unconfirmed'='true'))
 FROM agent_pairing_computers c WHERE c.id=$1`, in.ComputerID, valueOrEmpty(in.RunID)).Scan(&policyRaw, &raw, &at, &running)
	if err != nil {
		return err
	}
	var policy hostcapacity.Policy
	var signals *hostcapacity.Signals
	if len(policyRaw) > 8192 || len(raw) > 8192 || json.Unmarshal(policyRaw, &policy) != nil || policy.Validate() != nil || json.Unmarshal(raw, &signals) != nil || signals == nil || signals.Validate() != nil || at == nil || at.After(now) || now.Sub(*at) > time.Minute {
		return nativeDenial(nativecontract.ReasonHostFull)
	}
	roots, err := NativeRootCountTx(ctx, tx, "", in.ComputerID, exceptSession)
	if err != nil {
		return err
	}
	if reason, _ := hostcapacity.Evaluate(policy, signals, at, running+roots, now); reason != "" {
		return nativeDenial(nativecontract.ReasonHostFull)
	}
	return agentpairing.AccountFence(ctx, tx, in.AccountID, false)
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// ReserveNativeRootTx is called after admission in the same final transaction.
// The hold survives expiry/outage and is not a renewable launch credential.
func ReserveNativeRootTx(ctx context.Context, tx pgx.Tx, in NativeAdmission, session, epoch string, budget NativeBudget) (*string, error) {
	if in.RunID != nil {
		return nil, nil
	}
	if len(budget.windows) == 0 || len(budget.windows) > 4096 || budget.now.IsZero() || len(budget.estimates) == 0 || session != in.OwnedSessionID || budget.tenantID != in.TenantID || budget.accountID != in.AccountID || budget.sessionID != session {
		return nil, nativeDenial(nativecontract.ReasonIdentityMismatch)
	}
	// Materialize only the winning root's provisional/fact ledgers, like the
	// existing worker router, without inventing an agent_run or refresh-run FK.
	for i := range budget.windows {
		w := &budget.windows[i]
		if w.ID != "" {
			if w.capacityKind == "fact" {
				if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET used=$2,capacity_read_at=$3 WHERE id=$1`, w.ID, w.Used, w.capacityReadAt); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := tx.QueryRow(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,burst_ratio,capacity_kind,capacity_read_at,capacity_allowed,capacity_source,capacity_bucket)
 SELECT tenant_id,id,$2,$3,$7,$8,$9,'unrestricted',0,$5,$4,true,$10,$6 FROM agent_accounts WHERE id=$1
 ON CONFLICT(tenant_id,account_id,capacity_kind,capacity_bucket,ends_at) WHERE capacity_kind IS NOT NULL
 DO UPDATE SET used=EXCLUDED.used,capacity_read_at=EXCLUDED.capacity_read_at RETURNING id::text`, w.AccountID, w.StartsAt, w.EndsAt, w.capacityReadAt, w.capacityKind, w.capacityBucket, w.Unit, w.Allowance, w.Used, w.capacitySource).Scan(&w.ID); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(budget.windows, func(a, b Window) int { return strings.Compare(a.ID, b.ID) })
	holds := make([]nativeBudgetHold, 0, len(budget.windows))
	for _, w := range budget.windows {
		held := windowEstimate(w, budget.estimates)
		tag, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET reserved=reserved+$2
 WHERE id=$1 AND used+reserved+$2<=allowance`, w.ID, held)
		if err != nil {
			return nil, err
		}
		if held < 1 || tag.RowsAffected() != 1 {
			return nil, nativeDenial(nativecontract.ReasonQuotaLimited)
		}
		holds = append(holds, nativeBudgetHold{WindowID: w.ID, ReservedUnits: held})
		if w.Unit == "percent" {
			budget.estimates["percent"] = max(budget.estimates["percent"], held)
		}
	}
	holdsRaw, err := json.Marshal(holds)
	if err != nil {
		return nil, err
	}
	estimatesRaw, err := json.Marshal(budget.estimates)
	if err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO native_session_roots(tenant_id,id,owned_session_id,project_id,owner_person_id,account_id,computer_id,runtime_epoch,hold_state,created_at,budget_holds,account_limit_estimates)
 VALUES($1,gen_random_uuid(),$2,$3,$4,$5,$6,$7,'starting',$8,$9,$10) RETURNING id::text`, in.TenantID, session, in.ProjectID, in.OwnerPersonID, in.AccountID, in.ComputerID, epoch, budget.now, holdsRaw, estimatesRaw).Scan(&id)
	return &id, err
}

// nativeLimitHeldTx makes native roots visible to the existing worker/router
// and account projections too. Active roots retain their saved estimate across
// periods; without native telemetry a confirmed exit retains the overlapping
// period's estimate. A fresh vendor reading can reconcile an exited percent
// hold, exactly as for legacy blind runs. Workers never enter this sum.
func nativeLimitHeldTx(ctx context.Context, tx pgx.Tx, accountID, unit string, start time.Time, exceptSession string, now time.Time) (float64, error) {
	estimates, err := learnedLimitEstimates(ctx, tx, accountID, nil)
	if err != nil {
		return 0, err
	}
	var held float64
	err = tx.QueryRow(ctx, `SELECT coalesce(sum(coalesce((r.account_limit_estimates->>$5)::bigint,$6)),0)::float8
 FROM native_session_roots r WHERE r.account_id=$1 AND r.run_id IS NULL AND r.owned_session_id::text<>$2
 AND (r.released_at IS NULL OR
 ($5='runs' AND r.created_at>=$3 AND r.created_at<=$4) OR
 ($5<>'runs' AND r.created_at<=$4 AND r.released_at>=$3 AND
 ($5<>'percent' OR NOT EXISTS(SELECT 1 FROM account_capacity_readings c
 WHERE c.account_id=r.account_id AND c.source<>'estimate' AND c.read_at>=r.released_at AND c.read_at<=$4))))`, accountID, exceptSession, start, now, unit, estimates[unit]).Scan(&held)
	return held, err
}

// SetNativeRootStateTx cannot release occupancy. Only verified no-fork or
// root+group exit, validated by the owning lifecycle service, can release it.
func SetNativeRootStateTx(ctx context.Context, tx pgx.Tx, session, epoch, state string) error {
	switch state {
	case "queued", "starting", "running", "idle", "taken", "exit_unconfirmed":
	default:
		return nativeDenial(nativecontract.ReasonInvalidInput)
	}
	tag, err := tx.Exec(ctx, `UPDATE native_session_roots SET hold_state=$3 WHERE owned_session_id=$1 AND runtime_epoch=$2 AND released_at IS NULL AND run_id IS NULL`, session, epoch, state)
	if err == nil && tag.RowsAffected() != 1 {
		return nativeDenial(nativecontract.ReasonWrongGeneration)
	}
	return err
}
