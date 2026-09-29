// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/usagedashboard"
)

func uid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type world struct {
	db                           *dbtest.DB
	mux                          *http.ServeMux
	home, admin, member, foreign tenant.Principal
	agent                        string
}

func (w *world) tx(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{db: dbtest.Open(t), mux: http.NewServeMux()}
	w.home = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	w.admin = tenant.Principal{ID: uid(), TenantID: w.home.TenantID, Kind: tenant.Person}
	w.member = tenant.Principal{ID: uid(), TenantID: w.home.TenantID, Kind: tenant.Person}
	w.foreign = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	w.agent = uid()
	for _, p := range []tenant.Principal{w.home, w.foreign} {
		err := db.InTenant(dbtest.Seed(t.Context()), w.db.Admin, p.TenantID, func(tx pgx.Tx) error {
			_, e := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Usage')`, p.TenantID, "u-"+p.TenantID[:8])
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{w.home, w.admin, w.member, w.foreign} {
		w.tx(t, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person',$3)`, p.TenantID, p.ID, "person")
			return err
		})
	}
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','worker')`, w.home.TenantID, w.agent)
		return err
	})
	dbtest.BindRole(t, w.db, w.home.TenantID, w.home.ID, "owner")
	dbtest.BindRole(t, w.db, w.foreign.TenantID, w.foreign.ID, "owner")
	usagedashboard.New(w.db.App).Mount(w.mux)
	return w
}

func (w *world) project(t *testing.T, p tenant.Principal, key, title string) string {
	t.Helper()
	id := uid()
	w.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,$3,id,$4 FROM node_kinds WHERE slug='project'`, p.TenantID, id, key, title)
		return err
	})
	return id
}

func (w *world) ticket(t *testing.T, p tenant.Principal, project, key, title string) string {
	t.Helper()
	id := uid()
	w.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,$3,id,$4,$5 FROM node_kinds WHERE slug='ticket'`, p.TenantID, id, key, title, project)
		return err
	})
	return id
}

type modelUsage struct {
	model           string
	in, out, cached *int64
	cost            *string
	provisional     bool
	billing         string
	subscription    *string
}

func (w *world) session(t *testing.T, p tenant.Principal, project string, ticket *string, at time.Time, decoy string, runID *string, reports []modelUsage, digest byte) string {
	t.Helper()
	id := uid()
	shape := "unknown"
	if ticket != nil {
		shape = "ship"
	}
	management := "unmanaged"
	var run any
	if runID != nil {
		management = "managed"
		run = *runID
	}
	w.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,ticket_node_id,run_id,harness,host,management,role,work_shape,ref_digest,lease_digest,model,account_label,created_at)
			VALUES($1,$2,$3,$4,$5,$6,'codex','builder',$7,'worker',$8,$9,$9,$10,'session-account-decoy',$11)`,
			p.TenantID, id, project, w.agent, ticket, run, management, shape, []byte{digest}, decoy, at)
		return err
	})
	for i, report := range reports {
		var price any
		if report.cost != nil {
			price = 1
		}
		w.tx(t, p, func(tx pgx.Tx) error {
			if report.cost != nil {
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_prices(tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million)
					VALUES($1,$2,1,1,1,1)
					ON CONFLICT (tenant_id, model, version) DO NOTHING`, p.TenantID, report.model); err != nil {
					return err
				}
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_session_usage(tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,price_version,estimated_cost_usd,billing_mode,subscription_label,reported_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
				p.TenantID, id, report.model, i+1, report.in, report.out, report.cached, report.provisional, price, report.cost, report.billing, report.subscription, at)
			return err
		})
	}
	return id
}

func (w *world) deliver(t *testing.T, p tenant.Principal, session string, at time.Time) {
	t.Helper()
	w.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped', stopped_at=$2, stop_reason='completed' WHERE id=$1`, session, at)
		return err
	})
}

func (w *world) managedRun(t *testing.T, p tenant.Principal, project string) string {
	t.Helper()
	orderNode, run := uid(), uid()
	w.tx(t, p, func(tx pgx.Tx) error {
		orderKey := "WOR" + strings.ToUpper(strings.ReplaceAll(orderNode, "-", ""))[:4] + "-1"
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,$3,id,$4,$5 FROM node_kinds WHERE slug='work_order'`, p.TenantID, orderNode, orderKey, "Order", project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'done')`, p.TenantID, orderNode, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,status,input_tokens,output_tokens,cost_micros,started_at,ended_at)
			VALUES($1,$2,$3,$4,'completed',999999,999999,999999,$5,$5)`, p.TenantID, run, orderNode, w.agent, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,input_tokens_delta,output_tokens_delta,cost_micros_delta)
			VALUES($1,$2,1,'usage',888888,888888,888888)`, p.TenantID, run)
		return err
	})
	return run
}

func (w *world) allowanceWindow(t *testing.T, key, label string, allowance, used, reserved int64) (string, string) {
	t.Helper()
	var account, window string
	w.tx(t, w.home, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label)
			VALUES($1,$2,'codex','daemon-ud1',$3,$4) RETURNING id::text`, w.home.TenantID, key, w.agent, label).Scan(&account); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,reserved,pace_model,burst_ratio)
			VALUES($1,$2,now()-interval '1 hour',now()+interval '1 hour','tokens',$3,$4,$5,'unrestricted',0.1) RETURNING id::text`,
			w.home.TenantID, account, allowance, used, reserved).Scan(&window)
	})
	return account, window
}

func (w *world) settleWindow(t *testing.T, project, accountID, windowID string, actual int64) {
	t.Helper()
	orderNode, runID := uid(), uid()
	reserved := actual
	if reserved < 1 {
		reserved = 1
	}
	w.tx(t, w.home, func(tx pgx.Tx) error {
		orderKey := "WOR" + strings.ToUpper(strings.ReplaceAll(orderNode, "-", ""))[:4] + "-1"
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,$3,id,$4,$5 FROM node_kinds WHERE slug='work_order'`, w.home.TenantID, orderNode, orderKey, "Order", project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'done')`, w.home.TenantID, orderNode, w.home.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,account_id,status,started_at,ended_at)
			VALUES($1,$2,$3,$4,$5,'completed',$6,$6)`, w.home.TenantID, runID, orderNode, w.agent, accountID, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units,actual_units,state,settled_at)
			VALUES($1,$2,$3,$4,$5,'settled',now())`, w.home.TenantID, runID, windowID, reserved, actual)
		return err
	})
}

func (w *world) get(t *testing.T, p tenant.Principal, raw string) (int, usagedashboard.Dashboard, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, raw, nil).WithContext(tenant.WithPrincipal(context.Background(), p))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, r)
	var page usagedashboard.Dashboard
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, page, rec.Body.String()
}

func i64(v int64) *int64   { return &v }
func str(v string) *string { return &v }

func group(t *testing.T, groups []usagedashboard.UsageGroup, label string) usagedashboard.UsageGroup {
	t.Helper()
	for _, g := range groups {
		if g.Label == label {
			return g
		}
	}
	t.Fatalf("missing group %s in %+v", label, groups)
	return usagedashboard.UsageGroup{}
}

func usd(t *testing.T, value *string) string {
	t.Helper()
	if value == nil {
		t.Fatal("missing estimated cost")
	}
	return *value
}

func TestDashboardAggregatesVisibleSessionsOnly(t *testing.T) {
	w := newWorld(t)
	visible := w.project(t, w.home, "VIS-1", "Visible project")
	hidden := w.project(t, w.home, "HID-1", "Hidden beacon")
	known := w.ticket(t, w.home, visible, "VIS-2", "Known ticket")
	apiTicket := w.ticket(t, w.home, visible, "VIS-3", "API ticket")
	blank := w.ticket(t, w.home, visible, "VIS-4", "Blank ticket")
	secret := w.ticket(t, w.home, hidden, "HID-2", "Hidden beacon ticket")
	foreignProject := w.project(t, w.foreign, "FOR-1", "Foreign project")
	foreignTicket := w.ticket(t, w.foreign, foreignProject, "FOR-2", "Foreign spend")
	foreignAgent := uid()
	w.tx(t, w.foreign, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','foreign worker')`, w.foreign.TenantID, foreignAgent)
		return err
	})
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	run := w.managedRun(t, w.home, visible)
	w.session(t, w.home, visible, &known, at, "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(1000), out: i64(400), cached: i64(50), cost: str("1.250000000000"), billing: "subscription", subscription: str("Codex Pro")},
		{model: "claude-sonnet", in: i64(10), out: i64(5), provisional: true, billing: "subscription", subscription: str("Claude Max")},
	}, 1)
	w.session(t, w.home, visible, nil, at.Add(time.Second), "ghost-model", nil, nil, 2)
	w.session(t, w.home, visible, &apiTicket, at.Add(2*time.Second), "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(2), out: i64(2), cached: i64(2), cost: str("0.000000000001"), billing: "api"},
	}, 3)
	w.session(t, w.home, visible, &known, at.Add(3*time.Second), "session-model-decoy", &run, []modelUsage{
		{model: "gpt-4.1", in: i64(100), out: i64(20), cached: i64(0), cost: str("0.250000000000"), billing: "subscription", subscription: str("Codex Pro")},
	}, 4)
	w.session(t, w.home, visible, &blank, at.Add(4*time.Second), "session-model-decoy", nil, []modelUsage{
		{model: "zero-model", in: i64(0), out: i64(0), cached: i64(0), billing: "unknown"},
	}, 5)
	w.session(t, w.home, visible, &known, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(9), out: i64(9), cached: i64(0), cost: str("99.000000000000"), billing: "subscription", subscription: str("Codex Pro")},
	}, 6)
	w.session(t, w.home, hidden, &secret, at, "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(9), out: i64(9), cached: i64(0), cost: str("9.000000000000"), billing: "subscription", subscription: str("Codex Pro")},
	}, 7)
	homeAgent := w.agent
	w.agent = foreignAgent
	w.session(t, w.foreign, foreignProject, &foreignTicket, at, "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(9), out: i64(9), cached: i64(0), cost: str("8.000000000000"), billing: "subscription", subscription: str("Foreign Sub")},
	}, 8)
	w.agent = homeAgent

	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
			SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, w.home.TenantID, w.member.ID, visible)
		return err
	})
	w.allowanceWindow(t, "ud1-opaque-key", "Pacing window", 1000, 100, 50)
	measuredAccount, measuredWindow := w.allowanceWindow(t, "ud1-measured-key", "Measured window", 1000, 100, 50)
	mixedAccount, mixedWindow := w.allowanceWindow(t, "ud1-mixed-key", "Mixed window", 800, 40, 10)
	w.settleWindow(t, visible, measuredAccount, measuredWindow, 100)
	w.settleWindow(t, visible, mixedAccount, mixedWindow, 40)
	w.settleWindow(t, visible, mixedAccount, mixedWindow, 0)
	dbtest.BindRole(t, w.db, w.home.TenantID, w.admin.ID, "admin")

	code, page, body := w.get(t, w.member, "/api/usage/dashboard?from=2026-09-10T00:00:00Z&to=2026-09-11T00:00:00Z")
	if code != 200 {
		t.Fatalf("member status %d %s", code, body)
	}
	if page.Attribution != "lifetime_for_sessions_started_in_range" || page.TrendBasis != "session_started_utc_day" || page.ListPriceCurrency != "USD" || page.Truncated {
		t.Fatalf("attribution %+v", page)
	}
	if page.Totals.Sessions != 5 || page.Totals.UsageRows != 5 || page.Totals.UnreportedSessions != 1 || page.Totals.TokensState != "partial" || page.Totals.CostState != "partial" || page.Totals.ProvisionalRows != 1 || page.Totals.ProvisionalSessions != 1 || page.Totals.CostUnknownRows != 5 {
		t.Fatalf("totals %+v", page.Totals)
	}
	if page.Totals.InputTokens == nil || *page.Totals.InputTokens != "1112" || page.Totals.InputKnownRows != 5 || page.Totals.InputUnknownRows != 1 || page.Totals.OutputTokens == nil || *page.Totals.OutputTokens != "427" || page.Totals.CachedInputTokens == nil || *page.Totals.CachedInputTokens != "52" || page.Totals.CachedInputKnownRows != 4 || page.Totals.CachedInputUnknownRows != 2 {
		t.Fatalf("tokens %+v", page.Totals)
	}
	if usd(t, page.Totals.EstimatedCostUSD) != "0.000000000001" || page.Totals.CostKnownRows != 1 {
		t.Fatalf("cost %+v", page.Totals)
	}
	if got := group(t, page.ByProject, "Visible project"); got.Sessions != 5 || got.Key != "VIS-1" {
		t.Fatalf("project %+v", got)
	}
	if got := group(t, page.ByModel, "gpt-4.1"); got.Sessions != 3 || usd(t, got.EstimatedCostUSD) != "0.000000000001" || got.CostState != "partial" {
		t.Fatalf("model %+v", got)
	}
	if got := group(t, page.ByModel, "Unreported"); got.Sessions != 1 || got.CostState != "unknown" || got.EstimatedCostUSD != nil {
		t.Fatalf("unreported model %+v", got)
	}
	if got := group(t, page.BySubscription, "Codex Pro"); got.Sessions != 2 || got.BillingMode != "subscription" || got.EstimatedCostUSD != nil || got.CostState != "unknown" {
		t.Fatalf("subscription %+v", got)
	}
	if got := group(t, page.BySubscription, "Claude Max"); got.Sessions != 1 || got.BillingMode != "subscription" || got.CostState != "unknown" || got.ProvisionalRows != 1 {
		t.Fatalf("claude %+v", got)
	}
	if got := group(t, page.BySubscription, "API"); got.Sessions != 1 || got.BillingMode != "api" || usd(t, got.EstimatedCostUSD) != "0.000000000001" {
		t.Fatalf("api %+v", got)
	}
	subscriptionSessions := 0
	for _, item := range page.BySubscription {
		subscriptionSessions += item.Sessions
	}
	if subscriptionSessions != 6 {
		t.Fatalf("subscription groups should count a two-model session twice, got %d", subscriptionSessions)
	}
	if len(page.Trend) != 1 || page.Trend[0].Day != "2026-09-10" || page.Trend[0].Group.Sessions != 5 || page.Trend[0].Group.Label != "" {
		t.Fatalf("trend %+v", page.Trend)
	}
	if page.TicketsCostUnknown != 2 || len(page.Tickets) != 1 {
		t.Fatalf("tickets %d unknown %d", len(page.Tickets), page.TicketsCostUnknown)
	}
	if page.Tickets[0].Key != "VIS-3" || page.Tickets[0].Sessions != 1 || usd(t, page.Tickets[0].EstimatedCostUSD) != "0.000000000001" {
		t.Fatalf("rank %+v", page.Tickets)
	}
	for _, forbidden := range []string{"Hidden beacon", "HID-2", "Foreign spend", "FOR-2", "Foreign Sub", "ud1-opaque-key", "ud1-measured-key", "ud1-mixed-key", "Pacing window", "Measured window", "Mixed window", "session-model-decoy", "ghost-model", "session-account-decoy", "1.250000000000", "0.250000000000", "99.000000000000", "9.000000000000", "8.000000000000", "list_cost_micros", "covered"} {
		if bytes.Contains([]byte(body), []byte(forbidden)) {
			t.Fatalf("member response leaked %s", forbidden)
		}
	}
	if page.Allowance.State != "withheld" || len(page.Allowance.Windows) != 0 {
		t.Fatalf("allowance %+v", page.Allowance)
	}

	code, filtered, body := w.get(t, w.member, "/api/usage/dashboard?from=2026-09-10T00:00:00Z&to=2026-09-11T00:00:00Z&project="+hidden)
	if code != 200 || filtered.Totals.Sessions != 0 || bytes.Contains([]byte(body), []byte("Hidden beacon")) {
		t.Fatalf("hidden project filter %d sessions %d %s", code, filtered.Totals.Sessions, body)
	}

	code, admin, body := w.get(t, w.admin, "/api/usage/dashboard?from=2026-09-10T00:00:00Z&to=2026-09-11T00:00:00Z")
	if code != 200 || !bytes.Contains([]byte(body), []byte("Hidden beacon")) || bytes.Contains([]byte(body), []byte("Foreign spend")) || bytes.Contains([]byte(body), []byte("ud1-opaque-key")) {
		t.Fatalf("admin visibility %d %s", code, body)
	}
	if admin.Totals.Sessions != 6 || admin.Totals.InputTokens == nil || *admin.Totals.InputTokens != "1121" || usd(t, admin.Totals.EstimatedCostUSD) != "0.000000000001" || admin.Totals.CostKnownRows != 1 || admin.Totals.CostState != "partial" {
		t.Fatalf("admin totals %+v", admin.Totals)
	}
	if admin.Allowance.State != "visible" || len(admin.Allowance.Windows) != 3 {
		t.Fatalf("windows %+v", admin.Allowance)
	}
	byLabel := map[string]usagedashboard.AllowanceWindow{}
	for _, window := range admin.Allowance.Windows {
		byLabel[window.Label] = window
	}
	provisional := byLabel["Pacing window"]
	if provisional.Unit != "tokens" || provisional.Allowance != 1000 || provisional.Reserved != 50 || !provisional.Provisional || provisional.Used != nil || provisional.Headroom != nil || provisional.HardRemaining != nil || provisional.PaceCap == nil || *provisional.PaceCap != 1000 {
		t.Fatalf("provisional pace %+v", provisional)
	}
	measured := byLabel["Measured window"]
	if measured.Provisional || measured.Allowance != 1000 || measured.Reserved != 50 || measured.Used == nil || *measured.Used != 100 || measured.PaceCap == nil || *measured.PaceCap != 1000 || measured.Headroom == nil || *measured.Headroom != 850 || measured.HardRemaining == nil || *measured.HardRemaining != 850 {
		t.Fatalf("measured pace %+v", measured)
	}
	mixed := byLabel["Mixed window"]
	if !mixed.Provisional || mixed.Allowance != 800 || mixed.Reserved != 10 || mixed.Used != nil || mixed.Headroom != nil || mixed.HardRemaining != nil || mixed.PaceCap == nil || *mixed.PaceCap != 800 {
		t.Fatalf("mixed pace %+v", mixed)
	}
	if bytes.Count([]byte(body), []byte(`"used":null`)) < 2 || bytes.Contains([]byte(body), []byte(`"used":0`)) || bytes.Contains([]byte(body), []byte(`"hard_remaining":0`)) {
		t.Fatalf("unknown allowance was zero or omitted: %s", body)
	}
	if len(admin.Tickets) != 1 || admin.Tickets[0].Key != "VIS-3" || usd(t, admin.Tickets[0].EstimatedCostUSD) != "0.000000000001" {
		t.Fatalf("admin rank %+v", admin.Tickets)
	}
	for _, forbidden := range []string{"1.250000000000", "0.250000000000", "9.000000000000", "99.000000000000"} {
		if bytes.Contains([]byte(body), []byte(forbidden)) {
			t.Fatalf("admin response summed subscription estimate %s", forbidden)
		}
	}
}

func TestDashboardMissingUsageIsAnError(t *testing.T) {
	w := newWorld(t)
	// Migration 0884 creates the relation. Dropping it here checks the
	// unavailable response, which must not become an empty dashboard.
	if _, err := w.db.App.Exec(t.Context(), `DROP TABLE harness_usage_receipts, harness_session_usage`); err != nil {
		t.Fatal(err)
	}
	project := w.project(t, w.home, "VIS-1", "Visible project")
	w.session(t, w.home, project, nil, time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC), "session-model-decoy", nil, nil, 1)
	code, _, body := w.get(t, w.home, "/api/usage/dashboard?from=2026-09-10&to=2026-09-11")
	if code != http.StatusServiceUnavailable || !bytes.Contains([]byte(body), []byte("session usage is not available")) {
		t.Fatalf("missing usage %d %s", code, body)
	}
	if bytes.Contains([]byte(body), []byte(`"input_tokens"`)) || bytes.Contains([]byte(body), []byte("0.000000000000")) {
		t.Fatalf("missing usage was masked as a dashboard: %s", body)
	}
}

func TestDashboardRatesByVoteSnapshot(t *testing.T) {
	w := newWorld(t)
	project := w.project(t, w.home, "VIS-1", "Visible project")
	ticket := w.ticket(t, w.home, project, "VIS-2", "Known ticket")
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	sessionID := w.session(t, w.home, project, &ticket, at, "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(10), out: i64(2), cached: i64(0), cost: str("1.000000000000"), billing: "api"},
	}, 1)
	second := w.session(t, w.home, project, &ticket, at.Add(time.Minute), "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(4), out: i64(1), cached: i64(0), cost: str("0.250000000000"), billing: "api"},
	}, 2)
	w.deliver(t, w.home, sessionID, at.Add(time.Hour))
	w.deliver(t, w.home, second, at.Add(2*time.Hour))
	for i, voter := range []tenant.Principal{w.home, w.member} {
		var score any
		if i == 0 {
			score = 5
		}
		w.tx(t, w.home, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO agent_delivery_votes
				(tenant_id, session_id, ticket_node_id, voter_principal_id, score, tags, comment, harness, model)
				VALUES ($1,$2,$3,$4,$5,'{}','Needs another pass','codex','vote-model')`, w.home.TenantID, sessionID, ticket, voter.ID, score)
			return err
		})
	}
	code, page, body := w.get(t, w.home, "/api/usage/dashboard?from=2026-09-10T00:00:00Z&to=2026-09-11T00:00:00Z")
	if code != 200 {
		t.Fatalf("status %d %s", code, body)
	}
	if page.Totals.Sessions != 2 || usd(t, page.Totals.EstimatedCostUSD) != "1.250000000000" {
		t.Fatalf("usage totals changed %+v", page.Totals)
	}
	if got := group(t, page.ByModel, "gpt-4.1"); got.Sessions != 2 {
		t.Fatalf("priced model %+v", got)
	}
	if got := group(t, page.ByHarness, "codex"); got.Sessions != 2 {
		t.Fatalf("harness usage %+v", page.ByHarness)
	}
	if page.Ratings.Votes != 2 || page.Ratings.Exceptions != 1 || page.Ratings.Deliveries != 2 || page.Ratings.ReworkRate == nil || *page.Ratings.ReworkRate != "1/2" {
		t.Fatalf("ratings %+v", page.Ratings)
	}
	if page.Ratings.Average == nil || *page.Ratings.Average != "5.00" {
		t.Fatalf("optional score still averages %+v", page.Ratings.Average)
	}
	if len(page.Ratings.ByModel) != 1 || page.Ratings.ByModel[0].Label != "gpt-4.1" || page.Ratings.ByModel[0].Exceptions != 1 || page.Ratings.ByModel[0].Deliveries != 2 || page.Ratings.ByModel[0].ReworkRate == nil || *page.Ratings.ByModel[0].ReworkRate != "1/2" {
		t.Fatalf("rating models %+v", page.Ratings.ByModel)
	}
	if len(page.Ratings.ByHarness) != 1 || page.Ratings.ByHarness[0].Label != "codex" || page.Ratings.ByHarness[0].Votes != 2 || page.Ratings.ByHarness[0].Exceptions != 1 {
		t.Fatalf("rating harness %+v", page.Ratings.ByHarness)
	}
	if strings.Contains(body, "vote-model") || strings.Contains(body, "session-model-decoy") {
		t.Fatalf("rate followed a vote snapshot or the session model instead of the delivery: %s", body)
	}
}

func TestDashboardRatingsCountStoppedDeliveriesOnly(t *testing.T) {
	w := newWorld(t)
	project := w.project(t, w.home, "VIS-1", "Visible project")
	ticket := w.ticket(t, w.home, project, "VIS-2", "Known ticket")
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	stoppedID := w.session(t, w.home, project, &ticket, at, "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(10), out: i64(2), cached: i64(0), cost: str("1.000000000000"), billing: "api"},
	}, 1)
	runningID := w.session(t, w.home, project, &ticket, at.Add(time.Minute), "session-model-decoy", nil, []modelUsage{
		{model: "gpt-4.1", in: i64(4), out: i64(1), cached: i64(0), cost: str("0.250000000000"), billing: "api"},
	}, 2)
	w.deliver(t, w.home, stoppedID, at.Add(time.Hour))
	for _, sessionID := range []string{stoppedID, runningID} {
		w.tx(t, w.home, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO agent_delivery_votes
				(tenant_id, session_id, ticket_node_id, voter_principal_id, score, tags, comment, harness, model)
				VALUES ($1,$2,$3,$4,4,'{}','Still running','codex','vote-model')`, w.home.TenantID, sessionID, ticket, w.home.ID)
			return err
		})
	}
	code, page, body := w.get(t, w.home, "/api/usage/dashboard?from=2026-09-10T00:00:00Z&to=2026-09-11T00:00:00Z")
	if code != 200 {
		t.Fatalf("status %d %s", code, body)
	}
	if page.Totals.Sessions != 2 {
		t.Fatalf("usage still counts the running session %+v", page.Totals)
	}
	if got := group(t, page.ByModel, "gpt-4.1"); got.Sessions != 2 {
		t.Fatalf("priced model %+v", got)
	}
	if page.Ratings.Votes != 1 || page.Ratings.Exceptions != 1 || page.Ratings.Deliveries != 1 || page.Ratings.ReworkRate == nil || *page.Ratings.ReworkRate != "1/1" {
		t.Fatalf("running session counted as a delivery %+v", page.Ratings)
	}
	if len(page.Ratings.ByModel) != 1 || page.Ratings.ByModel[0].Deliveries != 1 || page.Ratings.ByModel[0].Exceptions != 1 {
		t.Fatalf("rating models %+v", page.Ratings.ByModel)
	}
	if len(page.Ratings.ByHarness) != 1 || page.Ratings.ByHarness[0].Deliveries != 1 || page.Ratings.ByHarness[0].Votes != 1 {
		t.Fatalf("rating harness %+v", page.Ratings.ByHarness)
	}
}

func TestDashboardMissingVotesStayEmpty(t *testing.T) {
	w := newWorld(t)
	if _, err := w.db.App.Exec(t.Context(), `DROP TABLE agent_delivery_votes`); err != nil {
		t.Fatal(err)
	}
	project := w.project(t, w.home, "VIS-1", "Visible project")
	at := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	sessionID := w.session(t, w.home, project, nil, at, "session-model-decoy", nil, nil, 1)
	w.deliver(t, w.home, sessionID, at.Add(time.Hour))
	code, page, body := w.get(t, w.home, "/api/usage/dashboard?from=2026-09-10&to=2026-09-11")
	if code != 200 || page.Totals.Sessions != 1 || page.Ratings.Votes != 0 || page.Ratings.Exceptions != 0 || page.Ratings.Deliveries != 1 || page.Ratings.ReworkRate != nil || page.Ratings.Average != nil || page.Ratings.ByModel == nil || page.Ratings.ByHarness == nil {
		t.Fatalf("missing votes %d %+v %s", code, page.Ratings, body)
	}
}

func TestDashboardRejectsBadRange(t *testing.T) {
	w := newWorld(t)
	for _, raw := range []string{
		"/api/usage/dashboard?from=2026-09-11&to=2026-09-10",
		"/api/usage/dashboard?from=2026-01-01&to=2027-02-01",
		"/api/usage/dashboard?from=2026-09-01",
		"/api/usage/dashboard?project=not-a-uuid",
	} {
		code, _, body := w.get(t, w.home, raw)
		if code != http.StatusBadRequest {
			t.Fatalf("%s -> %d %s", raw, code, body)
		}
	}
}
