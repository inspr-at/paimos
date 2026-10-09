// SPDX-License-Identifier: AGPL-3.0-only
package views

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: nested daily writes bypass revisions/ownership, legacy dial saves erase
// the controls, or malformed stored settings turn into a permissive snapshot.
func TestAgentsDailyWritesRevisionLegacyPreservationAndFailClosed(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var tid string
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('daily-writes','Daily writes') RETURNING id::text`).Scan(&tid))
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, tid).Scan(&owner.ID))
	dbtest.BindRole(t, d, tid, owner.ID, "admin")
	alias := owner
	must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,linked_to) VALUES($1,'person','Alias',$2) RETURNING id::text`, tid, owner.ID).Scan(&alias.ID))
	dbtest.BindRole(t, d, tid, alias.ID, "admin")
	var now time.Time
	must(d.Admin.QueryRow(ctx, `SELECT now()`).Scan(&now))
	_, end, err := agentplan.LocalDay(now, "UTC")
	must(err)
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	request := func(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(tenant.WithPrincipal(ctx, p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	save := func(p tenant.Principal, value any, revision any, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"value": value, "expected_updated_at": revision})
		must(err)
		w := request(p, "PUT", "/api/preferences/agents.working", string(raw))
		if w.Code != status {
			t.Fatalf("save %d want %d: %s", w.Code, status, w.Body.String())
		}
		return w
	}
	read := func() agentplan.Snapshot {
		t.Helper()
		w := request(owner, "GET", "/api/agents/plan", "")
		if w.Code != 200 {
			t.Fatalf("read %d: %s", w.Code, w.Body.String())
		}
		var s agentplan.Snapshot
		must(json.Unmarshal(w.Body.Bytes(), &s))
		return s
	}
	dsettings := agentplan.DefaultDaily()
	dsettings.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 45, EnteredAs: "left", Until: end}
	plan := agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"codex": dsettings}}
	first := save(alias, plan, nil, 200)
	var pref preference
	must(json.Unmarshal(first.Body.Bytes(), &pref))
	state := read()
	if !agentplan.SameDaily(state.Daily["codex"], dsettings) || state.PrincipalID != owner.ID {
		t.Fatal("canonical daily save lost")
	}
	save(owner, agentplan.Plan{Total: 0, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{}}, nil, 409)
	if !agentplan.SameDaily(read().Daily["codex"], dsettings) {
		t.Fatal("stale save changed daily")
	}
	save(owner, map[string]any{"cap": 6, "view": "area"}, pref.UpdatedAt, 200)
	latest := read()
	if latest.Total != 6 || !agentplan.SameDaily(latest.Daily["codex"], dsettings) {
		t.Fatal("legacy save erased daily")
	}
	bad := dsettings
	bad.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 60, EnteredAs: "used", Until: end.Add(time.Hour)}
	save(owner, agentplan.Plan{Total: 6, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"codex": bad}}, latest.UpdatedAt, 400)
	if !read().UpdatedAt.Equal(*latest.UpdatedAt) {
		t.Fatal("invalid expiry advanced revision")
	}
	agent := alias
	agent.Kind = tenant.Agent
	agent.KeyCreatorID = owner.ID
	agent.Scopes = []string{agentplan.ReadScope}
	save(agent, plan, latest.UpdatedAt, 403)
	limited := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Only plan writer') RETURNING id::text`, tid).Scan(&limited.ID))
	var role string
	must(d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'daily_plan_only','Only plan') RETURNING id::text`, tid).Scan(&role))
	_, err = d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'views.write'),($1,$2,'agents.plan.read')`, tid, role)
	must(err)
	_, err = d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tid, limited.ID, role)
	must(err)
	save(limited, plan, nil, 403)
	withoutBoost := agentplan.DefaultDaily()
	save(limited, agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"codex": withoutBoost}}, nil, 200)
	// Malformed persisted daily fails closed, rather than applying defaults.
	_, err = d.Admin.Exec(ctx, `UPDATE user_preferences SET value='{"total":5,"daily":{"codex":{"pace":{"mode":"pace","points_per_day":0},"boost_today":null,"at_limit":"ladder"}}}' WHERE tenant_id=$1 AND principal_id=$2 AND key='agents.working'`, tid, owner.ID)
	must(err)
	if w := request(owner, "GET", "/api/agents/plan", ""); w.Code != 500 || !strings.Contains(w.Body.String(), "plan read failed") {
		t.Fatalf("corrupt plan: %d %s", w.Code, w.Body.String())
	}
}

// Risk: a weekly percent that falls inside one reset aborts GET /api/agents/plan.
func TestAgentsPlanStays200WhenWeeklyPercentDeclines(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var tid string
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('daily-decline','Daily decline') RETURNING id::text`).Scan(&tid))
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, tid).Scan(&owner.ID))
	dbtest.BindRole(t, d, tid, owner.ID, "admin")
	var agent string
	must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Runner') RETURNING id::text`, tid).Scan(&agent))
	var now time.Time
	must(d.Admin.QueryRow(ctx, `SELECT now()`).Scan(&now))
	zone, start, _ := dailyZoneWithRoom(t, now, 2*time.Hour, 30*time.Minute)
	_, err := d.Admin.Exec(ctx, `INSERT INTO personal_profiles(tenant_id,principal_id,timezone) VALUES($1,$2,$3)`, tid, owner.ID, zone)
	must(err)
	weeklyReset := now.Add(5 * 24 * time.Hour)
	baselineAt := start.Add(time.Minute)
	var account string
	must(d.Admin.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,label,registered_by_principal_id,owner_person_id,linked_at,last_probe_at,last_probe_ok,last_daemon_generation,usage_floor_percent)
		VALUES($1,'down','codex','daily-daemon','Down',$2,$3,$4,$5,true,'daily-generation',20) RETURNING id::text`, tid, agent, owner.ID, start.Add(-time.Hour), now).Scan(&account))
	_, err = d.Admin.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source)
		VALUES($1,$2,'weekly','',10080,10,$3,$4,'harness'),($1,$2,'weekly','',10080,60,$3,$5,'harness'),($1,$2,'weekly','',10080,40,$3,$6,'harness')`, tid, account, weeklyReset, start.Add(-time.Minute), baselineAt, now)
	must(err)
	_, err = d.Admin.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_kind,capacity_bucket,capacity_read_at,capacity_source)
		VALUES($1,$2,$3,$4,'percent',100,40,'unrestricted','weekly','',$5,'harness')`, tid, account, weeklyReset.Add(-10080*time.Minute), weeklyReset, now)
	must(err)
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/agents/plan", nil)
	r = r.WithContext(tenant.WithPrincipal(ctx, owner))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		var direct error
		_ = db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			_, direct = agentaccounts.ReadPlanTx(ctx, tx, owner)
			return nil
		})
		t.Fatalf("plan read %d (direct %v): %s", w.Code, direct, w.Body.String())
	}
	var snap agentplan.Snapshot
	must(json.Unmarshal(w.Body.Bytes(), &snap))
	if len(snap.DailyState["codex"].Accounts) != 1 {
		t.Fatalf("declining account missing: %+v", snap.DailyState)
	}
	down := snap.DailyState["codex"].Accounts[0]
	if down.UsedPct == nil || down.LeftPct == nil || down.StartOfDayUsedPct == nil || down.LimitUsedPct == nil || *down.UsedPct != 40 || *down.LeftPct != 60 || *down.StartOfDayUsedPct != 60 || *down.LimitUsedPct != 70 {
		t.Fatalf("declining weekly reading lost: %+v", down)
	}
	if snap.DailyState["codex"].TodayPointsUsed == nil || *snap.DailyState["codex"].TodayPointsUsed != 0 {
		t.Fatalf("today consumption not clamped: %+v", snap.DailyState["codex"])
	}
}

// Risk: a timezone change makes a still-active boost miss the new midnight and
// the plan read fails instead of keeping that boost until its stored instant.
func TestAgentsPlanKeepsFutureBoostAfterTimezoneChange(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var tid string
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('daily-boost-zone','Daily boost zone') RETURNING id::text`).Scan(&tid))
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, tid).Scan(&owner.ID))
	dbtest.BindRole(t, d, tid, owner.ID, "admin")
	var now time.Time
	must(d.Admin.QueryRow(ctx, `SELECT now()`).Scan(&now))
	zone, _, end := dailyZoneWithRoom(t, now, 0, 30*time.Minute)
	_, err := d.Admin.Exec(ctx, `INSERT INTO personal_profiles(tenant_id,principal_id,timezone) VALUES($1,$2,$3)`, tid, owner.ID, zone)
	must(err)
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(tenant.WithPrincipal(ctx, owner))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	settings := agentplan.DefaultDaily()
	settings.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 80, EnteredAs: "used", Until: end}
	raw, err := json.Marshal(map[string]any{"value": agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"codex": settings}}, "expected_updated_at": nil})
	must(err)
	saved := request(http.MethodPut, "/api/preferences/agents.working", string(raw))
	if saved.Code != http.StatusOK {
		t.Fatalf("save boost %d: %s", saved.Code, saved.Body.String())
	}
	var pref preference
	must(json.Unmarshal(saved.Body.Bytes(), &pref))
	nextZone, nextEnd := "", time.Time{}
	for _, candidate := range []string{"UTC", "Europe/Vienna", "America/Los_Angeles", "America/New_York", "Asia/Tokyo", "Australia/Sydney", "Pacific/Auckland", "Pacific/Honolulu"} {
		if candidate == zone {
			continue
		}
		_, candidateEnd, err := agentplan.LocalDay(now, candidate)
		if err != nil || candidateEnd.Equal(end) || !now.Before(end) {
			continue
		}
		nextZone, nextEnd = candidate, candidateEnd
		break
	}
	if nextZone == "" || nextEnd.Equal(end) {
		t.Fatal("no timezone moves local midnight")
	}
	_, err = d.Admin.Exec(ctx, `UPDATE personal_profiles SET timezone=$3 WHERE tenant_id=$1 AND principal_id=$2`, tid, owner.ID, nextZone)
	must(err)
	read := request(http.MethodGet, "/api/agents/plan", "")
	if read.Code != http.StatusOK {
		t.Fatalf("plan read %d: %s", read.Code, read.Body.String())
	}
	var snap agentplan.Snapshot
	must(json.Unmarshal(read.Body.Bytes(), &snap))
	kept := snap.Daily["codex"].BoostToday
	if kept == nil || !kept.Until.Equal(end) || kept.LimitUsedPct != 80 {
		t.Fatalf("future boost cleared after timezone change: %+v", snap.Daily["codex"])
	}
	shifted := settings
	shifted.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 70, EnteredAs: "used", Until: end}
	raw, err = json.Marshal(map[string]any{"value": agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"codex": shifted}}, "expected_updated_at": pref.UpdatedAt})
	must(err)
	rejected := request(http.MethodPut, "/api/preferences/agents.working", string(raw))
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("write accepted a boost that misses the new midnight: %d %s", rejected.Code, rejected.Body.String())
	}
	again := request(http.MethodGet, "/api/agents/plan", "")
	if again.Code != http.StatusOK {
		t.Fatalf("plan read after rejected write %d: %s", again.Code, again.Body.String())
	}
	must(json.Unmarshal(again.Body.Bytes(), &snap))
	if snap.Daily["codex"].BoostToday == nil || !snap.Daily["codex"].BoostToday.Until.Equal(end) {
		t.Fatal("rejected boost write changed the stored boost")
	}
}

func dailyZoneWithRoom(t *testing.T, now time.Time, sinceStart, untilEnd time.Duration) (string, time.Time, time.Time) {
	t.Helper()
	for _, zone := range []string{"UTC", "Europe/Vienna", "America/Los_Angeles", "America/New_York", "Asia/Tokyo", "Australia/Sydney", "Pacific/Auckland", "Pacific/Honolulu"} {
		start, end, err := agentplan.LocalDay(now, zone)
		if err != nil {
			continue
		}
		if now.Sub(start) >= sinceStart && end.Sub(now) >= untilEnd {
			return zone, start, end
		}
	}
	t.Fatal("no timezone leaves room in the local day")
	return "", time.Time{}, time.Time{}
}
