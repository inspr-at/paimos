// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestQuotaWarningThresholdFreshness(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	used := 90.0
	fact := ReadinessFact{ReadinessFactWrite: ReadinessFactWrite{WindowKey: "5h", Source: "harness", ReadingAt: &now, ResetsAt: &reset, UsedPercent: &used}}
	settings := QuotaWarningSettings{10, 3}
	for _, tc := range []struct {
		used float64
		want string
	}{{89.99, ""}, {90, "early"}, {96.99, "early"}, {97, "urgent"}, {100, "urgent"}} {
		fact.UsedPercent = &tc.used
		level, left, fresh := quotaWarningLevel(fact, settings, now)
		if level != tc.want || !fresh || left != 100-tc.used {
			t.Fatalf("%.2f: %s %f %v", tc.used, level, left, fresh)
		}
	}
	fact.UsedPercent = &used
	for _, tc := range []struct {
		name   string
		change func(*ReadinessFact)
	}{
		{"missing", func(f *ReadinessFact) { f.UsedPercent = nil }},
		{"estimated", func(f *ReadinessFact) { f.Source = "estimate" }},
		{"stale", func(f *ReadinessFact) { at := now.Add(-10*time.Minute - time.Nanosecond); f.ReadingAt = &at }},
		{"future", func(f *ReadinessFact) { at := now.Add(time.Nanosecond); f.ReadingAt = &at }},
		{"reset passed", func(f *ReadinessFact) { f.ResetsAt = &now }},
		{"no reading", func(f *ReadinessFact) { f.ReadingAt = nil }},
		{"failed", func(f *ReadinessFact) { f.ReadingError = "timeout" }},
		{"invalid", func(f *ReadinessFact) { v := math.NaN(); f.UsedPercent = &v }},
		{"unknown balance", func(f *ReadinessFact) { f.UsedPercent = nil; v := 2.0; f.Remaining = &v }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fact
			tc.change(&f)
			if level, _, fresh := quotaWarningLevel(f, settings, now); fresh || level != "" {
				t.Fatalf("unqualified measurement warned: %s %v", level, fresh)
			}
		})
	}
	at := now.Add(-10 * time.Minute)
	fact.ReadingAt = &at
	if level, _, fresh := quotaWarningLevel(fact, settings, now); !fresh || level != "early" {
		t.Fatal("freshness boundary must follow readiness package A")
	}
	if level, _, _ := quotaWarningLevel(fact, QuotaWarningSettings{25, 12}, now); level != "urgent" {
		t.Fatal("custom thresholds ignored")
	}
}

func TestQuotaWarningSettingsValidation(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-settings", now)
	var settings QuotaWarningSettings
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/settings/quota-warnings", "", 200, &settings)
	if settings != (QuotaWarningSettings{10, 3}) {
		t.Fatalf("defaults %+v", settings)
	}
	for _, body := range []string{`{}`, `{"early_percent":10}`, `{"early_percent":10,"urgent_percent":10}`, `{"early_percent":10,"urgent_percent":11}`, `{"early_percent":51,"urgent_percent":3}`, `{"early_percent":10,"urgent_percent":0}`, `{"early_percent":10.5,"urgent_percent":3}`, `{"early_percent":10,"urgent_percent":3,"extra":true}`} {
		callStatus(t, f.mod, &f.admin, "", "PUT", "/api/settings/quota-warnings", body, 400, nil)
	}
	callStatus(t, f.mod, &f.runner, f.token, "PUT", "/api/settings/quota-warnings", `{"early_percent":20,"urgent_percent":5}`, 403, nil)
	member := addPrincipal(t, f.admin.TenantID, "person", "Member", []string{"member"})
	callStatus(t, f.mod, &member, "", "PUT", "/api/settings/quota-warnings", `{"early_percent":20,"urgent_percent":5}`, 403, nil)
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/settings/quota-warnings", `{"early_percent":20,"urgent_percent":5}`, 200, &settings)
	if settings != (QuotaWarningSettings{20, 5}) {
		t.Fatal("save did not return new thresholds")
	}
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/settings/quota-warnings", "", 200, &settings)
	if settings != (QuotaWarningSettings{20, 5}) {
		t.Fatal("save was not durable")
	}
}

func quotaFact(t *testing.T, f limitFixture, used float64, at, reset time.Time) ReadinessFactWrite {
	t.Helper()
	var resource string
	if err := adminPool.QueryRow(t.Context(), `SELECT resource_id::text FROM account_readiness_memberships WHERE account_id=$1 ORDER BY resource_id LIMIT 1`, f.account.ID).Scan(&resource); err != nil {
		t.Fatal(err)
	}
	return ReadinessFactWrite{ResourceID: resource, WindowKey: "5h", Source: "harness", ObservedAt: at, ReadingAt: &at, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown", StopKind: "none"}
}
func quotaReport(t *testing.T, f limitFixture, fact ReadinessFactWrite, now time.Time) {
	t.Helper()
	mod := fixedClockModule{Module: New(appPool), at: now}
	callStatus(t, mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: f.account.DaemonID, DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}}), 200, nil)
}

func TestQuotaWarningDedupeRecoveryAndReset(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-dedupe", now)
	reset := now.Add(time.Hour)
	fact := quotaFact(t, f, 98, now, reset)
	quotaReport(t, f, fact, now)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE NOT suppressed`); n != 1 {
		t.Fatalf("crossing both must warn only urgent: %d", n)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE suppressed AND severity='early'`); n != 1 {
		t.Fatalf("early must retain suppressed receipt: %d", n)
	}
	for step := 1; step <= 3; step++ {
		at := now.Add(time.Duration(step) * time.Second)
		fact = quotaFact(t, f, 99, at, reset)
		quotaReport(t, f, fact, at)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings`); n != 2 {
		t.Fatalf("reminders duplicated receipts: %d", n)
	}
	stale := now.Add(-11 * time.Minute)
	fact = quotaFact(t, f, 20, stale, reset)
	fact.ObservedAt = now.Add(4 * time.Second)
	quotaReport(t, f, fact, fact.ObservedAt)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE NOT suppressed AND recovered_at IS NULL`); n != 1 {
		t.Fatal("stale recovery cleared warning")
	}
	// Reset passing without a fresh reading cannot write recovery evidence.
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE NOT suppressed AND recovered_at IS NULL`); n != 1 {
		t.Fatal("timer passage changed durable receipt")
	}
	at := now.Add(5 * time.Second)
	fact = quotaFact(t, f, 80, at, reset)
	quotaReport(t, f, fact, at)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE recovered_at IS NULL`); n != 0 {
		t.Fatalf("fresh measured recovery did not clear: %d", n)
	}
	at = now.Add(6 * time.Second)
	fact = quotaFact(t, f, 98, at, reset)
	quotaReport(t, f, fact, at)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE NOT suppressed`); n != 1 {
		t.Fatal("recovery erased deduplication history")
	}
	at = reset.Add(time.Second)
	nextReset := reset.Add(time.Hour)
	fact = quotaFact(t, f, 98, at, nextReset)
	quotaReport(t, f, fact, at)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE NOT suppressed`); n != 2 {
		t.Fatalf("new reset did not get independent warning: %d", n)
	}
}

func quotaSession(t *testing.T, f limitFixture, project, run string, parent *string, name string) string {
	t.Helper()
	var id string
	role := "worker"
	if parent == nil {
		role = "coordinator"
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		if run != "" {
			if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2 WHERE id=$1`, run, f.account.ID); err != nil {
				return err
			}
		}
		return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,parent_id,harness,host,management,role,ref_digest,lease_digest,display_label,phase) VALUES($1,$2,$3,$4,$5,'codex','fixture','unmanaged',$6,$7,$7,$8,'working') RETURNING id::text`, f.admin.TenantID, project, f.runner.ID, nullableUUID(run), parent, role, []byte(name), name).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestQuotaWarningsSharedComputersPrivacyAndProjectInbox(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-shared", now)
	project, run := insertProjectRun(t, f.admin, f.runner, f.profile, "First project")
	lead := quotaSession(t, f, project, "", nil, "Lead A")
	child := quotaSession(t, f, project, run, &lead, "Worker A")
	otherProject, otherRun := insertProjectRun(t, f.admin, f.runner, f.profile, "Second project")
	otherLead := quotaSession(t, f, otherProject, "", nil, "Lead B")
	quotaSession(t, f, otherProject, otherRun, &otherLead, "Worker B")
	// The two enrollments report against one confirmed resource, with different
	// account and daemon identifiers. Reporter IDs are never receipt identity.
	var sibling Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"second","harness":"codex","daemon_id":"daemon-b","label":"Second"}`, 201, &sibling)
	fact := quotaFact(t, f, 92, now, now.Add(time.Hour))
	if _, err := adminPool.Exec(t.Context(), `INSERT INTO account_readiness_memberships(tenant_id,account_id,resource_id,binding_revision) VALUES($1,$2,$3,0)`, f.admin.TenantID, sibling.ID, fact.ResourceID); err != nil {
		t.Fatal(err)
	}
	quotaReport(t, f, fact, now)
	at := now.Add(time.Second)
	fact.ObservedAt = at
	fact.ReadingAt = &at
	second := f
	second.account = sibling
	quotaReport(t, second, fact, at)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE NOT suppressed`); n != 1 {
		t.Fatalf("two computers duplicated warning: %d", n)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); n != 2 {
		t.Fatalf("expected one notice per project lead: %d", n)
	}
	rows, err := adminPool.Query(t.Context(), `SELECT recipient_session_id::text,body FROM inbox_messages WHERE tenant_id=$1 AND idempotency_key LIKE 'quota-warning/%'`, f.admin.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var recipient, body string
		if err := rows.Scan(&recipient, &body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, "%") || strings.Contains(body, "urgent") || recipient == lead && (!strings.Contains(body, "Worker A") || strings.Contains(body, "Worker B")) || recipient == otherLead && (!strings.Contains(body, "Worker B") || strings.Contains(body, "Worker A")) {
			t.Fatalf("private or cross-project summary: %q", body)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	// A sibling with no owner cannot disclose pooled quota to the first owner.
	var page struct {
		Items     []map[string]any `json:"items"`
		NextAfter *string          `json:"next_after"`
	}
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/quota-warnings?limit=1", "", 200, &page)
	if len(page.Items) != 1 || page.NextAfter == nil || page.Items[0]["details_redacted"] != true || page.Items[0]["remaining_percent"] != nil || page.Items[0]["severity"] != nil {
		t.Fatalf("shared sibling privacy or pagination: %+v", page)
	}
	ownFixtureAccount(t, f.admin, &sibling)
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	if len(page.Items) != 2 || page.NextAfter != nil || page.Items[0]["remaining_percent"] != float64(8) {
		t.Fatalf("owner warning detail: %+v", page)
	}
	peer := addPrincipal(t, f.admin.TenantID, "person", "Peer", []string{"admin"})
	callStatus(t, f.mod, &peer, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "remaining_percent") || strings.Contains(string(raw), "threshold_percent") || strings.Contains(string(raw), "severity") {
		t.Fatalf("admin bypassed owner privacy: %s", raw)
	}
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=true WHERE id=ANY($1::uuid[])`, []string{f.account.ID, sibling.ID}); err != nil {
		t.Fatal(err)
	}
	callStatus(t, f.mod, &peer, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	if len(page.Items) != 2 || page.Items[0]["remaining_percent"] != float64(8) {
		t.Fatal("explicit sharing failed")
	}
	staleMod := fixedClockModule{Module: New(appPool), at: now.Add(11 * time.Minute)}
	callStatus(t, staleMod, &f.admin, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("stale warning detail still shown")
	}
	_ = child
}
