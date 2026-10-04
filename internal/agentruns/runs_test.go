// SPDX-License-Identifier: AGPL-3.0-only

package agentruns_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type fixture struct {
	d                             *dbtest.DB
	mux                           *http.ServeMux
	person, agent, other, foreign tenant.Principal
	profile                       string
	token                         string
}

func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), mux: http.NewServeMux()}
	tid := uuid()
	f.person = tenant.Principal{ID: uuid(), TenantID: tid, Kind: tenant.Person}
	f.agent = tenant.Principal{ID: uuid(), TenantID: tid, Kind: tenant.Agent}
	f.other = tenant.Principal{ID: uuid(), TenantID: tid, Kind: tenant.Agent}
	f.foreign = tenant.Principal{ID: uuid(), TenantID: uuid(), Kind: tenant.Person}
	for _, p := range []tenant.Principal{f.person, f.foreign} {
		err := db.InTenant(dbtest.Seed(t.Context()), f.d.Admin, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'P2.3 tests')`, p.TenantID, "work-"+p.TenantID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{f.person, f.agent, f.other, f.foreign} {
		f.tx(t, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(id,tenant_id,kind,name) VALUES($1,$2,$3,'Test')`, p.ID, p.TenantID, p.Kind)
			return err
		})
	}
	// Handlers see project data only through a binding (ADR-003 P2).
	dbtest.BindRole(t, f.d, tid, f.person.ID, "admin")
	dbtest.BindRole(t, f.d, tid, f.agent.ID, "member")
	dbtest.BindRole(t, f.d, tid, f.other.ID, "member")
	f.profile = uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'test','1','codex','openai','requested-test-model','high','strong')`, tid, f.profile)
		return err
	})
	f.token = f.key(t, f.agent, []string{"work_orders.read", "work_orders.write", "run.read", "run.create", "run.claim", "run.telemetry"})
	workorders.New(f.d.App).Mount(f.mux)
	agentruns.New(f.d.App).Mount(f.mux)
	return f
}
func (f *fixture) tx(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) key(t *testing.T, p tenant.Principal, scopes []string) string {
	t.Helper()
	prefix := strings.ReplaceAll(p.TenantID, "-", "") + strings.ReplaceAll(uuid(), "-", "")[:16]
	secret := uuid()
	sum := sha256.Sum256([]byte(secret))
	f.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'test',$3,$4,$5)`, p.TenantID, p.ID, prefix, hex.EncodeToString(sum[:]), scopes)
		return err
	})
	return "aeon_" + prefix + "_" + secret
}
func (f *fixture) request(p tenant.Principal, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set(agentruns.DaemonHeader, "daemon-test")
	r.Header.Set(agentruns.GenerationHeader, "generation-1")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func (f *fixture) call(t *testing.T, p tenant.Principal, method, path string, body any, status int, dst any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	token := ""
	if p.ID == f.agent.ID {
		token = f.token
	}
	w := f.request(p, method, path, string(b), token)
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	if dst != nil {
		if err = json.Unmarshal(w.Body.Bytes(), dst); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *fixture) order(t *testing.T, cost any) workorders.Order {
	t.Helper()
	body := map[string]any{"title": "Work", "criteria": []string{"Tests pass", "Evidence recorded"}, "assignee_principal_id": f.agent.ID}
	if cost != nil {
		body["max_cost_micros"] = cost
	}
	var o workorders.Order
	f.call(t, f.person, "POST", "/api/work-orders", body, 201, &o)
	f.call(t, f.person, "PATCH", "/api/work-orders/"+o.NodeID, map[string]any{"expected_revision": o.Revision, "status": "ready"}, 200, &o)
	return o
}
func (f *fixture) run(t *testing.T, o workorders.Order) agentruns.Run {
	t.Helper()
	var v agentruns.Run
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 201, &v)
	return v
}
func (f *fixture) reserve(t *testing.T, v agentruns.Run) []string {
	t.Helper()
	account, window, reservation := uuid(), uuid(), uuid()
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation) VALUES($1,$2::uuid,$2::text,'codex','daemon-test',$3,'Test',clock_timestamp(),true,'generation-1')`, []any{f.agent.TenantID, account, f.agent.ID}},
			{`UPDATE agent_runs SET account_id=$2 WHERE id=$1`, []any{v.ID, account}},
			{`INSERT INTO account_allowance_windows(tenant_id,id,account_id,starts_at,ends_at,unit,allowance,reserved) VALUES($1,$2,$3,now()-interval '1 hour',now()+interval '1 hour','cost_micros',1000000,100)`, []any{f.agent.TenantID, window, account}},
			{`INSERT INTO account_reservations(tenant_id,id,run_id,window_id,reserved_units) VALUES($1,$2,$3,$4,100)`, []any{f.agent.TenantID, reservation, v.ID, window}},
		} {
			if _, err := tx.Exec(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	roundTheClock(t, f, v.ID)
	return []string{reservation}
}
func claimBody(ids []string) map[string]any {
	return map[string]any{"daemon_id": "daemon-test", "daemon_generation": "generation-1", "reservation_ids": ids}
}
func (f *fixture) claim(t *testing.T, v agentruns.Run) agentruns.Run {
	t.Helper()
	ids := f.reserve(t, v)
	f.call(t, f.agent, "POST", "/api/runs/"+v.ID+"/claim", claimBody(ids), 200, &v)
	return v
}
func (f *fixture) count(t *testing.T, p tenant.Principal, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	f.tx(t, p, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), sql, args...).Scan(&n) })
	return n
}

func TestWorkOrderLifecycleAndIsolation(t *testing.T) {
	f := setup(t)
	o := f.order(t, 1000)
	path := "/api/work-orders/" + o.NodeID
	if len(o.Criteria) != 2 || o.Revision != 2 || o.Status != "ready" {
		t.Fatalf("order: %+v", o)
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1 AND k.slug='work_order'`, o.NodeID); n != 1 {
		t.Fatal("missing work-order node")
	}
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	f.call(t, f.foreign, "PATCH", path, map[string]any{"expected_revision": 2, "status": "done"}, 404, nil)
	f.call(t, f.person, "PATCH", path, map[string]any{"expected_revision": 1, "status": "done"}, 409, nil)
	f.call(t, f.person, "PATCH", path, map[string]any{"expected_revision": 2, "status": "done"}, 409, nil)
	for _, c := range o.Criteria {
		f.call(t, f.agent, "POST", path+"/criteria/"+c.ID+"/check", map[string]bool{"checked": true}, 200, nil)
	}
	f.call(t, f.person, "GET", path, nil, 200, &o)
	f.call(t, f.person, "PATCH", path, map[string]any{"expected_revision": o.Revision, "status": "done"}, 409, nil)
	var e workorders.Evidence
	f.call(t, f.agent, "POST", path+"/evidence", map[string]any{"kind": "text", "reference": "Test evidence", "criterion_id": o.Criteria[0].ID}, 201, &e)
	if e.SubmittedBy != f.agent.ID || e.OrderID != o.NodeID {
		t.Fatal("invalid evidence")
	}
	f.call(t, f.person, "GET", path, nil, 200, &o)
	f.call(t, f.person, "PATCH", path, map[string]any{"expected_revision": o.Revision, "status": "done"}, 200, &o)
	f.call(t, f.person, "POST", path+"/criteria/"+o.Criteria[0].ID+"/check", map[string]bool{"checked": false}, 409, nil)
	events := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1`, o.NodeID)
	f.call(t, f.person, "POST", path+"/criteria/"+o.Criteria[0].ID+"/check", map[string]bool{"checked": true}, 200, nil)
	if got := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1`, o.NodeID); got != events {
		t.Fatal("idempotent check added event")
	}
	f.call(t, f.person, "PATCH", path, map[string]any{"expected_revision": o.Revision, "status": "draft", "assignee_principal_id": nil, "max_cost_micros": nil}, 200, &o)
	if o.Assignee != nil || o.MaxCost != nil {
		t.Fatal("nullable patch ignored")
	}
	foreign := f.count(t, f.foreign, `SELECT count(*) FROM work_orders`)
	if foreign != 0 {
		t.Fatal("RLS leaked orders")
	}
	// Evidence references cannot cross orders or tenants.
	other := f.order(t, nil)
	f.call(t, f.person, "POST", path+"/evidence", map[string]any{"kind": "text", "reference": "invalid", "criterion_id": other.Criteria[0].ID}, 400, nil)
	f.call(t, f.person, "POST", path+"/evidence", map[string]any{"kind": "node", "reference": uuid()}, 400, nil)
}

func TestScopeChecksValidationAndPagination(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	for _, tc := range []struct {
		p      tenant.Principal
		token  string
		status int
	}{
		{tenant.Principal{}, "", 401}, {f.agent, "", 403}, {f.agent, f.key(t, f.agent, []string{"run.read"}), 403},
		{f.other, f.token, 403}, {f.agent, f.token, 200},
	} {
		w := f.request(tc.p, "GET", "/api/work-orders", "", tc.token)
		if w.Code != tc.status {
			t.Fatalf("authorization got %d want %d", w.Code, tc.status)
		}
	}
	otherKey := f.key(t, f.other, []string{"work_orders.write"})
	w := f.request(f.other, "PATCH", "/api/work-orders/"+o.NodeID, `{"expected_revision":2,"status":"done"}`, otherKey)
	if w.Code != 403 {
		t.Fatalf("other agent write: %d", w.Code)
	}
	for _, body := range []string{`null`, `{}`, `{"title":"x","criteria":[]}`, `{"title":"x","criteria":[""]}`, `{"title":"x","criteria":["y"],"max_cost_micros":-1}`, `{"title":"x","criteria":["y"],"max_duration_seconds":0}`, `{"title":"x","criteria":["y"],"unknown":true}`, `{} {}`, `{"title":"x","criteria":["y"],"parent_id":"bad"}`} {
		w = f.request(f.person, "POST", "/api/work-orders", body, "")
		if w.Code != 400 {
			t.Fatalf("invalid input accepted: %s (%d)", body, w.Code)
		}
	}
	f.call(t, f.person, "GET", "/api/work-orders?limit=201", nil, 400, nil)
	f.call(t, f.person, "GET", "/api/work-orders?cursor=wrong", nil, 400, nil)
	f.order(t, nil)
	var first, second []workorders.Order
	page := f.request(f.person, "GET", "/api/work-orders?limit=1", "", "")
	if page.Code != 200 {
		t.Fatal(page.Body.String())
	}
	if err := json.Unmarshal(page.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	cursor := page.Header().Get("X-Next-Cursor")
	if cursor == "" || page.Header().Get("Link") == "" {
		t.Fatal("missing continuation headers")
	}
	f.call(t, f.person, "GET", "/api/work-orders?limit=1&cursor="+cursor, nil, 200, &second)
	f.call(t, f.foreign, "GET", "/api/work-orders?limit=1&cursor="+cursor, nil, 400, nil)
	if len(first) != 1 || len(second) != 1 || first[0].NodeID == second[0].NodeID {
		t.Fatal("pagination duplicated order")
	}
}

func TestRunClaimTelemetryReplayAndBudget(t *testing.T) {
	f := setup(t)
	o := f.order(t, 100)
	v := f.run(t, o)
	path := "/api/runs/" + v.ID
	if v.RequestedModel == nil || *v.RequestedModel != "requested-test-model" || v.ModelEvidence != "unverified" {
		t.Fatal("profile not pinned")
	}
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	f.call(t, f.person, "GET", "/api/runs/queued", nil, 403, nil)
	var queued []agentruns.Run
	f.call(t, f.agent, "GET", "/api/runs/queued", nil, 200, &queued)
	if len(queued) != 1 {
		t.Fatal("queued run missing")
	}
	f.call(t, f.agent, "POST", path+"/claim", claimBody([]string{uuid()}), 409, nil)
	ids := f.reserve(t, v)
	f.call(t, f.agent, "POST", path+"/claim", claimBody([]string{uuid()}), 409, nil)
	f.call(t, f.agent, "POST", path+"/claim", claimBody(ids), 200, &v)
	if v.Status != "starting" || v.StartedAt == nil {
		t.Fatal("claim did not start run")
	}
	n := f.count(t, f.person, `SELECT count(*) FROM events`)
	f.call(t, f.agent, "POST", path+"/claim", claimBody(ids), 200, nil)
	if f.count(t, f.person, `SELECT count(*) FROM events`) != n {
		t.Fatal("claim replay mutated events")
	}
	conflict := claimBody(ids)
	conflict["daemon_generation"] = "generation-2"
	f.call(t, f.agent, "POST", path+"/claim", conflict, 409, nil)
	report := map[string]any{"sequence": 1, "kind": "started", "cost_micros_delta": 40, "input_tokens_delta": 7, "effective_model": "vendor-model", "model_evidence": "vendor_reported"}
	f.call(t, f.agent, "POST", path+"/telemetry", report, 200, &v)
	if v.Cost != 40 || v.InputTokens != 7 || v.Status != "running" || v.EffectiveModel == nil || *v.EffectiveModel != "vendor-model" || *v.RequestedModel != "requested-test-model" {
		t.Fatalf("telemetry totals/model: %+v", v)
	}
	n = f.count(t, f.person, `SELECT count(*) FROM events`)
	f.call(t, f.agent, "POST", path+"/telemetry", report, 200, &v)
	if v.Cost != 40 || f.count(t, f.person, `SELECT count(*) FROM events`) != n {
		t.Fatal("replay charged twice")
	}
	report["effective_model"] = "different-model"
	f.call(t, f.agent, "POST", path+"/telemetry", report, 409, nil)
	report["effective_model"] = "vendor-model"
	report["status"] = "running"
	f.call(t, f.agent, "POST", path+"/telemetry", report, 409, nil)
	delete(report, "status")
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 3, "kind": "usage", "cost_micros_delta": 60}, 200, &v)
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 2, "kind": "heartbeat"}, 409, nil)
	f.call(t, f.person, "GET", "/api/work-orders/"+o.NodeID, nil, 200, &o)
	if o.Status != "blocked" {
		t.Fatal("exhausted order not blocked")
	}
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 409, nil)
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 4, "kind": "finished", "status": "failed", "error_code": "turn_failed", "cost_micros_delta": 10}, 200, &v)
	if v.Cost != 110 || v.Status != "failed" || v.EndedAt == nil {
		t.Fatal("actual overshoot/terminal report lost")
	}
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 5, "kind": "heartbeat"}, 409, nil)
	report["sequence"] = 1
	f.call(t, f.agent, "POST", path+"/telemetry", report, 200, &v)
	if v.Cost != 110 {
		t.Fatal("historical replay changed totals")
	}
}

func TestTelemetryFencesAndAtomicRollback(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	v := f.claim(t, f.run(t, o))
	path := "/api/runs/" + v.ID + "/telemetry"
	for _, body := range []string{`{"sequence":1,"kind":"usage","body":"vendor text"}`, `{"sequence":1,"kind":"usage","effective_model":"vendor text\nraw"}`, `{"sequence":1,"kind":"status"}`, `{"sequence":1,"kind":"finished","status":"running"}`, `{"sequence":1,"kind":"usage","cost_micros_delta":-1}`, `{"sequence":1,"kind":"usage","error_code":"raw error text"}`, `{"sequence":1,"kind":"usage","model_evidence":"vendor_reported"}`} {
		w := f.request(f.agent, "POST", path, body, f.token)
		if w.Code != 400 {
			t.Fatalf("invalid telemetry %s: %d", body, w.Code)
		}
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(`{"sequence":1,"kind":"heartbeat"}`))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("missing fence accepted")
	}
	r = httptest.NewRequest("POST", path, strings.NewReader(`{"sequence":1,"kind":"heartbeat"}`))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set(agentruns.DaemonHeader, "daemon-test")
	r.Header.Set(agentruns.GenerationHeader, "generation-2")
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("wrong generation accepted")
	}
	original := f.mux
	f.mux = http.NewServeMux()
	agentruns.New(f.d.App, func(context.Context, pgx.Tx, tenant.Principal, agentruns.Run, agentruns.Telemetry) error {
		return errors.New("settlement unavailable")
	}).Mount(f.mux)
	n := f.count(t, f.person, `SELECT count(*) FROM events`)
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 1, "kind": "usage", "cost_micros_delta": 9}, 500, nil)
	if f.count(t, f.person, `SELECT cost_micros FROM agent_runs WHERE id=$1`, v.ID) != 0 || f.count(t, f.person, `SELECT count(*) FROM run_telemetry WHERE run_id=$1`, v.ID) != 0 || f.count(t, f.person, `SELECT count(*) FROM events`) != n {
		t.Fatal("failed settlement partially committed")
	}
	f.mux = original
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 1, "kind": "usage", "cost_micros_delta": 9}, 200, nil)
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_daemon_generation='generation-2' WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 2, "kind": "heartbeat"}, 403, nil)
}

func TestConcurrentTelemetrySerializesBudgetAndReplay(t *testing.T) {
	f := setup(t)
	o := f.order(t, 100)
	a := f.claim(t, f.run(t, o))
	b := f.claim(t, f.run(t, o))
	var wg sync.WaitGroup
	codes := make(chan int, 4)
	for _, v := range []agentruns.Run{a, a, b, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := f.request(f.agent, "POST", "/api/runs/"+v.ID+"/telemetry", `{"sequence":1,"kind":"usage","cost_micros_delta":60}`, f.token)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent telemetry status %d", code)
		}
	}
	if f.count(t, f.person, `SELECT sum(cost_micros)::bigint FROM agent_runs WHERE work_order_id=$1`, o.NodeID) != 120 {
		t.Fatal("incorrect aggregate usage")
	}
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='work_order.budget_exhausted'`, o.NodeID) != 1 {
		t.Fatal("budget block not serialized")
	}
	if f.count(t, f.person, `SELECT count(*) FROM run_telemetry`) != 2 {
		t.Fatal("duplicate telemetry inserted")
	}
}

func TestElapsedBudgetAndZeroCostCeiling(t *testing.T) {
	f := setup(t)
	var zero workorders.Order
	f.call(t, f.person, "POST", "/api/work-orders", map[string]any{"title": "Zero", "criteria": []string{"x"}, "max_cost_micros": 0}, 201, &zero)
	f.call(t, f.person, "PATCH", "/api/work-orders/"+zero.NodeID, map[string]any{"expected_revision": 1, "status": "ready"}, 409, nil)
	o := f.order(t, nil)
	f.call(t, f.person, "PATCH", "/api/work-orders/"+o.NodeID, map[string]any{"expected_revision": o.Revision, "max_duration_seconds": 1}, 200, &o)
	v := f.claim(t, f.run(t, o))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET started_at=clock_timestamp()-interval '2 seconds' WHERE id=$1`, v.ID)
		return err
	})
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 409, nil)
	f.call(t, f.agent, "POST", "/api/runs/"+v.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "heartbeat"}, 200, nil)
	f.call(t, f.person, "GET", "/api/work-orders/"+o.NodeID, nil, 200, &o)
	if o.Status != "blocked" {
		t.Fatal("elapsed ceiling not enforced")
	}
}

func TestDelegatedClaimNeedsLiveGrantAndKeyScope(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	f.call(t, f.person, "PATCH", "/api/work-orders/"+o.NodeID, map[string]any{"expected_revision": o.Revision, "assignee_principal_id": f.other.ID}, 200, &o)
	var v agentruns.Run
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{"agent_principal_id": f.other.ID, "model_profile_id": f.profile}, 201, &v)
	ids := f.reserve(t, v) // The daemon owner differs from the assigned run agent.
	path := "/api/runs/" + v.ID
	f.call(t, f.agent, "POST", path+"/claim", claimBody(ids), 403, nil)
	grant := uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,id,proposed_by_principal_id,agent_principal_id,run_id,scope,resource_kind,resource_id,rationale,expires_at)
		 VALUES($1,$2,$3,$3,$4,'run.claim','run',$4,'delegated daemon',now()+interval '1 hour')`, f.person.TenantID, grant, f.agent.ID, v.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'approved')`, f.person.TenantID, grant, f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_permission_grants(tenant_id,approval_request_id,agent_principal_id,scope,resource_kind,resource_id,valid_until)
		 SELECT tenant_id,id,agent_principal_id,scope,resource_kind,resource_id,expires_at FROM approval_requests WHERE id=$1`, grant)
		return err
	})
	body, _ := json.Marshal(claimBody(ids))
	readOnly := f.key(t, f.agent, []string{"run.read"})
	if w := f.request(f.agent, "POST", path+"/claim", string(body), readOnly); w.Code != 403 {
		t.Fatal("approval enlarged key scope")
	}
	f.call(t, f.agent, "POST", path+"/claim", claimBody(ids), 200, nil)
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 1, "kind": "heartbeat"}, 200, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_permission_grants SET revoked_at=clock_timestamp() WHERE approval_request_id=$1`, grant)
		return err
	})
	f.call(t, f.agent, "POST", path+"/claim", claimBody(ids), 403, nil)
	f.call(t, f.agent, "POST", path+"/telemetry", map[string]any{"sequence": 2, "kind": "heartbeat"}, 403, nil)
}

func TestClaimRejectsStaleProbeAndReplacedGeneration(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	v := f.run(t, o)
	ids := f.reserve(t, v)
	path := "/api/runs/" + v.ID + "/claim"
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=clock_timestamp()-interval '3 minutes' WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, claimBody(ids), 409, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=clock_timestamp() WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, claimBody(ids), 200, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_daemon_generation='generation-2' WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, claimBody(ids), 409, nil)
}

func TestCreationRollsBackWhenEventCannotBeWritten(t *testing.T) {
	f := setup(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.Admin, f.person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_work_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		 IF NEW.type='work_order.created' THEN RAISE EXCEPTION 'test event failure' USING ERRCODE='XX000'; END IF;
		 RETURN NEW; END; $$`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `CREATE TRIGGER reject_work_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_work_event()`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.call(t, f.person, "POST", "/api/work-orders", map[string]any{"title": "Must roll back", "criteria": []string{"not persisted"}}, 500, nil)
	for _, table := range []string{"work_orders", "work_criteria", "nodes", "events", "node_key_counters"} {
		if f.count(t, f.person, `SELECT count(*) FROM `+table) != 0 {
			t.Fatalf("%s survived failed event", table)
		}
	}
}

func TestTelemetryHookRunsOnceAndCountersCannotOverflow(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	v := f.claim(t, f.run(t, o))
	path := "/api/runs/" + v.ID + "/telemetry"
	calls := 0
	f.mux = http.NewServeMux()
	agentruns.New(f.d.App, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, run agentruns.Run, report agentruns.Telemetry) error {
		calls++
		var total int64
		if err := tx.QueryRow(ctx, `SELECT cost_micros FROM agent_runs WHERE id=$1`, run.ID).Scan(&total); err != nil {
			return err
		}
		if total != report.Cost || total != run.Cost {
			return errors.New("hook did not observe transactional totals")
		}
		return nil
	}).Mount(f.mux)
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 1, "kind": "usage", "cost_micros_delta": int64(9223372036854775807)}, 200, nil)
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 1, "kind": "usage", "cost_micros_delta": int64(9223372036854775807)}, 200, nil)
	f.call(t, f.agent, "POST", path, map[string]any{"sequence": 2, "kind": "usage", "cost_micros_delta": 1}, 400, nil)
	if calls != 1 {
		t.Fatalf("settlement hook calls: %d", calls)
	}
}
