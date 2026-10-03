// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestQuotaWarningRestartHeartbeatRetainsNotices(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-restart", now)
	project, run := insertProjectRun(t, f.admin, f.runner, f.profile, "Restart project")
	lead := quotaSession(t, f, project, "", nil, "Lead")
	quotaSession(t, f, project, run, &lead, "Worker")
	fact := quotaFact(t, f, 95, now, now.Add(time.Hour))
	quotaReport(t, f, fact, now)
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); n != 1 {
		t.Fatalf("fixture needs one delivered early notice: %d", n)
	}
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/settings/quota-warnings", `{"early_percent":10,"urgent_percent":6}`, 200, nil)
	path := "/api/agent-accounts/" + f.account.ID
	var check AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("quota-old-generation", 0), 202, &check)
	if check.DaemonGeneration == nil || *check.DaemonGeneration != "g1" {
		t.Fatalf("fixture must bind the pending check to g1: %+v", check)
	}
	later := fixedClockModule{Module: New(appPool), at: now.Add(time.Second)}
	// The rejected old check claims recovery. Only the already accepted fresh
	// measurement may drive the newly configured urgent notice.
	rejectedFact := quotaFact(t, f, 20, later.at, now.Add(time.Hour))
	probe := probeWrite{DaemonID: f.account.DaemonID, DaemonGeneration: "g2", Available: true, Readiness: &ReadinessReport{CheckID: check.ID, BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{rejectedFact}}}
	mux := http.NewServeMux()
	later.Mount(mux)
	r := httptest.NewRequest("POST", path+"/probe", strings.NewReader(encoded(t, probe)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.runner))
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var rejection struct{ Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &rejection); err != nil {
		t.Fatal(err)
	}
	if w.Code != 409 || rejection.Code != "stale_binding" || w.Header().Get("X-Aeon-Write-Committed") != "true" {
		t.Fatalf("restart must honestly reject the stale check after committing: %d %s %s", w.Code, w.Header().Get("X-Aeon-Write-Committed"), w.Body.String())
	}
	if scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_daemon_generation='g2' AND last_probe_at=$2 AND last_probe_ok`, f.account.ID, later.at) != 1 {
		t.Fatal("restart heartbeat was not committed")
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='invalidated' AND result IS NULL`, check.ID) != 1 || scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='5h' AND used_percent=95`, fact.ResourceID) != 1 {
		t.Fatal("stale check facts replaced the accepted measurement")
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE severity='urgent' AND threshold_percent=6 AND NOT suppressed AND recovered_at IS NULL`); n != 1 {
		t.Fatalf("committed restart skipped the urgent notice from accepted fresh evidence: %d", n)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages m JOIN inbox_receipts r ON r.tenant_id=m.tenant_id AND r.message_id=m.id WHERE m.idempotency_key LIKE 'quota-warning/%' AND r.state='queued'`); n != 2 {
		t.Fatalf("both notices need durable inbox acceptance: %d", n)
	}
}

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
