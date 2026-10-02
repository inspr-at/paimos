// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// A permit identifies the exact resource/window/wait, independent of the
// daemon generation. Advice only prepares it; reservation consumes it atomically.
type recoveryPermit struct {
	resource, window, wait string
	check                  *string
}

func recoverableFact(f ReadinessFact) bool {
	return f.StopKind == "money_402" || f.StopKind == "unnamed" &&
		(f.DenialReason == "" || f.DenialReason == "vendor_denied") &&
		(f.UsedPercent == nil || *f.UsedPercent < 100) &&
		(f.Remaining == nil || *f.Remaining > 0) && f.CreditState != "exhausted"
}

func recoveryBackoff(step int) time.Duration { return time.Hour << min(max(step, 0), 3) }

// Current legacy telemetry is projected even before its first fenced write.
// Its canonical row prevents a cleared run stop from being imported again.
func legacyVendorFact(ctx context.Context, tx pgx.Tx, a Account) (*ReadinessFact, error) {
	var at time.Time
	var reset *time.Time
	err := tx.QueryRow(ctx, `SELECT t.at,COALESCE(t.limit_resets_at,
 (SELECT max(r.resets_at) FROM account_capacity_readings r WHERE r.account_id IN (`+quotaAccounts+`)
  AND r.run_id=t.run_id AND r.source<>'estimate' AND (r.ordinary_usage_allowed=false OR r.used_percent>=100)
  AND (t.limit_window='' OR r.window_kind=t.limit_window) AND r.resets_at>t.at))
 FROM run_telemetry t JOIN agent_runs ar ON ar.tenant_id=t.tenant_id AND ar.id=t.run_id
 WHERE ar.account_id IN (`+quotaAccounts+`) AND t.error_code='vendor_limit'
 ORDER BY t.at DESC,t.run_id,t.sequence DESC LIMIT 1`, a.ID).Scan(&at, &reset)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	resource, err := localReadinessResource(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	var already bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor' AND observed_at >= $2)`, resource, at).Scan(&already); err != nil || already {
		return nil, err
	}
	f := &ReadinessFact{ReadinessFactWrite: ReadinessFactWrite{ResourceID: resource, WindowKey: "vendor", Source: "harness", ObservedAt: at, ResetsAt: reset, CreditState: "unknown", StopKind: "unnamed", DenialReason: "vendor_denied"}}
	if reset != nil {
		f.StopKind = "named_reset"
	} else {
		next := at.Add(time.Hour)
		f.NextAttemptAt = &next
	}
	return f, nil
}

func reconcileVendorStop(ctx context.Context, tx pgx.Tx, a Account, now time.Time) error {
	f, err := legacyVendorFact(ctx, tx, a)
	if err != nil || f == nil {
		return err
	}
	// Import the original stop deadline, never restart the wait at daemon boot.
	return storeReadinessFact(ctx, tx, a, f.ReadinessFactWrite, f.ObservedAt)
}

func admissionFacts(ctx context.Context, tx pgx.Tx, a Account, now time.Time) ([]ReadinessFact, error) {
	facts, err := loadReadinessFacts(ctx, tx, a, now)
	if err != nil {
		return nil, err
	}
	legacy, err := legacyReadinessFacts(ctx, tx, a, now)
	if err != nil {
		return nil, err
	}
	facts = append(facts, legacy...)
	vendor, err := legacyVendorFact(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	if vendor != nil {
		facts = append(facts, *vendor)
	}
	if c := a.OpenRouterCredits; c != nil && c.Remaining != nil {
		facts = append(facts, ReadinessFact{ReadinessFactWrite: ReadinessFactWrite{WindowKey: "key_cap", ObservedAt: c.ObservedAt, ReadingAt: &c.ObservedAt, Remaining: c.Remaining, CreditState: "unknown"}})
	}
	return facts, nil
}

func readinessAdmission(ctx context.Context, tx pgx.Tx, a Account, now time.Time, run runRow, claiming bool) ([]recoveryPermit, *CapacityWait, error) {
	facts, err := admissionFacts(ctx, tx, a, now)
	if err != nil {
		return nil, nil, err
	}
	permits := []recoveryPermit{}
	var blocked *CapacityWait
	for _, f := range facts {
		if f.ReadingError == "identity_mismatch" || f.ReadingError == "authentication_failed" {
			return nil, waitFor("sign_in"), nil
		}
		if f.StopKind == "named_reset" && f.ResetsAt != nil && !now.Before(*f.ResetsAt) {
			continue
		}
		hard := f.StopKind != "" && f.StopKind != "none" ||
			(f.ResetsAt == nil || now.Before(*f.ResetsAt)) &&
				(f.UsedPercent != nil && *f.UsedPercent >= 100 || f.Remaining != nil && *f.Remaining == 0 || f.CreditState == "exhausted")
		if !hard {
			continue
		}
		until := f.NextAttemptAt
		if f.StopKind == "named_reset" {
			until = f.ResetsAt
		}
		allowed := recoverableFact(f) && f.WaitID == nil && until != nil && !now.Before(*until)
		if recoverableFact(f) && f.WaitID != nil {
			permit := recoveryPermit{resource: f.ResourceID, window: f.WindowKey, wait: *f.WaitID}
			if f.recoveryRunID != nil {
				allowed = claiming && *f.recoveryRunID == run.ID
			} else if !claiming {
				allowed = until != nil && !now.Before(*until)
				if !allowed && !f.EarlyRecoveryUsed {
					var check string
					err := tx.QueryRow(ctx, `SELECT c.id::text FROM account_readiness_check_waits w
 JOIN account_readiness_checks c ON c.tenant_id=w.tenant_id AND c.id=w.check_id
 JOIN agent_accounts a ON a.tenant_id=c.tenant_id AND a.id=c.account_id
 JOIN account_readiness_memberships m ON m.tenant_id=w.tenant_id AND m.resource_id=w.resource_id AND m.account_id=a.id AND m.binding_revision=a.link_revision
 WHERE w.resource_id=$1 AND w.window_key=$2 AND w.wait_id=$3 AND c.early_recovery_requested
 AND c.state IN ('pending','completed') AND c.binding_revision=a.link_revision
 AND c.daemon_generation IS NOT DISTINCT FROM a.last_daemon_generation
 AND c.actor_principal_id=a.owner_person_id AND c.requested_at>$4
 ORDER BY c.requested_at,c.id LIMIT 1`, f.ResourceID, f.WindowKey, *f.WaitID, now.Add(-CheckTTL)).Scan(&check)
					if err != nil && !isNoRows(err) {
						return nil, nil, err
					}
					if err == nil {
						permit.check, allowed = &check, true
					}
				}
			}
			if allowed && f.recoveryRunID == nil {
				var busy bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.status IN ('queued','starting','running','waiting') AND ar.id::text<>$3 AND (ar.account_id IN (`+quotaAccounts+`) OR ar.account_id IN (SELECT m.account_id FROM account_readiness_memberships m JOIN agent_accounts a ON a.tenant_id=m.tenant_id AND a.id=m.account_id WHERE m.resource_id=$2 AND m.binding_revision=a.link_revision)))`, a.ID, f.ResourceID, run.ID).Scan(&busy); err != nil {
					return nil, nil, err
				}
				allowed = !busy
			}
			if allowed {
				permits = append(permits, permit)
			}
		}
		if !allowed {
			if blocked == nil || until != nil && (blocked.Until == nil || until.After(*blocked.Until)) {
				blocked = &CapacityWait{Code: "vendor", Until: until, ReadAt: &f.ObservedAt}
			}
		}
	}
	return permits, blocked, nil
}

func consumeRecovery(ctx context.Context, tx pgx.Tx, runID string, permits []recoveryPermit) error {
	// Every caller holds the pairing lock before rows. Facts are loaded in
	// resource/window order, so this batch retains the same global order.
	for _, p := range permits {
		tag, err := tx.Exec(ctx, `UPDATE account_readiness_facts SET recovery_run_id=$4,
 recovery_check_id=$5,early_recovery_used=early_recovery_used OR $5::uuid IS NOT NULL
 WHERE resource_id=$1 AND window_key=$2 AND wait_id=$3 AND recovery_run_id IS NULL`, p.resource, p.window, p.wait, runID, p.check)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(409, "recovery wait already reserved")
		}
	}
	return nil
}

// settleRecovery is called before the event counter. A completed process is
// insufficient: successful inference requires positive output telemetry and
// no vendor denial. Failed/unevidenced runs keep the stop and open a new wait.
func settleRecovery(ctx context.Context, tx pgx.Tx, run runRow, now time.Time) error {
	switch run.Status {
	case "completed", "failed", "cancelled", "ownership_lost":
	default:
		return nil
	}
	var started, evidenced bool
	if err := tx.QueryRow(ctx, `SELECT started_at IS NOT NULL,
 status='completed' AND EXISTS(SELECT 1 FROM run_telemetry WHERE run_id=$1 AND output_tokens_delta>0)
 AND NOT EXISTS(SELECT 1 FROM run_telemetry WHERE run_id=$1 AND error_code IS NOT NULL)
 FROM agent_runs WHERE id=$1`, run.ID).Scan(&started, &evidenced); err != nil {
		return err
	}
	if !started {
		_, err := tx.Exec(ctx, `UPDATE account_readiness_facts SET recovery_run_id=NULL,recovery_check_id=NULL,
 early_recovery_used=CASE WHEN recovery_check_id IS NOT NULL THEN false ELSE early_recovery_used END WHERE recovery_run_id=$1`, run.ID)
		return err
	}
	if evidenced {
		_, err := tx.Exec(ctx, `UPDATE account_readiness_facts SET stop_kind='none',denial_reason='',
 credit_state='unknown',used_percent=NULL,remaining=NULL,reading_at=NULL,observed_at=GREATEST(observed_at,$2),
 wait_id=NULL,next_attempt_at=NULL,backoff_step=0,early_recovery_used=false,recovery_run_id=NULL,recovery_check_id=NULL
 WHERE recovery_run_id=$1 AND stop_kind IN ('unnamed','money_402')`, run.ID, now)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE account_readiness_facts SET backoff_step=least(backoff_step+1,3),
 next_attempt_at=$2::timestamptz+CASE backoff_step WHEN 0 THEN interval '2 hours' WHEN 1 THEN interval '4 hours' ELSE interval '8 hours' END,
 observed_at=GREATEST(observed_at,$2),wait_id=gen_random_uuid(),early_recovery_used=false,recovery_run_id=NULL,recovery_check_id=NULL
 WHERE recovery_run_id=$1 AND stop_kind IN ('unnamed','money_402')`, run.ID, now)
	return err
}

func releaseRecovery(ctx context.Context, tx pgx.Tx, run runRow, now time.Time) error {
	if run.Status == "queued" {
		_, err := tx.Exec(ctx, `UPDATE account_readiness_facts SET recovery_run_id=NULL,recovery_check_id=NULL,
 early_recovery_used=CASE WHEN recovery_check_id IS NOT NULL THEN false ELSE early_recovery_used END WHERE recovery_run_id=$1`, run.ID)
		return err
	}
	return settleRecovery(ctx, tx, run, now)
}

func tenantContext(ctx context.Context, actor tenant.Principal) context.Context {
	return tenant.WithPrincipal(ctx, actor)
}
