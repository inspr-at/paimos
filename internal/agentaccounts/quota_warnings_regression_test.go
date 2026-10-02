// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/jackc/pgx/v5"
)

func quotaPoolComputer(t *testing.T, f limitFixture) limitFixture {
	t.Helper()
	var sibling Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"second","harness":"codex","daemon_id":"daemon-b","label":"Second"}`, 201, &sibling)
	ownFixtureAccount(t, f.admin, &sibling)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET quota_pool_fingerprint=$2,quota_fingerprint=$2 WHERE id=ANY($1::uuid[])`, []string{f.account.ID, sibling.ID}, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	second := f
	second.account = sibling
	return second
}

func quotaSessionWarnings(t *testing.T, f limitFixture, now time.Time) []QuotaWarningSession {
	t.Helper()
	var page struct {
		Items []QuotaWarningSession `json:"items"`
	}
	callStatus(t, fixedClockModule{Module: New(appPool), at: now}, &f.admin, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	return page.Items
}

func TestQuotaWarningDelayedPoolReadingCannotUndoRecovery(t *testing.T) {
	for _, transition := range []string{"recovery", "reset", "healthy-first"} {
		t.Run(transition, func(t *testing.T) {
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			f := readinessWorld(t, "quota-delayed-"+transition, now)
			second := quotaPoolComputer(t, f)
			project, run := insertProjectRun(t, f.admin, f.runner, f.profile, "Pool project")
			lead := quotaSession(t, f, project, "", nil, "Lead")
			quotaSession(t, f, project, run, &lead, "Worker")
			reset := now.Add(time.Hour)
			if transition != "healthy-first" {
				quotaReport(t, f, quotaFact(t, f, 92, now, reset), now)
				if got := quotaSessionWarnings(t, f, now); len(got) != 1 || got[0].RemainingPercent != 8 {
					t.Fatalf("initial early warning: %+v", got)
				}
			}
			newReset := reset
			if transition == "reset" {
				newReset = reset.Add(time.Hour)
			}
			recoveryAt := now.Add(3 * time.Second)
			quotaReport(t, second, quotaFact(t, second, 80, recoveryAt, newReset), recoveryAt)
			if got := quotaSessionWarnings(t, f, recoveryAt); len(got) != 0 {
				t.Fatalf("new healthy pool measurement must clear warning: %+v", got)
			}
			before := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`)
			// The first computer retained a still-fresh low reading measured BEFORE
			// the second computer's recovery. Delivery order is fixed by the clock;
			// neither sleeps nor timing thresholds establish this interleaving.
			deliveredAt := now.Add(4 * time.Second)
			delayed := quotaFact(t, f, 98, now.Add(2*time.Second), reset)
			delayed.ObservedAt = deliveredAt
			quotaReport(t, f, delayed, deliveredAt)
			if got := quotaSessionWarnings(t, f, deliveredAt); len(got) != 0 {
				t.Fatalf("delayed pool reading undid newer %s evidence: %+v", transition, got)
			}
			if after := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); after != before {
				t.Fatalf("delayed sample sent a new notice: before=%d after=%d", before, after)
			}
			// The watermark also rejects a conflicting sample at the same time.
			delayed.ReadingAt = &recoveryAt
			quotaReport(t, f, delayed, deliveredAt)
			if got := quotaSessionWarnings(t, f, deliveredAt); len(got) != 0 {
				t.Fatalf("equal-time low sample replaced healthy evidence: %+v", got)
			}
		})
	}
}

func TestQuotaWarningUrgentRecoveryEarlyKeepsAvailabilityWithoutNotice(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-urgent-recovery-early", now)
	project, run := insertProjectRun(t, f.admin, f.runner, f.profile, "Warning project")
	lead := quotaSession(t, f, project, "", nil, "Lead")
	child := quotaSession(t, f, project, run, &lead, "Worker")
	reset := now.Add(time.Hour)
	quotaReport(t, f, quotaFact(t, f, 98, now, reset), now)
	if got := quotaSessionWarnings(t, f, now); len(got) != 1 || got[0].Severity != "urgent" {
		t.Fatalf("initial urgent availability: %+v", got)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); n != 1 {
		t.Fatalf("urgent-first must send exactly one notice: %d", n)
	}
	at := now.Add(time.Second)
	quotaReport(t, f, quotaFact(t, f, 80, at, reset), at)
	if got := quotaSessionWarnings(t, f, at); len(got) != 0 {
		t.Fatalf("healthy evidence must clear availability: %+v", got)
	}
	at = now.Add(2 * time.Second)
	quotaReport(t, f, quotaFact(t, f, 92, at, reset), at)
	if got := quotaSessionWarnings(t, f, at); len(got) != 1 || got[0].SessionID != child || got[0].Severity != "early" || got[0].RemainingPercent != 8 || got[0].Availability != "limited" {
		t.Fatalf("suppressed early notification hid current availability: %+v", got)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); n != 1 {
		t.Fatalf("early return must not send a notice after urgent-first: %d", n)
	}
	at = now.Add(3 * time.Second)
	quotaReport(t, f, quotaFact(t, f, 98, at, reset), at)
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); n != 1 {
		t.Fatalf("urgent return duplicated a notice: %d", n)
	}
}

func TestQuotaWarningUpgradePreservesHistoricalRecoveryWatermark(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-upgrade-recovery", now)
	project, run := insertProjectRun(t, f.admin, f.runner, f.profile, "Upgrade project")
	lead := quotaSession(t, f, project, "", nil, "Lead")
	quotaSession(t, f, project, run, &lead, "Worker")
	reset := now.Add(time.Hour)
	fact := quotaFact(t, f, 98, now.Add(2*time.Second), reset)
	recoveryAt := now.Add(3 * time.Second)
	// Seed the prior binary's retained receipt directly, without a current
	// observation. Its reading_at precedes recovered_at, and the healthy
	// percentage was never retained. An upgrade must keep that evidence.
	if _, err := adminPool.Exec(t.Context(), `INSERT INTO account_quota_warnings(tenant_id,resource_id,quota_key,window_key,reset_key,threshold_percent,severity,suppressed,reading_at,remaining_percent,resets_at,recovered_at)
        SELECT $1,$2::uuid,$2::uuid::text,'5h',$3,v.threshold,v.severity,v.suppressed,$4,2,$5,$6 FROM (VALUES(3,'urgent',false),(10,'early',true)) v(threshold,severity,suppressed)`, f.admin.TenantID, fact.ResourceID, reset.UTC().Format(time.RFC3339Nano), now, reset, recoveryAt); err != nil {
		t.Fatal(err)
	}
	deliveredAt := now.Add(4 * time.Second)
	fact.ObservedAt = deliveredAt
	quotaReport(t, f, fact, deliveredAt)
	if got := quotaSessionWarnings(t, f, deliveredAt); len(got) != 0 {
		t.Fatalf("upgrade lost historical recovery evidence: %+v", got)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); n != 0 {
		t.Fatalf("upgrade replay sent a new notice: %d", n)
	}
	// A genuinely newer reading resumes current availability while preserving
	// the urgent-only receipt and its early-notification suppression semantics.
	at := now.Add(5 * time.Second)
	quotaReport(t, f, quotaFact(t, f, 92, at, reset), at)
	if got := quotaSessionWarnings(t, f, at); len(got) != 1 || got[0].Severity != "early" || got[0].RemainingPercent != 8 {
		t.Fatalf("new measured evidence did not replace upgrade watermark: %+v", got)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); n != 0 {
		t.Fatalf("upgrade discarded early-notification suppression: %d", n)
	}
}

func TestQuotaWarningInboxDeliveryLifecycle(t *testing.T) {
	for _, reason := range []string{inbox.ReasonNoListener, inbox.ReasonSessionEnded} {
		t.Run(reason, func(t *testing.T) {
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			f := readinessWorld(t, "quota-delivery-"+reason, now)
			project, run := insertProjectRun(t, f.admin, f.runner, f.profile, "Delivery project")
			lead := quotaSession(t, f, project, "", nil, "Lead")
			quotaSession(t, f, project, run, &lead, "Worker")
			if _, err := adminPool.Exec(t.Context(), `INSERT INTO inbox_delivery_settings(tenant_id,session_deadline_seconds,unbound_deadline_seconds,max_attempts) VALUES($1,600,900,8)`, f.admin.TenantID); err != nil {
				t.Fatal(err)
			}
			quotaReport(t, f, quotaFact(t, f, 98, now, now.Add(time.Hour)), now)
			var message string
			if err := adminPool.QueryRow(t.Context(), `SELECT id::text FROM inbox_messages WHERE tenant_id=$1 AND idempotency_key LIKE 'quota-warning/%'`, f.admin.TenantID).Scan(&message); err != nil {
				t.Fatal(err)
			}
			var queued bool
			if err := adminPool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM inbox_receipts r JOIN inbox_messages m ON m.tenant_id=r.tenant_id AND m.id=r.message_id WHERE r.message_id=$1 AND r.state='queued' AND r.deliver_by=m.created_at+interval '600 seconds' AND m.acked_at IS NULL AND m.fetched_at IS NULL)`, message).Scan(&queued); err != nil {
				t.Fatal(err)
			}
			if !queued {
				t.Fatal("unread quota warning needs a queued receipt and the workspace delivery deadline")
			}
			if n := scalar(t, f.admin, `SELECT count(*) FROM events WHERE type='inbox.receipt_queued' AND after->>'message_id'='`+message+`'`); n != 1 {
				t.Fatalf("acceptance must audit one queued receipt: %d", n)
			}
			if reason == inbox.ReasonNoListener {
				// Set a deadline in the known past; do not wait for wall-clock time.
				if _, err := adminPool.Exec(t.Context(), `UPDATE inbox_receipts SET deliver_by=clock_timestamp()-interval '1 second' WHERE message_id=$1`, message); err != nil {
					t.Fatal(err)
				}
				if n, err := inbox.NewSweeper(appPool).SweepTenant(t.Context(), f.admin.TenantID); err != nil || n != 1 {
					t.Fatalf("unread-warning sweep: failed=%d error=%v", n, err)
				}
			} else {
				err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
					if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp() WHERE id=$1`, lead); err != nil {
						return err
					}
					return inbox.FailSessionMessages(t.Context(), tx, f.admin.TenantID, lead)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			var state, failure string
			var hidden bool
			if err := adminPool.QueryRow(t.Context(), `SELECT r.state,r.failure_reason,m.expires_at<=clock_timestamp() FROM inbox_receipts r JOIN inbox_messages m ON m.tenant_id=r.tenant_id AND m.id=r.message_id WHERE r.message_id=$1`, message).Scan(&state, &failure, &hidden); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || failure != reason || !hidden {
				t.Fatalf("quota warning did not settle terminally: state=%s reason=%s hidden=%v", state, failure, hidden)
			}
			if n, err := inbox.NewSweeper(appPool).SweepTenant(t.Context(), f.admin.TenantID); err != nil || n != 0 {
				t.Fatalf("terminal warning replay: failed=%d error=%v", n, err)
			}
			if n := scalar(t, f.admin, `SELECT count(*) FROM events WHERE type='inbox.receipt_failed' AND after->>'message_id'='`+message+`'`); n != 1 {
				t.Fatalf("failure must be audited once: %d", n)
			}
		})
	}
}
