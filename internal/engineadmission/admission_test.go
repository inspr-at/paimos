// SPDX-License-Identifier: AGPL-3.0-only
package engineadmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type prReaderFunc func(context.Context, string) (int, error)

func (f prReaderFunc) Count(ctx context.Context, tid string) (int, error) { return f(ctx, tid) }

type fixture struct {
	d                                  *dbtest.DB
	m                                  *Module
	mux                                *http.ServeMux
	person, agent, foreign             tenant.Principal
	project, account, computer, window string
	at                                 time.Time
	wip                                int
	wipErr                             error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), at: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), mux: http.NewServeMux()}
	for i, p := range []*tenant.Principal{&f.person, &f.foreign} {
		p.Kind = tenant.Person
		must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Admission') RETURNING id::text`, fmt.Sprintf("admission-%d", i)).Scan(&p.TenantID))
		must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, p.TenantID).Scan(&p.ID))
		dbtest.BindRole(t, f.d, p.TenantID, p.ID, "admin")
	}
	f.agent = tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Agent, KeyCreatorID: f.person.ID, Scopes: []string{"nodes.read", "engine.admission", "engine.read", "engine.manage", "agents.plan.read", "account.overview.read"}}
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Runtime') RETURNING id::text`, f.person.TenantID).Scan(&f.agent.ID))
	dbtest.BindRole(t, f.d, f.person.TenantID, f.agent.ID, "admin")
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'ADM-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.person.TenantID).Scan(&f.project))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,label,registered_by_principal_id,owner_person_id,capacity_owner,linked_at,last_probe_at,last_probe_ok,last_daemon_generation)
 VALUES($1,'fixture','codex','fixture-daemon','Fixture',$2,$3,$3,$4,$4,true,'fixture-generation') RETURNING id::text`, f.person.TenantID, f.agent.ID, f.person.ID, f.at).Scan(&f.account))
	var profile, key, request string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'admission-profile','1','codex','openai','fixture-model','high','standard') RETURNING id::text`, f.person.TenantID).Scan(&profile))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'Fixture',gen_random_uuid()::text,'fixture-not-a-credential',$3) RETURNING id::text`, f.person.TenantID, f.agent.ID, f.person.ID).Scan(&key))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by)
 VALUES($1,gen_random_uuid(),'887887887',repeat('a',64),repeat('b',64),repeat('c',64),'{}','fixture','redeemed',$2) RETURNING id::text`, f.person.TenantID, f.person.ID).Scan(&request))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,capacity_signals,capacity_reported_at)
 VALUES($1,gen_random_uuid(),$2,$3,$4,'fixture-daemon',repeat('c',64),'{"load":5,"cores":18,"memory_pressure":"normal","power":"plugged_in","thermal":"normal"}',$5) RETURNING id::text`, f.person.TenantID, request, f.agent.ID, key, f.at).Scan(&f.computer))
	f.exec(t, `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, f.person.TenantID, f.account, f.computer, request, profile, f.at.Add(time.Hour), f.at)
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source)
 VALUES($1,$2,$3,$4,'percent',100,20,'unrestricted',$3,true,'weekly','codex','harness') RETURNING id::text`, f.person.TenantID, f.account, f.at, f.at.Add(7*24*time.Hour)).Scan(&f.window))
	f.exec(t, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','codex',10080,20,$3,$4,'harness')`, f.person.TenantID, f.account, f.at.Add(7*24*time.Hour), f.at)
	s := capacity.DefaultSchedule()
	s.Override = "sprint"
	s.Reserve = "off"
	f.schedule(t, s)
	f.m = New(f.d.App, prReaderFunc(func(_ context.Context, tid string) (int, error) {
		if tid != f.person.TenantID {
			return 0, errors.New("foreign inventory")
		}
		return f.wip, f.wipErr
	}))
	f.m.now = func() time.Time { return f.at }
	f.m.Mount(f.mux)
	return f
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := f.d.Admin.Exec(t.Context(), q, args...)
	must(t, err)
}
func (f *fixture) schedule(t *testing.T, s capacity.Schedule) {
	t.Helper()
	raw, err := json.Marshal(s)
	must(t, err)
	f.exec(t, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3::uuid,$4) ON CONFLICT(tenant_id,principal_id,scope,scope_key) DO UPDATE SET schedule=EXCLUDED.schedule`, f.person.TenantID, f.person.ID, f.account, raw)
}
func call(ctx context.Context, mux *http.ServeMux, p tenant.Principal, method, path string, in any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func (f *fixture) call(t *testing.T, p tenant.Principal, method, path string, in any, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := call(t.Context(), f.mux, p, method, path, in)
	if w.Code != want {
		t.Fatalf("%s %s status=%d want=%d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}
func (f *fixture) request(id, kind string) Request {
	return Request{RequestID: id, Kind: kind, Harness: "codex", Project: f.project, Estimate: 1}
}
func (f *fixture) decide(t *testing.T, in Request, reason string) Decision {
	t.Helper()
	w := f.call(t, f.person, "POST", "/api/engine/admission", in, 200)
	var out Decision
	must(t, json.Unmarshal(w.Body.Bytes(), &out))
	if out.Reason != reason || out.Enforced || out.Allowed != (reason == "allowed") {
		t.Fatalf("wrong decision: %+v", out)
	}
	return out
}
func (f *fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	must(t, f.d.Admin.QueryRow(t.Context(), q, args...).Scan(&n))
	return n
}
func (f *fixture) enable(t *testing.T) {
	t.Helper()
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/admission-settings", map[string]any{"expected_revision": 0, "shadow_enabled": true}, 200)
}

// Risks: quota/limit errors, false launch authority, missing audit and tenant leaks.
func TestAdmissionShadowLimitsFloorsFreshnessAndTenantIsolation(t *testing.T) {
	f := newFixture(t)
	off := f.decide(t, f.request("off", "first_build"), "shadow_disabled")
	if off.Mode != "off" || off.RetryAfter != nil {
		t.Fatal("switch must default off")
	}
	f.enable(t)
	yes := true
	in := f.request("first", "first_build")
	in.ScriptAllowed = &yes
	allowed := f.decide(t, in, "allowed")
	if allowed.Mode != "shadow" || allowed.ScriptAgrees == nil || !*allowed.ScriptAgrees || allowed.RetryAfter != nil {
		t.Fatal("shadow comparison or allowed retry was incorrect")
	}
	if f.count(t, `SELECT count(*) FROM agent_runs`) > 0 || f.count(t, `SELECT count(*) FROM account_reservations`) > 0 {
		t.Fatal("shadow evaluation launched or reserved work")
	}
	for _, tc := range []struct {
		name, kind           string
		total, codex, claude int
		limit                agentplan.Limit
		reason               string
	}{
		{"reserve", "first_build", 4, 1, 1, agentplan.Limit{Mode: agentplan.NoLimit}, "finishing_reserve"},
		{"fix", "fix", 4, 1, 1, agentplan.Limit{Mode: agentplan.NoLimit}, ""},
		{"merge", "merge", 4, 1, 1, agentplan.Limit{Mode: agentplan.NoLimit}, ""},
		{"land", "land", 4, 1, 1, agentplan.Limit{Mode: agentplan.NoLimit}, ""},
		{"review", "review", 4, 1, 1, agentplan.Limit{Mode: agentplan.NoLimit}, ""},
		{"off", "fix", 0, 1, 1, agentplan.Limit{Mode: agentplan.Off}, "harness_off"},
		{"total", "fix", 2, 1, 1, agentplan.Limit{Mode: agentplan.NoLimit}, "plan_total"},
		{"harness", "fix", 8, 1, 3, agentplan.Limit{Mode: agentplan.AtMost, Value: 1}, "harness_limit"},
		{"other harness", "fix", 8, 1, 3, agentplan.Limit{Mode: agentplan.AtMost, Value: 2}, ""},
		{"zero harness", "fix", 8, 0, 0, agentplan.Limit{Mode: agentplan.AtMost, Value: 0}, "harness_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := agentplan.Snapshot{Plan: agentplan.Plan{Total: tc.total, Limits: map[string]agentplan.Limit{"codex": tc.limit}}, Running: map[string]int{"codex": tc.codex, "claude": tc.claude}}
			if got := planReason(f.request("table", tc.kind), s); got != tc.reason {
				t.Fatalf("got=%s want=%s", got, tc.reason)
			}
		})
	}
	f.wip = 9
	blocked := f.decide(t, f.request("wip", "first_build"), "wip_limit")
	if blocked.RetryAfter == nil {
		t.Fatal("missing retry")
	}
	f.decide(t, f.request("fix-wip", "fix"), "allowed")
	f.wip = 8
	f.decide(t, f.request("wip-boundary", "first_build"), "allowed")
	f.wipErr = errors.New("offline")
	f.decide(t, f.request("wip-unreadable", "first_build"), "wip_unreadable")
	f.wipErr = nil
	f.exec(t, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working','{"total":"unreadable"}')`, f.person.TenantID, f.person.ID)
	f.decide(t, f.request("plan-unreadable", "fix"), "daily_limit_unknown")
	f.exec(t, `UPDATE user_preferences SET value='{"total":3,"limits":{}}' WHERE principal_id=$1 AND key='agents.working'`, f.person.ID)
	f.exec(t, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,$4,'claude','fixture','unmanaged','worker',uuid_send(gen_random_uuid()),uuid_send(gen_random_uuid()))`, f.person.TenantID, f.project, f.agent.ID, f.person.ID)
	f.decide(t, f.request("live-reserve", "first_build"), "finishing_reserve")
	f.decide(t, f.request("live-fix", "fix"), "allowed")
	f.exec(t, `UPDATE agent_pairing_computers SET capacity_signals=jsonb_set(capacity_signals,'{load}','30') WHERE id=$1`, f.computer)
	f.decide(t, f.request("load", "fix"), "host_load")
	f.exec(t, `UPDATE agent_pairing_computers SET capacity_signals=jsonb_set(capacity_signals,'{load}','5'),capacity_reported_at=$2 WHERE id=$1`, f.computer, f.at.Add(-time.Minute-time.Nanosecond))
	f.decide(t, f.request("stale-host", "fix"), "host_inputs_unreadable")
	f.exec(t, `UPDATE agent_pairing_computers SET capacity_reported_at=$2 WHERE id=$1`, f.computer, f.at)
	// Age the retained post-link measurement with the injected clock. Moving
	// only the ledger timestamp before the link instead tests an old binding
	// and leaves no matching append-only percentage for the daily ceiling.
	measuredAt := f.at
	f.at = measuredAt.Add(11 * time.Minute)
	f.decide(t, f.request("stale-account", "fix"), "daily_limit_unknown")
	f.at = measuredAt
	// The current 20% measurement leaves exactly the person's 80% floor.
	// Retain its append-only reading and give pacing a prior-period baseline.
	f.exec(t, `UPDATE account_allowance_windows SET capacity_read_at=$2,starts_at=$3 WHERE id=$1`, f.window, f.at, f.at.Add(-24*time.Hour))
	s := capacity.DefaultSchedule()
	s.Reserve = "fixed"
	s.ReservePercent = 80
	s.Week = make([]capacity.Day, 7)
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	f.schedule(t, s)
	f.decide(t, f.request("floor", "fix"), "account_reserve")
	s.Override = "hold"
	f.schedule(t, s)
	f.decide(t, f.request("hold", "fix"), "account_hold")
	f.call(t, f.foreign, "GET", "/api/projects/"+f.project+"/admission-settings", nil, 404)
	f.call(t, f.foreign, "POST", "/api/engine/admission", f.request("foreign", "fix"), 404)
	must(t, db.InTenant(tenant.WithPrincipal(t.Context(), f.foreign), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM engine_admission_decisions`).Scan(&n)
		if err == nil && n != 0 {
			t.Fatal("RLS crossed tenants")
		}
		return err
	}))
	events := f.count(t, `SELECT count(*) FROM events WHERE type='engine.admission_decided'`)
	decisions := f.count(t, `SELECT count(*) FROM engine_admission_decisions`)
	if events != decisions {
		t.Fatalf("every decision must be an event: events=%d decisions=%d", events, decisions)
	}
	var snapshot string
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT after::text FROM events WHERE type='engine.admission_decided' ORDER BY id LIMIT 1`).Scan(&snapshot))
	for _, private := range []string{"owner_person_id", "capacity_signals", "windows", "quota_pool", "running_total"} {
		if strings.Contains(snapshot, private) {
			t.Fatal("private input published in decision event")
		}
	}
}

// Risks: implicit agent grants, stale permission after network reads and duplicate actions.
func TestAdmissionExplicitAuthorityFinalWriteAndIdempotentReplay(t *testing.T) {
	f := newFixture(t)
	f.enable(t)
	f.call(t, f.agent, "POST", "/api/engine/admission", f.request("default-agent", "first_build"), 403)
	f.call(t, f.agent, "PUT", "/api/projects/"+f.project+"/admission-settings", map[string]any{"expected_revision": 1, "shadow_enabled": false}, 403)
	var role string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'admission_observer','Admission observer') RETURNING id::text`, f.person.TenantID).Scan(&role))
	for _, permission := range []string{"nodes.read", "engine.admission", "engine.read", "agents.plan.read", "account.overview.read"} {
		f.exec(t, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.person.TenantID, role, permission)
	}
	f.exec(t, `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, f.agent.ID, role)
	f.call(t, f.agent, "POST", "/api/engine/admission", f.request("explicit-agent", "first_build"), 200)
	ownerless := f.agent
	ownerless.KeyCreatorID = ""
	f.call(t, ownerless, "POST", "/api/engine/admission", f.request("ownerless", "first_build"), 403)
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/admission-settings", map[string]any{"expected_revision": 0, "shadow_enabled": false}, 409)
	request := f.request("replay", "fix")
	first := f.decide(t, request, "allowed")
	before := f.count(t, `SELECT count(*) FROM events`)
	f.exec(t, `UPDATE agent_pairing_computers SET capacity_signals=jsonb_set(capacity_signals,'{load}','35') WHERE id=$1`, f.computer)
	second := f.decide(t, request, "allowed")
	if first.EvaluatedAt != second.EvaluatedAt || f.count(t, `SELECT count(*) FROM events`) != before {
		t.Fatal("replay changed its snapshot or duplicated the event")
	}
	request.Estimate = 2
	f.call(t, f.person, "POST", "/api/engine/admission", request, 409)
	f.exec(t, `UPDATE agent_pairing_computers SET capacity_signals=jsonb_set(capacity_signals,'{load}','5') WHERE id=$1`, f.computer)
	// Establish the exact interleaving: the external WIP read began after the
	// initial permission check, then authority is revoked before it returns.
	reached := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	f.m.prs = prReaderFunc(func(ctx context.Context, _ string) (int, error) {
		close(reached)
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- call(ctx, f.mux, f.agent, "POST", "/api/engine/admission", f.request("revoked", "first_build"))
	}()
	dbtest.Await(t, ctx, reached)
	f.exec(t, `DELETE FROM role_permissions WHERE role_id=$1 AND permission='engine.admission'`, role)
	close(release)
	w := dbtest.Await(t, ctx, done)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("stale permission accepted or wrong failure: %d %s", w.Code, w.Body.String())
	}
	if f.count(t, `SELECT count(*) FROM engine_admission_decisions WHERE request_id='revoked'`) != 0 {
		t.Fatal("revoked write persisted")
	}
	// A real DB barrier proves simultaneous duplicate evaluations serialize at
	// the tenant fence, even though shadow decisions reserve no execution slots.
	f.m.prs = prReaderFunc(func(context.Context, string) (int, error) { return 0, nil })
	pool, barrier, raceCtx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.HasPrefix(q, "INSERT INTO engine_admission_decisions") })
	racing := New(pool, f.m.prs)
	racing.now = f.m.now
	mux := http.NewServeMux()
	racing.Mount(mux)
	result1 := make(chan *httptest.ResponseRecorder, 1)
	result2 := make(chan *httptest.ResponseRecorder, 1)
	req := f.request("concurrent", "fix")
	go func() { result1 <- call(raceCtx, mux, f.person, "POST", "/api/engine/admission", req) }()
	pid := barrier.Wait(t, raceCtx)
	go func() { result2 <- call(raceCtx, mux, f.person, "POST", "/api/engine/admission", req) }()
	dbtest.WaitForLock(t, raceCtx, f.d, pid, "transactionid")
	barrier.Release()
	for _, ch := range []chan *httptest.ResponseRecorder{result1, result2} {
		if w := dbtest.Await(t, raceCtx, ch); w.Code != 200 {
			t.Fatalf("concurrent replay: %d %s", w.Code, w.Body.String())
		}
	}
	if f.count(t, `SELECT count(*) FROM engine_admission_decisions WHERE request_id='concurrent'`) != 1 || f.count(t, `SELECT count(*) FROM events WHERE type='engine.admission_decided' AND after->'decision'->>'request_id'='concurrent'`) != 1 {
		t.Fatal("duplicate concurrent audit/projection")
	}
}

// Risk: truncated, malformed or duplicate GitHub inventories authorize a first build.
func TestAdmissionWIPInventoryRejectsPartialReads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages []string
		fail  bool
		want  int
	}{
		{"draft-and-unrelated", []string{`[{"number":1,"state":"open","draft":false,"head":{"ref":"work/a"}},{"number":2,"state":"open","draft":true,"head":{"ref":"work/b"}},{"number":3,"state":"open","draft":false,"head":{"ref":"docs/a"}}]`}, false, 1},
		{"missing-draft", []string{`[{"number":1,"state":"open","head":{"ref":"work/a"}}]`}, true, 0},
		{"duplicate", []string{`[{"number":1,"state":"open","draft":false,"head":{"ref":"work/a"}},{"number":1,"state":"open","draft":false,"head":{"ref":"work/a"}}]`}, true, 0},
		{"transport", nil, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := 0
			n, err := countOpenWorkPRs(func(_ string, out any) error {
				if page >= len(tc.pages) {
					return errors.New("unreadable page")
				}
				raw := tc.pages[page]
				page++
				return json.Unmarshal([]byte(raw), out)
			})
			if (err != nil) != tc.fail || n != tc.want {
				t.Fatalf("count=%d err=%v", n, err)
			}
		})
	}
	full := make([]map[string]any, 100)
	for i := range full {
		full[i] = map[string]any{"number": i + 1, "state": "open", "draft": false, "head": map[string]string{"ref": "work/fixture"}}
	}
	raw, _ := json.Marshal(full)
	calls := 0
	n, err := countOpenWorkPRs(func(_ string, out any) error {
		calls++
		if calls == 1 {
			return json.Unmarshal(raw, out)
		}
		return errors.New("second page unavailable")
	})
	if n != 0 || err == nil || calls != 2 {
		t.Fatal("partial page became a complete count")
	}
}
