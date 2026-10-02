// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/jackc/pgx/v5"
)

// Raw legacy fixtures deliberately retain blank/unknown classifications. A
// normal estimate write would fill them with classifier suggestions instead.
func placementNode(t *testing.T, w planningWorld, key string, fields map[string]any) nodeJSON {
	t.Helper()
	n := w.node(t, key, "ticket", w.root.ID, "open", nil)
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
		n := w.node(t, fmt.Sprintf("HISTORY-%d", i+1), "ticket", w.root.ID, "done", nil)
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
			legacy := planningSnapshot{Source: "session", Hours: &hours, Tokens: &tokens, Cost: &cost, Route: views[e.node.Key].Route,
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
	if s.RouteGap != "" || s.Route == nil || s.Tokens.Calibration.AnyRoute || *s.Tokens.Estimated != 4_000_000 || *s.Cost.ListEstimated != "4.000000" || !reflect.DeepEqual(s, b) || before.gap == s.RouteGap || before.rate == s.Tokens.Calibration.TokensPerHour || before.cost == 4 {
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
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT snapshot FROM ticket_estimate_snapshots WHERE ticket_node_id=$1)=(SELECT snapshot FROM ticket_estimate_snapshots WHERE ticket_node_id=$2)`, security.ID, twin.ID).Scan(&same); err != nil {
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
		body := fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Area","fields":{"area":%q}}`, kindBySlug(t, w.admin, "ticket").ID, tc.project, tc.area)
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
	code, _ := call(t, &w.admin, "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Archived","fields":{"area":"firmware"}}`, kindBySlug(t, w.admin, "ticket").ID, w.root.ID))
	if code != 400 {
		t.Fatalf("archived area accepted: %d", code)
	}
}
