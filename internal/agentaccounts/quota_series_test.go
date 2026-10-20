// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: periodic idle probes grow history/events, replay overwrites newer quota,
// or an inactive account loses its last real reading. Exercise the public probe
// and read handlers with an injected clock, both quota windows and tenant RLS.
func TestIdleQuotaProbeHourlyRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 5, 0, 0, time.UTC)
	f := readinessWorld(t, "idle-quota-series", now)
	f.mod = accountsMod() // Each callAt supplies its own clock after fixture setup.
	path := "/api/agent-accounts/" + f.account.ID
	var resource string
	seed(t, f.admin, func(tx pgx.Tx) error {
		var err error
		resource, err = localReadinessResource(t.Context(), tx, f.account)
		return err
	})
	query := "?from=2026-10-18T12:00:00Z&to=2026-10-18T15:00:00Z"
	read := func(p tenant.Principal, token, suffix string, at time.Time) quotaSeries {
		t.Helper()
		var out quotaSeries
		status, raw := callAt(t, f.mod, &p, token, "GET", path+"/quota-series"+query+suffix, "", at)
		if status != http.StatusOK || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("read series: %d %s", status, raw)
		}
		return out
	}
	if out := read(f.admin, "", "", now); len(out.LatestReadings) != 0 || len(out.HourlyReadings) != 0 {
		t.Fatal("missing quota was invented")
	}
	report := func(at time.Time, five, week float64) probeWrite {
		fiveReset, weekReset := now.Add(4*time.Hour), now.Add(5*24*time.Hour)
		return probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, MeasurementOnly: true,
			Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{
				{ResourceID: resource, WindowKey: "5h:codex", Source: "agentd", ObservedAt: at, ReadingAt: &at, ResetsAt: &fiveReset, UsedPercent: &five, CreditState: "unknown", StopKind: "none"},
				{ResourceID: resource, WindowKey: "weekly:codex", Source: "agentd", ObservedAt: at, ReadingAt: &at, ResetsAt: &weekReset, UsedPercent: &week, CreditState: "unknown", StopKind: "none"},
			}}}
	}
	post := func(in probeWrite, at time.Time) {
		t.Helper()
		status, raw := callAt(t, f.mod, &f.runner, f.token, "POST", path+"/probe", encoded(t, in), at)
		if status != http.StatusOK {
			t.Fatalf("probe: %d %s", status, raw)
		}
	}
	eventsBefore := scalar(t, f.admin, `SELECT count(*) FROM events WHERE type='account.probed'`)
	first := report(now, 10, 20)
	post(first, now)
	// AEON-1354 retains the initial readiness transition, then suppresses ticks.
	if n := scalar(t, f.admin, `SELECT count(*) FROM events WHERE type='account.probed'`); n != eventsBefore+1 {
		t.Fatalf("initial readiness transition appended %d events", n-eventsBefore)
	}
	eventsBefore++
	post(first, now) // Idempotent delivery.
	second := report(now.Add(5*time.Minute), 15, 25)
	post(second, now.Add(5*time.Minute))
	post(first, now.Add(6*time.Minute)) // Delayed delivery cannot regress either window.
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_hourly WHERE account_id=$1`, f.account.ID); n != 1 {
		t.Fatalf("per-probe quota rows: %d", n)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_capacity_readings WHERE account_id=$1`, f.account.ID); n != 0 {
		t.Fatalf("idle probes entered append-only reading history: %d", n)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM events WHERE type='account.probed'`); n != eventsBefore {
		t.Fatalf("measurement-only probes appended audit telemetry: %d -> %d", eventsBefore, n)
	}
	out := read(f.admin, "", "", now.Add(2*time.Hour))
	if len(out.LatestReadings) != 2 || len(out.HourlyReadings) != 1 || len(out.HourlyReadings[0].Readings) != 2 {
		t.Fatalf("lost quota windows: %+v", out)
	}
	for i, want := range []float64{15, 25} {
		v := out.LatestReadings[i]
		if v.UsedPercent != want || v.RemainingPercent != 100-want || !v.ReadAt.Equal(now.Add(5*time.Minute)) || v.ReadingAgeSeconds != 6900 || v.ResetsAt.IsZero() || out.HourlyReadings[0].Readings[i].UsedPercent != want {
			t.Fatalf("inactive account lost its original reading: %+v", v)
		}
	}
	// Sparse responses retain other windows. A same-time agentd observation
	// cannot displace the harness's higher-authority reading.
	tie := report(now.Add(5*time.Minute), 15, 26)
	tie.Readiness.Facts = tie.Readiness.Facts[1:]
	tie.Readiness.Facts[0].Source = "harness"
	post(tie, now.Add(10*time.Minute))
	post(report(now.Add(5*time.Minute), 15, 80), now.Add(11*time.Minute))
	out = read(f.admin, "", "", now.Add(12*time.Minute))
	if len(out.LatestReadings) != 2 || out.LatestReadings[0].UsedPercent != 15 || out.LatestReadings[1].UsedPercent != 26 || out.LatestReadings[1].Source != "harness" {
		t.Fatal("sparse response or source tie regressed latest quota")
	}
	post(report(now.Add(time.Hour), 30, 40), now.Add(time.Hour))
	post(report(now.Add(2*time.Hour), 35, 45), now.Add(2*time.Hour))
	page := read(f.runner, f.token, "&window_kind=weekly&limit=1", now.Add(2*time.Hour))
	if len(page.LatestReadings) != 1 || len(page.HourlyReadings) != 1 || len(page.HourlyReadings[0].Readings) != 1 || page.NextCursor != "2026-10-18T12:00:00Z" {
		t.Fatalf("unbounded or unfiltered first page: %+v", page)
	}
	page = read(f.runner, f.token, "&window_kind=weekly&limit=1&cursor="+url.QueryEscape(page.NextCursor), now.Add(2*time.Hour))
	if len(page.HourlyReadings) != 1 || page.HourlyReadings[0].HourAt.Hour() != 13 || page.NextCursor != "2026-10-18T13:00:00Z" {
		t.Fatalf("cursor repeated or skipped an hour: %+v", page)
	}
	page = read(f.admin, "", "&limit=1&cursor="+url.QueryEscape(page.NextCursor), now.Add(2*time.Hour))
	if len(page.HourlyReadings) != 1 || page.HourlyReadings[0].HourAt.Hour() != 14 || page.NextCursor != "" {
		t.Fatalf("last page: %+v", page)
	}
	// Separate enrolled accounts of the same harness keep separate snapshots.
	var sibling Account
	status, raw := callAt(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"secondary","harness":"codex","daemon_id":"daemon-a","label":"Main"}`, now)
	if status != 201 || json.Unmarshal(raw, &sibling) != nil {
		t.Fatalf("second enrollment: %d %s", status, raw)
	}
	ownFixtureAccount(t, f.admin, &sibling)
	siblingPath := "/api/agent-accounts/" + sibling.ID
	v := capacity.Reading{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 73, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "harness"}
	status, raw = callAt(t, f.mod, &f.runner, f.token, "POST", siblingPath+"/readings", encoded(t, readingsWrite{[]capacity.Reading{v}}), now)
	if status != 204 {
		t.Fatalf("second account reading: %d %s", status, raw)
	}
	var siblingQuota quotaSeries
	status, raw = callAt(t, f.mod, &f.admin, "", "GET", siblingPath+"/quota-series"+query, "", now)
	if status != 200 || json.Unmarshal(raw, &siblingQuota) != nil || len(siblingQuota.LatestReadings) != 1 || siblingQuota.LatestReadings[0].UsedPercent != 73 || len(siblingQuota.HourlyReadings) != 1 {
		t.Fatalf("second account lost its own quota: %d %s", status, raw)
	}
	if original := read(f.admin, "", "&window_kind=weekly", now.Add(2*time.Hour)); len(original.LatestReadings) != 1 || original.LatestReadings[0].UsedPercent != 45 {
		t.Fatal("a sibling replaced this account's quota")
	}
	// A different tenant cannot read or insert rows for this account through RLS.
	other := makePrincipal(t, "idle-quota-other", "person", "Other tenant", []string{"admin"})
	callStatus(t, f.mod, &other, "", "GET", path+"/quota-series"+query, "", 404, nil)
	if n := scalar(t, other, `SELECT count(*) FROM account_quota_hourly WHERE account_id=$1`, f.account.ID); n != 0 {
		t.Fatal("tenant RLS disclosed hourly data")
	}
	err := db.InTenant(t.Context(), appPool, other.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO account_quota_hourly(tenant_id,account_id,hour_at,binding_revision,samples) VALUES($1,$2,$3,0,'[]')`, f.admin.TenantID, f.account.ID, now.Truncate(time.Hour))
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("RLS insert failed for wrong reason: %v", err)
	}
	// Private measurements stay private even to a different workspace admin.
	peer := addPrincipal(t, f.admin.TenantID, "person", "Peer admin", []string{"admin"})
	if out := read(peer, "", "", now); len(out.LatestReadings) != 0 || len(out.HourlyReadings) != 0 || out.NextCursor != "" {
		t.Fatal("private quota disclosed")
	}
	// Relinking invalidates the latest snapshot and historical view together.
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET link_revision=link_revision+1 WHERE id=$1`, f.account.ID); err != nil {
		t.Fatal(err)
	}
	if out := read(f.admin, "", "", now); len(out.LatestReadings) != 0 || len(out.HourlyReadings) != 0 {
		t.Fatal("previous binding quota survived relink")
	}
}

// Risk: credential-shaped fields leak through error strings, events or stored
// samples; unauthorized readers or oversized ranges bypass API bounds.
func TestIdleQuotaRedactionAndReadBounds(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "idle-quota-redaction", now)
	path := "/api/agent-accounts/" + f.account.ID
	query := "?from=2026-10-18T12:00:00Z&to=2026-10-19T12:00:00Z"
	for _, q := range []string{"", "?from=bad&to=bad", "?from=2026-10-18T12:01:00Z&to=2026-10-19T12:00:00Z", "?from=2026-10-18T12:00:00Z&to=2026-12-19T12:00:00Z", query + "&limit=0", query + "&limit=169", query + "&limit=bad", query + "&window_kind=monthly", query + "&cursor=2026-10-17T12:00:00Z", query + "&cursor=" + strings.Repeat("x", 1024)} {
		callStatus(t, f.mod, &f.admin, "", "GET", path+"/quota-series"+q, "", 400, nil)
	}
	callStatus(t, f.mod, nil, "", "GET", path+"/quota-series"+query, "", 401, nil)
	noScope := issueKey(t, f.runner, []string{"account.read"})
	callStatus(t, f.mod, &f.runner, noScope, "GET", path+"/quota-series"+query, "", 403, nil)
	otherRunner := addPrincipal(t, f.admin.TenantID, "agent", "Other runner", []string{"admin"})
	otherToken := issueKey(t, otherRunner, []string{"account.probe"})
	callStatus(t, f.mod, &otherRunner, otherToken, "GET", path+"/quota-series"+query, "", 403, nil)
	secret := "sk-ant-synthetic-credential-must-not-escape-1499"
	var resource string
	seed(t, f.admin, func(tx pgx.Tx) error {
		var err error
		resource, err = localReadinessResource(t.Context(), tx, f.account)
		return err
	})
	used, reset := 25.0, now.Add(time.Hour)
	in := probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, MeasurementOnly: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{{ResourceID: resource, WindowKey: "weekly:" + secret, Source: "agentd", ObservedAt: now, ReadingAt: &now, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown"}}}}
	for _, body := range []string{encoded(t, in), strings.TrimSuffix(encoded(t, in), "}") + `,"access_token":"` + secret + `"}`} {
		status, raw := call(t, f.mod, &f.runner, f.token, "POST", path+"/probe", body)
		if status != 400 || strings.Contains(string(raw), secret) {
			t.Fatalf("credential rejection leaked data or had wrong cause: %d", status)
		}
	}
	var raw string
	if err := adminPool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(e)::text,'[]') FROM events e WHERE tenant_id=$1`, f.admin.TenantID).Scan(&raw); err != nil || strings.Contains(raw, secret) {
		t.Fatal("credential bytes reached audit history", err)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_quota_hourly WHERE account_id=$1`, f.account.ID) != 0 {
		t.Fatal("rejected credential-shaped sample was persisted")
	}
	// Run readings also populate the bounded series without storing estimates.
	v := capacity.Reading{WindowKind: "weekly", WindowMinutes: 10080, Bucket: "codex", UsedPercent: 22, ReadAt: now, ResetsAt: reset, Source: "harness"}
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", encoded(t, readingsWrite{[]capacity.Reading{v}}), 204, nil)
	var out quotaSeries
	callStatus(t, f.mod, &f.admin, "", "GET", path+"/quota-series"+query, "", 200, &out)
	if len(out.LatestReadings) != 1 || out.LatestReadings[0].UsedPercent != 22 || out.LatestReadings[0].Source != "harness" {
		t.Fatal("run observation failed to populate quota series")
	}
}
