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

	"github.com/inspr-at/paimos/internal/db"
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
	for _, path := range []string{"/api/agent-accounts", "/api/agent-accounts/capacity", "/api/agent-accounts/" + f.account.ID + "/readings", "/api/agent-accounts/capacity/next?harness=codex", "/api/agent-accounts/readiness"} {
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
