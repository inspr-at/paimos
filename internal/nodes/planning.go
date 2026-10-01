// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"
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
	// exist. Eligibility and route matching happen before the window limit.
	calibrationWindow  = 30
	calibrationMinimum = 5
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
	Snapshot *planningSnapshot `json:"estimate_snapshot,omitempty"`
	Route    *planningRoute    `json:"route"`
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
	// ListCostMicros and PaidMicros are the sort keys as integer micro-dollars,
	// decimal strings: spent when present, otherwise the estimate. A client
	// compares them as integers. The USD strings above stay the display.
	ListCostMicros *string `json:"list_cost_micros"`
	PaidMicros     *string `json:"paid_micros"`
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
    LEFT JOIN plan_prices price ON price.model=u.model`

// Materialize once: RLS checks and latest-version selection must not be
// repeated for each session's usage row.
func planningPricesCTE() string {
	return `plan_prices AS MATERIALIZED (
        SELECT DISTINCT ON (mp.model) mp.model, mp.input_usd_per_million, mp.output_usd_per_million, mp.cached_input_usd_per_million
        FROM model_prices mp WHERE mp.tenant_id=current_setting('aeon.tenant_id')::uuid
        ORDER BY mp.model, mp.version DESC
    )`
}

// planSortRate is one route's estimate rates for the SQL sort key. Role "" is
// any route (no area, or a role the registry did not resolve). Paid is 0 under
// a subscription, the list rate when billed per use, and null when unknown.
type planSortRate struct {
	Role   string   `json:"role"`
	Tokens float64  `json:"tokens_per_hour"`
	List   *float64 `json:"list_per_hour"`
	Paid   *float64 `json:"paid_per_hour"`
}

// preparePlanningSort resolves each role once and stores the rates the list
// statement multiplies. The sort key itself is SQL over the filtered rows, so
// a tokens or cost sort does not build a planning view per matching row.
func preparePlanningSort(ctx context.Context, tx pgx.Tx, q *listQuery) error {
	raw, err := planningRates(ctx, tx, q.seen)
	if err != nil {
		return err
	}
	q.planRates = raw
	return nil
}

// planningRates is the JSON the cost statements multiply. Sort and display
// both read it, so an estimate is one product, not two.
func planningRates(ctx context.Context, tx pgx.Tx, seen assigneeSeen) (json.RawMessage, error) {
	var seeds []planRow
	for _, role := range []string{"scout", "mechanical", "build", "build-hard", "review-gate"} {
		seeds = append(seeds, planRow{role: role, area: "backend"})
	}
	routes, err := resolvePlanRoutes(ctx, tx, seeds)
	if err != nil {
		return nil, err
	}
	samples, err := loadCalibrationSamples(ctx, tx, routes, seen.costVisible)
	if err != nil {
		return nil, err
	}
	billing := map[string]planBilling{}
	if len(routes) > 0 {
		if billing, err = loadPlanBilling(ctx, tx, routes, seen); err != nil {
			return nil, err
		}
	}
	pl := &planner{routes: routes, samples: samples, billing: billing, calibrations: map[routeKey]calibration{}}
	anyRoute := pl.calibration(nil)
	rates := []planSortRate{{Role: "", Tokens: anyRoute.tokensPerHour, List: anyRoute.listPerHour}}
	for role, route := range routes {
		if route.view == nil {
			continue
		}
		c := pl.calibration(route)
		rate := planSortRate{Role: role, Tokens: c.tokensPerHour, List: c.listPerHour}
		switch billing[route.view.Harness].mode {
		case "subscription":
			zero := 0.0
			rate.Paid = &zero
		case "api":
			rate.Paid = c.listPerHour
		}
		rates = append(rates, rate)
	}
	return json.Marshal(rates)
}

func (s assigneeSeen) costVisible(project string) bool {
	return s.harnessAll || slices.Contains(s.projects, project)
}

func sortsByPlanningValue(q listQuery) bool {
	return sortsBy(q, "tokens") || sortsBy(q, "list_cost") || sortsBy(q, "paid")
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
	listUnpriced          bool
	paidUnknown           bool
	plans                 map[string]bool
}

func newPlanUsage() *planUsage {
	return &planUsage{sessions: map[string]bool{}, unreported: map[string]bool{}, plans: map[string]bool{}}
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
	if l.listCost() == nil {
		u.listUnpriced = true
	}
	switch {
	case l.billing != nil && *l.billing == "subscription":
		if l.plan != nil && strings.TrimSpace(*l.plan) != "" {
			u.plans[strings.TrimSpace(*l.plan)] = true
		} else {
			u.plans["Subscription"] = true
		}
	case l.billing != nil && *l.billing == "api" && l.cost != nil:
		// Paid dollars are the SQL micro-dollar integer, not a second sum.
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

// usdPlaces is the precision list and paid amounts are projected with.
// SQL rounds once to this many decimal places, as an integer numeric of
// micro-dollars, and the API prints that integer. Nothing multiplies the
// rate again in Go.
const usdPlaces = 6

// usdMicrosSQL rounds a non-negative USD amount to integer micro-dollars,
// half away from zero, once. The result stays numeric, so a summed cost of
// any accepted size still comes back with the list. NULL stays NULL.
// Scans cast it with usdMicrosTextSQL; ORDER BY compares the numeric.
func usdMicrosSQL(expr string) string {
	return `(round((` + expr + `)::numeric * 1000000))::numeric`
}

// usdMicrosTextSQL is one micro-dollar numeric as an integer decimal string.
func usdMicrosTextSQL(expr string) string {
	return `(` + expr + `)::text`
}

// planMicros is the list and paid amounts for one row, in micro-dollars.
// Spent is usage; est is the hour estimate. Nil means that figure is absent.
// The integers are arbitrary precision: five large usage rows sum past int64.
type planMicros struct {
	listSpent, listEst, paidSpent, paidEst *big.Int
}

// microsFromText parses one numeric::text micro-dollar integer into a new
// value, so a reused scan buffer cannot alias the previous row. NULL is absent.
func microsFromText(s *string) (*big.Int, error) {
	if s == nil {
		return nil, nil
	}
	text := strings.TrimSpace(*s)
	if text == "" {
		return nil, nil
	}
	n, ok := new(big.Int).SetString(text, 10)
	if !ok || n.Sign() < 0 {
		return nil, fmt.Errorf("planning micro-dollars %q", text)
	}
	return n, nil
}

func planMicrosFromText(listSpent, listEst, paidSpent, paidEst *string) (planMicros, error) {
	var m planMicros
	var err error
	if m.listSpent, err = microsFromText(listSpent); err != nil {
		return planMicros{}, err
	}
	if m.listEst, err = microsFromText(listEst); err != nil {
		return planMicros{}, err
	}
	if m.paidSpent, err = microsFromText(paidSpent); err != nil {
		return planMicros{}, err
	}
	if m.paidEst, err = microsFromText(paidEst); err != nil {
		return planMicros{}, err
	}
	return m, nil
}

// usdFromMicros prints the integer micro-dollar amount at six decimal places.
// The split is integer division, so a total past the float64 mantissa stays exact.
func usdFromMicros(v *big.Int) *string {
	if v == nil || v.Sign() < 0 {
		return nil
	}
	scale := big.NewInt(1_000_000)
	whole := new(big.Int)
	frac := new(big.Int)
	whole.QuoRem(new(big.Int).Set(v), scale, frac)
	digits := frac.String()
	if len(digits) < usdPlaces {
		digits = strings.Repeat("0", usdPlaces-len(digits)) + digits
	}
	s := whole.String() + "." + digits
	return &s
}

// sortMicros is the integer the list orders by: spent when SQL produced it,
// otherwise the estimate. The decimal string is that integer; SQL ordered the numeric.
func sortMicros(spent, est *big.Int) *string {
	v := spent
	if v == nil {
		v = est
	}
	if v == nil || v.Sign() < 0 {
		return nil
	}
	s := v.String()
	return &s
}

// planPrice freezes the exact row used to price the resolved route.
type planPrice struct {
	Version               *int64
	Input, Output, Cached *string
}

// planRoute is one resolved role, shared by every row with that role. view
// is nil when the registry selected nothing.
type planRoute struct {
	price    *planPrice
	view     *planningRoute
	key      routeKey
	mixPrice *float64
}

// planBilling is how a harness was billed most recently.
type planBilling struct{ mode, plan string }

// planner holds what one list page's estimates share: the resolved roles, the
// calibration samples and each routed harness's billing.
type planner struct {
	routes       map[string]*planRoute
	samples      []calibrationSample
	billing      map[string]planBilling
	calibrations map[routeKey]calibration
}

// planEstimate is one ticket's or task's estimate; tokens is nil without hours.
// Dollar amounts are not stored here: SQL returns them as micro-dollars.
type planEstimate struct {
	tokens             *int64
	plans              []string
	cal                calibration
	anyRoute           bool
	unpriced, unbilled bool
}

// route is the resolved route for a row, or nil when the role, the area or
// the registry leaves none (the estimate then uses any route).
func (pl *planner) route(r planRow) *planRoute {
	route := pl.routes[r.role]
	if route == nil || route.view == nil || r.area == "" || !modelregistry.KnownRouteArea(r.area) {
		return nil
	}
	return route
}

func (pl *planner) calibration(route *planRoute) calibration {
	key, mix := routeKey{}, (*float64)(nil)
	if route != nil {
		key, mix = route.key, route.mixPrice
	}
	c, ok := pl.calibrations[key]
	if !ok {
		c = calibrate(pl.samples, key, mix)
		pl.calibrations[key] = c
	}
	return c
}

// estimate records the route's token rate and whether list or paid dollars
// exist. The dollar product itself is SQL, shared with the sort.
func (pl *planner) estimate(r planRow) planEstimate {
	if r.hours == nil {
		return planEstimate{}
	}
	route := pl.route(r)
	c := pl.calibration(route)
	n := int64(math.Round(*r.hours * c.tokensPerHour))
	e := planEstimate{tokens: &n, cal: c, anyRoute: route == nil, unbilled: true, unpriced: c.listPerHour == nil}
	if route != nil {
		switch b := pl.billing[route.view.Harness]; b.mode {
		case "subscription":
			e.plans, e.unbilled = []string{b.plan}, false
		case "api":
			e.unbilled = c.listPerHour == nil
		}
	}
	return e
}

// epicEstimate sums the token estimates of an epic's open and done children
// and folds their price flags. Dollar totals come back from SQL.
func (pl *planner) epicEstimate(kids []planRow) (planEstimate, planningChildren) {
	counts := planningChildren{Total: len(kids)}
	var sum planEstimate
	var tokens int64
	for _, kid := range kids {
		e := pl.estimate(kid)
		if e.tokens == nil {
			continue
		}
		counts.Estimated++
		tokens += *e.tokens
		sum.unpriced = sum.unpriced || e.unpriced
		sum.unbilled = sum.unbilled || e.unbilled
		sum.plans = append(sum.plans, e.plans...)
	}
	if counts.Estimated > 0 {
		sum.tokens = &tokens
	}
	return sum, counts
}

// view assembles one row. costVisible is harness.read on the row's project.
// money is the SQL micro-dollar amounts; nil leaves the dollar strings empty.
// Nil when there is nothing to show.
func (pl *planner) view(self planRow, kids []planRow, used *planUsage, costVisible bool, money *planMicros) *planningView {
	view := &planningView{}
	if used != nil {
		view.Tokens.Sessions, view.Tokens.Unreported = len(used.sessions), len(used.unreported)
		if used.reported {
			spent := used.input + used.output
			view.Tokens.Spent = &spent
			view.Tokens.Input, view.Tokens.Output, view.Tokens.Cached = used.input, used.output, used.cached
		}
	}
	var e planEstimate
	if self.kind == "epic" {
		var counts planningChildren
		e, counts = pl.epicEstimate(kids)
		if counts.Total > 0 {
			view.Children = &counts
		}
	} else {
		if self.role != "" {
			switch route := pl.routes[self.role]; {
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
		e = pl.estimate(self)
		if e.tokens != nil {
			view.Tokens.Calibration = &planningCalibration{Basis: e.cal.basis, Tickets: e.cal.tickets, TokensPerHour: int64(math.Round(e.cal.tokensPerHour)), AnyRoute: e.anyRoute}
		}
	}
	view.Tokens.Estimated = e.tokens
	if costVisible && (used != nil || e.tokens != nil) {
		view.Cost = planCost(used, e)
		if money != nil {
			view.Cost.ListSpent = usdFromMicros(money.listSpent)
			view.Cost.ListEstimated = usdFromMicros(money.listEst)
			view.Cost.PaidSpent = usdFromMicros(money.paidSpent)
			view.Cost.PaidEstimated = usdFromMicros(money.paidEst)
			view.Cost.ListCostMicros = sortMicros(money.listSpent, money.listEst)
			view.Cost.PaidMicros = sortMicros(money.paidSpent, money.paidEst)
		}
	}
	if view.Route == nil && view.RouteGap == "" && view.Tokens.Sessions == 0 && view.Tokens.Estimated == nil {
		return nil
	}
	return view
}

// planCost projects price flags. The dollar strings are applied from the
// SQL micro-dollar integers, which are also the sort keys.
func planCost(used *planUsage, e planEstimate) *planningCost {
	c := &planningCost{Plans: []string{}}
	plans := map[string]bool{}
	if used != nil {
		c.ListUnpriced, c.PaidUnknown = used.listUnpriced, used.paidUnknown
		for plan := range used.plans {
			plans[plan] = true
		}
	}
	if e.tokens != nil {
		c.ListUnpriced = c.ListUnpriced || e.unpriced
		c.PaidUnknown = c.PaidUnknown || e.unbilled
		for _, plan := range e.plans {
			plans[plan] = true
		}
	}
	for plan := range plans {
		c.Plans = append(c.Plans, plan)
	}
	sort.Strings(c.Plans)
	return c
}

// loadPlanning computes the planning view for the page's tickets, tasks and
// epics. The audience gates usage costs on both the row and source projects.
// money, when non-nil, is the micro-dollar integers the list statement already
// computed for the sort; otherwise they are read for this page with the same SQL.
func loadPlanning(ctx context.Context, tx pgx.Tx, items []listItem, seen assigneeSeen, money map[string]planMicros) (map[string]*planningView, error) {
	cost := seen.costVisible
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
	usage, err := loadPlanUsage(ctx, tx, ids, cost)
	if err != nil {
		return nil, err
	}
	pl := &planner{billing: map[string]planBilling{}, calibrations: map[routeKey]calibration{}}
	if pl.routes, err = resolvePlanRoutes(ctx, tx, rows); err != nil {
		return nil, err
	}
	if slices.ContainsFunc(rows, func(r planRow) bool { return r.hours != nil }) {
		if pl.samples, err = loadCalibrationSamples(ctx, tx, pl.routes, cost); err != nil {
			return nil, err
		}
	}
	visible := func(item listItem) bool { return item.Project != nil && cost(item.Project.ID) }
	if len(pl.routes) > 0 && slices.ContainsFunc(items, visible) {
		if pl.billing, err = loadPlanBilling(ctx, tx, pl.routes, seen); err != nil {
			return nil, err
		}
	}
	if money == nil {
		money, err = loadPlanMicros(ctx, tx, ids, seen)
		if err != nil {
			return nil, err
		}
	}
	snapshots, err := loadPlanningSnapshots(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	own := map[string]planRow{}
	children := map[string][]planRow{}
	for _, r := range rows {
		if r.parent == "" {
			own[r.id] = r
		} else {
			children[r.parent] = append(children[r.parent], r)
		}
	}
	for _, item := range items {
		self, ok := own[item.ID]
		if !ok {
			continue
		}
		m := money[item.ID]
		view := pl.view(self, children[item.ID], usage[item.ID], visible(item), &m)
		if snap := snapshots[item.ID]; snap != nil {
			if view == nil {
				view = &planningView{}
			}
			if !visible(item) || !cost(snap.CostProject) {
				snap.hideCost()
			}
			view.Snapshot = snap
		}
		if view != nil {
			out[item.ID] = view
		}
	}
	return out, nil
}

// loadPlanMicros reads list and paid micro-dollars for the page with the same
// rounding the cost sort uses.
func loadPlanMicros(ctx context.Context, tx pgx.Tx, ids []string, seen assigneeSeen) (map[string]planMicros, error) {
	out := map[string]planMicros{}
	if len(ids) == 0 {
		return out, nil
	}
	rates, err := planningRates(ctx, tx, seen)
	if err != nil {
		return nil, err
	}
	if len(rates) == 0 {
		rates = json.RawMessage(`[]`)
	}
	projects := seen.projects
	if projects == nil {
		projects = []string{}
	}
	rows, err := tx.Query(ctx, `WITH filtered AS MATERIALIZED (
        SELECT n.id, n.project_id, k.slug AS kind_slug
        FROM nodes n
        JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
        WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.deleted_at IS NULL
            AND n.id = ANY($1::uuid[]) AND k.slug IN ('ticket','task','epic')
    )`+planningSortSQL("$2", "$3", "$4", true)+`
    SELECT id::text, `+usdMicrosTextSQL("list_spent_micros")+`, `+usdMicrosTextSQL("list_est_micros")+`, `+usdMicrosTextSQL("paid_spent_micros")+`, `+usdMicrosTextSQL("paid_est_micros")+`
    FROM planning_values`, ids, string(rates), seen.harnessAll, projects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var listSpent, listEst, paidSpent, paidEst *string
		if err := rows.Scan(&id, &listSpent, &listEst, &paidSpent, &paidEst); err != nil {
			return nil, err
		}
		m, err := planMicrosFromText(listSpent, listEst, paidSpent, paidEst)
		if err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
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
func loadPlanUsage(ctx context.Context, tx pgx.Tx, ids []string, cost func(string) bool) (map[string]*planUsage, error) {
	rows, err := tx.Query(ctx, planUsageSQL(), ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*planUsage{}
	for rows.Next() {
		var root, project string
		l, err := scanUsageLine(rows, &root, &project)
		if err != nil {
			return nil, err
		}
		if out[root] == nil {
			out[root] = newPlanUsage()
		}
		if !cost(project) {
			l.hideCost()
		}
		out[root].add(l)
	}
	return out, rows.Err()
}

// planUsageSQL scans the roots' usage together; Go groups each root in one pass.
func planUsageSQL() string {
	return `WITH ` + planningStatesCTE() + `, ` + planningPricesCTE() + `
    SELECT t.root::text, s.project_id::text, ` + usageLineColumns + `
    FROM (` + planningSubtreeSQL(`SELECT unnest($1::uuid[]) AS root`) + `) t` + planningUsageFrom
}

// Hidden source costs cannot enter either spent figures or calibration rates.
func (l *usageLine) hideCost() {
	l.billing, l.plan, l.cost = nil, nil, nil
	l.rateIn, l.rateOut, l.rateCached = nil, nil, nil
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
			price := &planPrice{}
			err := tx.QueryRow(ctx, `SELECT input_usd_per_million::float8, output_usd_per_million::float8, cached_input_usd_per_million::float8, version, input_usd_per_million::text, output_usd_per_million::text, cached_input_usd_per_million::text
                FROM model_prices WHERE model=$1 ORDER BY version DESC LIMIT 1`, p.Model).Scan(&in, &outRate, &cached, &price.Version, &price.Input, &price.Output, &price.Cached)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				route.price = price
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

// modelKeySQL is ModelKey in SQL: lower case, and an Anthropic alias or id
// collapses to its family word so "opus" and "claude-opus-5-5" share a route.
func modelKeySQL(expr string) string {
	key := `lower(btrim(` + expr + `))`
	return `CASE WHEN strpos(` + key + `, 'claude') > 0 OR strpos(` + key + `, '-') = 0 THEN CASE
        WHEN strpos(` + key + `, 'fable') > 0 THEN 'fable'
        WHEN strpos(` + key + `, 'opus') > 0 THEN 'opus'
        WHEN strpos(` + key + `, 'sonnet') > 0 THEN 'sonnet'
        WHEN strpos(` + key + `, 'haiku') > 0 THEN 'haiku'
        ELSE ` + key + ` END ELSE ` + key + ` END`
}

// planRateRoleSQL is the role whose prepared rate prices this row. An unknown
// area, or none, uses the any-route rate (role ”).
func planRateRoleSQL(fields string) string {
	return `CASE WHEN btrim(coalesce(` + fields + `->>'area','')) IN ('backend','frontend','full-stack','infra','design','docs') THEN btrim(coalesce(` + fields + `->>'route_role','')) ELSE '' END`
}

func planCostVisibleSQL(project, harnessAll, projects string) string {
	return `(` + harnessAll + ` OR ` + project + ` = ANY(` + projects + `::uuid[]))`
}

// planningSortSQL is the tokens / list / paid sort key for every filtered
// ticket, task and epic: spent usage when any of it was reported, otherwise
// the estimate. List and paid are integer micro-dollars, rounded once.
// Cost stays null where the caller cannot see it (AEON-370).
// pageUsage limits the usage read to the filtered sessions. The sort of a
// whole list reads the tenant once instead; a page is small enough to probe.
func planningSortSQL(ratesArg, harnessAll, projects string, pageUsage bool) string {
	visible := planCostVisibleSQL("ps.project_id", harnessAll, projects)
	rowVisible := planCostVisibleSQL("f.project_id", harnessAll, projects)
	usageWhere := `u.tenant_id=current_setting('aeon.tenant_id')::uuid`
	if pageUsage {
		usageWhere += ` AND u.session_id IN (SELECT session_id FROM plan_sessions)`
	}
	rateCols := `raw.hours,
            (CASE WHEN spec.role IS NOT NULL THEN spec.tokens_per_hour ELSE anyr.tokens_per_hour END)::numeric AS tokens_per_hour,
            (CASE WHEN spec.role IS NOT NULL THEN spec.list_per_hour ELSE anyr.list_per_hour END)::numeric AS list_per_hour,
            (CASE WHEN spec.role IS NOT NULL THEN spec.paid_per_hour ELSE anyr.paid_per_hour END)::numeric AS paid_per_hour`
	rateJoin := `
            LEFT JOIN plan_rates spec ON spec.role = raw.role AND raw.role <> ''
            LEFT JOIN plan_rates anyr ON anyr.role = ''`
	return `, ` + planningStatesCTE() + `, ` + planningPricesCTE() + `, plan_rates AS (
        SELECT * FROM jsonb_to_recordset(` + ratesArg + `::jsonb) AS r(role text, tokens_per_hour float8, list_per_hour float8, paid_per_hour float8)
    ), plan_sub AS MATERIALIZED (
        ` + planningSubtreeSQL(`SELECT f.id AS root FROM filtered f WHERE f.kind_slug IN ('ticket','task','epic')`) + `
    ), plan_sessions AS MATERIALIZED (
        SELECT t.root AS id, s.id AS session_id, s.project_id
        FROM plan_sub t
        JOIN harness_sessions s ON s.tenant_id=current_setting('aeon.tenant_id')::uuid AND s.ticket_node_id=t.id
            AND ((SELECT aeon_visible_all()) OR s.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    ), plan_usage_rows AS MATERIALIZED (
        -- Scan this tenant's usage once. A join from each session into the
        -- usage index re-plans as a nested loop with a per-row visibility
        -- subplan once that table grows; the lines below hash these rows.
        SELECT u.session_id, u.model, u.input_tokens, u.output_tokens, u.cached_input_tokens,
            u.billing_mode, u.estimated_cost_usd
        FROM harness_session_usage u
        WHERE ` + usageWhere + `
    ), plan_lines AS MATERIALIZED (
        SELECT ps.id,
            (u.model IS NOT NULL AND u.input_tokens IS NOT NULL AND u.output_tokens IS NOT NULL AND u.cached_input_tokens IS NOT NULL) AS complete,
            CASE WHEN u.model IS NOT NULL AND u.input_tokens IS NOT NULL AND u.output_tokens IS NOT NULL AND u.cached_input_tokens IS NOT NULL
                THEN u.input_tokens + u.output_tokens END AS tokens,
            ` + visible + ` AS cost_ok,
            CASE
                WHEN NOT ` + visible + ` THEN NULL
                WHEN u.billing_mode = 'api' AND u.estimated_cost_usd IS NOT NULL THEN u.estimated_cost_usd
                WHEN u.model IS NOT NULL AND u.input_tokens IS NOT NULL AND u.output_tokens IS NOT NULL AND u.cached_input_tokens IS NOT NULL
                    AND price.input_usd_per_million IS NOT NULL AND price.output_usd_per_million IS NOT NULL AND price.cached_input_usd_per_million IS NOT NULL
                THEN ((u.input_tokens - u.cached_input_tokens) * price.input_usd_per_million
                    + u.output_tokens * price.output_usd_per_million
                    + u.cached_input_tokens * price.cached_input_usd_per_million) / 1000000
            END AS list_usd,
            u.billing_mode, u.estimated_cost_usd
        FROM plan_sessions ps
        JOIN plan_usage_rows u ON u.session_id=ps.session_id
        LEFT JOIN plan_prices price ON price.model=u.model
    ), plan_usage_agg AS MATERIALIZED (
        SELECT id,
            CASE WHEN bool_or(complete) THEN coalesce(sum(tokens) FILTER (WHERE complete), 0) END AS tokens,
            ` + usdMicrosSQL(`sum(list_usd) FILTER (WHERE list_usd IS NOT NULL)`) + ` AS list_micros,
            ` + usdMicrosSQL(`CASE WHEN bool_or(cost_ok AND (billing_mode = 'subscription' OR (billing_mode = 'api' AND estimated_cost_usd IS NOT NULL)))
                THEN coalesce(sum(CASE
                    WHEN cost_ok AND billing_mode = 'subscription' THEN 0
                    WHEN cost_ok AND billing_mode = 'api' AND estimated_cost_usd IS NOT NULL THEN estimated_cost_usd
                END), 0) END`) + ` AS paid_micros
        FROM plan_lines GROUP BY id
    ), plan_own AS MATERIALIZED (
        SELECT base.id,
            CASE WHEN base.kind_slug <> 'epic' AND rated.hours IS NOT NULL THEN round(rated.hours * rated.tokens_per_hour)::bigint END AS tokens,
            CASE WHEN base.kind_slug <> 'epic' AND rated.hours IS NOT NULL THEN ` + usdMicrosSQL(`rated.hours * rated.list_per_hour`) + ` END AS list_micros,
            CASE WHEN base.kind_slug <> 'epic' AND rated.hours IS NOT NULL THEN ` + usdMicrosSQL(`rated.hours * rated.paid_per_hour`) + ` END AS paid_micros
        FROM (
            SELECT f.id, f.kind_slug, ` + estimateHoursSQL("nd.fields") + ` AS hours, ` + planRateRoleSQL("nd.fields") + ` AS role
            FROM filtered f
            JOIN nodes nd ON nd.tenant_id=current_setting('aeon.tenant_id')::uuid AND nd.id=f.id
            WHERE f.kind_slug IN ('ticket','task','epic')
        ) base
        JOIN LATERAL (
            SELECT ` + rateCols + ` FROM (SELECT base.hours, base.role) raw` + rateJoin + `
        ) rated ON true
    ), plan_child_est AS MATERIALIZED (
        SELECT e.id,
            (sum(round(r.hours * r.tokens_per_hour)) FILTER (WHERE r.hours IS NOT NULL))::bigint AS tokens,
            ` + usdMicrosSQL(`sum(r.hours * r.list_per_hour) FILTER (WHERE r.hours IS NOT NULL AND r.list_per_hour IS NOT NULL)`) + ` AS list_micros,
            ` + usdMicrosSQL(`sum(r.hours * r.paid_per_hour) FILTER (WHERE r.hours IS NOT NULL AND r.paid_per_hour IS NOT NULL)`) + ` AS paid_micros
        FROM filtered e
        JOIN nodes c ON c.tenant_id=current_setting('aeon.tenant_id')::uuid AND c.parent_id=e.id AND c.deleted_at IS NULL
        JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id AND ck.slug IN ('ticket','task')
        LEFT JOIN plan_states cs ON cs.kind_id=c.kind_id AND cs.norm=` + workStateNormSQL("c.state") + `
        JOIN LATERAL (
            SELECT ` + rateCols + `
            FROM (SELECT ` + estimateHoursSQL("c.fields") + ` AS hours, ` + planRateRoleSQL("c.fields") + ` AS role) raw` + rateJoin + `
        ) r ON true
        WHERE e.kind_slug='epic' AND ` + workCountBucketSQL("c.state", "cs") + ` NOT IN ('cancelled','archived')
        GROUP BY e.id
    ), planning_values AS MATERIALIZED (
        SELECT f.id,
            coalesce(u.tokens, CASE WHEN f.kind_slug='epic' THEN ch.tokens ELSE o.tokens END) AS tokens,
            CASE WHEN ` + rowVisible + ` THEN u.list_micros END AS list_spent_micros,
            CASE WHEN ` + rowVisible + ` THEN CASE WHEN f.kind_slug='epic' THEN ch.list_micros ELSE o.list_micros END END AS list_est_micros,
            CASE WHEN ` + rowVisible + ` THEN u.paid_micros END AS paid_spent_micros,
            CASE WHEN ` + rowVisible + ` THEN CASE WHEN f.kind_slug='epic' THEN ch.paid_micros ELSE o.paid_micros END END AS paid_est_micros,
            CASE WHEN ` + rowVisible + ` THEN coalesce(u.list_micros, CASE WHEN f.kind_slug='epic' THEN ch.list_micros ELSE o.list_micros END) END AS list_micros,
            CASE WHEN ` + rowVisible + ` THEN coalesce(u.paid_micros, CASE WHEN f.kind_slug='epic' THEN ch.paid_micros ELSE o.paid_micros END) END AS paid_micros
        FROM filtered f
        LEFT JOIN plan_usage_agg u ON u.id=f.id
        LEFT JOIN plan_own o ON o.id=f.id
        LEFT JOIN plan_child_est ch ON ch.id=f.id
        WHERE f.kind_slug IN ('ticket','task','epic')
    )`
}

// calibrationSampleSQL keeps at most calibrationWindow eligible finished
// tickets per needed route, plus that many newest on any route. Eligibility
// (every session stopped and fully reported, at least a minute, some tokens)
// is applied before the limit, so unreported tickets are not transferred.
func calibrationSampleSQL() string {
	window := strconv.Itoa(calibrationWindow)
	return `WITH ` + planningStatesCTE() + `, ` + planningPricesCTE() + `, finished AS (
        SELECT n.id, n.updated_at FROM nodes n` + planningOpenChild("n") + `
        WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.deleted_at IS NULL
            AND ` + workCountBucketSQL("n.state", "ns") + `='done'
    ), vis AS (
        SELECT s.ticket_node_id AS ticket, s.id AS session, s.harness,
            coalesce(s.model,'') AS session_model,
            lower(coalesce(s.reasoning_effort,'')) AS effort,
            CASE WHEN s.stopped_at IS NULL THEN -1::float8 ELSE EXTRACT(EPOCH FROM (s.stopped_at-s.created_at))::float8 END AS seconds
        FROM harness_sessions s
        JOIN finished f ON f.id=s.ticket_node_id
        WHERE s.tenant_id=current_setting('aeon.tenant_id')::uuid
            AND ((SELECT aeon_visible_all()) OR s.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    ), usage_stat AS (
        SELECT v.session,
            count(u.session_id) AS usage_rows,
            count(*) FILTER (WHERE u.session_id IS NOT NULL AND (u.model IS NULL OR u.input_tokens IS NULL OR u.output_tokens IS NULL OR u.cached_input_tokens IS NULL)) AS incomplete_rows,
            coalesce(sum(u.input_tokens+u.output_tokens) FILTER (WHERE u.model IS NOT NULL AND u.input_tokens IS NOT NULL AND u.output_tokens IS NOT NULL AND u.cached_input_tokens IS NOT NULL), 0) AS tokens
        FROM vis v
        LEFT JOIN harness_session_usage u ON u.tenant_id=current_setting('aeon.tenant_id')::uuid AND u.session_id=v.session
        GROUP BY v.session
    ), good AS (
        SELECT v.ticket FROM vis v JOIN usage_stat u ON u.session=v.session
        GROUP BY v.ticket
        HAVING bool_and(v.seconds >= 0 AND u.usage_rows > 0 AND u.incomplete_rows = 0)
            AND sum(v.seconds) >= 60 AND sum(u.tokens) > 0
    ), main AS (
        SELECT DISTINCT ON (v.ticket) v.ticket, v.session, v.harness, v.session_model, v.effort
        FROM vis v
        JOIN usage_stat u ON u.session=v.session
        JOIN good g ON g.ticket=v.ticket
        ORDER BY v.ticket, u.tokens DESC, v.session::text ASC
    ), model_tokens AS (
        SELECT u.session_id, u.model, sum(u.input_tokens+u.output_tokens) AS n
        FROM harness_session_usage u
        JOIN main m ON m.session=u.session_id
        WHERE u.tenant_id=current_setting('aeon.tenant_id')::uuid
        GROUP BY u.session_id, u.model
    ), top_model AS (
        SELECT DISTINCT ON (session_id) session_id, model FROM model_tokens
        ORDER BY session_id, n DESC, model ASC
    ), routed AS (
        SELECT m.ticket AS id, f.updated_at, m.harness, m.effort,
            ` + modelKeySQL(`CASE WHEN m.session_model <> '' THEN m.session_model ELSE tm.model END`) + ` AS model_key
        FROM main m
        JOIN finished f ON f.id=m.ticket
        JOIN top_model tm ON tm.session_id=m.session
    ), picked AS (
        SELECT id FROM (SELECT id, row_number() OVER (ORDER BY updated_at DESC, id) AS rn FROM routed) s WHERE rn <= ` + window + `
        UNION
        SELECT id FROM (
            SELECT e.id, row_number() OVER (PARTITION BY n.ord ORDER BY e.updated_at DESC, e.id) AS rn
            FROM routed e
            JOIN unnest($1::text[], $2::text[], $3::text[]) WITH ORDINALITY AS n(harness, model, effort, ord)
                ON e.harness=n.harness AND e.model_key=n.model AND (e.effort='' OR e.effort=n.effort)
        ) s WHERE rn <= ` + window + `
    )
    SELECT t.id::text, s.project_id::text, CASE WHEN s.stopped_at IS NULL THEN -1 ELSE EXTRACT(EPOCH FROM (s.stopped_at-s.created_at))::float8 END, ` + usageLineColumns + `
    FROM (SELECT n.id, n.updated_at FROM nodes n JOIN picked p ON p.id=n.id) t` + planningUsageFrom + `
    ORDER BY t.updated_at DESC, t.id, s.id, u.model`
}

// loadCalibrationSamples reads a bounded set of eligible finished tickets,
// newest first, and keeps at most 30 samples per needed route.
func loadCalibrationSamples(ctx context.Context, tx pgx.Tx, routes map[string]*planRoute, cost func(string) bool) ([]calibrationSample, error) {
	var harnesses, models, efforts []string
	for _, route := range routes {
		if route == nil || route.view == nil {
			continue
		}
		harnesses = append(harnesses, route.key.harness)
		models = append(models, route.key.model)
		efforts = append(efforts, route.key.effort)
	}
	if harnesses == nil {
		harnesses, models, efforts = []string{}, []string{}, []string{}
	}
	rows, err := tx.Query(ctx, calibrationSampleSQL(), harnesses, models, efforts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	windows := map[routeKey]int{{}: 0}
	for _, route := range routes {
		if route.view != nil {
			windows[route.key] = 0
		}
	}
	var out []calibrationSample
	var lines []usageLine
	seconds := map[string]float64{}
	current := ""
	finish := func() bool {
		if sample, ok := sampleOf(lines, seconds); ok {
			include := false
			for key, count := range windows {
				if count < calibrationWindow && key.matches(sample) {
					windows[key]++
					include = true
				}
			}
			if include {
				out = append(out, sample)
			}
		}
		for _, count := range windows {
			if count < calibrationWindow {
				return false
			}
		}
		return true
	}
	for rows.Next() {
		var id, project string
		var duration float64
		l, err := scanUsageLine(rows, &id, &project, &duration)
		if err != nil {
			return nil, err
		}
		if id != current {
			if current != "" && finish() {
				return out, nil
			}
			current, lines, seconds = id, nil, map[string]float64{}
		}
		if !cost(project) {
			l.hideCost()
		}
		lines = append(lines, l)
		seconds[l.session] = duration
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if current != "" {
		finish()
	}
	return out, nil
}

// loadPlanBilling reads how each routed harness was billed most recently:
// the account routing the paid estimate follows. A subscription pool
// estimates 0 paid and names its plan.
func loadPlanBilling(ctx context.Context, tx pgx.Tx, routes map[string]*planRoute, seen assigneeSeen) (map[string]planBilling, error) {
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
        AND ($2::bool OR s.project_id=ANY($3::uuid[]))
        ORDER BY s.harness, u.reported_at DESC, s.id, u.model`, harnesses, seen.harnessAll, seen.projects)
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
