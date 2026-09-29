// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"errors"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/modelregistry"
)

// Planning columns (AEON-329). A list row carries the model its role and area
// resolve to in the model registry, the tokens spent on it and estimated for
// it, and, for callers who may see usage (harness.read on the row's project),
// what those tokens cost at API list prices and what was actually paid.
//
// Spent covers the node's own sessions and those of its ticket and task
// children and grandchildren that are not cancelled or archived: an epic rolls
// up its open and done children, a ticket includes its tasks. Tokens are
// input plus output; cached input is part of input. Estimated is
// estimate_hours × the route's tokens per hour; an epic sums its open and done
// ticket and task children.
const (
	// Tokens per hour is the median over the last calibrationWindow finished
	// tickets whose main session ran on the route, once calibrationMinimum
	// exist. Only the calibrationScan most recently finished tickets with
	// sessions are read per request.
	calibrationWindow  = 30
	calibrationMinimum = 5
	calibrationScan    = 400
	// defaultTokensPerHour applies below calibrationMinimum: about 60 agent
	// turns an hour on an 80k-token context, the usual shape of a coding
	// session in which cached context dominates.
	defaultTokensPerHour = 5_000_000
	// Without enough priced finished tickets, the list price per hour prices
	// the route's tokens per hour with this mix at the route model's list
	// price: 90% cached input, 8% fresh input, 2% output.
	mixCached = 0.90
	mixInput  = 0.08
	mixOutput = 0.02
)

type planningView struct {
	Route *planningRoute `json:"route"`
	// RouteGap says why a role has no model: "area" (no area set),
	// "review_gate" (resolved against the author's family at dispatch) or
	// "registry" (no available route in the registry).
	RouteGap string            `json:"route_gap,omitempty"`
	Tokens   planningTokens    `json:"tokens"`
	Cost     *planningCost     `json:"cost,omitempty"`
	Children *planningChildren `json:"children,omitempty"`
}
type planningRoute struct {
	Label    string `json:"label"`
	Profile  string `json:"profile"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Revision string `json:"revision"`
}
type planningTokens struct {
	Spent  *int64 `json:"spent"`
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
	Cached int64  `json:"cached"`
	// Sessions on the node (and its rolled-up children); Unreported of them
	// have no complete usage report, so Spent is a lower bound.
	Sessions    int                  `json:"sessions"`
	Unreported  int                  `json:"unreported"`
	Estimated   *int64               `json:"estimated"`
	Calibration *planningCalibration `json:"calibration,omitempty"`
}
type planningCalibration struct {
	// Basis is "median" (Tickets finished tickets on the route, or on any
	// route when none resolved) or "default".
	Basis         string `json:"basis"`
	Tickets       int    `json:"tickets"`
	TokensPerHour int64  `json:"tokens_per_hour"`
	AnyRoute      bool   `json:"any_route,omitempty"`
}

// planningCost holds exact USD decimal strings. List prices every token at the
// published API price; Paid counts pay-per-use charges only, so work covered
// by a subscription is 0 and names its plan.
type planningCost struct {
	ListSpent     *string `json:"list_spent"`
	ListEstimated *string `json:"list_estimated"`
	// ListUnpriced: some usage, or the route's model, has no list price.
	ListUnpriced  bool    `json:"list_unpriced"`
	PaidSpent     *string `json:"paid_spent"`
	PaidEstimated *string `json:"paid_estimated"`
	// PaidUnknown: some usage was reported without its billing mode, or the
	// route's harness has no billing on record yet.
	PaidUnknown bool     `json:"paid_unknown"`
	Plans       []string `json:"plans"`
}
type planningChildren struct {
	Total     int `json:"total"`
	Estimated int `json:"estimated"`
}

// planningStatesCTE is workStateCategoryCTE under its own name, so it can join
// a list statement that already defines "configured" for Hide closed.
func planningStatesCTE() string {
	return strings.Replace(workStateCategoryCTE(), "configured AS", "plan_states AS", 1)
}

// planningOpenChild joins kind and state for child alias c: a ticket or task
// that is not cancelled or archived.
func planningOpenChild(c string) string {
	return ` JOIN node_kinds ` + c + `k ON ` + c + `k.tenant_id=` + c + `.tenant_id AND ` + c + `k.id=` + c + `.kind_id AND ` + c + `k.slug IN ('ticket','task')
    LEFT JOIN plan_states ` + c + `s ON ` + c + `s.kind_id=` + c + `.kind_id AND ` + c + `s.norm=` + workStateNormSQL(c+".state")
}
func planningOpenWhere(c string) string {
	return c + `.deleted_at IS NULL AND ` + workCountBucketSQL(c+".state", c+"s") + ` NOT IN ('cancelled','archived')`
}

// planningSubtreeSQL lists (root, id) for each root in the relation roots
// (one uuid column named root): the root, its open or done ticket and task
// children, and theirs. Needs plan_states in scope.
func planningSubtreeSQL(roots string) string {
	tenant := `current_setting('aeon.tenant_id')::uuid`
	return `SELECT r.root, r.root AS id FROM (` + roots + `) r
    UNION ALL SELECT r.root, c.id FROM (` + roots + `) r
    JOIN nodes c ON c.tenant_id=` + tenant + ` AND c.parent_id=r.root` + planningOpenChild("c") + `
    WHERE ` + planningOpenWhere("c") + `
    UNION ALL SELECT r.root, g.id FROM (` + roots + `) r
    JOIN nodes c ON c.tenant_id=` + tenant + ` AND c.parent_id=r.root` + planningOpenChild("c") + `
    JOIN nodes g ON g.tenant_id=` + tenant + ` AND g.parent_id=c.id` + planningOpenChild("g") + `
    WHERE ` + planningOpenWhere("c") + ` AND ` + planningOpenWhere("g")
}

// planningUsageFrom joins the sessions of subtree t (columns root, id) to
// their usage rows and each row's latest list price. Sessions follow the
// caller's project visibility.
const planningUsageFrom = `
    JOIN harness_sessions s ON s.tenant_id=current_setting('aeon.tenant_id')::uuid AND s.ticket_node_id=t.id
        AND ((SELECT aeon_visible_all()) OR s.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    LEFT JOIN harness_session_usage u ON u.tenant_id=s.tenant_id AND u.session_id=s.id
    LEFT JOIN LATERAL (
        SELECT mp.input_usd_per_million, mp.output_usd_per_million, mp.cached_input_usd_per_million
        FROM model_prices mp WHERE mp.tenant_id=u.tenant_id AND mp.model=u.model
        ORDER BY mp.version DESC LIMIT 1
    ) price ON u.model IS NOT NULL`

// planningListCostSQL is one usage row at list price: the stored estimate for
// api billing (priced when reported), otherwise the tokens at the latest list
// price. Null when the model has no price or the counters are incomplete.
const planningListCostSQL = `coalesce(CASE WHEN u.billing_mode='api' THEN u.estimated_cost_usd END,
    ((u.input_tokens-u.cached_input_tokens)*price.input_usd_per_million + u.output_tokens*price.output_usd_per_million
     + u.cached_input_tokens*price.cached_input_usd_per_million)/1000000)`

// planningSortJoin adds plan.tokens, plan.list_usd and plan.paid_usd for
// filtered row f. Costs are null unless the caller holds harness.read on the
// row's project, so a sort never orders by a figure the caller is not shown.
func planningSortJoin(harnessAll, projects string) string {
	visible := harnessAll + ` OR f.project_id = ANY(` + projects + `::uuid[])`
	return ` LEFT JOIN LATERAL (
    SELECT sum(u.input_tokens+u.output_tokens) AS tokens,
        CASE WHEN ` + visible + ` THEN sum(` + planningListCostSQL + `) END AS list_usd,
        CASE WHEN ` + visible + ` THEN sum(CASE u.billing_mode WHEN 'api' THEN u.estimated_cost_usd WHEN 'subscription' THEN 0 END) END AS paid_usd
    FROM (` + planningSubtreeSQL(`SELECT f.id AS root`) + `) t` + planningUsageFrom + `
) plan ON true
 LEFT JOIN LATERAL (
    SELECT CASE rn.fields->>'route_role' WHEN 'scout' THEN 0 WHEN 'mechanical' THEN 1 WHEN 'build' THEN 2 WHEN 'build-hard' THEN 3 WHEN 'review-gate' THEN 4 END AS rank,
        nullif(rn.fields->>'area','') AS area
    FROM nodes rn WHERE rn.tenant_id=current_setting('aeon.tenant_id')::uuid AND rn.id=f.id
) route ON true`
}

var planningSorts = map[string]bool{"model": true, "tokens": true, "list_cost": true, "paid": true}

func sortsByPlanning(q listQuery) bool {
	for _, key := range q.Sort {
		if planningSorts[key.Name] {
			return true
		}
	}
	return false
}

// planRow is one page row, or one open or done child of a page epic.
type planRow struct {
	id, parent, kind, role, area string
	hours                        *float64
}

// planUsage accumulates usage rows for one root.
type planUsage struct {
	sessions, unreported  map[string]bool
	input, output, cached int64
	reported              bool
	list, paid            *big.Rat
	listed, paidKnown     bool
	listUnpriced          bool
	paidUnknown           bool
	plans                 map[string]bool
}

func newPlanUsage() *planUsage {
	return &planUsage{sessions: map[string]bool{}, unreported: map[string]bool{}, list: new(big.Rat), paid: new(big.Rat), plans: map[string]bool{}}
}

// usageLine is one harness_session_usage row with its session and list price.
type usageLine struct {
	session, harness, sessionModel, effort string
	model                                  *string
	input, output, cached                  *int64
	billing, plan, cost                    *string
	rateIn, rateOut, rateCached            *string
}

// complete reports whether the row has every counter.
func (l usageLine) complete() bool {
	return l.model != nil && l.input != nil && l.output != nil && l.cached != nil
}

// listCost is the row at list price, or nil when unpriced or incomplete.
func (l usageLine) listCost() *big.Rat {
	if l.billing != nil && *l.billing == "api" && l.cost != nil {
		if v, ok := new(big.Rat).SetString(*l.cost); ok {
			return v
		}
	}
	if !l.complete() || l.rateIn == nil || l.rateOut == nil || l.rateCached == nil {
		return nil
	}
	total := new(big.Rat)
	for _, part := range []struct {
		tokens int64
		rate   string
	}{{*l.input - *l.cached, *l.rateIn}, {*l.output, *l.rateOut}, {*l.cached, *l.rateCached}} {
		rate, ok := new(big.Rat).SetString(part.rate)
		if !ok {
			return nil
		}
		total.Add(total, rate.Mul(rate, big.NewRat(part.tokens, 1)))
	}
	return total.Quo(total, big.NewRat(1_000_000, 1))
}

func (u *planUsage) add(l usageLine) {
	u.sessions[l.session] = true
	if l.model == nil {
		// A session without any usage report.
		u.unreported[l.session] = true
		u.listUnpriced, u.paidUnknown = true, true
		return
	}
	if !l.complete() {
		u.unreported[l.session] = true
	} else {
		u.reported = true
		u.input += *l.input
		u.output += *l.output
		u.cached += *l.cached
	}
	if cost := l.listCost(); cost != nil {
		u.list.Add(u.list, cost)
		u.listed = true
	} else {
		u.listUnpriced = true
	}
	switch {
	case l.billing != nil && *l.billing == "subscription":
		if l.plan != nil && strings.TrimSpace(*l.plan) != "" {
			u.plans[strings.TrimSpace(*l.plan)] = true
		} else {
			u.plans["Subscription"] = true
		}
		u.paidKnown = true
	case l.billing != nil && *l.billing == "api" && l.cost != nil:
		if v, ok := new(big.Rat).SetString(*l.cost); ok {
			u.paid.Add(u.paid, v)
			u.paidKnown = true
		}
	default:
		u.paidUnknown = true
	}
}

// calibrationSample is one finished ticket: its tokens and list cost per
// hour of session time, and the route its main session ran on.
type calibrationSample struct {
	harness, model, effort string
	tokensPerHour          float64
	listPerHour            *float64
}

// routeKey matches samples: same harness and model; a sample that did not
// report its effort counts for every effort. An empty harness is any route.
type routeKey struct{ harness, model, effort string }

func (k routeKey) matches(s calibrationSample) bool {
	if k.harness == "" {
		return true
	}
	return s.harness == k.harness && s.model == k.model && (s.effort == "" || s.effort == k.effort)
}

// calibration is the rate an estimate multiplies.
type calibration struct {
	basis         string
	tickets       int
	tokensPerHour float64
	listPerHour   *float64
}

// calibrate takes the newest calibrationWindow matching samples (samples are
// newest first). With calibrationMinimum or more, tokens per hour is their
// median and so is list price per hour when as many were priced; otherwise the
// defaults apply. mixPrice is the route model's list USD per token under the
// documented mix (nil when unpriced) and prices the tokens when too few
// samples carried a price.
func calibrate(samples []calibrationSample, key routeKey, mixPrice *float64) calibration {
	var rates, lists []float64
	for _, s := range samples {
		if len(rates) == calibrationWindow {
			break
		}
		if !key.matches(s) {
			continue
		}
		rates = append(rates, s.tokensPerHour)
		if s.listPerHour != nil {
			lists = append(lists, *s.listPerHour)
		}
	}
	out := calibration{basis: "default", tokensPerHour: defaultTokensPerHour}
	if len(rates) >= calibrationMinimum {
		out = calibration{basis: "median", tickets: len(rates), tokensPerHour: median(rates)}
	}
	if out.basis == "median" && len(lists) >= calibrationMinimum {
		v := median(lists)
		out.listPerHour = &v
	} else if mixPrice != nil {
		v := out.tokensPerHour * *mixPrice
		out.listPerHour = &v
	}
	return out
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// mixPricePerToken is list USD per token for the documented token mix.
func mixPricePerToken(in, out, cached float64) float64 {
	return (mixInput*in + mixOutput*out + mixCached*cached) / 1_000_000
}

// sampleOf turns one finished ticket's usage into a calibration sample. It
// needs every session stopped and fully reported and at least a minute of
// session time. The main session (most tokens) names the route.
func sampleOf(lines []usageLine, seconds map[string]float64) (calibrationSample, bool) {
	perSession := map[string]int64{}
	models := map[string]map[string]int64{}
	var tokens int64
	list := new(big.Rat)
	priced := true
	for _, l := range lines {
		if !l.complete() {
			return calibrationSample{}, false
		}
		n := *l.input + *l.output
		tokens += n
		perSession[l.session] += n
		if models[l.session] == nil {
			models[l.session] = map[string]int64{}
		}
		models[l.session][*l.model] += n
		if cost := l.listCost(); cost != nil {
			list.Add(list, cost)
		} else {
			priced = false
		}
	}
	var hours float64
	for session := range perSession {
		s, ok := seconds[session]
		if !ok || s < 0 {
			return calibrationSample{}, false
		}
		hours += s / 3600
	}
	if hours < 1.0/60 || tokens <= 0 {
		return calibrationSample{}, false
	}
	main, best := "", int64(-1)
	for session, n := range perSession {
		if n > best || (n == best && session < main) {
			main, best = session, n
		}
	}
	var line usageLine
	for _, l := range lines {
		if l.session == main {
			line = l
			break
		}
	}
	model := line.sessionModel
	if model == "" {
		top := int64(-1)
		for m, n := range models[main] {
			if n > top || (n == top && m < model) {
				model, top = m, n
			}
		}
	}
	out := calibrationSample{harness: line.harness, model: modelregistry.ModelKey(model), effort: strings.ToLower(line.effort), tokensPerHour: float64(tokens) / hours}
	if priced {
		v, _ := list.Float64()
		v /= hours
		out.listPerHour = &v
	}
	return out, true
}

// usdString formats an exact amount with six decimals.
func usdString(v *big.Rat) *string {
	s := v.FloatString(6)
	return &s
}
func usdFloat(v float64) *string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil
	}
	s := new(big.Rat).SetFloat64(v).FloatString(6)
	return &s
}

// planRoute is one resolved role, shared by every row with that role.
type planRoute struct {
	view     *planningRoute
	key      routeKey
	mixPrice *float64
	gap      string
}

// planBilling is how a harness was billed most recently.
type planBilling struct{ mode, plan string }

// loadPlanning computes the planning view for the page's tickets, tasks and
// epics. cost reports whether the caller may see usage cost on a project.
func loadPlanning(ctx context.Context, tx pgx.Tx, items []listItem, cost func(projectID string) bool) (map[string]*planningView, error) {
	var ids []string
	for _, item := range items {
		if item.KindSlug == "ticket" || item.KindSlug == "task" || item.KindSlug == "epic" {
			ids = append(ids, item.ID)
		}
	}
	out := map[string]*planningView{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := loadPlanRows(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	usage, err := loadPlanUsage(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	routes, err := resolvePlanRoutes(ctx, tx, rows)
	if err != nil {
		return nil, err
	}
	var samples []calibrationSample
	estimating := false
	for _, r := range rows {
		if r.hours != nil {
			estimating = true
			break
		}
	}
	if estimating {
		if samples, err = loadCalibrationSamples(ctx, tx); err != nil {
			return nil, err
		}
	}
	billing := map[string]planBilling{}
	anyCost := false
	for _, item := range items {
		if item.Project != nil && cost(item.Project.ID) {
			anyCost = true
			break
		}
	}
	if anyCost && len(routes) > 0 {
		if billing, err = loadPlanBilling(ctx, tx, routes); err != nil {
			return nil, err
		}
	}
	calibrations := map[routeKey]calibration{}
	calibrationOf := func(route *planRoute) calibration {
		key, mix := routeKey{}, (*float64)(nil)
		if route != nil && route.view != nil {
			key, mix = route.key, route.mixPrice
		}
		c, ok := calibrations[key]
		if !ok {
			c = calibrate(samples, key, mix)
			calibrations[key] = c
		}
		return c
	}
	type estimate struct {
		tokens     *int64
		list, paid *float64
		plan       string
		cal        calibration
		anyRoute   bool
		unpriced   bool
		unbilled   bool
	}
	estimateOf := func(r planRow) estimate {
		if r.hours == nil {
			return estimate{}
		}
		route := routes[r.role]
		if route != nil && route.view == nil {
			route = nil
		}
		if r.area == "" || !modelregistry.KnownRouteArea(r.area) {
			route = nil
		}
		c := calibrationOf(route)
		n := int64(math.Round(*r.hours * c.tokensPerHour))
		e := estimate{tokens: &n, cal: c, anyRoute: route == nil}
		if c.listPerHour != nil {
			v := *r.hours * *c.listPerHour
			e.list = &v
		} else {
			e.unpriced = true
		}
		if route != nil {
			switch b := billing[route.view.Harness]; b.mode {
			case "subscription":
				zero := 0.0
				e.paid, e.plan = &zero, b.plan
			case "api":
				e.paid = e.list
				e.unbilled = e.list == nil
			default:
				e.unbilled = true
			}
		} else {
			e.unbilled = true
		}
		return e
	}
	children := map[string][]planRow{}
	for _, r := range rows {
		if r.parent != "" {
			children[r.parent] = append(children[r.parent], r)
		}
	}
	own := map[string]*planRow{}
	for i := range rows {
		if rows[i].parent == "" {
			own[rows[i].id] = &rows[i]
		}
	}
	for _, item := range items {
		self := own[item.ID]
		if self == nil {
			continue
		}
		view := &planningView{}
		used := usage[item.ID]
		if used != nil {
			view.Tokens.Sessions, view.Tokens.Unreported = len(used.sessions), len(used.unreported)
			if used.reported {
				spent := used.input + used.output
				view.Tokens.Spent = &spent
				view.Tokens.Input, view.Tokens.Output, view.Tokens.Cached = used.input, used.output, used.cached
			}
		}
		var listEst, paidEst *float64
		plans := map[string]bool{}
		unpriced, unbilled := false, false
		if self.kind == "epic" {
			kids := children[item.ID]
			view.Children = &planningChildren{Total: len(kids)}
			var tokens int64
			var list, paid float64
			listKnown, paidKnown := false, false
			for _, kid := range kids {
				e := estimateOf(kid)
				if e.tokens == nil {
					continue
				}
				view.Children.Estimated++
				tokens += *e.tokens
				if e.list != nil {
					list += *e.list
					listKnown = true
				}
				unpriced = unpriced || e.unpriced
				if e.paid != nil {
					paid += *e.paid
					paidKnown = true
				}
				unbilled = unbilled || e.unbilled
				if e.plan != "" {
					plans[e.plan] = true
				}
			}
			if view.Children.Estimated > 0 {
				view.Tokens.Estimated = &tokens
				if listKnown {
					listEst = &list
				}
				if paidKnown {
					paidEst = &paid
				}
			}
			if view.Children.Total == 0 {
				view.Children = nil
			}
		} else {
			if self.role != "" {
				switch route := routes[self.role]; {
				case self.area == "" || !modelregistry.KnownRouteArea(self.area):
					view.RouteGap = "area"
				case self.role == "review-gate":
					view.RouteGap = "review_gate"
				case route == nil || route.view == nil:
					view.RouteGap = "registry"
				default:
					copied := *route.view
					view.Route = &copied
				}
			}
			if e := estimateOf(*self); e.tokens != nil {
				view.Tokens.Estimated = e.tokens
				view.Tokens.Calibration = &planningCalibration{Basis: e.cal.basis, Tickets: e.cal.tickets, TokensPerHour: int64(math.Round(e.cal.tokensPerHour)), AnyRoute: e.anyRoute}
				listEst, paidEst, unpriced, unbilled = e.list, e.paid, e.unpriced, e.unbilled
				if e.plan != "" {
					plans[e.plan] = true
				}
			}
		}
		if item.Project != nil && cost(item.Project.ID) && (used != nil || view.Tokens.Estimated != nil) {
			c := &planningCost{Plans: []string{}}
			if used != nil && used.listed {
				c.ListSpent = usdString(used.list)
			}
			if used != nil && used.paidKnown {
				c.PaidSpent = usdString(used.paid)
			}
			if used != nil {
				c.ListUnpriced, c.PaidUnknown = used.listUnpriced, used.paidUnknown
				for plan := range used.plans {
					plans[plan] = true
				}
			}
			if view.Tokens.Estimated != nil {
				if listEst != nil {
					c.ListEstimated = usdFloat(*listEst)
				}
				if paidEst != nil {
					c.PaidEstimated = usdFloat(*paidEst)
				}
				c.ListUnpriced = c.ListUnpriced || unpriced
				c.PaidUnknown = c.PaidUnknown || unbilled
			}
			for plan := range plans {
				c.Plans = append(c.Plans, plan)
			}
			sort.Strings(c.Plans)
			view.Cost = c
		}
		if view.Route == nil && view.RouteGap == "" && view.Tokens.Sessions == 0 && view.Tokens.Estimated == nil {
			continue
		}
		out[item.ID] = view
	}
	return out, nil
}

// loadPlanRows reads role, area and hours for the page rows and for the open
// and done ticket and task children of page epics.
func loadPlanRows(ctx context.Context, tx pgx.Tx, ids []string) ([]planRow, error) {
	hours := estimateHoursSQL("n.fields")
	rows, err := tx.Query(ctx, `WITH `+planningStatesCTE()+`
    SELECT n.id::text, '' AS parent, k.slug, coalesce(n.fields->>'route_role',''), coalesce(n.fields->>'area',''), `+hours+`::float8
    FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
    WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=ANY($1::uuid[]) AND n.deleted_at IS NULL
    UNION ALL
    SELECT n.id::text, e.id::text, nk.slug, coalesce(n.fields->>'route_role',''), coalesce(n.fields->>'area',''), `+hours+`::float8
    FROM nodes e JOIN node_kinds ek ON ek.tenant_id=e.tenant_id AND ek.id=e.kind_id AND ek.slug='epic'
    JOIN nodes n ON n.tenant_id=e.tenant_id AND n.parent_id=e.id`+planningOpenChild("n")+`
    WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.id=ANY($1::uuid[]) AND `+planningOpenWhere("n"), ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []planRow
	for rows.Next() {
		var r planRow
		if err := rows.Scan(&r.id, &r.parent, &r.kind, &r.role, &r.area, &r.hours); err != nil {
			return nil, err
		}
		r.role, r.area = strings.TrimSpace(r.role), strings.TrimSpace(r.area)
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanUsageLine(rows pgx.Rows, dest ...any) (usageLine, error) {
	var l usageLine
	err := rows.Scan(append(dest, &l.session, &l.harness, &l.sessionModel, &l.effort, &l.model, &l.input, &l.output, &l.cached, &l.billing, &l.plan, &l.cost, &l.rateIn, &l.rateOut, &l.rateCached)...)
	return l, err
}

const usageLineColumns = `s.id::text, s.harness, coalesce(s.model,''), coalesce(s.reasoning_effort,''),
    u.model, u.input_tokens, u.output_tokens, u.cached_input_tokens, u.billing_mode, u.subscription_label, u.estimated_cost_usd::text,
    price.input_usd_per_million::text, price.output_usd_per_million::text, price.cached_input_usd_per_million::text`

// loadPlanUsage sums the usage of each root's subtree.
func loadPlanUsage(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*planUsage, error) {
	rows, err := tx.Query(ctx, `WITH `+planningStatesCTE()+`
    SELECT t.root::text, `+usageLineColumns+`
    FROM (`+planningSubtreeSQL(`SELECT unnest($1::uuid[]) AS root`)+`) t`+planningUsageFrom, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*planUsage{}
	for rows.Next() {
		var root string
		l, err := scanUsageLine(rows, &root)
		if err != nil {
			return nil, err
		}
		if out[root] == nil {
			out[root] = newPlanUsage()
		}
		out[root].add(l)
	}
	return out, rows.Err()
}

// resolvePlanRoutes resolves each role once through the model registry.
func resolvePlanRoutes(ctx context.Context, tx pgx.Tx, rows []planRow) (map[string]*planRoute, error) {
	out := map[string]*planRoute{}
	revision := ""
	now := time.Now()
	for _, r := range rows {
		if r.role == "" || out[r.role] != nil || !modelregistry.KnownRouteRole(r.role) {
			continue
		}
		if len(out) == 0 {
			var err error
			if revision, err = modelregistry.Revision(ctx, tx); err != nil {
				return nil, err
			}
		}
		// The ladder is keyed by role; any known area resolves it.
		resolved, err := modelregistry.ResolveTicketRoute(ctx, tx, r.role, "backend", now)
		if err != nil {
			return nil, err
		}
		route := &planRoute{}
		if resolved != nil {
			p := resolved.Profile
			route.view = &planningRoute{Label: p.Label(), Profile: p.Slug, Harness: p.Harness, Model: p.Model, Effort: p.Effort, Revision: revision}
			route.key = routeKey{harness: p.Harness, model: modelregistry.ModelKey(p.Model), effort: strings.ToLower(p.Effort)}
			var in, outRate, cached *float64
			err := tx.QueryRow(ctx, `SELECT input_usd_per_million::float8, output_usd_per_million::float8, cached_input_usd_per_million::float8
                FROM model_prices WHERE model=$1 ORDER BY version DESC LIMIT 1`, p.Model).Scan(&in, &outRate, &cached)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			if in != nil && outRate != nil && cached != nil {
				v := mixPricePerToken(*in, *outRate, *cached)
				route.mixPrice = &v
			}
		}
		out[r.role] = route
	}
	return out, nil
}

// loadCalibrationSamples reads the most recently finished tickets and tasks
// that have sessions, newest first.
func loadCalibrationSamples(ctx context.Context, tx pgx.Tx) ([]calibrationSample, error) {
	rows, err := tx.Query(ctx, `WITH `+planningStatesCTE()+`, finished AS (
        SELECT n.id, n.updated_at FROM nodes n`+planningOpenChild("n")+`
        WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.deleted_at IS NULL
            AND `+workCountBucketSQL("n.state", "ns")+`='done'
            AND EXISTS (SELECT 1 FROM harness_sessions hs WHERE hs.tenant_id=n.tenant_id AND hs.ticket_node_id=n.id)
        ORDER BY n.updated_at DESC, n.id LIMIT $1
    )
    SELECT t.id::text, CASE WHEN s.stopped_at IS NULL THEN -1 ELSE EXTRACT(EPOCH FROM (s.stopped_at-s.created_at))::float8 END, `+usageLineColumns+`
    FROM (SELECT f.id, f.id AS root, row_number() OVER (ORDER BY f.updated_at DESC, f.id) AS rn FROM finished f) t`+planningUsageFrom+`
    ORDER BY t.rn, s.id, u.model`, calibrationScan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type ticket struct {
		lines   []usageLine
		seconds map[string]float64
	}
	var order []string
	tickets := map[string]*ticket{}
	for rows.Next() {
		var id string
		var seconds float64
		l, err := scanUsageLine(rows, &id, &seconds)
		if err != nil {
			return nil, err
		}
		t := tickets[id]
		if t == nil {
			t = &ticket{seconds: map[string]float64{}}
			tickets[id] = t
			order = append(order, id)
		}
		t.seconds[l.session] = seconds
		t.lines = append(t.lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []calibrationSample
	for _, id := range order {
		if s, ok := sampleOf(tickets[id].lines, tickets[id].seconds); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// loadPlanBilling reads how each routed harness was billed most recently:
// the account routing the paid estimate follows. A subscription pool
// estimates 0 paid and names its plan.
func loadPlanBilling(ctx context.Context, tx pgx.Tx, routes map[string]*planRoute) (map[string]planBilling, error) {
	var harnesses []string
	for _, route := range routes {
		if route.view != nil {
			harnesses = append(harnesses, route.view.Harness)
		}
	}
	out := map[string]planBilling{}
	if len(harnesses) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (s.harness) s.harness, u.billing_mode,
            coalesce(nullif(btrim(u.subscription_label),''), nullif(btrim(a.plan),''), '')
        FROM harness_session_usage u
        JOIN harness_sessions s ON s.tenant_id=u.tenant_id AND s.id=u.session_id
        LEFT JOIN agent_accounts a ON a.tenant_id=u.tenant_id AND a.id=u.account_id
        WHERE u.tenant_id=current_setting('aeon.tenant_id')::uuid AND u.billing_mode<>'unknown' AND s.harness=ANY($1::text[])
        ORDER BY s.harness, u.reported_at DESC`, harnesses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var harness string
		var b planBilling
		if err := rows.Scan(&harness, &b.mode, &b.plan); err != nil {
			return nil, err
		}
		if b.mode == "subscription" && b.plan == "" {
			b.plan = "Subscription"
		}
		out[harness] = b
	}
	return out, rows.Err()
}
