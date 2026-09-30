// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPlanningMedian(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		want float64
	}{{nil, 0}, {[]float64{3}, 3}, {[]float64{5, 1, 3}, 3}, {[]float64{4, 1, 3, 2}, 2.5}} {
		if got := median(tc.in); got != tc.want {
			t.Errorf("median(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Calibration takes the newest 30 samples of the route, needs 5 of them, and
// otherwise falls back to the documented default; list price per hour is the
// median of priced samples or the route's price under the documented mix.
func TestPlanningCalibration(t *testing.T) {
	astra := routeKey{harness: "codex", model: "gpt-6-astra", effort: "xhigh"}
	sample := func(harness, model, effort string, rate float64, list *float64) calibrationSample {
		return calibrationSample{harness: harness, model: model, effort: effort, tokensPerHour: rate, listPerHour: list}
	}
	price := func(v float64) *float64 { return &v }
	mix := mixPricePerToken(10, 50, 1)
	if math.Abs(mix-2.7e-6) > 1e-12 {
		t.Fatalf("mix price %v", mix)
	}

	var few []calibrationSample
	for i := range 4 {
		few = append(few, sample("codex", "gpt-6-astra", "xhigh", float64(1_000_000*(i+1)), price(10)))
	}
	got := calibrate(few, astra, &mix)
	if got.basis != "default" || got.tokensPerHour != defaultTokensPerHour || got.tickets != 0 {
		t.Fatalf("four samples should use the default: %+v", got)
	}
	if got.listPerHour == nil || math.Abs(*got.listPerHour-defaultTokensPerHour*mix) > 1e-9 {
		t.Fatalf("default list price per hour: %+v", got.listPerHour)
	}
	if unpriced := calibrate(few, astra, nil); unpriced.listPerHour != nil {
		t.Fatalf("an unpriced route has no list estimate: %+v", unpriced)
	}

	// Other routes do not count; a sample without an effort counts for any effort.
	mixed := append([]calibrationSample{
		sample("claude", "opus", "xhigh", 99_000_000, price(99)),
		sample("codex", "gpt-6-astra", "medium", 88_000_000, price(88)),
		sample("codex", "gpt-6-astra", "", 5_000_000, price(50)),
	}, few...)
	got = calibrate(mixed, astra, &mix)
	if got.basis != "median" || got.tickets != 5 || got.tokensPerHour != 3_000_000 {
		t.Fatalf("five matching samples: %+v", got)
	}
	if got.listPerHour == nil || *got.listPerHour != 10 {
		t.Fatalf("median list price per hour: %+v", got.listPerHour)
	}

	// Only the newest 30 count (samples are newest first).
	var many []calibrationSample
	for range 30 {
		many = append(many, sample("codex", "gpt-6-astra", "xhigh", 2_000_000, nil))
	}
	for range 40 {
		many = append(many, sample("codex", "gpt-6-astra", "xhigh", 9_000_000, nil))
	}
	got = calibrate(many, astra, &mix)
	if got.tickets != 30 || got.tokensPerHour != 2_000_000 {
		t.Fatalf("window: %+v", got)
	}
	// Too few priced samples: the median tokens are priced with the mix.
	if got.listPerHour == nil || math.Abs(*got.listPerHour-2_000_000*mix) > 1e-9 {
		t.Fatalf("mix-priced median: %+v", got.listPerHour)
	}
	// Any route takes every sample.
	if any := calibrate(mixed, routeKey{}, nil); any.tickets != 7 || any.basis != "median" {
		t.Fatalf("any route: %+v", any)
	}
}

func TestPlanningSampleOf(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	s := func(v string) *string { return &v }
	api := s("api")
	lines := []usageLine{
		{session: "a", harness: "codex", sessionModel: "gpt-6-astra", effort: "XHigh", model: s("gpt-6-astra"), input: n(1_000_000), output: n(20_000), cached: n(800_000), billing: api, cost: s("3.8")},
		{session: "b", harness: "claude", sessionModel: "", model: s("claude-opus-5-5"), input: n(500_000), output: n(10_000), cached: n(400_000), billing: s("subscription"), rateIn: s("4"), rateOut: s("20"), rateCached: s("0.2")},
	}
	got, ok := sampleOf(lines, map[string]float64{"a": 1800, "b": 1800})
	if !ok || got.harness != "codex" || got.model != "gpt-6-astra" || got.effort != "xhigh" {
		t.Fatalf("main session: %+v %v", got, ok)
	}
	if got.tokensPerHour != 1_530_000 || got.listPerHour == nil || math.Abs(*got.listPerHour-4.48) > 1e-9 {
		t.Fatalf("rates: %+v %v", got, got.listPerHour)
	}
	if _, ok := sampleOf(lines, map[string]float64{"a": -1, "b": 1800}); ok {
		t.Fatal("a running session must not calibrate")
	}
	if _, ok := sampleOf(lines, map[string]float64{"a": 20, "b": 20}); ok {
		t.Fatal("under a minute must not calibrate")
	}
	partial := append([]usageLine{}, lines...)
	partial[1].output = nil
	if _, ok := sampleOf(partial, map[string]float64{"a": 1800, "b": 1800}); ok {
		t.Fatal("an incomplete report must not calibrate")
	}
	// The session's own model wins; without it the usage model with most tokens.
	lines[0].sessionModel = ""
	if got, _ := sampleOf(lines, map[string]float64{"a": 1800, "b": 1800}); got.model != "gpt-6-astra" {
		t.Fatalf("usage model: %+v", got)
	}
}

func TestPlanningListCost(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	s := func(v string) *string { return &v }
	priced := usageLine{model: s("gpt-6-astra"), input: n(1_000_000), output: n(20_000), cached: n(800_000), billing: s("subscription"), rateIn: s("10"), rateOut: s("50"), rateCached: s("1")}
	if got := priced.listCost(); got == nil || got.FloatString(6) != "3.800000" {
		t.Fatalf("list cost %v", got)
	}
	stored := priced
	stored.billing, stored.cost, stored.rateIn = s("api"), s("1.25"), nil
	if got := stored.listCost(); got == nil || got.FloatString(2) != "1.25" {
		t.Fatalf("stored api estimate %v", got)
	}
	unpriced := priced
	unpriced.rateOut = nil
	if unpriced.listCost() != nil {
		t.Fatal("unpriced model")
	}
}

type planningWorld struct {
	admin, viewer tenant.Principal
	root          nodeJSON
	nodes         map[string]nodeJSON
	agent         string
}

func planningSetup(t *testing.T) planningWorld {
	t.Helper()
	admin := newPrincipal(t, "planning-columns")
	w := planningWorld{admin: admin, nodes: map[string]nodeJSON{}}
	w.root = mustNode(t, admin, `{"kind_id":"`+kindBySlug(t, admin, "project").ID+`","title":"Planning"}`)
	// Registry: build-hard → Codex astra xhigh; build → Claude opus high;
	// mechanical has no route.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles (tenant_id, slug, version, harness, family, model, effort, tier)
            VALUES ($1,'codex-astra-xhigh','2','codex','openai','gpt-6-astra','xhigh','frontier'),
                   ($1,'claude-opus-high','2','claude','anthropic','opus','high','strong'),
                   ($1,'codex-sol-xhigh','2','codex','openai','gpt-6-sol','xhigh','strong')`, admin.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes (tenant_id, role, priority, profile_id)
            SELECT $1, v.role, 1, p.id FROM (VALUES ('build-hard','codex-astra-xhigh'), ('build','claude-opus-high')) v(role, slug)
            JOIN model_profiles p ON p.tenant_id=$1 AND p.slug=v.slug`, admin.TenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	w.agent = insertNamedAgent(t, admin.TenantID, "Builder")
	w.viewer = insertPerson(t, admin.TenantID, "Viewer")
	bindNamesOnly(t, admin.TenantID, w.viewer.ID)
	return w
}

func (w planningWorld) node(t *testing.T, key, kind, parent, state string, fields map[string]any) nodeJSON {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	if state == "done" && kind == "ticket" {
		fields["pill_en"], fields["pill_de"], fields["benefit_en"], fields["benefit_de"] = "Planning works now", "Planung geht jetzt", "Planning shows real numbers.", "Die Planung zeigt echte Zahlen."
	}
	raw, _ := json.Marshal(map[string]any{"kind_id": kindBySlug(t, w.admin, kind).ID, "key": key, "title": key, "state": state, "parent_id": parent, "fields": fields})
	n := mustNode(t, w.admin, string(raw))
	w.nodes[key] = n
	return n
}

// session adds a stopped session of the given length with one usage row.
// billing "api" stores the list estimate the reporter would have priced.
func (w planningWorld) session(t *testing.T, node, harness, model, effort, usageModel string, minutes int, input, output, cached int64, billing, plan string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions (
                tenant_id, project_id, agent_principal_id, ticket_node_id, harness, host, management, role, work_shape,
                ref_digest, lease_digest, phase, activity, model, reasoning_effort, created_at, heartbeat_at, stopped_at, stop_reason)
            VALUES ($1, $2, $3, $4, $5, 'test', 'unmanaged', 'worker', 'ship',
                decode(replace(gen_random_uuid()::text, '-', ''), 'hex'), decode(replace(gen_random_uuid()::text, '-', ''), 'hex'),
                'stopped', 'busy', nullif($6::text,''), nullif($7::text,''),
                clock_timestamp() - make_interval(mins => $8::int), clock_timestamp(), clock_timestamp(), 'completed')
            RETURNING id::text`, w.admin.TenantID, w.root.ID, w.agent, node, harness, model, effort, minutes).Scan(&id); err != nil {
			return err
		}
		if usageModel == "" {
			return nil
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_session_usage (tenant_id, session_id, model, sequence, input_tokens, output_tokens,
                cached_input_tokens, provisional, price_version, estimated_cost_usd, billing_mode, subscription_label)
            SELECT $1, $2::uuid, $3, 1, $4::bigint, $5::bigint, $6::bigint, false, price.version,
                CASE WHEN $7='api' THEN ((($4::bigint-$6::bigint)*price.input_usd_per_million + $5::bigint*price.output_usd_per_million + $6::bigint*price.cached_input_usd_per_million)/1000000) END,
                $7, nullif($8::text,'')
            FROM (SELECT 1) one LEFT JOIN LATERAL (SELECT * FROM model_prices WHERE model=$3 AND $7='api' ORDER BY version DESC LIMIT 1) price ON true`,
			w.admin.TenantID, id, usageModel, input, output, cached, billing, plan)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func planningOf(t *testing.T, who tenant.Principal, path string) map[string]*planningView {
	t.Helper()
	out := map[string]*planningView{}
	for _, item := range listPage(t, who, path).Items {
		out[item.Key] = item.Planning
	}
	return out
}

func TestListPlanningColumns(t *testing.T) {
	w := planningSetup(t)
	epic := w.node(t, "PLN-1", "epic", w.root.ID, "open", nil)
	built := w.node(t, "PLN-2", "ticket", epic.ID, "in_progress", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	task := w.node(t, "PLN-3", "task", built.ID, "open", nil)
	done := w.node(t, "PLN-4", "ticket", epic.ID, "done", map[string]any{"route_role": "mechanical", "area": "docs", "estimate_hours": 1})
	cancelled := w.node(t, "PLN-5", "ticket", epic.ID, "cancelled", map[string]any{"estimate_hours": 3})
	w.node(t, "PLN-6", "ticket", w.root.ID, "open", map[string]any{"route_role": "build"})
	w.node(t, "PLN-7", "ticket", w.root.ID, "open", map[string]any{"route_role": "review-gate", "area": "backend"})
	w.node(t, "PLN-8", "ticket", w.root.ID, "open", map[string]any{"route_role": "build", "area": "frontend"})
	w.node(t, "PLN-9", "ticket", w.root.ID, "open", nil)
	_ = done

	// PLN-2: $3.80 pay-per-use on Codex, $0.68 at list price under a Claude plan,
	// and its task's session adds 10k tokens without a list price.
	w.session(t, built.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1_000_000, 20_000, 800_000, "api", "")
	w.session(t, built.ID, "claude", "claude-opus-5-5", "high", "claude-opus-5-5", 30, 500_000, 10_000, 400_000, "subscription", "Max 20x")
	w.session(t, task.ID, "claude", "", "", "unpriced-model", 10, 9_000, 1_000, 0, "unknown", "")
	// A cancelled child does not roll up.
	w.session(t, cancelled.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 7_000_000, 0, 0, "api", "")

	path := "/api/nodes?within=" + w.root.ID + "&sort=key"
	views := planningOf(t, w.admin, path)

	// Model: the registry's resolution with its revision; gaps explain "—".
	b := views["PLN-2"]
	if b == nil || b.Route == nil || b.Route.Label != "Codex astra · xhigh" || b.Route.Profile != "codex-astra-xhigh" || !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(b.Route.Revision) {
		t.Fatalf("PLN-2 route: %#v", b)
	}
	if v := views["PLN-8"]; v == nil || v.Route == nil || v.Route.Label != "Claude opus · high" || v.Route.Revision != b.Route.Revision {
		t.Fatalf("PLN-8 route: %#v", v)
	}
	for key, gap := range map[string]string{"PLN-4": "registry", "PLN-6": "area", "PLN-7": "review_gate"} {
		if v := views[key]; v == nil || v.Route != nil || v.RouteGap != gap {
			t.Fatalf("%s gap: %#v", key, v)
		}
	}
	if views["PLN-9"] != nil {
		t.Fatalf("a ticket with nothing to plan carries no planning: %#v", views["PLN-9"])
	}

	// Tokens: spent covers the ticket's sessions and its task's.
	if b.Tokens.Spent == nil || *b.Tokens.Spent != 1_020_000+510_000+10_000 || b.Tokens.Sessions != 3 || b.Tokens.Unreported != 0 || b.Tokens.Cached != 1_200_000 {
		t.Fatalf("PLN-2 tokens: %#v", b.Tokens)
	}
	// Estimated: 2h × the default until five finished tickets exist.
	if b.Tokens.Estimated == nil || *b.Tokens.Estimated != 2*defaultTokensPerHour || b.Tokens.Calibration == nil || b.Tokens.Calibration.Basis != "default" || b.Tokens.Calibration.AnyRoute {
		t.Fatalf("PLN-2 estimate: %#v %#v", b.Tokens, b.Tokens.Calibration)
	}
	if d := views["PLN-4"]; d.Tokens.Estimated == nil || *d.Tokens.Estimated != defaultTokensPerHour || !d.Tokens.Calibration.AnyRoute || d.Tokens.Spent != nil {
		t.Fatalf("PLN-4 tokens: %#v", d.Tokens)
	}

	// Cost: list price for all usage, paid only for pay-per-use; the plan is named.
	c := b.Cost
	if c == nil || c.ListSpent == nil || *c.ListSpent != "4.480000" || c.PaidSpent == nil || *c.PaidSpent != "3.800000" || !c.ListUnpriced || !c.PaidUnknown || !slices.Equal(c.Plans, []string{"Max 20x"}) {
		t.Fatalf("PLN-2 cost: %#v", c)
	}
	// Estimated at list price: 10M tokens under the documented mix at gpt-6-astra
	// prices; Codex last billed pay-per-use, so paid follows list.
	if c.ListEstimated == nil || *c.ListEstimated != "27.000000" || c.PaidEstimated == nil || *c.PaidEstimated != "27.000000" {
		t.Fatalf("PLN-2 cost estimate: %#v %v %v", c, c.ListEstimated, c.PaidEstimated)
	}

	// Epic: rolls up its open and done children, not the cancelled one.
	e := views["PLN-1"]
	if e == nil || e.Route != nil || e.Children == nil || e.Children.Total != 2 || e.Children.Estimated != 2 {
		t.Fatalf("PLN-1 children: %#v", e)
	}
	if e.Tokens.Spent == nil || *e.Tokens.Spent != *b.Tokens.Spent || e.Tokens.Sessions != 3 {
		t.Fatalf("PLN-1 spent: %#v", e.Tokens)
	}
	if e.Tokens.Estimated == nil || *e.Tokens.Estimated != 3*defaultTokensPerHour || e.Tokens.Calibration != nil {
		t.Fatalf("PLN-1 estimate: %#v", e.Tokens)
	}
	if e.Cost == nil || *e.Cost.ListEstimated != "27.000000" || !e.Cost.ListUnpriced || *e.Cost.ListSpent != "4.480000" {
		t.Fatalf("PLN-1 cost: %#v", e.Cost)
	}

	// A person who may not see usage gets tokens but never cost.
	for key, v := range planningOf(t, w.viewer, path) {
		if v != nil && v.Cost != nil {
			t.Fatalf("%s leaked cost to a caller without harness.read: %#v", key, v.Cost)
		}
	}

	// Sorting: model by the role's rung, then area; tokens and cost by spent,
	// then estimate.
	keys := func(who tenant.Principal, sort string) []string {
		return listKeys(t, who, "/api/nodes?within="+w.root.ID+"&kind=ticket,epic&sort="+sort)
	}
	if got := keys(w.admin, "model"); strings.Join(got[:5], ",") != "PLN-4,PLN-8,PLN-6,PLN-2,PLN-7" {
		t.Fatalf("model sort: %v", got)
	}
	if got := keys(w.admin, "-tokens"); strings.Join(got[:4], ",") != "PLN-5,PLN-4,PLN-1,PLN-2" && strings.Join(got[:4], ",") != "PLN-5,PLN-4,PLN-2,PLN-1" {
		t.Fatalf("tokens sort: %v", got)
	}
	if got := keys(w.admin, "-paid"); got[0] != "PLN-5" || slices.Index(got, "PLN-9") < 3 {
		t.Fatalf("paid sort: %v", got)
	}
	// Without harness.read every cost ties, leaving only the node ID.
	hidden := listPage(t, w.viewer, "/api/nodes?within="+w.root.ID+"&kind=ticket,epic&sort=paid").Items
	for _, sort := range []string{"paid", "-paid", "list_cost", "-list_cost"} {
		got := listPage(t, w.viewer, "/api/nodes?within="+w.root.ID+"&kind=ticket,epic&sort="+sort).Items
		if !slices.IsSortedFunc(got, func(a, b listItem) int { return strings.Compare(a.ID, b.ID) }) || len(got) != len(hidden) {
			t.Fatalf("hidden cost sort %s must tie by ID: %+v", sort, got)
		}
	}

	// A registry change updates every row: build moves to Codex sol.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET profile_id=(SELECT id FROM model_profiles WHERE slug='codex-sol-xhigh') WHERE role='build'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	moved := planningOf(t, w.admin, path)
	if v := moved["PLN-8"]; v.Route == nil || v.Route.Label != "Codex sol · xhigh" || v.Route.Revision == b.Route.Revision || moved["PLN-2"].Route.Revision != v.Route.Revision {
		t.Fatalf("registry change: %#v", v.Route)
	}

	// Five finished Codex astra tickets calibrate the route: 2M, 4M, 6M, 8M and
	// 10M tokens an hour, median 6M. Their list price per hour is the median too.
	for i := range 5 {
		fin := w.node(t, "FIN-"+strconv.Itoa(i+1), "ticket", w.root.ID, "done", nil)
		tokens := int64(2_000_000 * (i + 1))
		w.session(t, fin.ID, "codex", "gpt-6-astra", "", "gpt-6-astra", 60, tokens, 0, 0, "api", "")
	}
	calibrated := planningOf(t, w.admin, path)["PLN-2"]
	cal := calibrated.Tokens.Calibration
	if cal == nil || cal.Basis != "median" || cal.Tickets != 5 || math.Abs(float64(cal.TokensPerHour)-6_000_000) > 2_000 {
		t.Fatalf("median calibration: %#v", cal)
	}
	if calibrated.Tokens.Estimated == nil || math.Abs(float64(*calibrated.Tokens.Estimated)-12_000_000) > 4_000 {
		t.Fatalf("calibrated estimate: %v", calibrated.Tokens.Estimated)
	}
	// 6M fresh input tokens an hour at $10 per million.
	if list := calibrated.Cost.ListEstimated; list == nil || !strings.HasPrefix(*list, "119.") && !strings.HasPrefix(*list, "120.") {
		t.Fatalf("calibrated list estimate: %v", list)
	}
}
