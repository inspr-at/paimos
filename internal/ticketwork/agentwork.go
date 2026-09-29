// SPDX-License-Identifier: AGPL-3.0-only

// Package ticketwork aggregates harness sessions onto a ticket, epic or task.
// Token counts and list-price estimates come from the current
// harness_session_usage rows (one per session and model). This package does
// not price tokens, does not read receipt history, and does not add
// agent_runs or run_telemetry for a session already represented there.
package ticketwork

import (
	"context"
	"errors"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	defaultLimit = 40
	maxLimit     = 80
	maxDepth     = 8
	maxScope     = 200
	maxModels    = 32
	currencyUSD  = "USD"
)

var (
	uuidPattern    = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	intPattern     = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)
	decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,12})?$`)
)

type module struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) httpapi.Module { return &module{pool: pool} }

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{nodeId}/agent-work", m.get)
}

// UsageModel is one current harness_session_usage row. Cost is an exact USD
// decimal string. Null counters and a null cost are unknown, not zero.
type UsageModel struct {
	Model             string  `json:"model"`
	InputTokens       *string `json:"input_tokens"`
	OutputTokens      *string `json:"output_tokens"`
	CachedInputTokens *string `json:"cached_input_tokens"`
	TokensState       string  `json:"tokens_state"`
	CachedState       string  `json:"cached_state"`
	EstimatedCostUSD  *string `json:"estimated_cost_usd"`
	CostState         string  `json:"cost_state"`
	Provisional       bool    `json:"provisional"`
	PriceVersion      *string `json:"price_version"`
	BillingMode       string  `json:"billing_mode"`
	SubscriptionLabel *string `json:"subscription_label"`
}

type Session struct {
	ID                 string       `json:"id"`
	TicketNodeID       string       `json:"ticket_node_id"`
	TicketKey          string       `json:"ticket_key"`
	TicketTitle        string       `json:"ticket_title"`
	Harness            string       `json:"harness"`
	Label              *string      `json:"label"`
	Model              *string      `json:"model"`
	ModelState         string       `json:"model_state"`
	Effort             *string      `json:"effort"`
	EffortState        string       `json:"effort_state"`
	Phase              string       `json:"phase"`
	StartedAt          time.Time    `json:"started_at"`
	EndedAt            *time.Time   `json:"ended_at"`
	DurationSeconds    *int64       `json:"duration_seconds"`
	DurationState      string       `json:"duration_state"`
	UsageReported      bool         `json:"usage_reported"`
	Models             []UsageModel `json:"models"`
	ModelsTruncated    bool         `json:"models_truncated"`
	InputTokens        *string      `json:"input_tokens"`
	OutputTokens       *string      `json:"output_tokens"`
	CachedInputTokens  *string      `json:"cached_input_tokens"`
	TokensState        string       `json:"tokens_state"`
	CachedState        string       `json:"cached_state"`
	EstimatedCostUSD   *string      `json:"estimated_cost_usd"`
	CostState          string       `json:"cost_state"`
	UnknownTokenModels int          `json:"unknown_token_models"`
	UnknownCostModels  int          `json:"unknown_cost_models"`
}

type Totals struct {
	SessionCount         int     `json:"session_count"`
	InputTokens          *string `json:"input_tokens"`
	OutputTokens         *string `json:"output_tokens"`
	CachedInputTokens    *string `json:"cached_input_tokens"`
	TokensState          string  `json:"tokens_state"`
	CachedState          string  `json:"cached_state"`
	EstimatedCostUSD     *string `json:"estimated_cost_usd"`
	CostState            string  `json:"cost_state"`
	Currency             string  `json:"currency"`
	DurationSeconds      *int64  `json:"duration_seconds"`
	DurationState        string  `json:"duration_state"`
	UnknownTokenSessions int     `json:"unknown_token_sessions"`
	UnknownCostSessions  int     `json:"unknown_cost_sessions"`
	UnknownTokenModels   int     `json:"unknown_token_models"`
	UnknownCostModels    int     `json:"unknown_cost_models"`
}

type Report struct {
	NodeID              string    `json:"node_id"`
	Kind                string    `json:"kind"`
	Currency            string    `json:"currency"`
	UsageAvailable      bool      `json:"usage_available"`
	IncludesDescendants bool      `json:"includes_descendants"`
	ScopeTruncated      bool      `json:"scope_truncated"`
	ListTruncated       bool      `json:"list_truncated"`
	Sessions            []Session `json:"sessions"`
	Totals              Totals    `json:"totals"`
}

type fail struct {
	status int
	msg    string
}

func (e *fail) Error() string { return e.msg }

func (m *module) get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidPattern.MatchString(p.ID) || !uuidPattern.MatchString(p.TenantID) {
		httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	nodeID := r.PathValue("nodeId")
	if !uuidPattern.MatchString(nodeID) {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	var report Report
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var loadErr error
		report, loadErr = load(r.Context(), tx, p.TenantID, nodeID, limit)
		return loadErr
	})
	if err != nil {
		var fe *fail
		if errors.As(err, &fe) {
			httpapi.WriteError(w, fe.status, fe.msg)
			return
		}
		httpapi.WriteError(w, http.StatusInternalServerError, "agent work could not be loaded")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, report)
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxLimit {
		return 0, errors.New("invalid limit")
	}
	return n, nil
}

func load(ctx context.Context, tx pgx.Tx, tenantID, nodeID string, limit int) (Report, error) {
	if err := assertUsageRelation(ctx, tx); err != nil {
		if relationMissing(err) {
			return Report{}, &fail{status: http.StatusInternalServerError, msg: "session usage relation is not available"}
		}
		return Report{}, err
	}
	ids, kind, truncated, err := scope(ctx, tx, tenantID, nodeID)
	if err != nil {
		return Report{}, err
	}
	if len(ids) == 0 {
		return Report{}, &fail{status: http.StatusNotFound, msg: "not found"}
	}
	sessions, listTruncated, err := sessions(ctx, tx, tenantID, ids, limit)
	if err != nil {
		return Report{}, err
	}
	if len(sessions) > 0 {
		usage, err := readUsage(ctx, tx, tenantID, sessionIDs(sessions))
		if err != nil {
			if relationMissing(err) {
				return Report{}, &fail{status: http.StatusInternalServerError, msg: "session usage relation is not available"}
			}
			return Report{}, err
		}
		for i := range sessions {
			applyUsage(&sessions[i], usage[sessions[i].ID])
		}
	}
	if sessions == nil {
		sessions = []Session{}
	}
	return Report{
		NodeID:              nodeID,
		Kind:                kind,
		Currency:            currencyUSD,
		UsageAvailable:      true,
		IncludesDescendants: len(ids) > 1,
		ScopeTruncated:      truncated,
		ListTruncated:       listTruncated,
		Sessions:            sessions,
		Totals:              summarize(sessions),
	}, nil
}

func relationMissing(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42703")
}

// assertUsageRelation reads the published US1 columns. A missing relation or
// column is an error. It is not reported as an empty cost.
func assertUsageRelation(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		SELECT session_id, model, input_tokens, output_tokens, cached_input_tokens, provisional,
			price_version, estimated_cost_usd, billing_mode, subscription_label
		FROM harness_session_usage WHERE false`)
	return err
}

func scope(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) ([]string, string, bool, error) {
	// The primary-key lookup establishes both the root kind and its visibility.
	var kind string
	err := tx.QueryRow(ctx, `SELECT k.slug FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.tenant_id = $1::uuid AND n.id = $2::uuid AND n.deleted_at IS NULL
			AND k.slug IN ('ticket', 'epic', 'task')
			AND ((SELECT aeon_visible_all()) OR n.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))`, tenantID, nodeID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	type item struct {
		id    string
		depth int
	}
	ids := []string{nodeID}
	frontier := []item{{nodeID, 0}}
	for len(frontier) > 0 {
		parent := frontier[0]
		frontier = frontier[1:]
		// This uses nodes_siblings_idx and fetches at most the remaining
		// capacity plus one. At depth eight the one-row probe tells us whether
		// any visible descendant was omitted by the depth cap.
		budget := maxScope + 1 - len(ids)
		if parent.depth == maxDepth {
			budget = 1
		}
		rows, err := tx.Query(ctx, `SELECT c.id::text FROM nodes c
			WHERE c.tenant_id = $1::uuid AND c.parent_id = $2::uuid AND c.deleted_at IS NULL
				AND ((SELECT aeon_visible_all()) OR c.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
			ORDER BY c.position, c.id LIMIT $3`, tenantID, parent.id, budget)
		if err != nil {
			return nil, "", false, err
		}
		var children []string
		for rows.Next() {
			var child string
			if err := rows.Scan(&child); err != nil {
				rows.Close()
				return nil, "", false, err
			}
			children = append(children, child)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, "", false, err
		}
		if parent.depth == maxDepth {
			if len(children) > 0 {
				return ids, kind, true, nil
			}
			continue
		}
		for _, child := range children {
			if len(ids) == maxScope {
				return ids, kind, true, nil
			}
			ids = append(ids, child)
			frontier = append(frontier, item{child, parent.depth + 1})
		}
	}
	return ids, kind, false, nil
}

func sessions(ctx context.Context, tx pgx.Tx, tenantID string, ids []string, limit int) ([]Session, bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.ticket_node_id::text, tn.key, tn.title, s.harness, s.display_label,
			s.model, s.reasoning_effort, s.phase, s.created_at, s.stopped_at,
			CASE
				WHEN s.stopped_at IS NOT NULL AND s.stopped_at < s.created_at THEN NULL
				WHEN coalesce(s.stopped_at, statement_timestamp()) < s.created_at THEN NULL
				ELSE (EXTRACT(EPOCH FROM (coalesce(s.stopped_at, statement_timestamp()) - s.created_at)))::bigint
			END,
			s.stopped_at IS NULL
		FROM harness_sessions s
		JOIN nodes tn ON tn.tenant_id = s.tenant_id AND tn.id = s.ticket_node_id AND tn.deleted_at IS NULL
		WHERE s.tenant_id = $1::uuid
			AND s.ticket_node_id = ANY($2::text[]::uuid[])
			AND ((SELECT aeon_visible_all()) OR s.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
		ORDER BY coalesce(s.stopped_at, s.heartbeat_at, s.created_at) DESC, s.id DESC
		LIMIT $3`, tenantID, ids, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s := Session{
			Models:      []UsageModel{},
			TokensState: "unknown",
			CachedState: "unknown",
			CostState:   "unknown",
		}
		var label, model, effort *string
		var duration *int64
		var ongoing bool
		if err := rows.Scan(&s.ID, &s.TicketNodeID, &s.TicketKey, &s.TicketTitle, &s.Harness, &label,
			&model, &effort, &s.Phase, &s.StartedAt, &s.EndedAt, &duration, &ongoing); err != nil {
			return nil, false, err
		}
		s.Label = cleanText(label, 128)
		s.Model = cleanText(model, 128)
		s.Effort = cleanText(effort, 40)
		s.ModelState = missingState(s.Model)
		s.EffortState = missingState(s.Effort)
		s.DurationSeconds = duration
		if duration == nil {
			s.DurationState = "unknown"
		} else if ongoing {
			s.DurationState = "ongoing"
		} else {
			s.DurationState = "known"
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}

type usageRow struct {
	model, billing              string
	input, output, cached, cost *string
	price, subscription         *string
	provisional                 bool
}

func sessionIDs(sessions []Session) []string {
	ids := make([]string, len(sessions))
	for i := range sessions {
		ids[i] = sessions[i].ID
	}
	return ids
}

func readUsage(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) (map[string][]usageRow, error) {
	// Each lateral lookup uses the (tenant_id, session_id, model) primary key
	// and reads at most 33 rows. Duration is read separately, once per session.
	rows, err := tx.Query(ctx, `
		SELECT selected.session_id::text, u.model, u.input_tokens::text, u.output_tokens::text,
			u.cached_input_tokens::text, u.provisional, u.price_version::text,
			u.estimated_cost_usd::text, u.billing_mode, u.subscription_label
		FROM unnest($2::text[]::uuid[]) AS selected(session_id)
		CROSS JOIN LATERAL (
			SELECT model, input_tokens, output_tokens, cached_input_tokens, provisional,
				price_version, estimated_cost_usd, billing_mode, subscription_label
			FROM harness_session_usage
			WHERE tenant_id = $1::uuid AND session_id = selected.session_id
			ORDER BY model LIMIT $3
		) u`, tenantID, ids, maxModels+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]usageRow{}
	for rows.Next() {
		var id string
		var row usageRow
		if err := rows.Scan(&id, &row.model, &row.input, &row.output, &row.cached, &row.provisional, &row.price, &row.cost, &row.billing, &row.subscription); err != nil {
			return nil, err
		}
		out[id] = append(out[id], row)
	}
	return out, rows.Err()
}

func applyUsage(s *Session, rows []usageRow) {
	if s.Models == nil {
		s.Models = []UsageModel{}
	}
	if len(rows) == 0 {
		return
	}
	s.UsageReported = true
	if len(rows) > maxModels {
		s.ModelsTruncated = true
		rows = rows[:maxModels]
	}
	var inSum, outSum, cachedSum int64
	costSum := new(big.Rat)
	var inCount, cachedCount, costCount int
	var inOverflow, cachedOverflow bool
	var costProvisional bool
	for _, row := range rows {
		m := normalizeModel(row)
		s.Models = append(s.Models, m)
		if m.TokensState == "known" {
			in, _ := parseNonNeg(m.InputTokens)
			out, _ := parseNonNeg(m.OutputTokens)
			var ok bool
			if inSum, ok = addNonNeg(inSum, in); !ok {
				inOverflow = true
			}
			if outSum, ok = addNonNeg(outSum, out); !ok {
				inOverflow = true
			}
			inCount++
		} else {
			s.UnknownTokenModels++
		}
		if m.CachedState == "known" {
			n, _ := parseNonNeg(m.CachedInputTokens)
			var ok bool
			if cachedSum, ok = addNonNeg(cachedSum, n); !ok {
				cachedOverflow = true
			}
			cachedCount++
		}
		if m.CostState == "estimated" {
			part, ok := parseDecimal(m.EstimatedCostUSD)
			if ok {
				costSum.Add(costSum, part)
				costCount++
				if m.Provisional {
					costProvisional = true
				}
			} else {
				s.UnknownCostModels++
			}
		} else {
			s.UnknownCostModels++
		}
	}
	shown := len(rows)
	total := shown
	if s.ModelsTruncated {
		total++
		s.UnknownTokenModels++
		s.UnknownCostModels++
	}
	s.TokensState, s.InputTokens, s.OutputTokens = pairState(inCount, total, inOverflow, inSum, outSum)
	s.CachedState, s.CachedInputTokens = oneState(cachedCount, total, cachedOverflow, cachedSum)
	s.CostState, s.EstimatedCostUSD = costTotal(costCount, total, tooWide(costSum), costProvisional, costSum)
}

func summarize(sessions []Session) Totals {
	t := Totals{
		SessionCount:  len(sessions),
		TokensState:   "unknown",
		CachedState:   "unknown",
		CostState:     "unknown",
		Currency:      currencyUSD,
		DurationState: "unknown",
	}
	if len(sessions) == 0 {
		return t
	}
	var inSum, outSum, cachedSum, durationSum int64
	var inCount, inComplete, cachedCount, cachedComplete, costCount, costComplete, durationKnown int
	var costProvisional, durationOngoing bool
	var inOverflow, cachedOverflow, durationOverflow bool
	costSum := new(big.Rat)
	for _, s := range sessions {
		t.UnknownTokenModels += s.UnknownTokenModels
		t.UnknownCostModels += s.UnknownCostModels
		in, out, inOK := tokenPair(s.InputTokens, s.OutputTokens)
		tokensUsable := inOK && (s.TokensState == "known" || s.TokensState == "partial")
		if tokensUsable {
			var ok bool
			if inSum, ok = addNonNeg(inSum, in); !ok {
				inOverflow = true
			}
			if outSum, ok = addNonNeg(outSum, out); !ok {
				inOverflow = true
			}
			inCount++
			if s.TokensState == "known" {
				inComplete++
			}
		}
		if !tokensUsable || s.TokensState != "known" {
			t.UnknownTokenSessions++
		}
		cached, cachedOK := parseNonNeg(s.CachedInputTokens)
		cachedUsable := cachedOK && (s.CachedState == "known" || s.CachedState == "partial")
		if cachedUsable {
			var ok bool
			if cachedSum, ok = addNonNeg(cachedSum, cached); !ok {
				cachedOverflow = true
			}
			cachedCount++
			if s.CachedState == "known" {
				cachedComplete++
			}
		}
		part, costOK := parseDecimal(s.EstimatedCostUSD)
		costUsable := costOK && (s.CostState == "estimated" || s.CostState == "provisional" || s.CostState == "partial")
		if costUsable {
			costSum.Add(costSum, part)
			costCount++
			if s.CostState != "partial" {
				costComplete++
			}
			if s.CostState == "provisional" {
				costProvisional = true
			}
		}
		if !costUsable || (s.CostState != "estimated" && s.CostState != "provisional") {
			t.UnknownCostSessions++
		}
		if s.DurationSeconds != nil && *s.DurationSeconds >= 0 {
			var ok bool
			if durationSum, ok = addNonNeg(durationSum, *s.DurationSeconds); !ok {
				durationOverflow = true
			}
			durationKnown++
			if s.DurationState == "ongoing" {
				durationOngoing = true
			}
		}
	}
	t.TokensState, t.InputTokens, t.OutputTokens = coverageState(inCount, inComplete, len(sessions), inOverflow, inSum, outSum)
	t.CachedState, t.CachedInputTokens = coverageOne(cachedCount, cachedComplete, len(sessions), cachedOverflow, cachedSum)
	completeCost := costComplete == len(sessions) && costCount == len(sessions)
	t.CostState, t.EstimatedCostUSD = costTotal(costCount, costDenom(costCount, completeCost), tooWide(costSum), costProvisional && completeCost, costSum)
	t.DurationState, t.DurationSeconds = durationTotal(durationKnown, len(sessions), durationOverflow, durationOngoing, durationSum)
	return t
}

func costDenom(contributors int, complete bool) int {
	if contributors == 0 {
		return 1
	}
	if complete {
		return contributors
	}
	return contributors + 1
}

func coverageState(contributors, complete, sessions int, overflow bool, in, out int64) (string, *string, *string) {
	if contributors == 0 || overflow || complete > sessions {
		return "unknown", nil, nil
	}
	state := "partial"
	if complete == sessions {
		state = "known"
	}
	return state, intString(in), intString(out)
}

func coverageOne(contributors, complete, sessions int, overflow bool, sum int64) (string, *string) {
	if contributors == 0 || overflow {
		return "unknown", nil
	}
	state := "partial"
	if complete == sessions {
		state = "known"
	}
	return state, intString(sum)
}

func pairState(known, total int, overflow bool, in, out int64) (string, *string, *string) {
	if known == 0 || overflow {
		return "unknown", nil, nil
	}
	state := "partial"
	if known == total {
		state = "known"
	}
	return state, intString(in), intString(out)
}

func oneState(known, total int, overflow bool, sum int64) (string, *string) {
	if known == 0 || overflow {
		return "unknown", nil
	}
	state := "partial"
	if known == total {
		state = "known"
	}
	return state, intString(sum)
}

func costTotal(known, total int, overflow, provisional bool, sum *big.Rat) (string, *string) {
	if known == 0 || overflow || sum == nil {
		return "unknown", nil
	}
	state := "partial"
	if known == total {
		if provisional {
			state = "provisional"
		} else {
			state = "estimated"
		}
	}
	return state, decimalString(sum)
}

func durationTotal(known, total int, overflow, ongoing bool, sum int64) (string, *int64) {
	if known == 0 || overflow {
		return "unknown", nil
	}
	state := "partial"
	if known == total {
		if ongoing {
			state = "ongoing"
		} else {
			state = "known"
		}
	}
	return state, &sum
}

func normalizeModel(row usageRow) UsageModel {
	m := UsageModel{
		TokensState: "unknown",
		CachedState: "unknown",
		CostState:   "unknown",
		BillingMode: "unknown",
		Provisional: row.provisional,
	}
	if name := cleanText(&row.model, 128); name != nil {
		m.Model = *name
	}
	if in, out, ok := tokenPair(row.input, row.output); ok {
		m.InputTokens = intString(in)
		m.OutputTokens = intString(out)
		m.TokensState = "known"
	}
	if cached, ok := parseNonNeg(row.cached); ok {
		m.CachedInputTokens = intString(cached)
		m.CachedState = "known"
	}
	switch row.billing {
	case "api", "subscription", "unknown":
		m.BillingMode = row.billing
	}
	// Only api billing is priced. A stored estimate on a subscription or
	// unknown row is historical and never shown or summed as dollars.
	if m.BillingMode == "api" {
		if cost, ok := parseDecimal(row.cost); ok {
			m.EstimatedCostUSD = decimalString(cost)
			m.CostState = "estimated"
		}
		if row.price != nil && intPattern.MatchString(strings.TrimSpace(*row.price)) {
			v := strings.TrimSpace(*row.price)
			m.PriceVersion = &v
		}
	}
	if m.BillingMode == "subscription" {
		m.SubscriptionLabel = cleanText(row.subscription, 120)
	}
	return m
}

func tokenPair(in, out *string) (int64, int64, bool) {
	a, aOK := parseNonNeg(in)
	b, bOK := parseNonNeg(out)
	if !aOK || !bOK {
		return 0, 0, false
	}
	return a, b, true
}

func parseNonNeg(s *string) (int64, bool) {
	if s == nil {
		return 0, false
	}
	v := strings.TrimSpace(*s)
	if !intPattern.MatchString(v) {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func parseDecimal(s *string) (*big.Rat, bool) {
	if s == nil {
		return nil, false
	}
	v := strings.TrimSpace(*s)
	if !decimalPattern.MatchString(v) {
		return nil, false
	}
	n, ok := new(big.Rat).SetString(v)
	if !ok || n.Sign() < 0 {
		return nil, false
	}
	return n, true
}

func tooWide(n *big.Rat) bool {
	if n == nil || n.Sign() == 0 {
		return false
	}
	whole := strings.Split(n.FloatString(12), ".")[0]
	return len(whole) > 18
}

func addNonNeg(sum, n int64) (int64, bool) {
	if n < 0 || sum > math.MaxInt64-n {
		return sum, false
	}
	return sum + n, true
}

func intString(n int64) *string {
	s := strconv.FormatInt(n, 10)
	return &s
}

func decimalString(n *big.Rat) *string {
	s := n.FloatString(12)
	return &s
}

func cleanText(s *string, max int) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" || utf8.RuneCountInString(v) > max || strings.ContainsFunc(v, unicode.IsControl) {
		return nil
	}
	return &v
}

func missingState(s *string) string {
	if s == nil {
		return "missing"
	}
	return "known"
}
