// SPDX-License-Identifier: AGPL-3.0-only

package hours

import (
	"context"
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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/costunits"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fixture struct {
	t                                     *testing.T
	database                              *dbtest.DB
	mod                                   *Module
	mux                                   *http.ServeMux
	admin, member, agent, customer, other tenant.Principal
	root, child, cost, run                string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	d := dbtest.Open(t)
	f := &fixture{t: t, database: d}
	tid := "10000000-0000-4000-8000-000000000001"
	other := "10000000-0000-4000-8000-000000000002"
	for i, id := range []string{tid, other} {
		err := db.InTenant(dbtest.Seed(t.Context()), d.Admin, id, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Hours test')`, id, fmt.Sprintf("hours-%d", i))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seedPrincipal := func(id, kind, role string) tenant.Principal {
		p := tenant.Principal{TenantID: id, Kind: tenant.PrincipalKind(kind), Roles: []string{role}}
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,$2,'Hours worker',$3) RETURNING id::text`, id, kind, p.Roles).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		dbtest.BindLegacy(t, d, id, p.ID)
		return p
	}
	f.admin = seedPrincipal(tid, "person", "admin")
	f.member = seedPrincipal(tid, "person", "member")
	f.agent = seedPrincipal(tid, "agent", "admin")
	f.customer = seedPrincipal(tid, "person", "customer")
	f.other = seedPrincipal(other, "person", "admin")
	reg := plugins.NewRegistry()
	hours, err := Plugin()
	if err != nil {
		t.Fatal(err)
	}
	costs, err := costunits.Plugin()
	if err != nil {
		t.Fatal(err)
	}
	for _, plug := range []plugins.Plugin{costs, hours} {
		if err := reg.Register(plug); err != nil {
			t.Fatal(err)
		}
	}
	reg.Seal()
	for _, id := range []string{tid, other} {
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			for _, plug := range []plugins.Plugin{costs, hours} {
				_, err := tx.Exec(t.Context(), `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id) VALUES($1,$2,$3,$4,$5,true,$6,(SELECT id FROM principals WHERE tenant_id=$1 AND kind='person' AND 'admin'=ANY(roles) LIMIT 1))`, id, plug.Manifest.ID, plug.Manifest.Version, plug.Manifest.DigestSHA256, plug.Manifest.Owner, plug.Manifest.Permissions)
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	f.sql(func(tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'cost_unit','Cost unit','CU','clock') RETURNING id::text`, tid).Scan(&kind); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) VALUES($1,$2,'CU-1','Hourly work') RETURNING id::text`, tid, kind).Scan(&f.cost); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PRJ-1','Root' FROM node_kinds WHERE slug='project' RETURNING id::text`, tid).Scan(&f.root); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'WOR-1','Child',$2 FROM node_kinds WHERE slug='work_order' RETURNING id::text`, tid, f.root).Scan(&f.child); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,assignee_principal_id) VALUES($1,$2,$3,$4)`, tid, f.child, f.admin.ID, f.agent.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,started_at,ended_at,cost_micros,input_tokens,output_tokens) VALUES($1,$2,$3,'completed','2026-09-23T12:00:00Z','2026-09-23T12:00:01Z',999999,999,999) RETURNING id::text`, tid, f.child, f.agent.ID).Scan(&f.run); err != nil {
			return err
		}
		for _, currency := range []string{"EUR", "USD"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO cost_unit_rates(tenant_id,cost_unit_node_id,unit,currency,internal_amount,bill_amount,effective_from,created_by_principal_id) VALUES($1,$2,'hour',$3,1,0.18,'2026-01-01',$4)`, tid, f.cost, currency, f.admin.ID); err != nil {
				return err
			}
		}
		return nil
	})
	f.mod = New(d.App, reg)
	f.mux = http.NewServeMux()
	f.mod.Mount(f.mux)
	return f
}
func (f *fixture) sql(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.database.App, f.admin.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) call(p tenant.Principal, method, path string, body any, bearer ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/api"+path, strings.NewReader(string(raw)))
	if len(bearer) > 0 && p.Kind == tenant.Agent {
		p.Scopes = []string{"hours.read", "hours.write"}
	}
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	if len(bearer) > 0 {
		r.Header.Set("Authorization", "Bearer "+bearer[0])
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func requireStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}
func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func (f *fixture) period(p tenant.Principal) Period {
	f.t.Helper()
	w := f.call(f.admin, "POST", "/time-periods", map[string]any{"principal_id": p.ID, "starts_at": "2026-09-01T00:00:00Z", "ends_at": "2026-10-01T00:00:00Z"})
	requireStatus(f.t, w, 201)
	return decode[Period](f.t, w)
}
func (f *fixture) manual(period Period, currency string) map[string]any {
	return map[string]any{"period_id": period.ID, "principal_id": period.PrincipalID, "cost_unit_node_id": f.cost, "node_id": f.child, "source": "manual", "currency": currency, "started_at": "2026-09-23T12:00:00Z", "ended_at": "2026-09-23T12:00:01Z"}
}
func (f *fixture) eventCount() int {
	var n int
	f.sql(func(tx pgx.Tx) error { return tx.QueryRow(f.t.Context(), `SELECT count(*) FROM events`).Scan(&n) })
	return n
}
func (f *fixture) snapshot(period Period) (Period, string) {
	w := f.call(f.admin, "GET", "/time-periods/"+period.ID, nil)
	requireStatus(f.t, w, 200)
	return decode[Period](f.t, w), strings.Trim(w.Header().Get("X-Entries-SHA256"), `"`)
}
func approvalBody(p Period, digest string) map[string]any {
	return map[string]any{"expected_revision": p.Revision, "expected_entries_sha256": digest}
}

func TestHoursSnapshotsApprovalTotalsAndIsolation(t *testing.T) {
	f := setup(t)
	period := f.period(f.member)
	empty, emptyHash := f.snapshot(period)
	for _, currency := range []string{"EUR", "USD"} {
		w := f.call(f.member, "POST", "/time-entries", f.manual(period, currency))
		requireStatus(t, w, 201)
		e := decode[Entry](t, w)
		if e.Amount.String() != "0.0001" || e.RateAmount.String() != "0.1800" || e.DurationSeconds != 1 {
			t.Fatalf("exact rounding: %+v", e)
		}
	}
	current, digest := f.snapshot(period)
	if current.Revision != 3 || digest == emptyHash {
		t.Fatalf("revision/digest not updated: %+v", current)
	}
	w := f.call(f.admin, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(empty, emptyHash))
	requireStatus(t, w, 409)
	w = f.call(f.member, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(current, digest))
	requireStatus(t, w, 403)
	w = f.call(f.other, "GET", "/time-periods/"+period.ID, nil)
	requireStatus(t, w, 404)
	w = f.call(f.other, "GET", "/time-entries", nil)
	requireStatus(t, w, 200)
	if len(decode[[]Entry](t, w)) != 0 {
		t.Fatal("cross-tenant entries")
	}
	w = f.call(f.other, "GET", "/nodes/"+f.root+"/time-totals", nil)
	requireStatus(t, w, 404)
	w = f.call(f.customer, "GET", "/time-periods", nil)
	requireStatus(t, w, 403)
	w = f.call(f.admin, "GET", "/nodes/"+f.root+"/time-totals?approved_only=true", nil)
	requireStatus(t, w, 200)
	if decode[Totals](t, w).DurationSeconds != 0 {
		t.Fatal("unapproved time included")
	}
	before := f.eventCount()
	w = f.call(f.admin, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(current, digest))
	requireStatus(t, w, 200)
	approved := decode[Period](t, w)
	if approved.State != "approved" || approved.Approval.EntriesSHA256 != digest || approved.Approval.TotalSeconds != 2 {
		t.Fatalf("approval %+v", approved)
	}
	requireStatus(t, f.call(f.admin, "POST", "/time-periods/"+period.ID+"/approve", approvalBody(current, digest)), 200)
	if f.eventCount() != before+1 {
		t.Fatal("approval replay emitted an event")
	}
	requireStatus(t, f.call(f.member, "POST", "/time-entries", f.manual(period, "EUR")), 409)
	w = f.call(f.admin, "GET", "/nodes/"+f.root+"/time-totals?approved_only=true", nil)
	requireStatus(t, w, 200)
	totals := decode[Totals](t, w)
	if totals.DurationSeconds != 2 || len(totals.Amounts) != 2 || totals.Amounts[0].Amount.String() != "0.0001" {
		t.Fatalf("totals %+v", totals)
	}
	// Moving/deleting a descendant changes live traversal, never the sealed entry.
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, f.child)
		return err
	})
	w = f.call(f.admin, "GET", "/nodes/"+f.root+"/time-totals", nil)
	requireStatus(t, w, 200)
	if decode[Totals](t, w).DurationSeconds != 0 {
		t.Fatal("deleted descendant included")
	}
}
func TestHoursAuthorityClosureAndRateEdges(t *testing.T) {
	f := setup(t)
	period := f.period(f.member)
	before := f.eventCount()
	bad := f.manual(period, "EUR")
	bad["principal_id"] = f.admin.ID
	requireStatus(t, f.call(f.member, "POST", "/time-entries", bad), 403)
	bad = f.manual(period, "GBP")
	requireStatus(t, f.call(f.member, "POST", "/time-entries", bad), 409)
	bad = f.manual(period, "EUR")
	bad["ended_at"] = "2026-09-23T12:00:00.5Z"
	requireStatus(t, f.call(f.member, "POST", "/time-entries", bad), 409)
	bad = f.manual(period, "EUR")
	bad["cost_unit_node_id"] = f.root
	requireStatus(t, f.call(f.member, "POST", "/time-entries", bad), 404)
	bad = f.manual(period, "EUR")
	bad["extra"] = true
	requireStatus(t, f.call(f.member, "POST", "/time-entries", bad), 400)
	requireStatus(t, f.call(f.admin, "POST", "/time-periods", map[string]any{"principal_id": f.member.ID, "starts_at": period.StartsAt, "ends_at": period.EndsAt}), 409)
	requireStatus(t, f.call(f.admin, "GET", "/nodes/"+f.root+"/time-totals?approved_only=yes", nil), 400)
	if f.eventCount() != before {
		t.Fatal("failed writes emitted events")
	}
	for _, sql := range []string{`UPDATE plugin_installations SET enabled=false WHERE plugin_id='business_hours'`, `UPDATE plugin_installations SET enabled=true,permissions='{}' WHERE plugin_id='business_hours'`, `UPDATE plugin_installations SET permissions=ARRAY['views.provide','steps.apply'],manifest_digest_sha256=repeat('a',64) WHERE plugin_id='business_hours'`} {
		f.sql(func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), sql); return err })
		requireStatus(t, f.call(f.member, "POST", "/time-entries", f.manual(period, "EUR")), 403)
		requireStatus(t, f.call(f.member, "GET", "/time-periods", nil), 403)
	}
	plug, _ := Plugin()
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plugin_installations SET manifest_digest_sha256=$1 WHERE plugin_id='business_hours'`, plug.Manifest.DigestSHA256)
		return err
	})
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plugin_installations SET enabled=false WHERE plugin_id='business_costs'`)
		return err
	})
	requireStatus(t, f.call(f.admin, "GET", "/time-periods", nil), 403)
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plugin_installations SET enabled=true WHERE plugin_id='business_costs'`)
		return err
	})
	// Exact date boundary: UTC start selects the new rate, irrespective of offset.
	f.sql(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE cost_unit_rates SET effective_until='2026-09-23' WHERE currency='EUR'`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO cost_unit_rates(tenant_id,cost_unit_node_id,unit,currency,internal_amount,bill_amount,effective_from,created_by_principal_id) VALUES($1,$2,'hour','EUR',0,99999999999999.9999,'2026-09-23',$3)`, f.admin.TenantID, f.cost, f.admin.ID)
		return err
	})
	bad = f.manual(period, "EUR")
	bad["started_at"] = "2026-09-22T23:00:00-02:00"
	bad["ended_at"] = "2026-09-23T00:00:00-02:00"
	w := f.call(f.member, "POST", "/time-entries", bad)
	requireStatus(t, w, 201)
	if decode[Entry](t, w).Amount.String() != "99999999999999.9999" {
		t.Fatal("large exact decimal changed")
	}
	bad["ended_at"] = "2026-09-23T01:00:00-02:00"
	requireStatus(t, f.call(f.member, "POST", "/time-entries", bad), 400)
}

type failingWriter struct{}

func (failingWriter) Append(context.Context, pgx.Tx, tenant.Principal, events.Change) (events.Event, error) {
	return events.Event{}, errors.New("test event failure")
}
func TestHoursEventAtomicityAndConcurrentApproval(t *testing.T) {
	f := setup(t)
	p := f.period(f.member)
	before := f.eventCount()
	f.mod.writer = failingWriter{}
	requireStatus(t, f.call(f.member, "POST", "/time-entries", f.manual(p, "EUR")), 500)
	current, digest := f.snapshot(p)
	if current.Revision != 1 || f.eventCount() != before {
		t.Fatal("failed event persisted entry/revision")
	}
	requireStatus(t, f.call(f.admin, "POST", "/time-periods/"+p.ID+"/approve", approvalBody(current, digest)), 500)
	if p2, _ := f.snapshot(p); p2.State != "open" {
		t.Fatal("failed approval event closed period")
	}
	f.mod.writer = events.Writer{}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- f.call(f.admin, "POST", "/time-periods/"+p.ID+"/approve", approvalBody(current, digest)).Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent approval %d", code)
		}
	}
	if f.eventCount() != before+1 {
		t.Fatal("concurrent approval duplicated event")
	}
}
func TestHoursRunConversion(t *testing.T) {
	f := setup(t)
	p := f.period(f.agent)
	body := map[string]any{"period_id": p.ID, "cost_unit_node_id": f.cost, "currency": "EUR", "source": "agent_run", "agent_run_id": f.run}
	requireStatus(t, f.call(f.member, "POST", "/time-entries", body), 403)
	requireStatus(t, f.call(f.agent, "POST", "/time-entries", body), 403) // no scoped key
	before := f.eventCount()
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.call(f.admin, "POST", "/time-entries", body) }()
	}
	wg.Wait()
	close(results)
	var id string
	for w := range results {
		requireStatus(t, w, 201)
		e := decode[Entry](t, w)
		if e.DurationSeconds != 1 || e.NodeID != f.child || e.PrincipalID != f.agent.ID || e.Amount.String() != "0.0001" {
			t.Fatalf("run conversion %+v", e)
		}
		if id != "" && id != e.ID {
			t.Fatal("duplicate run entries")
		}
		id = e.ID
	}
	if f.eventCount() != before+2 {
		t.Fatal("run replay emitted events")
	}
	current, digest := f.snapshot(p)
	requireStatus(t, f.call(f.admin, "POST", "/time-periods/"+p.ID+"/approve", approvalBody(current, digest)), 200)
	requireStatus(t, f.call(f.admin, "POST", "/time-entries", body), 201) // replay after seal
	body["currency"] = "USD"
	requireStatus(t, f.call(f.admin, "POST", "/time-entries", body), 409)
	body["currency"] = "EUR"
	body["started_at"] = "2026-09-23T12:00:00Z"
	requireStatus(t, f.call(f.admin, "POST", "/time-entries", body), 400)
	delete(body, "started_at")
	for _, state := range []string{"running", "ownership_lost", "failed", "cancelled"} {
		f.sql(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,started_at,ended_at) VALUES($1,$2,$3,$4,'2026-09-23T12:00:00Z','2026-09-23T12:00:00.5Z') RETURNING id::text`, f.admin.TenantID, f.child, f.agent.ID, state).Scan(&f.run)
		})
		body["agent_run_id"] = f.run
		requireStatus(t, f.call(f.admin, "POST", "/time-entries", body), 409)
	}
}
func TestPluginHooksCannotApproveCallerFacts(t *testing.T) {
	plug, err := Plugin()
	if err != nil {
		t.Fatal(err)
	}
	reg := plugins.NewRegistry()
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	decision, err := plug.Steps.ApplyResult(t.Context(), plugins.Call{}, plugins.StepResult{StepRequest: plugins.StepRequest{Operation: "period_approve", Payload: map[string]any{"approved": true}}, ClaimedOutcome: "succeeded"})
	if err != nil || decision.Proceed {
		t.Fatal("plugin accepted caller facts")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := plug.Steps.Evaluate(ctx, plugins.Call{}, plugins.StepRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel ignored")
	}
	if !validInterval(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)) {
		t.Fatal("valid interval refused")
	}
}

func TestScopedAgentTerminalTimeAndLeastPrivilege(t *testing.T) {
	f := setup(t)
	p := f.period(f.agent)
	other := f.period(f.member)
	sum := sha256.Sum256([]byte("fixture-only"))
	bearer := "aeon_fixture_fixture-only"
	f.sql(func(tx pgx.Tx) error {
		var roleID string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'fixture_agent_hours','Fixture agent hours') RETURNING id::text`, f.agent.TenantID).Scan(&roleID); err != nil {
			return err
		}
		// The binding must show the cost unit's project (ADR-003 P2); the key
		// itself stays limited to hours.
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'hours.read'),($1,$2,'hours.write'),($1,$2,'nodes.read')`, f.agent.TenantID, roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.agent.TenantID, f.agent.ID, roleID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'test','fixture',$3,ARRAY['hours.read','hours.write'],(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`, f.agent.TenantID, f.agent.ID, hex.EncodeToString(sum[:]))
		return err
	})
	requireStatus(t, f.call(f.agent, "GET", "/time-periods/"+p.ID, nil, bearer), 200)
	requireStatus(t, f.call(f.agent, "GET", "/time-periods/"+other.ID, nil, bearer), 403)
	requireStatus(t, f.call(f.agent, "GET", "/time-entries?principal_id="+f.member.ID, nil, bearer), 403)
	requireStatus(t, f.call(f.agent, "POST", "/time-entries", f.manual(other, "EUR"), bearer), 403)
	current, digest := f.snapshot(p)
	requireStatus(t, f.call(f.agent, "POST", "/time-periods/"+p.ID+"/approve", approvalBody(current, digest), bearer), 403)
	// Hours writes need steps.apply; a read-only costs grant is sufficient.
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plugin_installations SET permissions=CASE WHEN plugin_id='business_hours' THEN ARRAY['steps.apply'] ELSE ARRAY['views.provide'] END`)
		return err
	})
	body := map[string]any{"period_id": p.ID, "cost_unit_node_id": f.cost, "currency": "EUR", "source": "agent_run", "agent_run_id": f.run}
	requireStatus(t, f.call(f.agent, "POST", "/time-entries", body, bearer), 201)
	requireStatus(t, f.call(f.agent, "GET", "/time-periods", nil, bearer), 403)
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plugin_installations SET permissions=ARRAY['steps.apply','views.provide'] WHERE plugin_id='business_hours'`)
		return err
	})
	// All terminal statuses use stored interval, including matching fractional
	// offsets whose difference is an exact second. Tokens/cost do not contribute.
	for _, status := range []string{"failed", "cancelled"} {
		var runID string
		f.sql(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,started_at,ended_at) VALUES($1,$2,$3,$4,'2026-09-23T12:00:00.123456Z','2026-09-23T12:00:01.123456Z') RETURNING id::text`, f.agent.TenantID, f.child, f.agent.ID, status).Scan(&runID)
		})
		body["agent_run_id"] = runID
		w := f.call(f.agent, "POST", "/time-entries", body, bearer)
		requireStatus(t, w, 201)
		e := decode[Entry](t, w)
		if e.StartedAt.Nanosecond() != 123456000 || e.DurationSeconds != 1 {
			t.Fatalf("source timestamps changed: %+v", e)
		}
	}
	var runID string
	f.sql(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status) VALUES($1,$2,$3,'completed') RETURNING id::text`, f.agent.TenantID, f.child, f.agent.ID).Scan(&runID)
	})
	body["agent_run_id"] = runID
	requireStatus(t, f.call(f.agent, "POST", "/time-entries", body, bearer), 409)
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['hours.read'] WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	body["agent_run_id"] = f.run
	requireStatus(t, f.call(f.agent, "POST", "/time-entries", body, bearer), 403)
	w := f.call(f.agent, "GET", "/nodes/"+f.root+"/time-totals", nil, bearer)
	requireStatus(t, w, 200)
	if decode[Totals](t, w).DurationSeconds != 3 {
		t.Fatal("agent totals incorrect")
	}
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=now() WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	requireStatus(t, f.call(f.agent, "GET", "/time-periods", nil, bearer), 403)
}

type blockingApproval struct{ entered, release chan struct{} }

func (w blockingApproval) Append(ctx context.Context, tx pgx.Tx, p tenant.Principal, c events.Change) (events.Event, error) {
	if c.Type == "hours.period_approved" {
		close(w.entered)
		select {
		case <-w.release:
		case <-ctx.Done():
			return events.Event{}, ctx.Err()
		}
	}
	return events.Append(ctx, tx, p, c)
}
func TestApprovalLocksOutConcurrentEntry(t *testing.T) {
	f := setup(t)
	p := f.period(f.member)
	current, digest := f.snapshot(p)
	hook := blockingApproval{make(chan struct{}), make(chan struct{})}
	f.mod.writer = hook
	approved := make(chan *httptest.ResponseRecorder, 1)
	inserted := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		approved <- f.call(f.admin, "POST", "/time-periods/"+p.ID+"/approve", approvalBody(current, digest))
	}()
	select {
	case <-hook.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not reach writer")
	}
	go func() { inserted <- f.call(f.member, "POST", "/time-entries", f.manual(p, "EUR")) }()
	close(hook.release)
	requireStatus(t, <-approved, 200)
	requireStatus(t, <-inserted, 409)
	got, _ := f.snapshot(p)
	if got.Approval.TotalSeconds != 0 || got.Revision != 1 {
		t.Fatal("entry crossed sealed snapshot")
	}
}

type failPeriodUpdate struct{}

func (failPeriodUpdate) Append(ctx context.Context, tx pgx.Tx, p tenant.Principal, c events.Change) (events.Event, error) {
	if c.Type == "hours.period_updated" {
		return events.Event{}, errors.New("test second event failure")
	}
	return events.Append(ctx, tx, p, c)
}
func TestSecondEventFailureRollsBackEntryAndFirstEvent(t *testing.T) {
	f := setup(t)
	p := f.period(f.member)
	before := f.eventCount()
	f.mod.writer = failPeriodUpdate{}
	requireStatus(t, f.call(f.member, "POST", "/time-entries", f.manual(p, "EUR")), 500)
	current, _ := f.snapshot(p)
	w := f.call(f.member, "GET", "/time-entries?period_id="+p.ID, nil)
	requireStatus(t, w, 200)
	if current.Revision != 1 || len(decode[[]Entry](t, w)) != 0 || f.eventCount() != before {
		t.Fatal("partial entry/event/revision committed")
	}
}
