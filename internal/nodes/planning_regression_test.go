// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestPlanningSourceProjectCosts(t *testing.T) {
	w := planningSetup(t)
	target := w.node(t, "TARGET-1", "work", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	w.session(t, target.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	other := w
	other.root = mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Other project"}`)
	bindProjectRole(t, w.admin.TenantID, w.viewer.ID, "viewer", w.root.ID)
	bindProjectRole(t, w.admin.TenantID, w.viewer.ID, "guest", other.root.ID)
	for i := range 5 {
		n := other.node(t, fmt.Sprintf("PRIVATE-%d", i+1), "work", other.root.ID, "done", nil)
		other.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1_000_000, 0, 0, "api", "")
	}
	// Make the source's actual price unmistakable; it must not calibrate A's cost.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_session_usage u SET estimated_cost_usd=777
            FROM harness_sessions s WHERE s.tenant_id=u.tenant_id AND s.id=u.session_id AND s.project_id=$1`, other.root.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	private := other.node(t, "PRIVATE-6", "work", other.root.ID, "open", nil)
	other.session(t, private.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "subscription", "B confidential plan")
	path := "/api/nodes?within=" + w.root.ID + "&kind=work"
	for _, sort := range []string{"key", "tokens", "list_cost", "paid"} {
		got := planningOf(t, w.viewer, path+"&sort="+sort)[target.Key]
		if got == nil || got.Cost == nil || got.Cost.ListEstimated == nil || got.Cost.PaidEstimated == nil {
			t.Fatalf("missing allowed estimate: %+v", got)
		}
		if len(got.Cost.Plans) != 0 || *got.Cost.PaidEstimated != *got.Cost.ListEstimated {
			t.Fatalf("source billing leaked: %+v", got.Cost)
		}
		// Tokens are intentionally visible; only the public model-price mix may
		// price them without source harness.read (2M tokens at $2.70/M).
		if *got.Cost.ListEstimated != "5.400000" {
			t.Fatalf("source calibration cost leaked: %+v", got.Cost)
		}
		otherView := planningOf(t, w.viewer, "/api/nodes?within="+other.root.ID+"&sort="+sort)[private.Key]
		if otherView == nil || otherView.Cost != nil {
			t.Fatalf("B cost projection: %+v", otherView)
		}
	}
	admin := planningOf(t, w.admin, path)[target.Key]
	if !slices.Contains(admin.Cost.Plans, "B confidential plan") || !strings.HasPrefix(*admin.Cost.ListEstimated, "1553.") && !strings.HasPrefix(*admin.Cost.ListEstimated, "1554.") {
		t.Fatalf("authorized source should contribute: %+v", admin.Cost)
	}
}

// Bulk fixtures keep the regression realistic without hundreds of HTTP writes.
func planningBulk(t *testing.T, w planningWorld, count, sessions int, state, prefix string, reported bool) []string {
	t.Helper()
	kind := kindBySlug(t, w.admin, "work")
	var ids []string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,state,parent_id,position,fields)
            SELECT $1,$2::text||g,$3,$2::text||g,$4,$5,g,'{"estimate_hours":2,"route_role":"build-hard","area":"backend"}'::jsonb
            FROM generate_series(1,$6::int) g RETURNING id::text`, w.admin.TenantID, prefix, kind.ID, state, w.root.ID, count)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO harness_sessions (
            tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,
            ref_digest,lease_digest,phase,activity,model,reasoning_effort,created_at,heartbeat_at,stopped_at,stop_reason)
            SELECT $1,$2,$3,n,'codex','test','unmanaged','worker','ship',
                decode(md5(n::text||g::text),'hex'),decode(md5(g::text||n::text),'hex'),
                'stopped','busy','gpt-6-astra','xhigh',now()-interval '1 hour',now(),now(),'completed'
            FROM unnest($4::uuid[]) n CROSS JOIN generate_series(1,$5::int) g`, w.admin.TenantID, w.root.ID, w.agent, ids, sessions)
		if err != nil || !reported {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO harness_session_usage (tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,billing_mode,subscription_label)
            SELECT tenant_id,id,'gpt-6-astra',1,1000,0,0,false,'subscription','Test plan'
            FROM harness_sessions WHERE tenant_id=$1 AND ticket_node_id=ANY($2::uuid[])`, w.admin.TenantID, ids)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestPlanningCalibrationSkipsIneligibleAndOtherRoutes(t *testing.T) {
	w := planningSetup(t)
	target := w.node(t, "CAL-1", "work", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	for i := range 5 {
		n := w.node(t, fmt.Sprintf("FIN-%d", i+1), "work", w.root.ID, "done", nil)
		w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1_000_000, 0, 0, "api", "")
	}
	planningBulk(t, w, 400, 1, "done", "UNREPORTED-", false)
	// Thirty newer eligible samples from another route must not displace Codex.
	ids := planningBulk(t, w, 30, 1, "done", "OTHER-", true)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET harness='claude',model='opus' WHERE ticket_node_id=ANY($1::uuid[])`, ids)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=CAL-1")[target.Key]
	cal := got.Tokens.Calibration
	if cal == nil || cal.Basis != "median" || cal.Tickets != 5 || math.Abs(float64(cal.TokensPerHour)-1_000_000) > 100 {
		t.Fatalf("newer unusable samples displaced calibration: %+v", cal)
	}
}

func TestPlanningSortUsesDisplayedNumbersAndCursor(t *testing.T) {
	w := planningSetup(t)
	// Both routes are calibrated; hidden defaults are tested separately.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET profile_id=(SELECT id FROM model_profiles WHERE slug='codex-sol-xhigh') WHERE role='build'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		n := w.node(t, fmt.Sprintf("TRAIN-%d", i+1), "work", w.root.ID, "done", nil)
		w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 100_000_000, 0, 0, "api", "")
	}
	for i := range 5 {
		n := w.node(t, fmt.Sprintf("SOLTRAIN-%d", i+1), "work", w.root.ID, "done", nil)
		w.session(t, n.ID, "codex", "gpt-6-sol", "xhigh", "gpt-6-sol", 60, 5_000_000, 0, 0, "api", "")
	}
	high := w.node(t, "SORT-1", "work", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 1})
	low := w.node(t, "SORT-2", "work", w.root.ID, "open", map[string]any{"route_role": "build", "area": "backend", "estimate_hours": 2})
	tie := w.node(t, "SORT-3", "work", w.root.ID, "open", map[string]any{"route_role": "build", "area": "backend", "estimate_hours": 2})
	spent := w.node(t, "SORT-4", "work", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 100})
	w.session(t, spent.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 0, 0, 0, "api", "")
	empty := w.node(t, "SORT-5", "work", w.root.ID, "open", nil)
	ties := []nodeJSON{low, tie}
	slices.SortFunc(ties, func(a, b nodeJSON) int { return strings.Compare(a.ID, b.ID) })
	for _, field := range []string{"tokens", "list_cost", "paid"} {
		for _, desc := range []bool{false, true} {
			sort := field
			want := []string{spent.Key, ties[0].Key, ties[1].Key, high.Key, empty.Key}
			if desc {
				sort = "-" + field
				want = []string{high.Key, ties[0].Key, ties[1].Key, spent.Key, empty.Key}
			}
			path := "/api/nodes?within=" + w.root.ID + "&q=SORT-&sort=" + sort + "&limit=2"
			var got []string
			for cursor := ""; ; {
				page := listPage(t, w.admin, path+cursor)
				for _, item := range page.Items {
					got = append(got, item.Key)
				}
				if page.NextCursor == nil {
					break
				}
				cursor = "&cursor=" + *page.NextCursor
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s: got %v want %v", sort, got, want)
			}
		}
	}
}

func TestPlanningRoundedCostTie(t *testing.T) {
	w := planningSetup(t)
	first := w.node(t, "RND-1", "work", w.root.ID, "open", nil)
	second := w.node(t, "RND-2", "work", w.root.ID, "open", nil)
	w.session(t, first.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	w.session(t, second.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	// Digits past the six the API projects. The lower id holds the larger raw
	// amount, so an unrounded ascending sort would put it second.
	ordered := []nodeJSON{first, second}
	slices.SortFunc(ordered, func(a, b nodeJSON) int { return strings.Compare(a.ID, b.ID) })
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_session_usage u SET estimated_cost_usd=v.cost
			FROM harness_sessions s
			JOIN (VALUES ($2::uuid, 1.0000004::numeric), ($3::uuid, 1.0000001::numeric)) AS v(ticket, cost) ON s.ticket_node_id=v.ticket
			WHERE u.tenant_id=s.tenant_id AND u.session_id=s.id AND s.tenant_id=$1`, w.admin.TenantID, ordered[0].ID, ordered[1].ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	views := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=RND-")
	for _, n := range ordered {
		cost := views[n.Key].Cost
		if cost == nil || cost.ListSpent == nil || cost.PaidSpent == nil || *cost.ListSpent != "1.000000" || *cost.PaidSpent != "1.000000" {
			t.Fatalf("%s projected cost: %+v", n.Key, cost)
		}
	}
	want := []string{ordered[0].Key, ordered[1].Key}
	for _, sort := range []string{"list_cost", "-list_cost", "paid", "-paid"} {
		got := listKeys(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=RND-&sort="+sort)
		if !slices.Equal(got, want) {
			t.Fatalf("%s rounded tie: got %v want %v", sort, got, want)
		}
	}
}

// fixedMicros is the decimal integer of a six-place USD string, without leading zeros.
func fixedMicros(usd string) string {
	if usd == "" {
		return ""
	}
	whole, frac, _ := strings.Cut(usd, ".")
	if len(frac) > 6 {
		frac = frac[:6]
	} else {
		frac += strings.Repeat("0", 6-len(frac))
	}
	digits := strings.TrimLeft(whole+frac, "0")
	if digits == "" {
		return "0"
	}
	return digits
}

// Five usage rows of $2e12 each sum past a bigint of micro-dollars. The list
// still returns, with the integer text, numeric order, and six-place display.
func TestPlanningHugeUsageDoesNotBreakTheList(t *testing.T) {
	w := planningSetup(t)
	huge := w.node(t, "BIG-1", "work", w.root.ID, "open", nil)
	mid := w.node(t, "BIG-2", "work", w.root.ID, "open", nil)
	for range 5 {
		w.session(t, huge.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	}
	w.session(t, mid.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `UPDATE harness_session_usage u SET estimated_cost_usd=2000000000000::numeric
			FROM harness_sessions s
			WHERE u.tenant_id=s.tenant_id AND u.session_id=s.id AND s.tenant_id=$1 AND s.ticket_node_id=ANY($2::uuid[])`,
			w.admin.TenantID, []string{huge.ID, mid.ID})
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 6 {
			return fmt.Errorf("priced %d usage rows, want 6", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		huge.Key: {"10000000000000.000000", "10000000000000000000"},
		mid.Key:  {"2000000000000.000000", "2000000000000000000"},
	}
	for _, sort := range []string{"key", "list_cost", "paid"} {
		views := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=BIG-&sort="+sort)
		for key, pair := range want {
			cost := views[key]
			if cost == nil || cost.Cost == nil {
				t.Fatalf("%s via %s: no cost", key, sort)
			}
			got := func(p *string) string {
				if p == nil {
					return ""
				}
				return *p
			}
			if got(cost.Cost.ListSpent) != pair[0] || got(cost.Cost.PaidSpent) != pair[0] || got(cost.Cost.ListEstimated) != "" || got(cost.Cost.PaidEstimated) != "" {
				t.Fatalf("%s via %s display: list %q/%q paid %q/%q", key, sort, got(cost.Cost.ListSpent), got(cost.Cost.ListEstimated), got(cost.Cost.PaidSpent), got(cost.Cost.PaidEstimated))
			}
			if got(cost.Cost.ListCostMicros) != pair[1] || got(cost.Cost.PaidMicros) != pair[1] {
				t.Fatalf("%s via %s micros: list %q paid %q, want %s", key, sort, got(cost.Cost.ListCostMicros), got(cost.Cost.PaidMicros), pair[1])
			}
		}
	}
	// BIG-2 is one $2e12 row; BIG-1 is five of them. Ascending order follows the numeric.
	for _, field := range []string{"list_cost", "paid"} {
		asc := listKeys(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=BIG-&sort="+field)
		desc := listKeys(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=BIG-&sort=-"+field)
		if !slices.Equal(asc, []string{mid.Key, huge.Key}) || !slices.Equal(desc, []string{huge.Key, mid.Key}) {
			t.Fatalf("%s order asc %v desc %v", field, asc, desc)
		}
	}
}

// 1.000001 h × $13.50 is 13.5000135 and rounds once to 13.500014 micro-dollars.
// A spent row of that same figure ties by id; the neighbouring boundaries do too.
func TestPlanningCostMicrosAgree(t *testing.T) {
	w := planningSetup(t)
	// This rounding test needs a calibrated estimate, not a hidden default.
	for i := range 5 {
		n := w.node(t, fmt.Sprintf("MICROTRAIN-%d", i+1), "work", w.root.ID, "done", nil)
		w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 4_900_000, 100_000, 4_500_000, "api", "")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions s SET created_at='2026-09-01T12:00:00Z',stopped_at='2026-09-01T13:00:00Z' FROM nodes n WHERE s.ticket_node_id=n.id AND n.key LIKE 'MICROTRAIN-%'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// An api charge on this harness makes the paid estimate follow list price.
	bill := w.node(t, "BILL-1", "work", w.root.ID, "open", nil)
	w.session(t, bill.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")

	mk := func(key string, hours any) nodeJSON {
		t.Helper()
		f := map[string]any{"route_role": "build-hard", "area": "backend"}
		if hours != nil {
			f["estimate_hours"] = hours
		}
		return w.node(t, key, "work", w.root.ID, "open", f)
	}
	zeroA, zeroB := mk("MIC-11", nil), mk("MIC-12", nil)
	halfA, halfB := mk("MIC-21", nil), mk("MIC-22", nil)
	low := mk("MIC-31", nil)
	est := mk("MIC-41", nil)
	spent := mk("MIC-51", nil)
	big := mk("MIC-61", nil)
	largeA, largeB := mk("MIC-71", nil), mk("MIC-72", nil)
	for _, n := range []nodeJSON{zeroA, zeroB, halfA, halfB, low, spent, largeA, largeB} {
		w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields = jsonb_set(fields, '{estimate_hours}', '1.000001'::jsonb) WHERE id=$1`, est.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields = jsonb_set(fields, '{estimate_hours}', '200'::jsonb) WHERE id=$1`, big.ID); err != nil {
			return err
		}
		// A huge estimate must not move a row whose spent figure is smaller.
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields = jsonb_set(fields, '{estimate_hours}', '200'::jsonb) WHERE id=$1`, low.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_session_usage u SET estimated_cost_usd=v.cost
			FROM harness_sessions s
			JOIN (VALUES
				($2::uuid, 0::numeric), ($3::uuid, 0::numeric),
				($4::uuid, 2.5000005::numeric), ($5::uuid, 2.5000005::numeric),
				($6::uuid, 13.500013::numeric), ($7::uuid, 13.500014::numeric),
				($8::uuid, 100000000.1234565::numeric), ($9::uuid, 100000000.1234565::numeric)
			) AS v(ticket, cost) ON s.ticket_node_id=v.ticket
			WHERE u.tenant_id=s.tenant_id AND u.session_id=s.id AND s.tenant_id=$1`,
			w.admin.TenantID, zeroA.ID, zeroB.ID, halfA.ID, halfB.ID, low.ID, spent.ID, largeA.ID, largeB.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCost := func(key, spent, estimated string) {
		t.Helper()
		for _, sort := range []string{"key", "list_cost"} {
			view := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q="+key+"&sort="+sort)[key]
			if view == nil || view.Cost == nil {
				t.Fatalf("%s via %s: no cost", key, sort)
			}
			got := func(p *string) string {
				if p == nil {
					return ""
				}
				return *p
			}
			if got(view.Cost.ListSpent) != spent || got(view.Cost.PaidSpent) != spent || got(view.Cost.ListEstimated) != estimated || got(view.Cost.PaidEstimated) != estimated {
				t.Fatalf("%s via %s: list %q/%q paid %q/%q", key, sort, got(view.Cost.ListSpent), got(view.Cost.ListEstimated), got(view.Cost.PaidSpent), got(view.Cost.PaidEstimated))
			}
			wantMicros := fixedMicros(spent)
			if wantMicros == "" {
				wantMicros = fixedMicros(estimated)
			}
			if got(view.Cost.ListCostMicros) != wantMicros || got(view.Cost.PaidMicros) != wantMicros {
				t.Fatalf("%s via %s: micros list %q paid %q, want %q", key, sort, got(view.Cost.ListCostMicros), got(view.Cost.PaidMicros), wantMicros)
			}
		}
	}
	wantCost("MIC-41", "", "13.500014")
	wantCost("MIC-51", "13.500014", "")
	wantCost("MIC-31", "13.500013", "2700.000000")
	wantCost("MIC-21", "2.500001", "")
	wantCost("MIC-11", "0.000000", "")
	wantCost("MIC-71", "100000000.123457", "")
	wantCost("MIC-61", "", "2700.000000")

	byID := func(group []nodeJSON) []string {
		g := append([]nodeJSON(nil), group...)
		slices.SortFunc(g, func(a, b nodeJSON) int { return strings.Compare(a.ID, b.ID) })
		out := make([]string, len(g))
		for i, n := range g {
			out[i] = n.Key
		}
		return out
	}
	groups := [][]nodeJSON{{zeroA, zeroB}, {halfA, halfB}, {low}, {est, spent}, {big}, {largeA, largeB}}
	var asc []string
	for _, group := range groups {
		asc = append(asc, byID(group)...)
	}
	var desc []string
	for i := len(groups) - 1; i >= 0; i-- {
		desc = append(desc, byID(groups[i])...)
	}
	for _, field := range []string{"list_cost", "paid"} {
		for _, dir := range []struct {
			sort string
			want []string
		}{{field, asc}, {"-" + field, desc}} {
			got := listKeys(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=MIC-&sort="+dir.sort)
			if !slices.Equal(got, dir.want) {
				t.Fatalf("%s: got %v want %v", dir.sort, got, dir.want)
			}
		}
	}
}

func TestPlanningBulkUsagePerformance(t *testing.T) {
	w := planningSetup(t)
	planningBulk(t, w, 1000, 4, "open", "PERF-", true)
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes, harness_sessions, harness_session_usage, model_prices`); err != nil {
		t.Fatal(err)
	}
	// List cost so the explained sort key is the micro-dollar projection. Every
	// row reports the same 4000 tokens, so the page check does not depend on
	// which tied cost sorts first.
	path := "/api/nodes?within=" + w.root.ID + "&kind=work&sort=list_cost&limit=50"
	if page := listPage(t, w.admin, path); !planningBulkPage(page) {
		t.Fatal("bulk usage totals")
	}
	samples := make([]time.Duration, 0, 5)
	for range 5 {
		start := time.Now()
		page := listPage(t, w.admin, path)
		elapsed := time.Since(start)
		if !planningBulkPage(page) {
			t.Fatal("bulk usage totals")
		}
		samples = append(samples, elapsed)
	}
	slices.Sort(samples)
	median := samples[len(samples)/2]
	budget := planningPerfBudget()
	t.Logf("1000 tickets / 4000 usage rows, list median %s of %v (budget %s)", median, samples, budget)
	if median >= budget {
		t.Errorf("planning request exceeded %s: median %s", budget, median)
	}
	if median >= budget || os.Getenv("AEON_PLANNING_DIAGNOSTICS") == "1" {
		tracePlanningListRequest(t, w.admin, path)
	}
	tracePlanningWorkScope(t, w.admin, w.root.ID)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		q, err := parseListQuery(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			return err
		}
		q.seen = assigneeSeen{harnessAll: true, members: true}
		if err := preparePlanningSort(t.Context(), tx, &q); err != nil {
			return err
		}
		order, args := listSQL(q, nil)
		if _, err := tx.Exec(t.Context(), `SET LOCAL enable_nestloop = off`); err != nil {
			return err
		}
		var raw string
		if err := tx.QueryRow(t.Context(), "EXPLAIN (FORMAT JSON) "+order, args...).Scan(&raw); err != nil {
			return err
		}
		var docs []map[string]any
		if err := json.Unmarshal([]byte(raw), &docs); err != nil {
			return err
		}
		if len(docs) != 1 {
			return fmt.Errorf("explain documents: %d", len(docs))
		}
		root, _ := docs[0]["Plan"].(map[string]any)
		if root == nil {
			return fmt.Errorf("explain plan missing")
		}
		if problems := planningListPlanProblems(root); len(problems) != 0 {
			t.Errorf("list planning plan: %s\n%s", strings.Join(problems, "; "), planUsageSketch(root))
		}
		// A cheap index must not turn the usage read into a per-row loop. That
		// is the plan the full suite picked when usage had grown.
		if _, err := tx.Exec(t.Context(), `SET LOCAL random_page_cost = 0.1`); err != nil {
			return err
		}
		var skewed string
		if err := tx.QueryRow(t.Context(), "EXPLAIN (FORMAT JSON) "+order, args...).Scan(&skewed); err != nil {
			return err
		}
		var docs2 []map[string]any
		if err := json.Unmarshal([]byte(skewed), &docs2); err != nil {
			return err
		}
		root2, _ := docs2[0]["Plan"].(map[string]any)
		if problems := planningListPlanProblems(root2); len(problems) != 0 {
			t.Errorf("cheap-index plan: %s\n%s", strings.Join(problems, "; "), planUsageSketch(root2))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func planningBulkPage(page nodePage) bool {
	return len(page.Items) == 50 && page.Items[0].Planning != nil && page.Items[0].Planning.Tokens.Spent != nil && *page.Items[0].Planning.Tokens.Spent == 4000
}

// planningPerfBudget is a tripwire for a request that has left the expected
// range. A quiet machine stays at 200ms. Shared CI and a two-processor test
// run are several times noisier, so they only fail far beyond that.
func planningPerfBudget() time.Duration {
	if os.Getenv("CI") != "" || runtime.GOMAXPROCS(0) <= 2 {
		return 600 * time.Millisecond
	}
	return 200 * time.Millisecond
}

// planningListPlanProblems checks the list-with-planning plan: one grouped
// aggregation over usage for the tickets that define the page, no per-row
// subplan or nested loop from those tickets into usage, and a sort key that
// uses the integer micro-dollar projection.
func planningListPlanProblems(root map[string]any) []string {
	ctes := map[string]map[string]any{}
	var index func(map[string]any)
	index = func(n map[string]any) {
		if name, ok := n["Subplan Name"].(string); ok && strings.HasPrefix(name, "CTE ") {
			ctes[strings.TrimPrefix(name, "CTE ")] = n
		}
		for _, child := range planChildren(n) {
			index(child)
		}
	}
	index(root)
	var problems []string
	usageAggs := 0
	var walk func(map[string]any, bool, bool)
	walk = func(n map[string]any, nestedInner, inSubPlan bool) {
		if n["Relation Name"] == "harness_session_usage" {
			if nestedInner {
				problems = append(problems, "nested loop drives ticket rows into usage")
			}
			if inSubPlan {
				problems = append(problems, "per-row subplan scans usage")
			}
			if planHasSubPlan(n) && !strings.Contains(fmt.Sprint(n["Filter"]), "hashed SubPlan") {
				problems = append(problems, "per-row subplan over usage")
			}
		}
		if n["Node Type"] == "Aggregate" && n["Partial Mode"] != "Partial" && planInputScansUsage(n, ctes, map[string]bool{}) {
			usageAggs++
			if !planGroupKeyHasID(n) {
				problems = append(problems, "usage aggregate is not grouped by ticket id")
			}
		}
		for _, child := range planChildren(n) {
			rel, _ := child["Parent Relationship"].(string)
			walk(child, nestedInner || (n["Node Type"] == "Nested Loop" && rel == "Inner"), inSubPlan || rel == "SubPlan")
		}
	}
	walk(root, false, false)
	if usageAggs != 1 {
		problems = append(problems, fmt.Sprintf("grouped usage aggregates = %d, want 1", usageAggs))
	}
	if !planSortUsesCostMicros(root) {
		problems = append(problems, "sort key does not use the micro-dollar projection")
	}
	return problems
}

func TestPlanningListPlanShape(t *testing.T) {
	good := `{
      "Node Type": "Sort",
      "Sort Key": ["((plan.list_micros IS NULL))", "plan.list_micros", "f.id"],
      "Plans": [{
        "Node Type": "Aggregate", "Partial Mode": "Simple", "Group Key": ["plan_lines.id"],
        "Plans": [{
          "Node Type": "Hash Join",
          "Plans": [{
            "Node Type": "Seq Scan", "Relation Name": "harness_session_usage",
            "Filter": "(ANY ((hashed SubPlan 1).col1))",
            "Plans": [{"Node Type": "Seq Scan", "Parent Relationship": "SubPlan", "Relation Name": "harness_sessions"}]
          }]
        }]
      }]
    }`
	if problems := planningListPlanProblems(mustPlan(t, good)); len(problems) != 0 {
		t.Fatalf("grouped plan: %v", problems)
	}
	nested := `{
      "Node Type": "Sort",
      "Sort Key": ["plan.paid_micros"],
      "Plans": [{
        "Node Type": "Nested Loop",
        "Plans": [
          {"Node Type": "Seq Scan", "Parent Relationship": "Outer", "Relation Name": "nodes"},
          {"Node Type": "Index Scan", "Parent Relationship": "Inner", "Relation Name": "harness_session_usage"}
        ]
      }]
    }`
	if !planProblemContains(planningListPlanProblems(mustPlan(t, nested)), "nested loop") {
		t.Fatal("nested loop into usage was accepted")
	}
	looped := `{
      "Node Type": "Aggregate", "Partial Mode": "Simple", "Group Key": ["id"],
      "Plans": [{
        "Node Type": "Index Scan", "Relation Name": "harness_session_usage",
        "Filter": "EXISTS(SubPlan 9)",
        "Plans": [{"Node Type": "Index Scan", "Parent Relationship": "SubPlan", "Subplan Name": "SubPlan 9", "Relation Name": "harness_sessions"}]
      }]
    }`
	if !planProblemContains(planningListPlanProblems(mustPlan(t, looped)), "per-row subplan") {
		t.Fatal("per-row usage subplan was accepted")
	}
}

func mustPlan(t *testing.T, raw string) map[string]any {
	t.Helper()
	var n map[string]any
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func planProblemContains(problems []string, fragment string) bool {
	return strings.Contains(strings.Join(problems, "; "), fragment)
}

func planUsageSketch(root map[string]any) string {
	var b strings.Builder
	var walk func(map[string]any, []string)
	walk = func(n map[string]any, ancestors []string) {
		typ, _ := n["Node Type"].(string)
		rel, _ := n["Relation Name"].(string)
		parent, _ := n["Parent Relationship"].(string)
		label := typ
		if rel != "" {
			label += " " + rel
		}
		if parent != "" {
			label += " (" + parent + ")"
		}
		if n["Relation Name"] == "harness_session_usage" {
			filter, _ := n["Filter"].(string)
			if len(filter) > 160 {
				filter = filter[:160]
			}
			fmt.Fprintf(&b, "%s -> %s filter %s\n", strings.Join(ancestors, " / "), label, filter)
		}
		next := append(append([]string{}, ancestors...), label)
		for _, child := range planChildren(n) {
			walk(child, next)
		}
	}
	walk(root, nil)
	return b.String()
}

func planChildren(n map[string]any) []map[string]any {
	raw, _ := n["Plans"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, child := range raw {
		if m, ok := child.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func planHasSubPlan(n map[string]any) bool {
	for _, child := range planChildren(n) {
		if child["Parent Relationship"] == "SubPlan" {
			return true
		}
	}
	return false
}

func planGroupKeyHasID(n map[string]any) bool {
	keys, _ := n["Group Key"].([]any)
	for _, key := range keys {
		if s, ok := key.(string); ok && strings.Contains(s, "id") {
			return true
		}
	}
	return false
}

func planSortUsesCostMicros(n map[string]any) bool {
	found := false
	var walk func(map[string]any)
	walk = func(n map[string]any) {
		if keys, ok := n["Sort Key"].([]any); ok {
			for _, key := range keys {
				if s, ok := key.(string); ok && costMicrosSortKey(s) {
					found = true
				}
			}
		}
		if s, ok := n["Window"].(string); ok && costMicrosSortKey(s) {
			found = true
		}
		for _, child := range planChildren(n) {
			walk(child)
		}
	}
	walk(n)
	return found
}

func costMicrosSortKey(s string) bool {
	if strings.Contains(s, "list_micros") || strings.Contains(s, "paid_micros") {
		return true
	}
	// A planner that inlines the CTE still has to sort by the one micro-dollar round.
	return strings.Contains(s, "round(") && strings.Contains(s, "1000000") && (strings.Contains(s, "list_") || strings.Contains(s, "paid_"))
}

// planInputScansUsage reports whether n's own input reads harness_session_usage.
// InitPlans and subplans are separate statements. An aggregated CTE is that
// statement's result, not another scan of usage.
func planInputScansUsage(n map[string]any, ctes map[string]map[string]any, seen map[string]bool) bool {
	for _, child := range planChildren(n) {
		rel, _ := child["Parent Relationship"].(string)
		if rel == "InitPlan" || rel == "SubPlan" || child["Node Type"] == "Aggregate" {
			continue
		}
		if child["Relation Name"] == "harness_session_usage" {
			return true
		}
		if child["Node Type"] == "CTE Scan" {
			name, _ := child["CTE Name"].(string)
			if name == "" || seen[name] {
				continue
			}
			def, ok := ctes[name]
			if !ok || def["Node Type"] == "Aggregate" {
				continue
			}
			seen[name] = true
			if def["Relation Name"] == "harness_session_usage" || planInputScansUsage(def, ctes, seen) {
				return true
			}
			continue
		}
		if planInputScansUsage(child, ctes, seen) {
			return true
		}
	}
	return false
}
