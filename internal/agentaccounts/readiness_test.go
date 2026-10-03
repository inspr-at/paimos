// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestReadinessProjectionUnknownAndHardFacts(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	input := ReadinessInput{AccountID: "opaque", Now: now}
	out := ProjectReadiness(input)
	if !out.CanTry || out.State != "unknown" || out.MeasuredUsage != nil || out.DisplayReason != UsageUnknownReason {
		t.Fatalf("unknown must not throttle or claim Ready: %+v", out)
	}
	stale := now.Add(-time.Hour)
	reset := now.Add(time.Hour)
	used := 100.0
	input.Facts = []ReadinessFact{{ReadinessFactWrite: ReadinessFactWrite{ReadingAt: &stale, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown", StopKind: "named_reset"}}}
	out = ProjectReadiness(input)
	if out.CanTry || out.MeasuredUsage != nil || !slices.Contains(out.ReasonCodes, "quota_exhausted") {
		t.Fatalf("stale 100%% without denial still blocks: %+v", out)
	}
	input.Now = reset
	if out = ProjectReadiness(input); !out.CanTry {
		t.Fatalf("own reset must expire quota: %+v", out)
	}
	zero := 0.0
	input.Now = now
	input.Facts = []ReadinessFact{{ReadinessFactWrite: ReadinessFactWrite{ReadingAt: &stale, Remaining: &zero, CreditState: "unknown"}}}
	if out = ProjectReadiness(input); out.CanTry || !slices.Contains(out.ReasonCodes, "key_cap_exhausted") {
		t.Fatalf("stale zero cap: %+v", out)
	}
	input.Facts = []ReadinessFact{{ReadinessFactWrite: ReadinessFactWrite{CreditState: "unknown", StopKind: "money_402"}, NextAttemptAt: &stale}}
	if out = ProjectReadiness(input); out.CanTry || out.DisplayReason != "money_exhausted" {
		t.Fatalf("backoff expiry never grants permission itself: %+v", out)
	}
	input.HardReasons = []string{"slots_occupied", "paused", "manual_limit"}
	input.Facts = append(input.Facts, ReadinessFact{ReadingError: "identity_mismatch"})
	out = ProjectReadiness(input)
	if !slices.Equal(out.ReasonCodes, []string{"paused", "identity_mismatch", "slots_occupied", "money_exhausted", "manual_limit", UsageUnknownReason}) {
		t.Fatalf("all reasons in stable precedence: %+v", out)
	}
	for _, result := range []string{"timeout", "protocol", "launch_failed", "unsupported"} {
		input.HardReasons = nil
		input.Facts = []ReadinessFact{{ReadingError: result}}
		if out = ProjectReadiness(input); !out.CanTry || out.State != "unknown" {
			t.Fatalf("measurement error is not sign-out: %+v", out)
		}
	}
}

func readinessWorld(t *testing.T, slug string, now time.Time) limitFixture {
	t.Helper()
	f := limitWorldAt(t, slug, 3, now)
	dbtest.BindRole(t, testDB, f.runner.TenantID, f.runner.ID, "admin")
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=$3 WHERE id=$1`, f.account.ID, f.admin.ID, now); err != nil {
		t.Fatal(err)
	}
	f.account.OwnerPersonID = &f.admin.ID
	return f
}

func requestBody(key string, revision int64) string {
	return encodedSimple(map[string]any{"binding_revision": revision, "idempotency_key": key})
}
func encodedSimple(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestReadinessCheckOwnerCoalescingGapAndAudit(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "readiness-check", now)
	path := "/api/agent-accounts/" + f.account.ID + "/check"
	other := addPrincipal(t, f.admin.TenantID, "person", "Other admin", []string{"admin"})
	callStatus(t, f.mod, &other, "", "POST", path, requestBody("other", 0), 403, nil)
	callStatus(t, f.mod, &f.runner, f.token, "POST", path, requestBody("agent", 0), 403, nil)
	callStatus(t, f.mod, &f.admin, "", "POST", path, requestBody("stale", 1), 409, nil)
	for _, body := range []string{`{"binding_revision":0}`, `{"binding_revision":0,"idempotency_key":"valid","vendor_diagnostics":"forbidden"}`, requestBody(strings.Repeat("a", 129), 0)} {
		callStatus(t, f.mod, &f.admin, "", "POST", path, body, 400, nil)
	}
	var first, retry, coalesced AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", path, requestBody("first", 0), 202, &first)
	callStatus(t, f.mod, &f.admin, "", "POST", path, requestBody("first", 0), 202, &retry)
	freshProcess := fixedClockModule{Module: New(appPool), at: now}
	callStatus(t, freshProcess, &f.admin, "", "POST", path, requestBody("second", 0), 202, &coalesced)
	if first.ID == "" || first.ID != retry.ID || first.ID != coalesced.ID || !first.RetryAt.Equal(now.Add(CheckGap)) {
		t.Fatalf("idempotent persisted check: %+v %+v %+v", first, retry, coalesced)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM events WHERE type='account.check_requested'`); n != 1 {
		t.Fatalf("accepted canonical check audit count %d", n)
	}
	var audit map[string]any
	var raw []byte
	if err := adminPool.QueryRow(t.Context(), `SELECT after FROM events WHERE tenant_id=$1 AND type='account.check_requested'`, f.admin.TenantID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit) != 4 || audit["account_id"] != f.account.ID || audit["check_id"] != first.ID || audit["actor_principal_id"] != f.admin.ID {
		t.Fatalf("value-free check audit: %s", raw)
	}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{CheckID: first.ID, BindingRevision: ptrRevision(0), Result: "timeout"}}), 200, nil)
	status, body := call(t, freshProcess, &f.admin, "", "POST", path, requestBody("third", 0))
	if status != 429 {
		t.Fatalf("persisted gap status %d: %s", status, body)
	}
	var gap struct {
		Code    string    `json:"code"`
		RetryAt time.Time `json:"retry_at"`
	}
	if err := json.Unmarshal(body, &gap); err != nil || gap.Code != "check_gap" || !gap.RetryAt.Equal(now.Add(CheckGap)) {
		t.Fatalf("honest retry: %s (%v)", body, err)
	}
	callStatus(t, freshProcess, &f.admin, "", "POST", path, requestBody("second", 0), 202, &retry)
	if retry.ID != first.ID || retry.State != "completed" {
		t.Fatalf("completed replay %+v", retry)
	}
	later := fixedClockModule{Module: New(appPool), at: now.Add(CheckGap)}
	var next AccountCheck
	callStatus(t, later, &f.admin, "", "POST", path, requestBody("third", 0), 202, &next)
	if next.ID == first.ID {
		t.Fatal("gap boundary must allow a new check")
	}
	// Revocation of management authority after HTTP authentication must be
	// observed inside the final write, even with the stale principal roles.
	if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.admin.TenantID, f.admin.ID); err != nil {
		t.Fatal(err)
	}
	callStatus(t, later, &f.admin, "", "POST", path, requestBody("fourth", 0), 403, nil)
}

func ptrRevision(n int64) *int64 { return &n }

func TestReadinessReportsBindingMembershipAndStopRetention(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "readiness-report", now)
	var resources []ReadinessResource
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.runner), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		var err error
		resources, err = ReadinessResources(t.Context(), tx, f.account)
		return err
	})
	if err != nil || len(resources) != 1 {
		t.Fatalf("local resource seed: %+v %v", resources, err)
	}
	resource := resources[0].ID
	path := "/api/agent-accounts/" + f.account.ID + "/probe"
	fact := ReadinessFactWrite{ResourceID: resource, WindowKey: "provider", Source: "provider", ObservedAt: now, CreditState: "exhausted", StopKind: "money_402", DenialReason: "money_exhausted"}
	report := ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}
	probe := probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &report}
	callStatus(t, f.mod, &f.runner, f.token, "POST", path, encoded(t, probe), 200, nil)
	var originalWait string
	if err := adminPool.QueryRow(t.Context(), `SELECT wait_id::text FROM account_readiness_facts WHERE resource_id=$1 AND window_key='provider'`, resource).Scan(&originalWait); err != nil || originalWait == "" {
		t.Fatalf("durable wait: %v", err)
	}
	fact.ObservedAt = now.Add(time.Second)
	fact.CreditState = "unknown"
	fact.StopKind = "none"
	fact.DenialReason = ""
	report.Facts = []ReadinessFactWrite{fact}
	later := fixedClockModule{Module: New(appPool), at: now.Add(time.Second)}
	callStatus(t, later, &f.runner, f.token, "POST", path, encoded(t, probe), 200, nil)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='provider' AND stop_kind='money_402' AND wait_id::text=$2 AND backoff_step=0 AND NOT early_recovery_used`, resource, originalWait); n != 1 {
		t.Fatal("null measurement erased stop or minted recovery")
	}
	positive := 50.0
	readAt := now.Add(2 * time.Second)
	fact.ObservedAt = readAt
	fact.ReadingAt = &readAt
	fact.Remaining = &positive
	fact.CreditState = "available"
	report.Facts = []ReadinessFactWrite{fact}
	later = fixedClockModule{Module: New(appPool), at: readAt}
	callStatus(t, later, &f.runner, f.token, "POST", path, encoded(t, probe), 200, nil)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='provider' AND stop_kind='money_402'`, resource); n != 1 {
		t.Fatal("check inferred replenishment without recovery inference")
	}
	report.BindingRevision = ptrRevision(1)
	callStatus(t, later, &f.runner, f.token, "POST", path, encoded(t, probe), 409, nil)
	report.BindingRevision = ptrRevision(0)
	report.Facts[0].ResourceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	callStatus(t, later, &f.runner, f.token, "POST", path, encoded(t, probe), 403, nil)
	report.Facts = nil
	report.Result = "vendor diagnostic text"
	callStatus(t, later, &f.runner, f.token, "POST", path, encoded(t, probe), 400, nil)
	report.Result = "identity_mismatch"
	callStatus(t, later, &f.runner, f.token, "POST", path, encoded(t, probe), 200, nil)
	var page struct {
		Items []AccountReadiness `json:"items"`
	}
	callStatus(t, later, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &page)
	if len(page.Items) != 1 || page.Items[0].CanTry || !slices.Contains(page.Items[0].ReasonCodes, "identity_mismatch") || !slices.Contains(page.Items[0].ReasonCodes, "money_exhausted") {
		t.Fatalf("all hard facts: %+v", page.Items)
	}
}

func TestReadinessCheckGenerationAndRelinkInvalidate(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "readiness-invalidate", now)
	path := "/api/agent-accounts/" + f.account.ID
	var c AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("first", 0), 202, &c)
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g2","available":true}`, 200, nil)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='invalidated'`, c.ID); n != 1 {
		t.Fatal("restart retained pending generation")
	}
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g2", Available: true, Readiness: &ReadinessReport{CheckID: c.ID, BindingRevision: ptrRevision(0), Result: "success"}}), 409, nil)
	later := fixedClockModule{Module: New(appPool), at: now.Add(CheckGap)}
	callStatus(t, later, &f.admin, "", "POST", path+"/check", requestBody("new-generation", 0), 202, &c)
	callStatus(t, later, &f.admin, "", "PUT", path+"/sharing", `{"binding_revision":0,"share_usage":true}`, 204, nil)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=NULL,linked_at=NULL,link_revision=link_revision+1 WHERE id=$1`, f.account.ID); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='invalidated'`, c.ID); n != 1 {
		t.Fatal("unlink retained pending check")
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND share_usage`, f.account.ID); n != 0 {
		t.Fatal("sharing survived ownership change")
	}
}

func TestReadinessPrivacyAcrossHTTPRolesAndSharing(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "readiness-privacy", now)
	f.report(t, 41, now, now.Add(24*time.Hour))
	teammate := addPrincipal(t, f.admin.TenantID, "person", "Other admin", []string{"admin"})
	nonownerAgent := addPrincipal(t, f.admin.TenantID, "agent", "Other agent", []string{"admin"})
	for _, path := range []string{"/api/agent-accounts", "/api/agent-accounts/capacity", "/api/agent-accounts/" + f.account.ID + "/readings", "/api/agent-accounts/capacity/next?harness=codex", "/api/agent-accounts/readiness", "/api/agent-accounts/catalog", "/api/agent-accounts/use?harness=codex&account_id=" + f.account.ID} {
		t.Run(path, func(t *testing.T) {
			code, body := call(t, f.mod, &teammate, "", "GET", path, "")
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			for _, private := range []string{`"used_percent"`, `"resets_at"`, `"probe_failure"`, `"cap_percent"`, `"limiting_reset"`, `"reading_error"`, `"spend_month_usd"`} {
				if strings.Contains(string(body), private) {
					t.Fatalf("teammate got %s: %s", private, body)
				}
			}
			if !strings.HasSuffix(path, "/readings") && !strings.Contains(string(body), f.account.ID) {
				t.Fatalf("availability lost: %s", body)
			}
		})
	}
	code, preview := call(t, f.mod, &teammate, "", "POST", "/api/agent-accounts/capacity/preview", encoded(t, map[string]any{"schedule": capacity.DefaultSchedule("Europe/Vienna")}))
	if code != 200 || strings.Contains(string(preview), `"used_percent"`) || strings.Contains(string(preview), `"resets_at"`) {
		t.Fatalf("preview privacy: %d %s", code, preview)
	}

	ownStatus, ownBody := call(t, f.mod, &f.runner, f.token, "GET", "/api/agent-accounts/"+f.account.ID+"/readings", "")
	if ownStatus != 200 || !strings.Contains(string(ownBody), `"used_percent":41`) {
		t.Fatalf("scoped enrolling daemon lost readings: %s", ownBody)
	}
	callStatus(t, f.mod, &nonownerAgent, issueKey(t, nonownerAgent, []string{"account.probe"}), "GET", "/api/agent-accounts/"+f.account.ID+"/readings", "", 403, nil)
	callStatus(t, f.mod, &teammate, "", "PUT", "/api/agent-accounts/"+f.account.ID+"/sharing", `{"binding_revision":0,"share_usage":true}`, 403, nil)
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/agent-accounts/"+f.account.ID+"/sharing", `{"binding_revision":1,"share_usage":true}`, 409, nil)
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/agent-accounts/"+f.account.ID+"/sharing", `{"binding_revision":0,"share_usage":true}`, 204, nil)
	_, body := call(t, f.mod, &teammate, "", "GET", "/api/agent-accounts/"+f.account.ID+"/readings", "")
	if !strings.Contains(string(body), `"used_percent":41`) {
		t.Fatalf("owner opt-in not respected: %s", body)
	}
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/agent-accounts/"+f.account.ID+"/sharing", `{"binding_revision":0,"share_usage":false}`, 204, nil)
	_, body = call(t, f.mod, &teammate, "", "GET", "/api/agent-accounts/"+f.account.ID+"/readings", "")
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("sharing withdrawal leaked history: %s", body)
	}
}

func TestReadinessRoutesAuthenticateAndBoundQuery(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	for method, path := range map[string]string{"GET": "/api/agent-accounts/readiness", "POST": "/api/agent-accounts/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/check", "PUT": "/api/agent-accounts/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/sharing"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != 401 {
			t.Fatalf("%s unauthenticated: %d", path, w.Code)
		}
	}
	f := readinessWorld(t, "readiness-query", time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	for _, query := range []string{"?limit=101", "?limit=0", "?limit=word", "?after=bad"} {
		callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness"+query, "", 400, nil)
	}
	var page struct {
		Items     []AccountReadiness `json:"items"`
		NextAfter *string            `json:"next_after"`
	}
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness?limit=1", "", 200, &page)
	if len(page.Items) != 1 || !page.Items[0].CanTry || page.Items[0].State != "unknown" || page.Items[0].DisplayReason != UsageUnknownReason {
		t.Fatalf("unknown account readiness: %+v", page.Items)
	}
}

func TestReadinessWaitSnapshotAndNewWaitReset(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "readiness-waits", now)
	var resource string
	if err := adminPool.QueryRow(t.Context(), `SELECT resource_id::text FROM account_readiness_memberships WHERE account_id=$1`, f.account.ID).Scan(&resource); err != nil {
		t.Fatal(err)
	}
	fact := ReadinessFactWrite{ResourceID: resource, WindowKey: "vendor", Source: "harness", ObservedAt: now, CreditState: "unknown", StopKind: "unnamed", DenialReason: "vendor_denied"}
	report := ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}
	probe := probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &report}
	path := "/api/agent-accounts/" + f.account.ID
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, nil)
	var c AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("wait-click", 0), 202, &c)
	if !c.EarlyRecoveryRequested || scalar(t, f.admin, `SELECT count(*) FROM account_readiness_check_waits s JOIN account_readiness_facts f USING(tenant_id,resource_id,window_key,wait_id) WHERE s.check_id=$1`, c.ID) != 1 {
		t.Fatal("click was not bound to the current wait")
	}
	// Simulate B consuming the marker; a repeated observation of this same
	// stop must not create another early allowance or reset its durable backoff.
	if _, err := adminPool.Exec(t.Context(), `UPDATE account_readiness_facts SET early_recovery_used=true,backoff_step=2 WHERE resource_id=$1 AND window_key='vendor'`, resource); err != nil {
		t.Fatal(err)
	}
	fact.ObservedAt = now.Add(time.Second)
	report.Facts = []ReadinessFactWrite{fact}
	later := fixedClockModule{Module: New(appPool), at: fact.ObservedAt}
	callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, nil)
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_check_waits s JOIN account_readiness_facts f USING(tenant_id,resource_id,window_key,wait_id) WHERE s.check_id=$1 AND f.early_recovery_used AND f.backoff_step=2`, c.ID) != 1 {
		t.Fatal("repeated denial renewed a consumed wait")
	}
	used := 20.0
	at := now.Add(2 * time.Second)
	fact.ObservedAt = at
	fact.ReadingAt = &at
	fact.UsedPercent = &used
	fact.StopKind = "none"
	fact.DenialReason = ""
	report.Facts = []ReadinessFactWrite{fact}
	later = fixedClockModule{Module: New(appPool), at: at}
	callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, nil)
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor' AND stop_kind='none' AND wait_id IS NULL AND next_attempt_at IS NULL AND NOT early_recovery_used AND backoff_step=0`, resource) != 1 {
		t.Fatal("newer same-window room did not clear/reset quota wait")
	}
	fact.ObservedAt = now.Add(3 * time.Second)
	fact.ReadingAt = nil
	fact.UsedPercent = nil
	fact.StopKind = "unnamed"
	fact.DenialReason = "vendor_denied"
	report.Facts = []ReadinessFactWrite{fact}
	later = fixedClockModule{Module: New(appPool), at: fact.ObservedAt}
	callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, nil)
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_check_waits s JOIN account_readiness_facts f USING(tenant_id,resource_id,window_key,wait_id) WHERE s.check_id=$1`, c.ID) != 0 {
		t.Fatal("old click authorized a later stop")
	}
	var replay AccountCheck
	callStatus(t, later, &f.admin, "", "POST", path+"/check", requestBody("wait-retry", 0), 202, &replay)
	if replay.ID != c.ID || scalar(t, f.admin, `SELECT count(*) FROM account_readiness_check_waits WHERE check_id=$1`, c.ID) != 1 {
		t.Fatal("coalesced retry changed the wait snapshot")
	}
}

func TestReadinessDaemonPollScopesAndResourcePrivacy(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "readiness-poll", now)
	f.runner.Scopes = []string{"account.read", "account.probe"}
	token := issueKey(t, f.runner, f.runner.Scopes)
	var check AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("poll", 0), 202, &check)
	var accounts []Account
	callStatus(t, f.mod, &f.runner, token, "GET", "/api/agent-accounts?include_checks=true", "", 200, &accounts)
	if len(accounts) != 1 || accounts[0].PendingCheck == nil || accounts[0].PendingCheck.ID != check.ID || len(accounts[0].ReadinessResources) != 1 {
		t.Fatalf("scoped poll: %+v", accounts)
	}
	accounts = nil
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts?include_checks=true", "", 200, &accounts)
	if accounts[0].PendingCheck != nil || len(accounts[0].ReadinessResources) != 0 {
		t.Fatal("person received daemon-only poll fields")
	}
	callStatus(t, f.mod, &f.runner, issueKey(t, f.runner, []string{"account.read"}), "GET", "/api/agent-accounts?include_checks=true", "", 403, nil)
	other := addPrincipal(t, f.admin.TenantID, "agent", "Other daemon", []string{"admin"})
	dbtest.BindRole(t, testDB, other.TenantID, other.ID, "admin")
	other.Scopes = []string{"account.read", "account.probe"}
	callStatus(t, f.mod, &other, issueKey(t, other, []string{"account.read", "account.probe"}), "GET", "/api/agent-accounts?include_checks=true", "", 200, &accounts)
	if len(accounts) != 0 {
		t.Fatal("another daemon received the check")
	}
	// Two accounts consuming one resource do not disclose a private owner's
	// quota merely because the other account is owned by the caller.
	peer := addPrincipal(t, f.admin.TenantID, "person", "Private owner", []string{"admin"})
	var sibling Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"sibling","harness":"codex","daemon_id":"daemon-a","label":"Other"}`, 201, &sibling)
	ownFixtureAccount(t, peer, &sibling)
	if _, err := adminPool.Exec(t.Context(), `INSERT INTO account_readiness_memberships(tenant_id,account_id,resource_id,binding_revision) SELECT tenant_id,$2,resource_id,0 FROM account_readiness_memberships WHERE account_id=$1`, f.account.ID, sibling.ID); err != nil {
		t.Fatal(err)
	}
	load := func() bool {
		var allowed bool
		err := db.InTenant(t.Context(), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
			policy, err := accountprivacy.Load(t.Context(), tx, f.admin, []string{f.account.ID})
			allowed = policy[f.account.ID]
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return allowed
	}
	if load() {
		t.Fatal("shared resource bypassed other owner's privacy")
	}
	callStatus(t, f.mod, &peer, "", "PUT", "/api/agent-accounts/"+sibling.ID+"/sharing", `{"binding_revision":0,"share_usage":true}`, 204, nil)
	if !load() {
		t.Fatal("shared owner opt-in was not recognized")
	}
	// Current role/key revocation is checked within the report transaction.
	if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.runner.TenantID, f.runner.ID); err != nil {
		t.Fatal(err)
	}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{CheckID: check.ID, BindingRevision: ptrRevision(0), Result: "success"}}), 403, nil)
}

func TestReadinessPairingRevocationInvalidatesPending(t *testing.T) {
	for _, scope := range []string{"consent", "computer"} {
		t.Run(scope, func(t *testing.T) {
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			f := readinessWorld(t, "readiness-revoke-"+scope, now)
			if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET harness='claude',max_parallel_runs=1 WHERE id=$1`, f.account.ID); err != nil {
				t.Fatal(err)
			}
			f.account.Harness = "claude"
			pairClaudeAccount(t, f.admin, f.runner, f.account)
			if _, err := adminPool.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET ongoing_approved_at=$2 WHERE account_id=$1`, f.account.ID, now); err != nil {
				t.Fatal(err)
			}
			path := "/api/agent-accounts/" + f.account.ID
			var c AccountCheck
			callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("paired", 0), 202, &c)
			callStatus(t, f.mod, &f.admin, "", "PUT", path+"/sharing", `{"binding_revision":0,"share_usage":true}`, 204, nil)
			query := `UPDATE agent_pairing_enrollments SET ongoing_approved_at=NULL WHERE account_id=$1`
			status := 409
			if scope == "computer" {
				query = `UPDATE agent_pairing_computers SET state='revoked' WHERE id=(SELECT computer_id FROM agent_pairing_enrollments WHERE account_id=$1)`
				status = 410
			}
			if _, err := adminPool.Exec(t.Context(), query, f.account.ID); err != nil {
				t.Fatal(err)
			}
			if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='invalidated'`, c.ID) != 1 {
				t.Fatal("revoked pairing retained pending check")
			}
			// Privacy controls remain available when execution consent is revoked.
			callStatus(t, f.mod, &f.admin, "", "PUT", path+"/sharing", `{"binding_revision":0,"share_usage":false}`, 204, nil)
			if scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND share_usage`, f.account.ID) != 0 {
				t.Fatal("revoked computer prevented privacy withdrawal")
			}
			callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("paired-retry", 0), status, nil)
			callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{CheckID: c.ID, BindingRevision: ptrRevision(0), Result: "success"}}), status, nil)
		})
	}
}

func TestFix2OwnerMixedSchedules(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "fix2-schedules", now)
	schedule := capacity.DefaultSchedule("Europe/Vienna")
	for _, scope := range []string{"user", "pool", "account"} {
		in := scheduleOverride{Scope: scope, Schedule: &schedule}
		if scope == "pool" {
			in.Pool = "codex"
		}
		if scope == "account" {
			in.AccountID = f.account.ID
		}
		callStatus(t, f.mod, &f.admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, in), 204, nil)
	}
	var rows []struct {
		Scope           string
		Schedule        capacity.Schedule
		DetailsRedacted bool `json:"details_redacted"`
	}
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/capacity/schedule", "", 200, &rows)
	if len(rows) != 3 {
		t.Fatalf("expected all three stored schedules: %+v", rows)
	}
	for _, row := range rows {
		if row.DetailsRedacted || encoded(t, row.Schedule) != encoded(t, schedule) {
			t.Fatalf("owner's %s schedule changed: %+v", row.Scope, row)
		}
	}
}

func TestFix2PooledDaemonOwnControlFields(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "fix2-pooled", now)
	other := addPrincipal(t, f.admin.TenantID, "agent", "Peer daemon", []string{"admin"})
	peer := addPrincipal(t, f.admin.TenantID, "person", "Peer owner", []string{"admin"})
	dbtest.BindRole(t, testDB, other.TenantID, other.ID, "admin")
	other.Scopes = []string{"account.read", "account.probe", "account.manage"}
	token := issueKey(t, other, other.Scopes)
	var sibling Account
	callStatus(t, f.mod, &other, token, "POST", "/api/agent-accounts", `{"account_key":"peer","harness":"codex","daemon_id":"peer-daemon","label":"Peer"}`, 201, &sibling)
	ownFixtureAccount(t, peer, &sibling)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=repeat('a',64),quota_pool_fingerprint=repeat('a',64) WHERE id=ANY($1::uuid[])`, []string{f.account.ID, sibling.ID}); err != nil {
		t.Fatal(err)
	}
	var check AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("pooled", 0), 202, &check)
	f.runner.Scopes = []string{"account.read", "account.probe"}
	var rows []struct {
		Account
		DetailsRedacted bool `json:"details_redacted"`
	}
	callStatus(t, f.mod, &f.runner, issueKey(t, f.runner, f.runner.Scopes), "GET", "/api/agent-accounts?include_checks=true", "", 200, &rows)
	if len(rows) != 1 || rows[0].PendingCheck == nil || rows[0].PendingCheck.ID != check.ID || len(rows[0].ReadinessResources) != 1 {
		t.Fatalf("daemon lost its own capture: %+v", rows)
	}
	if len(rows[0].Windows) != 0 || !rows[0].DetailsRedacted {
		t.Fatalf("pooled quota was disclosed: %+v", rows)
	}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{CheckID: check.ID, BindingRevision: ptrRevision(0), Result: "timeout"}}), 200, nil)
	var page struct {
		Items []AccountReadiness `json:"items"`
	}
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &page)
	for _, row := range page.Items {
		if row.AccountID == f.account.ID && (row.CheckResult != "timeout" || !row.DetailsRedacted || row.MeasuredUsage != nil) {
			t.Fatalf("owner's check result lost or quota disclosed: %+v", row)
		}
	}
}

func TestFix2RestartReadinessProbe(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "fix2-restart", now)
	path := "/api/agent-accounts/" + f.account.ID
	var check AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("old", 0), 202, &check)
	later := fixedClockModule{Module: New(appPool), at: now.Add(time.Minute)}
	report := ReadinessReport{BindingRevision: ptrRevision(0), Result: "unsupported"}
	probe := probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g2", Available: true, Readiness: &report}
	var account Account
	callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, &account)
	if account.LastProbeAt == nil || !account.LastProbeAt.Equal(later.at) {
		t.Fatalf("restart lost heartbeat: %+v", account)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='invalidated'`, check.ID) != 1 {
		t.Fatal("old-generation capture survived")
	}
	report.CheckID = check.ID
	callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 409, nil)
	report.CheckID = ""
	callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, nil)
	var fresh AccountCheck
	callStatus(t, later, &f.admin, "", "POST", path+"/check", requestBody("new", 0), 202, &fresh)
	report.CheckID = fresh.ID
	report.Result = "success"
	callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, nil)
}

func TestFix2PendingCheckExpiry(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "fix2-expiry", now)
	path := "/api/agent-accounts/" + f.account.ID
	var first, coalesced, fresh AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("first", 0), 202, &first)
	before := fixedClockModule{Module: New(appPool), at: now.Add(5*time.Minute - time.Microsecond)}
	callStatus(t, before, &f.admin, "", "POST", path+"/check", requestBody("before", 0), 202, &coalesced)
	if coalesced.ID != first.ID {
		t.Fatal("capture expired before TTL")
	}
	after := fixedClockModule{Module: New(appPool), at: now.Add(5 * time.Minute)}
	f.runner.Scopes = []string{"account.read", "account.probe"}
	var accounts []Account
	callStatus(t, after, &f.runner, issueKey(t, f.runner, f.runner.Scopes), "GET", "/api/agent-accounts?include_checks=true", "", 200, &accounts)
	if len(accounts) != 1 || accounts[0].PendingCheck != nil {
		t.Fatalf("expired capture still polled: %+v", accounts)
	}
	callStatus(t, after, &f.admin, "", "POST", path+"/check", requestBody("fresh", 0), 202, &fresh)
	if fresh.ID == first.ID || !fresh.RequestedAt.Equal(after.at) {
		t.Fatalf("expired capture coalesced forever: %+v", fresh)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='invalidated'`, first.ID) != 1 {
		t.Fatal("expiry not persisted")
	}
	callStatus(t, after, &f.admin, "", "POST", path+"/check", requestBody("first", 0), 409, nil)
	callStatus(t, after, &f.runner, f.token, "POST", path+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{CheckID: first.ID, BindingRevision: ptrRevision(0), Result: "success"}}), 409, nil)
}

func TestFix3RestartFirstReportCarriesOldCheck(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "fix3-restart", now)
	path := "/api/agent-accounts/" + f.account.ID
	var check AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("old", 0), 202, &check)
	if check.DaemonGeneration == nil || *check.DaemonGeneration != "g1" {
		t.Fatalf("fixture must bind the pending check to g1: %+v", check)
	}
	var resource string
	if err := adminPool.QueryRow(t.Context(), `SELECT resource_id::text FROM account_readiness_memberships WHERE account_id=$1`, f.account.ID).Scan(&resource); err != nil {
		t.Fatal(err)
	}
	later := fixedClockModule{Module: New(appPool), at: now.Add(time.Minute)}
	used := 37.0
	report := ReadinessReport{CheckID: check.ID, BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{{ResourceID: resource, WindowKey: "restart", Source: "harness", ObservedAt: later.at, ReadingAt: &later.at, UsedPercent: &used, CreditState: "unknown"}}}
	probe := probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g2", Available: true, Readiness: &report}
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
		t.Fatalf("stale completion must report its committed heartbeat: %d %s %s", w.Code, w.Header().Get("X-Aeon-Write-Committed"), w.Body.String())
	}
	var heartbeat time.Time
	var generation string
	var available bool
	if err := adminPool.QueryRow(t.Context(), `SELECT last_probe_at,last_daemon_generation,last_probe_ok FROM agent_accounts WHERE id=$1`, f.account.ID).Scan(&heartbeat, &generation, &available); err != nil {
		t.Fatal(err)
	}
	if !heartbeat.Equal(later.at) || generation != "g2" || !available {
		t.Fatalf("first restarted heartbeat rolled back: %v %s %t", heartbeat, generation, available)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='invalidated' AND result IS NULL`, check.ID) != 1 || scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1`, resource) != 0 {
		t.Fatal("stale completion or facts were retained")
	}
	f.runner.Scopes = []string{"account.read", "account.probe"}
	var accounts []Account
	callStatus(t, later, &f.runner, issueKey(t, f.runner, f.runner.Scopes), "GET", "/api/agent-accounts?include_checks=true", "", 200, &accounts)
	if len(accounts) != 1 || accounts[0].PendingCheck != nil {
		t.Fatalf("old-generation check still polled: %+v", accounts)
	}
	var fresh AccountCheck
	callStatus(t, later, &f.admin, "", "POST", path+"/check", requestBody("new", 0), 202, &fresh)
	if fresh.ID == check.ID || fresh.DaemonGeneration == nil || *fresh.DaemonGeneration != "g2" {
		t.Fatalf("fresh check did not bind the committed generation: %+v", fresh)
	}
	report.CheckID = fresh.ID
	callStatus(t, later, &f.runner, f.token, "POST", path+"/probe", encoded(t, probe), 200, nil)
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks WHERE id=$1 AND state='completed' AND result='success'`, fresh.ID) != 1 || scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='restart' AND used_percent=37`, resource) != 1 {
		t.Fatal("fresh completion or its measurement was lost")
	}
}

func TestFix3ExpiredPendingReceiptRejected(t *testing.T) {
	for _, operation := range []string{"replay", "completion"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			f := readinessWorld(t, "fix3-expiry-"+operation, now)
			path := "/api/agent-accounts/" + f.account.ID
			var check AccountCheck
			callStatus(t, f.mod, &f.admin, "", "POST", path+"/check", requestBody("first", 0), 202, &check)
			assertPending := func() {
				t.Helper()
				if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_checks c JOIN agent_accounts a ON a.id=c.account_id AND a.tenant_id=c.tenant_id WHERE c.id=$1 AND c.state='pending' AND c.binding_revision=a.link_revision AND c.daemon_generation='g1' AND a.last_daemon_generation='g1' AND c.result IS NULL`, check.ID) != 1 {
					t.Fatal("expiry fixture must remain pending with matching binding and generation")
				}
			}
			assertPending()
			if !check.ExpiresAt.Equal(now.Add(CheckTTL)) {
				t.Fatalf("expiry does not match original request: %+v", check)
			}
			after := fixedClockModule{Module: New(appPool), at: check.ExpiresAt}
			var rejection struct{ Code string }
			if operation == "replay" {
				callStatus(t, after, &f.admin, "", "POST", path+"/check", requestBody("first", 0), 409, &rejection)
			} else {
				callStatus(t, after, &f.runner, f.token, "POST", path+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{CheckID: check.ID, BindingRevision: ptrRevision(0), Result: "success"}}), 409, &rejection)
			}
			if rejection.Code != "stale_binding" {
				t.Fatalf("expiry failed for another reason: %+v", rejection)
			}
			assertPending()
			if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE reported_by_account_id=$1`, f.account.ID) != 0 {
				t.Fatal("expired receipt wrote capture facts")
			}
		})
	}
}

func TestFix2SharedResourceReadingErrorsStayPerAccount(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "fix2-errors", now)
	var sibling Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"sibling","harness":"codex","daemon_id":"daemon-a","label":"Sibling"}`, 201, &sibling)
	ownFixtureAccount(t, f.admin, &sibling)
	if _, err := adminPool.Exec(t.Context(), `INSERT INTO account_readiness_memberships(tenant_id,account_id,resource_id,binding_revision) SELECT tenant_id,$2,resource_id,0 FROM account_readiness_memberships WHERE account_id=$1`, f.account.ID, sibling.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/agent-accounts/" + f.account.ID + "/probe"
	report := ReadinessReport{BindingRevision: ptrRevision(0), Result: "identity_mismatch"}
	callStatus(t, f.mod, &f.runner, f.token, "POST", path, encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &report}), 200, nil)
	check := func(want string) {
		t.Helper()
		var page struct {
			Items []AccountReadiness `json:"items"`
		}
		callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &page)
		if len(page.Items) != 2 {
			t.Fatalf("missing shared accounts: %+v", page)
		}
		for _, row := range page.Items {
			if row.AccountID == sibling.ID && (slices.Contains(row.ReasonCodes, "identity_mismatch") || slices.Contains(row.ReasonCodes, "sign_in") || row.CheckResult == "identity_mismatch") {
				t.Fatalf("sibling inherited account error: %+v", row)
			}
			if row.AccountID == f.account.ID && row.CheckResult != want {
				t.Fatalf("own error overwritten by sibling: %+v", row)
			}
		}
	}
	check("identity_mismatch")
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+sibling.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success"}}), 200, nil)
	check("identity_mismatch")
}

func TestFix2LegacyReadinessFactsUseMembershipAndUnknownBalance(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "fix2-legacy", now)
	f.report(t, 41, now, now.Add(time.Hour))
	var facts []ReadinessFact
	err := db.InTenant(t.Context(), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		var err error
		facts, err = legacyReadinessFacts(t.Context(), tx, f.account, now)
		return err
	})
	if err != nil || len(facts) != 1 {
		t.Fatalf("legacy facts: %+v %v", facts, err)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_memberships WHERE account_id=$1 AND resource_id=$2 AND binding_revision=0`, f.account.ID, facts[0].ResourceID) != 1 {
		t.Fatalf("legacy fact invents membership: %+v", facts)
	}
}

func TestFix2KnownKeyCapKeepsTotalBalanceUnknown(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	remaining := 12.0
	input := ReadinessInput{AccountID: "opaque", Now: now, Facts: []ReadinessFact{{ReadinessFactWrite: ReadinessFactWrite{WindowKey: "key_cap", ObservedAt: now, ReadingAt: &now, Remaining: &remaining, CreditState: "unknown"}}}}
	out := ProjectReadiness(input)
	if !out.CanTry || out.State != "unknown" || !slices.Contains(out.ReasonCodes, UsageUnknownReason) || len(out.MeasuredUsage) != 1 {
		t.Fatalf("known key cap pretended to know total balance: %+v", out)
	}
}

func TestFix2OversizedMutationReportsCommittedOutcome(t *testing.T) {
	p := tenant.Principal{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", TenantID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Kind: tenant.Person}
	r := httptest.NewRequest("POST", "/api/agent-accounts", nil).WithContext(tenant.WithPrincipal(t.Context(), p))
	w := httptest.NewRecorder()
	committed := false
	(&Module{}).privateResponse(func(w http.ResponseWriter, r *http.Request) {
		committed = true
		w.WriteHeader(201)
		_, _ = w.Write([]byte(strings.Repeat("x", (4<<20)+1)))
	})(w, r)
	if !committed || w.Code != 503 || w.Header().Get("X-Aeon-Write-Committed") != "true" || !strings.Contains(w.Body.String(), "write committed") {
		t.Fatalf("committed mutation reported as failed: %d %s", w.Code, w.Body.String())
	}
}
