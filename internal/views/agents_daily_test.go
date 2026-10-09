// SPDX-License-Identifier: AGPL-3.0-only
package views

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
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
	save(owner, agentplan.Plan{Total: 0, Daily: map[string]agentplan.DailySettings{}}, nil, 409)
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
	save(owner, agentplan.Plan{Total: 6, Daily: map[string]agentplan.DailySettings{"codex": bad}}, latest.UpdatedAt, 400)
	if !read().UpdatedAt.Equal(*latest.UpdatedAt) {
		t.Fatal("invalid expiry advanced revision")
	}
	agent := alias
	agent.Kind = tenant.Agent
	agent.KeyCreatorID = owner.ID
	agent.Scopes = []string{agentplan.ReadScope}
	save(agent, plan, latest.UpdatedAt, 403)
	// Malformed persisted daily fails closed, rather than applying defaults.
	_, err = d.Admin.Exec(ctx, `UPDATE user_preferences SET value='{"total":5,"daily":{"codex":{"pace":{"mode":"pace","points_per_day":0},"boost_today":null,"at_limit":"ladder"}}}' WHERE tenant_id=$1 AND principal_id=$2 AND key='agents.working'`, tid, owner.ID)
	must(err)
	if w := request(owner, "GET", "/api/agents/plan", ""); w.Code != 500 || !strings.Contains(w.Body.String(), "plan read failed") {
		t.Fatalf("corrupt plan: %d %s", w.Code, w.Body.String())
	}
}
