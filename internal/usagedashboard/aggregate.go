// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const (
	maxSpan              = 366 * 24 * time.Hour
	maxSessions          = 5000
	maxTickets           = 12
	usageTable           = "harness_session_usage"
	attributionLifetime  = "lifetime_for_sessions_started_in_range"
	trendBasisStartedDay = "session_started_utc_day"
	listPriceCurrency    = "USD"
)

// Dashboard is one read of visible session usage and, when permitted, pacing.
// Totals are lifetime usage for sessions that started in [from, to). They are
// not spend consumed during that interval.
type Dashboard struct {
	From               time.Time       `json:"from"`
	To                 time.Time       `json:"to"`
	GeneratedAt        time.Time       `json:"generated_at"`
	Attribution        string          `json:"attribution"`
	TrendBasis         string          `json:"trend_basis"`
	ListPriceCurrency  string          `json:"list_price_currency"`
	Truncated          bool            `json:"truncated"`
	Totals             UsageGroup      `json:"totals"`
	ByProject          []UsageGroup    `json:"by_project"`
	ByModel            []UsageGroup    `json:"by_model"`
	ByHarness          []UsageGroup    `json:"by_harness"`
	BySubscription     []UsageGroup    `json:"by_subscription"`
	Trend              []UsageTrend    `json:"trend"`
	Tickets            []UsageTicket   `json:"tickets"`
	TicketsCostUnknown int             `json:"tickets_cost_unknown"`
	Allowance          AllowanceReport `json:"allowance"`
	Ratings            Ratings         `json:"ratings"`
}

// Ratings is rework by exception for the sessions in this dashboard.
// A vote row is an exception. ReworkRate is exceptions/deliveries.
// Average is the mean of optional scores, null when none were given.
type Ratings struct {
	Votes      int           `json:"votes"`
	Average    *string       `json:"average"`
	Exceptions int           `json:"exceptions"`
	Deliveries int           `json:"deliveries"`
	ReworkRate *string       `json:"rework_rate"`
	ByModel    []RatingGroup `json:"by_model"`
	ByHarness  []RatingGroup `json:"by_harness"`
}

// RatingGroup is one usage model or harness and its rework rate.
// Labels match the usage breakdown. Votes counts exception rows.
type RatingGroup struct {
	Label      string  `json:"label"`
	Votes      int     `json:"votes"`
	Average    *string `json:"average"`
	Exceptions int     `json:"exceptions"`
	Deliveries int     `json:"deliveries"`
	ReworkRate *string `json:"rework_rate"`
}

// UsageGroup is one project, model, reported subscription, or the range total.
// Session counts are distinct. Token and cost sums walk model rows once.
// Null totals mean nothing in the group reported that figure.
type UsageGroup struct {
	ID                     string  `json:"id,omitempty"`
	Key                    string  `json:"key,omitempty"`
	Label                  string  `json:"label"`
	BillingMode            string  `json:"billing_mode,omitempty"`
	Sessions               int     `json:"sessions"`
	UsageRows              int     `json:"usage_rows"`
	UnreportedSessions     int     `json:"unreported_sessions"`
	InputTokens            *string `json:"input_tokens"`
	InputKnownRows         int     `json:"input_known_rows"`
	InputUnknownRows       int     `json:"input_unknown_rows"`
	OutputTokens           *string `json:"output_tokens"`
	OutputKnownRows        int     `json:"output_known_rows"`
	OutputUnknownRows      int     `json:"output_unknown_rows"`
	CachedInputTokens      *string `json:"cached_input_tokens"`
	CachedInputKnownRows   int     `json:"cached_input_known_rows"`
	CachedInputUnknownRows int     `json:"cached_input_unknown_rows"`
	TokensState            string  `json:"tokens_state"`
	EstimatedCostUSD       *string `json:"estimated_cost_usd"`
	CostKnownRows          int     `json:"cost_known_rows"`
	CostUnknownRows        int     `json:"cost_unknown_rows"`
	CostState              string  `json:"cost_state"`
	ProvisionalRows        int     `json:"provisional_rows"`
	ProvisionalSessions    int     `json:"provisional_sessions"`
}

// UsageTrend is lifetime usage of the sessions that started on one UTC day.
type UsageTrend struct {
	Day   string     `json:"day"`
	Group UsageGroup `json:"group"`
}

// UsageTicket ranks one visible ticket by its known list-price estimate.
type UsageTicket struct {
	ProjectID  string `json:"project_id"`
	ProjectKey string `json:"project_key"`
	UsageGroup
}

// AllowanceReport is pacing from registered windows, or an explicit withhold.
type AllowanceReport struct {
	State   string            `json:"state"`
	Windows []AllowanceWindow `json:"windows"`
}

// AllowanceWindow is one open registered bound. It carries no account key.
type AllowanceWindow struct {
	AccountID     string    `json:"account_id"`
	Label         string    `json:"label"`
	Harness       string    `json:"harness"`
	AccountState  string    `json:"account_state"`
	WindowID      string    `json:"window_id"`
	Unit          string    `json:"unit"`
	Allowance     int64     `json:"allowance"`
	Used          *int64    `json:"used"`
	Reserved      int64     `json:"reserved"`
	PaceModel     string    `json:"pace_model"`
	BurstRatio    string    `json:"burst_ratio"`
	StartsAt      time.Time `json:"starts_at"`
	EndsAt        time.Time `json:"ends_at"`
	Provisional   bool      `json:"provisional"`
	PaceCap       *int64    `json:"pace_cap"`
	Headroom      *int64    `json:"headroom"`
	HardRemaining *int64    `json:"hard_remaining"`
}

func deliveryModelLabel(row sessionRow) string {
	if row.reported && row.model != nil && *row.model != "" {
		return *row.model
	}
	return "Unreported"
}

func deliveryHarnessLabel(row sessionRow) string {
	if row.harness != "" {
		return row.harness
	}
	return "Unreported"
}

type sessionRow struct {
	id, projectID, projectKey, projectTitle string
	ticketID, ticketKey, ticketTitle        *string
	created                                 time.Time
	harness                                 string
	reported                                bool
	model                                   *string
	input, output, cached                   *int64
	cost                                    *string
	provisional                             bool
	billingMode, subscription               *string
	delivered                               bool
}

func parseRange(fromRaw, toRaw string, now time.Time) (time.Time, time.Time, error) {
	if fromRaw == "" && toRaw == "" {
		end := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
		return end.Add(-30 * 24 * time.Hour), end, nil
	}
	if fromRaw == "" || toRaw == "" {
		return time.Time{}, time.Time{}, workorders.Fail(http.StatusBadRequest, "from and to are both required")
	}
	from, err := parseBound(fromRaw)
	if err != nil {
		return time.Time{}, time.Time{}, workorders.Fail(http.StatusBadRequest, "invalid from")
	}
	to, err := parseBound(toRaw)
	if err != nil {
		return time.Time{}, time.Time{}, workorders.Fail(http.StatusBadRequest, "invalid to")
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, workorders.Fail(http.StatusBadRequest, "from must be before to")
	}
	if to.Sub(from) > maxSpan {
		return time.Time{}, time.Time{}, workorders.Fail(http.StatusBadRequest, "range is longer than 366 days")
	}
	return from, to, nil
}

func parseBound(raw string) (time.Time, error) {
	if len(raw) == 10 {
		day, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return time.Time{}, err
		}
		return day.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, raw)
	}
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func (m *Module) dashboard(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	q := r.URL.Query()
	now := time.Now().UTC()
	from, to, err := parseRange(q.Get("from"), q.Get("to"), now)
	if err != nil {
		return nil, err
	}
	project := q.Get("project")
	if project != "" && !workorders.UUID(project) {
		return nil, workorders.Fail(http.StatusBadRequest, "invalid project")
	}
	out := Dashboard{
		From: from, To: to, GeneratedAt: now,
		Attribution: attributionLifetime, TrendBasis: trendBasisStartedDay, ListPriceCurrency: listPriceCurrency,
		ByProject: []UsageGroup{}, ByModel: []UsageGroup{}, ByHarness: []UsageGroup{}, BySubscription: []UsageGroup{},
		Trend: []UsageTrend{}, Tickets: []UsageTicket{},
		Allowance: AllowanceReport{State: "withheld", Windows: []AllowanceWindow{}},
		Ratings:   emptyRatings(),
	}
	if err := tx.QueryRow(r.Context(), `SELECT now()`).Scan(&out.GeneratedAt); err != nil {
		return nil, err
	}
	out.GeneratedAt = out.GeneratedAt.UTC()
	present, err := usageTablePresent(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, workorders.Fail(http.StatusServiceUnavailable, "session usage is not available")
	}
	rows, err := loadSessions(r.Context(), tx, from, to, project)
	if err != nil {
		return nil, err
	}
	rows, out.Truncated = trimSessions(rows)
	totals, projects, models, harnesses, subs, trend, tickets, unknownTickets, err := aggregate(rows)
	if err != nil {
		return nil, err
	}
	out.Totals = totals
	out.ByProject = projects
	out.ByModel = models
	out.ByHarness = harnesses
	out.BySubscription = subs
	ratings, err := loadVoteRatings(r.Context(), tx, rows)
	if err != nil {
		return nil, err
	}
	out.Ratings = ratings
	out.Trend = trend
	out.Tickets = tickets
	out.TicketsCostUnknown = unknownTickets
	allowance, err := loadAllowance(r.Context(), tx, p, out.GeneratedAt)
	if err != nil {
		return nil, err
	}
	out.Allowance = allowance
	return out, nil
}

func usageTablePresent(ctx context.Context, tx pgx.Tx) (bool, error) {
	var present bool
	err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, usageTable).Scan(&present)
	return present, err
}

func loadSessions(ctx context.Context, tx pgx.Tx, from, to time.Time, project string) ([]sessionRow, error) {
	var projectArg any
	if project != "" {
		projectArg = project
	}
	rows, err := tx.Query(ctx, sessionSelect(), from, to, projectArg, maxSessions+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []sessionRow{}
	for rows.Next() {
		var row sessionRow
		var provisional *bool
		if err := rows.Scan(
			&row.id, &row.projectID, &row.projectKey, &row.projectTitle,
			&row.ticketID, &row.ticketKey, &row.ticketTitle, &row.created, &row.harness,
			&row.delivered,
			&row.model, &row.input, &row.output, &row.cached, &row.cost,
			&provisional, &row.billingMode, &row.subscription,
		); err != nil {
			return nil, err
		}
		row.reported = provisional != nil
		if provisional != nil {
			row.provisional = *provisional
		}
		found = append(found, row)
	}
	return found, rows.Err()
}

// sessionSelect reads current usage rows only. Model identity comes from the
// usage row. Sessions with no row stay unreported. Run telemetry and agent-run
// totals are not joined.
func sessionSelect() string {
	return `WITH started AS (
	    SELECT s.tenant_id, s.id, s.project_id, p.key AS project_key, p.title AS project_title,
	           t.id AS ticket_id, t.key AS ticket_key, t.title AS ticket_title, s.created_at, s.harness,
	           (s.phase = 'stopped') AS delivered
	      FROM harness_sessions s
	      JOIN nodes p ON p.tenant_id = s.tenant_id AND p.id = s.project_id
	      LEFT JOIN nodes t ON t.tenant_id = s.tenant_id AND t.id = s.ticket_node_id AND t.deleted_at IS NULL
	     WHERE s.created_at >= $1 AND s.created_at < $2
	       AND ($3::uuid IS NULL OR s.project_id = $3)
	     ORDER BY s.created_at, s.id
	     LIMIT $4
	)
	SELECT started.id::text, started.project_id::text, started.project_key, started.project_title,
	       started.ticket_id::text, started.ticket_key, started.ticket_title, started.created_at, started.harness,
	       started.delivered,
	       u.model, u.input_tokens, u.output_tokens, u.cached_input_tokens, u.estimated_cost_usd::text,
	       u.provisional, u.billing_mode, u.subscription_label
	  FROM started
	  LEFT JOIN harness_session_usage u ON u.tenant_id = started.tenant_id AND u.session_id = started.id
	 ORDER BY started.created_at, started.id, u.model`
}

func trimSessions(rows []sessionRow) ([]sessionRow, bool) {
	order := []string{}
	seen := map[string]struct{}{}
	for _, row := range rows {
		if _, ok := seen[row.id]; ok {
			continue
		}
		seen[row.id] = struct{}{}
		order = append(order, row.id)
	}
	if len(order) <= maxSessions {
		return rows, false
	}
	drop := order[len(order)-1]
	kept := make([]sessionRow, 0, len(rows))
	for _, row := range rows {
		if row.id != drop {
			kept = append(kept, row)
		}
	}
	return kept, true
}

func aggregate(rows []sessionRow) (UsageGroup, []UsageGroup, []UsageGroup, []UsageGroup, []UsageGroup, []UsageTrend, []UsageTicket, int, error) {
	total := newBucket("", "", "All visible sessions", false)
	projects := map[string]*bucket{}
	models := map[string]*bucket{}
	harnesses := map[string]*bucket{}
	subs := map[string]*bucket{}
	days := map[string]*bucket{}
	tickets := map[string]*bucket{}
	var unknownTickets int
	for _, row := range rows {
		if err := total.add(row); err != nil {
			return UsageGroup{}, nil, nil, nil, nil, nil, nil, 0, err
		}
		projectLabel := row.projectTitle
		if projectLabel == "" {
			projectLabel = row.projectKey
		}
		if err := touch(projects, row.projectID, row.projectID, row.projectKey, projectLabel, false).add(row); err != nil {
			return UsageGroup{}, nil, nil, nil, nil, nil, nil, 0, err
		}
		modelLabel := deliveryModelLabel(row)
		modelKey := ""
		if modelLabel != "Unreported" {
			modelKey = modelLabel
		}
		if err := touch(models, "m:"+modelKey, "", modelKey, modelLabel, false).add(row); err != nil {
			return UsageGroup{}, nil, nil, nil, nil, nil, nil, 0, err
		}
		harnessLabel := deliveryHarnessLabel(row)
		harnessKey := ""
		if harnessLabel != "Unreported" {
			harnessKey = harnessLabel
		}
		if err := touch(harnesses, "h:"+harnessKey, "", harnessKey, harnessLabel, false).add(row); err != nil {
			return UsageGroup{}, nil, nil, nil, nil, nil, nil, 0, err
		}
		subLabel, subKey := subscription(row)
		if err := touch(subs, "s:"+subKey, "", subKey, subLabel, true).add(row); err != nil {
			return UsageGroup{}, nil, nil, nil, nil, nil, nil, 0, err
		}
		day := row.created.UTC().Format("2006-01-02")
		if err := touch(days, day, "", day, day, false).add(row); err != nil {
			return UsageGroup{}, nil, nil, nil, nil, nil, nil, 0, err
		}
		if row.ticketID == nil || row.ticketKey == nil || row.ticketTitle == nil {
			continue
		}
		bucket := touch(tickets, *row.ticketID, *row.ticketID, *row.ticketKey, *row.ticketTitle, false)
		bucket.projectID = row.projectID
		bucket.projectKey = row.projectKey
		if err := bucket.add(row); err != nil {
			return UsageGroup{}, nil, nil, nil, nil, nil, nil, 0, err
		}
	}
	ranked := []UsageTicket{}
	for _, bucket := range tickets {
		group := bucket.group()
		if group.EstimatedCostUSD == nil {
			unknownTickets++
			continue
		}
		ranked = append(ranked, UsageTicket{ProjectID: bucket.projectID, ProjectKey: bucket.projectKey, UsageGroup: group})
	}
	sort.Slice(ranked, func(i, j int) bool {
		cmp := compareUSD(ranked[i].EstimatedCostUSD, ranked[j].EstimatedCostUSD)
		if cmp != 0 {
			return cmp > 0
		}
		return ranked[i].Key < ranked[j].Key
	})
	if len(ranked) > maxTickets {
		ranked = ranked[:maxTickets]
	}
	return total.group(), groupsOf(projects), groupsOf(models), groupsOf(harnesses), groupsOf(subs), trendsOf(days), ranked, unknownTickets, nil
}

func subscription(row sessionRow) (label, key string) {
	if !row.reported {
		return "Unreported", "unreported"
	}
	mode := "unknown"
	if row.billingMode != nil {
		switch *row.billingMode {
		case "subscription", "api", "unknown":
			mode = *row.billingMode
		}
	}
	switch mode {
	case "subscription":
		if row.subscription != nil && *row.subscription != "" {
			return *row.subscription, "subscription:" + *row.subscription
		}
		return "Subscription", "subscription:"
	case "api":
		return "API", "api"
	default:
		return "Unknown", "unknown"
	}
}

func rowMode(row sessionRow) string {
	if !row.reported {
		return "unreported"
	}
	if row.billingMode == nil {
		return "unknown"
	}
	switch *row.billingMode {
	case "subscription", "api", "unknown":
		return *row.billingMode
	default:
		return "unknown"
	}
}

type counter struct {
	sum            *big.Int
	known, unknown int
}

func (c *counter) add(value *int64, unknown bool) {
	if unknown || value == nil {
		c.unknown++
		return
	}
	if c.sum == nil {
		c.sum = new(big.Int)
	}
	c.sum.Add(c.sum, big.NewInt(*value))
	c.known++
}

func (c *counter) text() *string {
	if c.sum == nil || c.known == 0 {
		return nil
	}
	s := c.sum.String()
	return &s
}

type bucket struct {
	id, key, label        string
	projectID, projectKey string
	showMode              bool
	mode                  string
	seen                  map[string]struct{}
	provisionalSessions   map[string]struct{}
	sessions              int
	usageRows             int
	unreported            int
	input, output, cached counter
	costSum               *big.Int
	costKnown             int
	costUnknown           int
	provisionalRows       int
}

func newBucket(id, key, label string, showMode bool) *bucket {
	return &bucket{
		id: id, key: key, label: label, showMode: showMode,
		seen: map[string]struct{}{}, provisionalSessions: map[string]struct{}{},
	}
}

func touch(set map[string]*bucket, mapKey, id, key, label string, showMode bool) *bucket {
	if found := set[mapKey]; found != nil {
		return found
	}
	found := newBucket(id, key, label, showMode)
	set[mapKey] = found
	return found
}

func (b *bucket) add(row sessionRow) error {
	if _, ok := b.seen[row.id]; !ok {
		b.seen[row.id] = struct{}{}
		b.sessions++
		if !row.reported {
			b.unreported++
		}
	}
	b.noteMode(rowMode(row))
	if !row.reported {
		b.input.add(nil, true)
		b.output.add(nil, true)
		b.cached.add(nil, true)
		b.costUnknown++
		return nil
	}
	b.usageRows++
	if row.provisional {
		b.provisionalRows++
		b.provisionalSessions[row.id] = struct{}{}
	}
	b.input.add(row.input, false)
	b.output.add(row.output, false)
	b.cached.add(row.cached, false)
	if row.cost == nil {
		b.costUnknown++
		return nil
	}
	n, err := parseUSD(*row.cost)
	if err != nil {
		return err
	}
	if b.costSum == nil {
		b.costSum = new(big.Int)
	}
	b.costSum.Add(b.costSum, n)
	b.costKnown++
	return nil
}

func (b *bucket) noteMode(mode string) {
	if b.mode == "" {
		b.mode = mode
		return
	}
	if b.mode != mode {
		b.mode = "mixed"
	}
}

func (b *bucket) group() UsageGroup {
	g := UsageGroup{
		ID: b.id, Key: b.key, Label: b.label, Sessions: b.sessions,
		UsageRows: b.usageRows, UnreportedSessions: b.unreported,
		InputTokens: b.input.text(), InputKnownRows: b.input.known, InputUnknownRows: b.input.unknown,
		OutputTokens: b.output.text(), OutputKnownRows: b.output.known, OutputUnknownRows: b.output.unknown,
		CachedInputTokens: b.cached.text(), CachedInputKnownRows: b.cached.known, CachedInputUnknownRows: b.cached.unknown,
		TokensState:   tokensState(b.sessions, b.input, b.output, b.cached),
		CostKnownRows: b.costKnown, CostUnknownRows: b.costUnknown,
		CostState:       costState(b),
		ProvisionalRows: b.provisionalRows, ProvisionalSessions: len(b.provisionalSessions),
	}
	if b.showMode {
		g.BillingMode = b.mode
	}
	if b.costSum != nil && b.costKnown > 0 {
		s := formatUSD(b.costSum)
		g.EstimatedCostUSD = &s
	}
	return g
}

func tokensState(sessions int, counters ...counter) string {
	if sessions == 0 {
		return "unknown"
	}
	anyKnown := false
	allKnown := true
	for _, c := range counters {
		if c.known > 0 {
			anyKnown = true
		}
		if c.known == 0 || c.unknown > 0 {
			allKnown = false
		}
	}
	if !anyKnown {
		return "unknown"
	}
	if allKnown {
		return "known"
	}
	return "partial"
}

func costState(b *bucket) string {
	units := b.usageRows + b.unreported
	if b.sessions == 0 || units == 0 || b.costKnown == 0 {
		return "unknown"
	}
	if b.costUnknown == 0 && b.provisionalRows == 0 {
		return "known"
	}
	if b.costUnknown == 0 && b.unreported == 0 && b.provisionalRows == b.usageRows {
		return "provisional"
	}
	return "partial"
}

func groupsOf(set map[string]*bucket) []UsageGroup {
	out := make([]UsageGroup, 0, len(set))
	for _, bucket := range set {
		out = append(out, bucket.group())
	}
	sort.Slice(out, func(i, j int) bool {
		cmp := compareUSD(out[i].EstimatedCostUSD, out[j].EstimatedCostUSD)
		if cmp != 0 {
			return cmp > 0
		}
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func compareUSD(a, b *string) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	av, aerr := parseUSD(*a)
	bv, berr := parseUSD(*b)
	if aerr != nil || berr != nil {
		return 0
	}
	return av.Cmp(bv)
}

func trendsOf(set map[string]*bucket) []UsageTrend {
	days := make([]string, 0, len(set))
	for day := range set {
		days = append(days, day)
	}
	sort.Strings(days)
	out := make([]UsageTrend, 0, len(days))
	for _, day := range days {
		group := set[day].group()
		group.ID, group.Key, group.Label = "", "", ""
		out = append(out, UsageTrend{Day: day, Group: group})
	}
	return out
}

func loadAllowance(ctx context.Context, tx pgx.Tx, p tenant.Principal, now time.Time) (AllowanceReport, error) {
	out := AllowanceReport{State: "withheld", Windows: []AllowanceWindow{}}
	if err := authz.RequireTx(ctx, tx, p, "account.read", authz.Scope{}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return out, nil
		}
		return out, err
	}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.label, a.harness, a.state,
		       w.id::text, w.starts_at, w.ends_at, w.unit, w.allowance, w.used, w.reserved,
		       w.pace_model, w.burst_ratio::text,
		       NOT EXISTS (
		           SELECT 1 FROM account_reservations r
		           WHERE r.tenant_id = w.tenant_id AND r.window_id = w.id AND r.state = 'settled'
		       ) OR EXISTS (
		           SELECT 1 FROM account_reservations r
		           WHERE r.tenant_id = w.tenant_id AND r.window_id = w.id
		             AND r.state = 'settled' AND r.actual_units = 0
		       )
		  FROM account_allowance_windows w
		  JOIN agent_accounts a ON a.tenant_id = w.tenant_id AND a.id = w.account_id
		 WHERE w.starts_at <= $1 AND w.ends_at > $1
		 ORDER BY a.label, w.unit, w.id`, now)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var w AllowanceWindow
		var used int64
		if err := rows.Scan(&w.AccountID, &w.Label, &w.Harness, &w.AccountState, &w.WindowID, &w.StartsAt, &w.EndsAt, &w.Unit, &w.Allowance, &used, &w.Reserved, &w.PaceModel, &w.BurstRatio, &w.Provisional); err != nil {
			return out, err
		}
		w.StartsAt, w.EndsAt = w.StartsAt.UTC(), w.EndsAt.UTC()
		applyMeasuredAvailability(&w, used, now)
		out.Windows = append(out.Windows, w)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Windows) == 0 {
		out.State = "none"
	} else {
		out.State = "visible"
	}
	return out, nil
}
