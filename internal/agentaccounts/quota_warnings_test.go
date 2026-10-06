// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	var expired struct {
		Items []QuotaWarningSession `json:"items"`
	}
	callStatus(t, fixedClockModule{Module: New(appPool), at: reset}, &f.admin, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &expired)
	if len(expired.Items) != 0 {
		t.Fatal("reset-passed readings must be withheld")
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
	f.mod = fixedClockModule{Module: New(appPool), at: at}
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
	page.Items = nil
	page.NextAfter = nil // A redacted decode must not retain fields from an earlier owner response.
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/quota-warnings?limit=1", "", 200, &page)
	if len(page.Items) != 1 || page.NextAfter == nil || page.Items[0]["details_redacted"] != true || page.Items[0]["remaining_percent"] != nil || page.Items[0]["severity"] != nil {
		t.Fatalf("shared sibling privacy or pagination: %+v", page)
	}
	ownFixtureAccount(t, f.admin, &sibling)
	page.Items = nil
	page.NextAfter = nil // A redacted decode must not retain fields from an earlier owner response.
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	if len(page.Items) != 2 || page.NextAfter != nil || page.Items[0]["remaining_percent"] != float64(8) {
		t.Fatalf("owner warning detail: %+v", page)
	}
	peer := addPrincipal(t, f.admin.TenantID, "person", "Peer", []string{"admin"})
	page.Items = nil
	page.NextAfter = nil // A redacted decode must not retain fields from an earlier owner response.
	callStatus(t, f.mod, &peer, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "remaining_percent") || strings.Contains(string(raw), "threshold_percent") || strings.Contains(string(raw), "severity") {
		t.Fatalf("admin bypassed owner privacy: %s", raw)
	}
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=true WHERE id=ANY($1::uuid[])`, []string{f.account.ID, sibling.ID}); err != nil {
		t.Fatal(err)
	}
	page.Items = nil
	page.NextAfter = nil // A redacted decode must not retain fields from an earlier owner response.
	callStatus(t, f.mod, &peer, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	if len(page.Items) != 2 || page.Items[0]["remaining_percent"] != float64(8) {
		t.Fatal("explicit sharing failed")
	}
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/settings/quota-warnings", `{"early_percent":5,"urgent_percent":1}`, 200, nil)
	page.Items = nil
	page.NextAfter = nil
	callStatus(t, f.mod, &peer, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("warning projection ignored current workspace thresholds")
	}
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/settings/quota-warnings", `{"early_percent":10,"urgent_percent":3}`, 200, nil)
	staleMod := fixedClockModule{Module: New(appPool), at: now.Add(11 * time.Minute)}
	page.Items = nil
	page.NextAfter = nil // A redacted decode must not retain fields from an earlier owner response.
	callStatus(t, staleMod, &f.admin, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("stale warning detail still shown")
	}
	limited := addPrincipal(t, f.admin.TenantID, "person", "Limited reader", nil)
	seed(t, f.admin, func(tx pgx.Tx) error {
		var workspace, reader string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'quota_reader','Quota reader') RETURNING id::text`, f.admin.TenantID).Scan(&workspace); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM roles WHERE key='member'`).Scan(&reader); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'account.read')`, f.admin.TenantID, workspace); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'workspace',NULL),($1,$2,$4,'project',$5)`, f.admin.TenantID, limited.ID, workspace, reader, project)
		return err
	})
	page.Items = nil
	page.NextAfter = nil // A redacted decode must not retain fields from an earlier owner response.
	callStatus(t, f.mod, &limited, "", "GET", "/api/agent-accounts/quota-warnings", "", 200, &page)
	if len(page.Items) != 1 || page.Items[0]["session_id"] != child {
		t.Fatalf("warning escaped project visibility: %+v", page)
	}
}

func TestQuotaWarningsConfirmedPoolSurvivesComputerChanges(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-pool-stable", now)
	var sibling Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"second","harness":"codex","daemon_id":"daemon-b","label":"Second"}`, 201, &sibling)
	fingerprint := strings.Repeat("a", 64)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET quota_pool_fingerprint=$2,quota_fingerprint=$2 WHERE id=ANY($1::uuid[])`, []string{f.account.ID, sibling.ID}, fingerprint); err != nil {
		t.Fatal(err)
	}
	reset := now.Add(time.Hour)
	f.report(t, 92, now, reset)
	second := f
	second.account = sibling
	second.mod = fixedClockModule{Module: New(appPool), at: now.Add(time.Second)}
	second.report(t, 98, now.Add(time.Second), reset)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE NOT suppressed`); n != 2 {
		t.Fatalf("early then urgent across computers: %d", n)
	}
	// Removing the original reporter does not replace the receipt identity.
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET quota_pool_fingerprint='' WHERE id=$1`, f.account.ID); err != nil {
		t.Fatal(err)
	}
	second.mod = fixedClockModule{Module: New(appPool), at: now.Add(2 * time.Second)}
	second.report(t, 98, now.Add(2*time.Second), reset)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings`); n != 2 {
		t.Fatalf("computer change duplicated pool receipts: %d", n)
	}
}

func TestQuotaWarningsReporterWithoutProjectAccess(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-scoped-reporter", now)
	project, run := insertProjectRun(t, f.admin, f.runner, f.profile, "Private project")
	lead := quotaSession(t, f, project, "", nil, "Lead")
	quotaSession(t, f, project, run, &lead, "Private worker")
	leadPrincipal := addPrincipal(t, f.admin.TenantID, "agent", "Lead with access", []string{"admin"})
	dbtest.BindRole(t, testDB, f.admin.TenantID, leadPrincipal.ID, "admin")
	otherProject, otherRun := insertProjectRun(t, f.admin, f.runner, f.profile, "Revoked project")
	revokedLead := quotaSession(t, f, otherProject, "", nil, "Revoked lead")
	quotaSession(t, f, otherProject, otherRun, &revokedLead, "Revoked worker")
	revokedPrincipal := addPrincipal(t, f.admin.TenantID, "agent", "Lead without access", nil)
	seed(t, f.admin, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET agent_principal_id=$2 WHERE id=$1`, lead, leadPrincipal.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET agent_principal_id=$2 WHERE id=$1`, revokedLead, revokedPrincipal.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.runner.ID); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'quota_reporter','Quota reporter') RETURNING id::text`, f.admin.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'account.probe')`, f.admin.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.admin.TenantID, f.runner.ID, role)
		return err
	})
	fact := quotaFact(t, f, 98, now, now.Add(time.Hour))
	code, raw := call(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: f.account.DaemonID, DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}}))
	if code != 200 || strings.Contains(string(raw), "Private worker") || strings.Contains(string(raw), "Private project") {
		t.Fatalf("scoped reporter response status %d must exclude project details", code)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); n != 1 {
		t.Fatalf("expected one authorized lead notice, got %d", n)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE recipient_session_id='`+revokedLead+`'`); n != 0 {
		t.Fatal("lead without current project access received a notice")
	}
	// Check the same transaction after both service steps, rather than starting
	// a new transaction that would reset visibility and conceal a leak.
	ctx := tenant.WithPrincipal(t.Context(), f.runner)
	err := (&Module{pool: appPool}).inReadinessWrite(ctx, f.runner, func(tx pgx.Tx) error {
		assertScoped := func() {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM harness_sessions`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("system notice step leaked project visibility to reporter")
			}
		}
		assertScoped()
		a, err := getAccount(ctx, tx, f.account.ID)
		if err != nil {
			return err
		}
		notices, err := prepareQuotaWarnings(ctx, tx, f.runner, a, now)
		if err != nil {
			return err
		}
		if len(notices) != 1 || notices[0].session != lead || !strings.Contains(notices[0].body, "Private worker") || strings.Contains(notices[0].body, "Revoked worker") {
			t.Fatalf("internal notice targets: %+v", notices)
		}
		assertScoped()
		system, err := quotaSystemActor(ctx, tx, f.admin.TenantID, true)
		if err != nil {
			return err
		}
		if err := flushQuotaNotices(ctx, tx, system, notices); err != nil {
			return err
		}
		assertScoped()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Pause the first report at tenant entry and observe the second report waiting
// on that same tenant fence before either can finish.
type quotaFenceTrace struct {
	first             atomic.Bool
	acquired, started chan uint32
	release           chan struct{}
}
type quotaFenceContext struct{}

func (q *quotaFenceTrace) TraceQueryStart(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	switch data.SQL {
	case `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`:
		q.started <- c.PgConn().PID()
		return context.WithValue(ctx, quotaFenceContext{}, !q.first.Swap(true))
	}
	return ctx
}
func (q *quotaFenceTrace) TraceQueryEnd(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryEndData) {
	hold, _ := ctx.Value(quotaFenceContext{}).(bool)
	if hold && data.Err == nil {
		q.acquired <- c.PgConn().PID()
		select {
		case <-q.release:
		case <-ctx.Done():
		}
	}
}
func TestQuotaWarningsConcurrentComputersDeduplicate(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-concurrent", now)
	var sibling Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"second","harness":"codex","daemon_id":"daemon-b","label":"Second"}`, 201, &sibling)
	fact := quotaFact(t, f, 98, now, now.Add(time.Hour))
	if _, err := adminPool.Exec(t.Context(), `INSERT INTO account_readiness_memberships(tenant_id,account_id,resource_id,binding_revision) VALUES($1,$2,$3,0)`, f.admin.TenantID, sibling.ID, fact.ResourceID); err != nil {
		t.Fatal(err)
	}
	trace := &quotaFenceTrace{acquired: make(chan uint32, 1), started: make(chan uint32, 2), release: make(chan struct{})}
	config := appPool.Config()
	config.ConnConfig.Tracer = trace
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer func() {
		select {
		case <-trace.release:
		default:
			close(trace.release)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	mod := fixedClockModule{Module: New(pool), at: now}
	mux := http.NewServeMux()
	mod.Mount(mux)
	report := func(accountID, body string) int {
		r := httptest.NewRequest("POST", "/api/agent-accounts/"+accountID+"/probe", strings.NewReader(body)).WithContext(tenant.WithPrincipal(ctx, f.runner))
		r.Header.Set("Authorization", "Bearer "+f.token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	firstBody := encoded(t, probeWrite{DaemonID: f.account.DaemonID, DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}})
	secondBody := encoded(t, probeWrite{DaemonID: sibling.DaemonID, DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}})
	results := make(chan int, 2)
	go func() {
		results <- report(f.account.ID, firstBody)
	}()
	var firstPID uint32
	select {
	case firstPID = <-trace.acquired:
	case <-ctx.Done():
		t.Fatal("first report did not acquire tenant fence")
	}
	dbtest.Await(t, ctx, trace.started)
	go func() {
		results <- report(sibling.ID, secondBody)
	}()
	var secondPID uint32
	select {
	case secondPID = <-trace.started:
	case <-ctx.Done():
		t.Fatal("second report did not reach tenant fence")
	}
	for {
		var blocked bool
		if err := adminPool.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, firstPID, secondPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		runtime.Gosched()
	}
	close(trace.release)
	for range 2 {
		select {
		case code := <-results:
			if code != 200 {
				t.Fatalf("concurrent report status %d", code)
			}
		case <-ctx.Done():
			t.Fatal("reports did not finish")
		}
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE NOT suppressed`); n != 1 {
		t.Fatalf("overlapping reports duplicated urgent warning: %d", n)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warnings WHERE suppressed`); n != 1 {
		t.Fatalf("overlapping reports duplicated suppressed early receipt: %d", n)
	}
}

func TestQuotaWarningSettingsUndoRejectsInterveningWrite(t *testing.T) {
	f := readinessWorld(t, "quota-undo", time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/settings/quota-warnings", `{"early_percent":20,"urgent_percent":5,"expected_early_percent":10,"expected_urgent_percent":3}`, 200, nil)
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/settings/quota-warnings", `{"early_percent":25,"urgent_percent":6}`, 200, nil)
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/settings/quota-warnings", `{"early_percent":10,"urgent_percent":3,"expected_early_percent":20,"expected_urgent_percent":5}`, 409, nil)
	var actual QuotaWarningSettings
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/settings/quota-warnings", "", 200, &actual)
	if actual != (QuotaWarningSettings{25, 6}) {
		t.Fatal("stale Undo overwrote another manager", actual)
	}
}
