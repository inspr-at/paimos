// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Raw legacy fixtures deliberately retain blank/unknown classifications. A
// normal estimate write would fill them with classifier suggestions instead.
func placementNode(t *testing.T, w planningWorld, key string, fields map[string]any) nodeJSON {
	t.Helper()
	n := w.node(t, key, "work", w.root.ID, "open", nil)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, n.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

type placementEUEvidence struct{ expires time.Time }

func (f placementEUEvidence) ResidencyClass(_ context.Context, _ pgx.Tx, a agentaccounts.Account, _ string) (string, string, *time.Time, error) {
	class := "any"
	if a.Harness == "grok" {
		class = "eu"
	}
	return class, "fixture-proof", &f.expires, nil
}

func TestPlanningLinkedViewerAndCanonicalAssignee(t *testing.T) {
	w := planningSetup(t)
	placementCalibration(t, w)
	alias := insertPerson(t, w.admin.TenantID, "Linked viewer")
	d := insertPerson(t, w.admin.TenantID, "Other assignee")
	operator := tenant.Principal{ID: w.agent, TenantID: w.admin.TenantID, Kind: tenant.Agent}
	dbtest.BindRole(t, testDB, operator.TenantID, operator.ID, "admin")
	var grok string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE principals SET linked_to=$2 WHERE id=$1`, alias.ID, w.admin.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'grok-pref-high','1','grok','xai','grok-4.7','high','strong') RETURNING id::text`, w.admin.TenantID).Scan(&grok); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_prices(tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million) VALUES($1,'grok-4.7',100,1,1,1)`, w.admin.TenantID); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override, schedule.Reserve = "sprint", capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		for _, h := range []string{"grok", "claude", "codex"} {
			var account string
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,$2,$2,'placement-runner',$3,$2,now(),true,'fixture',$4) RETURNING id::text`, w.admin.TenantID, h, w.agent, w.admin.ID).Scan(&account); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted')`, w.admin.TenantID, account); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, w.admin.TenantID, w.admin.ID, account, raw); err != nil {
				return err
			}
		}
		eu := "eu"
		scope, err := modelprefs.SaveScope(t.Context(), tx, w.admin, modelprefs.Scope{Level: "person", PersonID: &w.admin.ID, Residency: &eu})
		if err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(t.Context(), tx, "backend", "")
		if err != nil {
			return err
		}
		return modelprefs.PutRow(t.Context(), tx, w.admin, scope, kind.ID, modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "pinned", ProfileID: grok}}})
	}); err != nil {
		t.Fatal(err)
	}
	var tickets []nodeJSON
	for i, assignee := range []string{"", w.agent, alias.ID, d.ID, w.admin.ID} {
		fields := map[string]any{"area": "backend", "complexity": "M", "route_role": "build", "estimate_hours": 2}
		if assignee != "" {
			fields["assignee"] = assignee
		}
		tickets = append(tickets, placementNode(t, w, fmt.Sprintf("LINK-%d", i+1), fields))
	}
	for i := range 5 {
		n := w.node(t, fmt.Sprintf("GROKHIST-%d", i+1), "work", w.root.ID, "done", nil)
		w.session(t, n.ID, "grok", "grok-4.7", "high", "grok-4.7", 60, 1_000_000, 0, 0, "api", "")
	}
	if _, err := testDB.Admin.Exec(t.Context(), `UPDATE harness_sessions SET created_at='2026-09-30T12:00:00Z',stopped_at='2026-09-30T13:00:00Z' WHERE tenant_id=$1`, w.admin.TenantID); err != nil {
		t.Fatal(err)
	}
	evidence := placementEUEvidence{expires: time.Now().Add(time.Hour)}
	page := func(p tenant.Principal, order string) nodePage {
		t.Helper()
		mux := http.NewServeMux()
		New(appPool, nil).Mount(mux)
		r := httptest.NewRequest("GET", "/api/nodes?within="+w.root.ID+"&kind=work&state=open&limit=100&sort="+order, nil)
		r = r.WithContext(agentaccounts.WithResidencyClassifier(tenant.WithPrincipal(r.Context(), p), evidence))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return decode[nodePage](t, rec.Code, rec.Body.Bytes(), 200)
	}
	for _, order := range []string{"tokens", "-tokens", "list_cost", "-list_cost", "paid", "-paid", "model", "-model"} {
		a, c := page(alias, order), page(w.admin, order)
		if !reflect.DeepEqual(a.Items, c.Items) {
			t.Fatalf("linked viewer differs from canonical viewer on %s", order)
		}
		byID := map[string]*planningView{}
		for _, row := range a.Items {
			byID[row.ID] = row.Planning
		}
		for _, i := range []int{0, 1, 2, 4} {
			v := byID[tickets[i].ID]
			if v == nil || v.Route == nil || v.Route.Profile != "grok-pref-high" || v.Route.SetBy != "person" || !v.Route.Pinned || v.Route.Effort != "high" || v.Tokens.Calibration.TokensPerHour != 1_000_000 || *v.Tokens.Estimated != 2_000_000 || *v.Cost.ListEstimated != "2.000000" {
				t.Fatalf("canonical You preview %d on %s: %+v", i, order, v)
			}
			if !reflect.DeepEqual(v, byID[tickets[4].ID]) {
				t.Fatalf("assignee fallback differs from canonical assignment %d", i)
			}
		}
		if v := byID[tickets[3].ID]; v.Route == nil || v.Route.Profile != "claude-opus-high" || v.Route.SetBy != "" || v.Tokens.Calibration.TokensPerHour != 8_000_000 {
			t.Fatalf("D gained the viewer's You preference: %+v", v)
		}
	}
	for _, row := range page(operator, "tokens").Items {
		if row.ID == tickets[0].ID || row.ID == tickets[1].ID || row.ID == tickets[3].ID {
			if row.Planning.Route == nil || row.Planning.Route.Profile != "claude-opus-high" || row.Planning.Route.SetBy != "" {
				t.Fatal("operator preview gained You scope", row.Planning)
			}
		}
	}
	ctx := agentaccounts.WithResidencyClassifier(t.Context(), evidence)
	if err := db.InTenant(dbtest.Seed(ctx), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		for _, n := range tickets {
			if err := CapturePlanningStart(ctx, tx, n.ID, "session"); err != nil {
				return err
			}
		}
		var same bool
		if err := tx.QueryRow(ctx, `SELECT (SELECT snapshot FROM ticket_estimate_snapshots WHERE ticket_node_id=$1)=(SELECT snapshot FROM ticket_estimate_snapshots WHERE ticket_node_id=$2)`, tickets[2].ID, tickets[4].ID).Scan(&same); err != nil {
			return err
		}
		if !same {
			t.Fatal("linked and canonical assignee snapshots differ")
		}
		var profile string
		if err := tx.QueryRow(ctx, `SELECT snapshot->'route'->>'profile' FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, tickets[0].ID).Scan(&profile); err != nil {
			return err
		}
		if profile != "claude-opus-high" {
			t.Fatal("unassigned snapshot borrowed viewer preference", profile)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// These recorded rates differ by role and from any-route. Fixed timestamps
// ensure the equality test cannot pass merely because all rates are 5M/h.
func placementCalibration(t *testing.T, w planningWorld) {
	t.Helper()
	for _, model := range []string{"gpt-6-astra", "opus"} {
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO model_prices(tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million) VALUES($1,$2,100,1,1,1)`, w.admin.TenantID, model)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10 {
		harness, model, effort, tokens := "codex", "gpt-6-astra", "xhigh", int64(2_000_000)
		if i >= 5 {
			harness, model, effort, tokens = "claude", "opus", "high", 8_000_000
		}
		n := w.node(t, fmt.Sprintf("HISTORY-%d", i+1), "work", w.root.ID, "done", nil)
		w.session(t, n.ID, harness, model, effort, model, 60, tokens, 0, 0, "api", "")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET created_at='2026-09-30T12:00:00Z',stopped_at='2026-09-30T13:00:00Z'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningEmptyMatrixLegacyEqualityAndSecurityChange(t *testing.T) {
	w := planningSetup(t)
	placementCalibration(t, w)
	type expected struct {
		node                     nodeJSON
		area, role, gap, profile string
		rate, cost               int64
	}
	var want []expected
	areas := []string{"", "firmware", "review", "other", "backend", "frontend", "full-stack", "infra", "design", "docs"}
	for i, area := range areas {
		for j, role := range []string{"build", "build-hard", "review-gate", "mechanical", ""} {
			fields := map[string]any{"area": area, "route_role": role, "complexity": "M", "estimate_hours": 2}
			n := placementNode(t, w, fmt.Sprintf("EQUAL-%d", i*5+j+1), fields)
			e := expected{node: n, area: area, role: role, rate: 5_000_000, cost: 10}
			known := i >= 4
			switch {
			case role == "":
			case !known:
				e.gap = "area"
			case role == "review-gate":
				e.gap = "review_gate"
			case role == "mechanical":
				e.gap = "registry"
			case role == "build":
				e.profile, e.rate, e.cost = "claude-opus-high", 8_000_000, 16
			case role == "build-hard":
				e.profile, e.rate, e.cost = "codex-astra-xhigh", 2_000_000, 4
			}
			want = append(want, e)
		}
	}
	path := "/api/nodes?within=" + w.root.ID + "&q=EQUAL-&limit=100"
	views := planningOf(t, w.admin, path+"&sort=key")
	var total int64
	for _, e := range want {
		v := views[e.node.Key]
		if v == nil || v.RouteGap != e.gap || v.Tokens.Calibration == nil || v.Tokens.Calibration.TokensPerHour != e.rate || *v.Tokens.Estimated != 2*e.rate || v.Cost == nil || *v.Cost.ListEstimated != fmt.Sprintf("%d.000000", e.cost) {
			t.Fatalf("legacy row %s: %+v", e.node.Key, v)
		}
		if e.profile == "" {
			if v.Route != nil {
				t.Fatalf("gap picked model: %+v", v)
			}
		} else if v.Route == nil || v.Route.Profile != e.profile || v.Route.SetBy != "" {
			t.Fatalf("legacy model changed: %+v", v)
		}
		total += e.cost
	}
	for _, order := range []string{"tokens", "-tokens", "list_cost", "-list_cost", "paid", "-paid", "model", "-model"} {
		ordered := append([]expected{}, want...)
		value := func(e expected) int64 {
			if order == "tokens" || order == "-tokens" {
				return e.rate
			}
			return e.cost
		}
		sort.Slice(ordered, func(i, j int) bool {
			a, b := ordered[i], ordered[j]
			if order == "model" || order == "-model" {
				x, y := a.profile, b.profile
				if x == "" || y == "" {
					if x != y {
						return y == ""
					}
				} else if x != y {
					if order == "-model" {
						return x > y
					}
					return x < y
				}
			} else if order == "paid" || order == "-paid" {
				if (a.profile == "") != (b.profile == "") {
					return b.profile == ""
				}
				if a.profile != "" && value(a) != value(b) {
					if order == "-paid" {
						return value(a) > value(b)
					}
					return value(a) < value(b)
				}
			} else if value(a) != value(b) {
				if order[0] == '-' {
					return value(a) > value(b)
				}
				return value(a) < value(b)
			}
			return a.node.ID < b.node.ID
		})
		keys := []string{}
		for _, e := range ordered {
			keys = append(keys, e.node.Key)
		}
		if got := listKeys(t, w.admin, path+"&sort="+order); !slices.Equal(got, keys) {
			t.Fatalf("legacy %s order: %v != %v", order, got, keys)
		}
	}
	// An independent pre-502c snapshot envelope, with known fixture rates and
	// exact price-row fields, must equal the stored JSON for every legacy row.
	// AEON-502e adds history evidence; its seeded tests verify those new fields.
	// AEON-503 also freezes the fixture's classification without changing rates.
	for _, e := range want {
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
			if err := CapturePlanningStart(t.Context(), tx, e.node.ID, "session"); err != nil {
				return err
			}
			var raw []byte
			if err := tx.QueryRow(t.Context(), `SELECT snapshot FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, e.node.ID).Scan(&raw); err != nil {
				return err
			}
			hours, tokens, cost, rate := 2.0, 2*e.rate, fmt.Sprintf("%d.000000", e.cost), fmt.Sprint(e.cost/2)
			classification, err := json.Marshal(map[string]string{"area": e.area, "route_role": e.role, "complexity": "M"})
			if err != nil {
				return err
			}
			legacy := planningSnapshot{WorkClassification: classification, Source: "session", Hours: &hours, Tokens: &tokens, Cost: &cost, Route: views[e.node.Key].Route,
				RateBasis: estimateRateBasis{planningCalibration: planningCalibration{Basis: "median", Tickets: 5, TokensPerHour: e.rate, AnyRoute: e.profile == ""}, TokensPerHourExact: fmt.Sprint(e.rate), ListPerHour: &rate, CachedMix: mixCached, InputMix: mixInput, OutputMix: mixOutput}}
			if e.profile == "" {
				legacy.RateBasis.Tickets = 10
			} else {
				version, price := int64(100), "1.000000"
				legacy.RateBasis.PriceVersion = &version
				legacy.RateBasis.Input = &price
				legacy.RateBasis.Output = &price
				legacy.RateBasis.Cached = &price
			}
			expectedRaw, _ := json.Marshal(legacy)
			var a, b any
			if err := json.Unmarshal(raw, &a); err != nil {
				return err
			}
			_ = json.Unmarshal(expectedRaw, &b)
			actual := a.(map[string]any)
			delete(actual, "model_estimate")
			evidence := actual["rate_basis"].(map[string]any)
			delete(evidence, "basis_text")
			delete(evidence, "level")
			if evidence["speed"] != float64(1) {
				t.Fatalf("legacy speed is not neutral: %v", evidence["speed"])
			}
			delete(evidence, "speed")
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("%s snapshot changed: %s != %s", e.node.Key, raw, expectedRaw)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	security := placementNode(t, w, "EQUAL-51", map[string]any{"route_role": "build-hard", "area": "security", "complexity": "M", "estimate_hours": 2})
	twin := placementNode(t, w, "EQUAL-52", map[string]any{"route_role": "build-hard", "area": "backend", "complexity": "M", "estimate_hours": 2})
	// Explicit historical security baseline: area gap, 5M/h, $10, no route.
	before := expected{gap: "area", rate: 5_000_000, cost: 10}
	views = planningOf(t, w.admin, path+"&sort=list_cost")
	s, b := views[security.Key], views[twin.Key]
	// The model and rates are equal; learning evidence names each distinct kind.
	sLegacy, bLegacy := *s, *b
	sLegacy.ModelEstimate, bLegacy.ModelEstimate = nil, nil
	if s.RouteGap != "" || s.Route == nil || s.Tokens.Calibration.AnyRoute || *s.Tokens.Estimated != 4_000_000 || *s.Cost.ListEstimated != "4.000000" || !reflect.DeepEqual(sLegacy, bLegacy) || before.gap == s.RouteGap || before.rate == s.Tokens.Calibration.TokensPerHour || before.cost == 4 {
		t.Fatalf("security did not change to twin: %+v %+v", s, b)
	}
	var afterTotal int64
	for _, v := range views {
		var usd int64
		_, _ = fmt.Sscanf(*v.Cost.ListEstimated, "%d.", &usd)
		afterTotal += usd
	}
	if afterTotal != total+8 {
		t.Fatalf("totals %d != %d + 2*4", afterTotal, total)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		for _, id := range []string{security.ID, twin.ID} {
			if err := CapturePlanningStart(t.Context(), tx, id, "session"); err != nil {
				return err
			}
		}
		var same bool
		// Rates and the legacy envelope stay equal; the immutable classifications
		// must retain each ticket's actual area. Check both exact classifications.
		if err := tx.QueryRow(t.Context(), `SELECT
            s.snapshot-'model_estimate'=jsonb_set(b.snapshot-'model_estimate','{work_classification}',
                '{"area":"security","route_role":"build-hard","complexity":"M"}'::jsonb)
            AND b.snapshot->'work_classification'='{"area":"backend","route_role":"build-hard","complexity":"M"}'::jsonb
            FROM ticket_estimate_snapshots s CROSS JOIN ticket_estimate_snapshots b
            WHERE s.ticket_node_id=$1 AND b.ticket_node_id=$2`, security.ID, twin.ID).Scan(&same); err != nil {
			return err
		}
		if !same {
			t.Fatal("security snapshot differs from backend twin")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTicketAreasAreProjectScopedAndSystemRowsStaySeparate(t *testing.T) {
	w := planningSetup(t)
	other := mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Other"}`)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO work_kinds(tenant_id,slug,label,project_id,created_by) VALUES($1,'firmware','Firmware',$2,$3)`, w.admin.TenantID, w.root.ID, w.admin.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		area, project string
		ok            bool
	}{{"security", w.root.ID, true}, {"firmware", w.root.ID, true}, {"firmware", other.ID, false}, {"review", w.root.ID, false}, {"other", w.root.ID, false}} {
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
			got, err := modelregistry.KnownRouteArea(t.Context(), tx, tc.area, tc.project)
			if err == nil && got != tc.ok {
				t.Fatalf("area %+v: %v", tc, got)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Area","fields":{"area":%q}}`, kindBySlug(t, w.admin, "work").ID, tc.project, tc.area)
		code, raw := call(t, &w.admin, "POST", "/api/nodes", body)
		if tc.ok && code != 201 || !tc.ok && code != 400 {
			t.Fatalf("area %+v: %d %s", tc, code, raw)
		}
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_kinds SET archived_at=now() WHERE slug='firmware'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	code, _ := call(t, &w.admin, "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Archived","fields":{"area":"firmware"}}`, kindBySlug(t, w.admin, "work").ID, w.root.ID))
	if code != 400 {
		t.Fatalf("archived area accepted: %d", code)
	}
}
