// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func routingFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.at = time.Now().UTC()
	modelregistry.New(f.d.App).Mount(f.mux)
	f.call(t, f.person, "GET", "/api/model-preferences/board", nil, 200, nil)
	f.tx(t, func(tx pgx.Tx) error {
		ctx := t.Context()
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields='{"area":"backend","estimate_hours":4}' WHERE id=$1`, f.ticket); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE model_pref_profiles SET thinking='deep' WHERE scope='workspace'`); err != nil {
			return err
		}
		for _, column := range []string{"backend", "design", "review:openai"} {
			rank := []string{"openai:sol", "anthropic:opus"}
			if column == "design" || column == "review:openai" {
				rank = []string{"anthropic:opus", "openai:sol"}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank)
 SELECT $1,id,$2,'first',$3 FROM model_pref_profiles WHERE scope='workspace'`, f.person.TenantID, column, rank); err != nil {
				return err
			}
		}
		for _, harness := range []string{"codex", "claude", "grok"} {
			var account string
			if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner)
 VALUES($1,$2,$2,'routing-fixture',$3,'Routing fixture',$4,true,'routing-generation',$5) RETURNING id::text`, f.person.TenantID, harness, f.agent.ID, f.at, f.person.ID).Scan(&account); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model)
 VALUES($1,$2,$3,$4,'requests',1000,'unrestricted')`, f.person.TenantID, account, f.at.Add(-time.Hour), f.at.Add(24*time.Hour)); err != nil {
				return err
			}
			schedule := capacity.DefaultSchedule()
			schedule.Override, schedule.Reserve = "sprint", capacity.ReserveOff
			raw, _ := json.Marshal(schedule)
			if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule)
 VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, f.person.TenantID, f.person.ID, account, raw); err != nil {
				return err
			}
		}
		return nil
	})
	return f
}

func routePath(f *fixture) string { return "/api/projects/" + f.project + "/routing-decisions" }

func roundRequest(f *fixture, number int, kind string, fix int) RoutingRequest {
	return RoutingRequest{RoundID: fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", number), TicketID: f.ticket, Kind: kind, FixRound: fix}
}

func routeProfile(t *testing.T, got RoutingDecision, harness, effort, situation, column string) {
	t.Helper()
	p := got.Route.Profile
	if p == nil || p.Harness != harness || p.Effort != effort || got.Route.Trace.Situation != situation || got.Route.Trace.Column != column || got.EventID == 0 || got.Mode != "shadow" {
		t.Fatalf("wrong shadow route: %+v; profile %+v", got, p)
	}
}

func saveRoutingPlan(t *testing.T, f *fixture, raw string) {
	t.Helper()
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,$3,$4::jsonb)
 ON CONFLICT(tenant_id,principal_id,key) DO UPDATE SET value=excluded.value`, f.person.TenantID, f.person.ID, agentplan.PreferenceKey, raw)
		return err
	})
}

// Risk: queue rounds could ignore preferences, route a disabled harness, reuse
// ticket escalation history or bypass design/review capability constraints.
func TestShadowRoutingRoundSituationsAndPolicy(t *testing.T) {
	f := routingFixture(t)
	settings := "/api/projects/" + f.project + "/routing-settings"
	var off RoutingSettings
	f.call(t, f.person, "GET", settings, nil, 200, &off)
	if off.Mode != "off" {
		t.Fatal("new projects must default off", off)
	}
	f.call(t, f.person, "POST", routePath(f), roundRequest(f, 1, "first_build", 0), 409, nil)
	f.call(t, f.person, "PUT", settings, RoutingSettings{Mode: "act"}, 400, nil)
	f.call(t, f.person, "PUT", settings, RoutingSettings{Mode: "shadow"}, 200, nil)
	for i, tc := range []struct {
		kind      string
		fix       int
		harness   string
		effort    string
		situation string
		column    string
	}{
		{"first_build", 0, "codex", "xhigh", "first", "backend"},
		{"fix", 1, "codex", "high", "fix", "backend"},
		{"fix", 3, "codex", "high", "fix", "backend"},
		{"fix", 4, "claude", "xhigh", "stuck", "backend"},
		{"merge", 0, "codex", "high", "fix", "backend"},
		{"land", 0, "codex", "high", "fix", "backend"},
		{"design", 0, "claude", "xhigh", "first", "design"},
		{"review", 0, "claude", "xhigh", "first", "review:openai"},
	} {
		in := roundRequest(f, i+2, tc.kind, tc.fix)
		in.PreviousFamily, in.AuthorFamily = "openai", "openai"
		var got RoutingDecision
		f.call(t, f.person, "POST", routePath(f), in, 200, &got)
		routeProfile(t, got, tc.harness, tc.effort, tc.situation, tc.column)
	}
	// Ticket history must not change an explicitly queued first/fix round.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"area":"frontend","fix_round":9}'::jsonb WHERE id=$1`, f.ticket)
		return err
	})
	in := roundRequest(f, 20, "first_build", 0)
	var design RoutingDecision
	f.call(t, f.person, "POST", routePath(f), in, 200, &design)
	routeProfile(t, design, "claude", "xhigh", "first", "design")
	if !design.DesignFirst {
		t.Fatal("frontend first build skipped design")
	}
	in.RoundID, in.DesignReady = roundRequest(f, 21, "first_build", 0).RoundID, true
	var build RoutingDecision
	f.call(t, f.person, "POST", routePath(f), in, 200, &build)
	routeProfile(t, build, "codex", "xhigh", "first", "frontend")
	if build.DesignFirst {
		t.Fatal("approved-design observation was ignored")
	}
	// Off filters the actual harness, with no rerouting merely for total=0.
	saveRoutingPlan(t, f, `{"total":0,"limits":{"claude":"off"}}`)
	in = roundRequest(f, 22, "design", 0)
	in.Observed = &RoutingTarget{Harness: "codex", Model: "gpt-6.1-sol", Effort: "xhigh"}
	var fallback RoutingDecision
	f.call(t, f.person, "POST", routePath(f), in, 200, &fallback)
	routeProfile(t, fallback, "codex", "xhigh", "first", "design")
	if fallback.Comparison != "match" || len(fallback.Route.Trace.Held) == 0 || !strings.Contains(fallback.Route.Trace.Held[0].Reason, "off in the plan") || fallback.Plan.Total != 0 {
		t.Fatal("plan-off comparison lost its explanation", fallback)
	}
	in.RoundID = roundRequest(f, 23, "design", 0).RoundID
	in.Observed.Effort = "high"
	f.call(t, f.person, "POST", routePath(f), in, 200, &fallback)
	if fallback.Comparison != "mismatch" {
		t.Fatal("mismatch reported as agreement")
	}
	// No-tools Grok cannot be promoted into a build by a pin or plan fallback.
	saveRoutingPlan(t, f, `{"total":15,"limits":{"claude":"off","codex":"off","pi":"off"}}`)
	in = roundRequest(f, 24, "design", 0)
	in.Observed = &RoutingTarget{Harness: "grok", Model: "grok-4.7", Effort: "xhigh"}
	f.call(t, f.person, "POST", routePath(f), in, 200, &fallback)
	if fallback.Route.Profile != nil || fallback.Route.Trace.Blocked == "" || fallback.Comparison != "blocked" {
		t.Fatal("unavailable route reported success", fallback)
	}
	saveRoutingPlan(t, f, `{"total":15,"limits":{"claude":false}}`)
	f.call(t, f.person, "POST", routePath(f), roundRequest(f, 25, "design", 0), 503, nil)
	// Shadow observations never mutate runs, account reservations or work.
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent_runs)+(SELECT count(*) FROM account_reservations)`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("shadow routing started or reserved work", count)
		}
		return nil
	})
}

func routingHTTP(f *fixture, p tenant.Principal, in RoutingRequest, ctx context.Context) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest(http.MethodPost, routePath(f), strings.NewReader(string(raw)))
	r = r.WithContext(tenant.WithPrincipal(ctx, p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

// Risk: retries could append conflicting decisions or reveal a foreign tenant
// or project. Real tenant-lock barriers prove simultaneous retries serialize.
func TestShadowRoutingEventReplayAndTenantBoundary(t *testing.T) {
	f := routingFixture(t)
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/routing-settings", RoutingSettings{Mode: "shadow"}, 200, nil)
	in := roundRequest(f, 1, "first_build", 0)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	holder, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err := holder.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { results <- routingHTTP(f, f.person, in, ctx) }()
	}
	for {
		var waiting int
		if err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) AND query=$2`, holder.Conn().PgConn().PID(), `SELECT id FROM tenants WHERE id=current_setting('aeon.tenant_id')::uuid FOR NO KEY UPDATE`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var first RoutingDecision
	for range 2 {
		response := dbtest.Await(t, ctx, results)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		var got RoutingDecision
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if first.EventID == 0 {
			first = got
		} else if !reflect.DeepEqual(first, got) {
			t.Fatal("concurrent retries did not return the same decision")
		}
	}
	in.Kind = "design"
	f.call(t, f.person, "POST", routePath(f), in, 409, nil)
	f.call(t, f.foreign, "GET", routePath(f)+"/"+in.RoundID, nil, 404, nil)
	f.call(t, f.foreign, "POST", routePath(f), in, 404, nil)
	var other string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'OTHER-1',id,'Other project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&other)
	})
	in.RoundID = roundRequest(f, 2, "design", 0).RoundID
	in.TicketID = other
	f.call(t, f.person, "POST", routePath(f), in, 404, nil)
	// A scope on a built-in agent role never implicitly authorizes routing.
	f.agent.Scopes = []string{"delivery.route", agentplan.ReadScope}
	f.agent.KeyCreatorID = f.person.ID
	f.call(t, f.agent, "POST", routePath(f), roundRequest(f, 3, "design", 0), 403, nil)
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/routing-settings", RoutingSettings{Mode: "off"}, 200, nil)
	var saved RoutingDecision
	f.call(t, f.person, "GET", routePath(f)+"/"+first.Request.RoundID, nil, 200, &saved)
	if !reflect.DeepEqual(first, saved) {
		t.Fatal("event projection changed after disabling routing")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='delivery.routing_decided'`).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("rejected requests or retries appended decisions", count)
		}
		return nil
	})
}

// Risk: a routing grant revoked while a request waits could still append an
// event. The observed tenant-lock wait proves revocation precedes final authz.
func TestShadowRoutingRechecksRevocationUnderFence(t *testing.T) {
	f := routingFixture(t)
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/routing-settings", RoutingSettings{Mode: "shadow"}, 200, nil)
	reader := tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Person}
	var role string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Router') RETURNING id::text`, reader.TenantID).Scan(&reader.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'router','Router') RETURNING id::text`, reader.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'delivery.route'),($1,$2,'agents.plan.read'),($1,$2,'nodes.read')`, reader.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, reader.TenantID, reader.ID, role)
		return err
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	holder, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err := holder.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, reader.TenantID); err != nil {
		t.Fatal(err)
	}
	results := make(chan *httptest.ResponseRecorder, 1)
	go func() { results <- routingHTTP(f, reader, roundRequest(f, 1, "design", 0), ctx) }()
	dbtest.WaitForLock(t, ctx, f.d, holder.Conn().PgConn().PID(), "transactionid")
	if _, err := holder.Exec(ctx, `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission='delivery.route'`, reader.TenantID, role); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	response := dbtest.Await(t, ctx, results)
	if response.Code != 403 || !strings.Contains(response.Body.String(), "permission") {
		t.Fatal("revoked routing permission wrote a decision", response.Code, response.Body.String())
	}
	f.call(t, f.person, "GET", routePath(f)+"/"+roundRequest(f, 1, "design", 0).RoundID, nil, 404, nil)
}
