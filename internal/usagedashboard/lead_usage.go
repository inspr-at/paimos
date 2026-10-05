// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const leadSessionLimit = 1000
const leadContributionLimit = 5000

// These are measured sums with coverage, never estimates or quota percentages.
type LeadUsageMeasure struct {
	Measured *string `json:"measured"`
	Known    int     `json:"known"`
	Unknown  int     `json:"unknown"`
}
type LeadUsageTotals struct {
	Contributions int              `json:"contributions"`
	Attempts      int              `json:"attempts"`
	Tokens        LeadUsageMeasure `json:"tokens"`
	Active        LeadUsageMeasure `json:"active_ms"`
	Waiting       LeadUsageMeasure `json:"waiting_ms"`
}
type LeadUsageHold struct {
	Window   string  `json:"window_id"`
	Unit     string  `json:"unit"`
	Reserved string  `json:"reserved_units"`
	Settled  *string `json:"settled_units"`
	Unknown  int     `json:"unknown_settlements"`
}
type LeadUsageHeld struct {
	State   string          `json:"state"`
	Scope   string          `json:"scope"`
	Windows []LeadUsageHold `json:"windows"`
}
type LeadUsage struct {
	Project    string          `json:"project_id"`
	Generation *int64          `json:"generation"`
	From       time.Time       `json:"from"`
	To         time.Time       `json:"to"`
	Basis      string          `json:"basis"`
	Partial    bool            `json:"partial"`
	Truncated  bool            `json:"truncated"`
	Gaps       []string        `json:"gaps"`
	Total      LeadUsageTotals `json:"total"`
	Lead       LeadUsageTotals `json:"lead"`
	Worker     LeadUsageTotals `json:"worker"`
	Review     LeadUsageTotals `json:"review"`
	Fix        LeadUsageTotals `json:"fix"`
	Held       LeadUsageHeld   `json:"held"`
	Forecast   string          `json:"finish_forecast"`
}

func (out *LeadUsage) gap(code string) {
	out.Partial = true
	for _, old := range out.Gaps {
		if old == code {
			return
		}
	}
	out.Gaps = append(out.Gaps, code)
}
func (m *LeadUsageMeasure) add(raw *string) error {
	if raw == nil {
		m.Unknown++
		return nil
	}
	n, ok := new(big.Int).SetString(*raw, 10)
	if !ok || n.Sign() < 0 {
		return errors.New("invalid stored lead measurement")
	}
	if m.Measured != nil {
		old, ok := new(big.Int).SetString(*m.Measured, 10)
		if !ok {
			return errors.New("invalid aggregate measurement")
		}
		n.Add(n, old)
	}
	s := n.String()
	m.Measured = &s
	m.Known++
	return nil
}

type leadEvidence struct {
	id, session, project, episodeProject, role string
	run                                        *string
	tokens, active, waiting                    *string
	complete                                   bool
	truncated                                  bool
}
type leadUsageAccumulator struct {
	totals   LeadUsageTotals
	attempts map[string]bool
	seen     map[string]bool
}

func newLeadAccumulator() *leadUsageAccumulator {
	return &leadUsageAccumulator{attempts: map[string]bool{}, seen: map[string]bool{}}
}
func (a *leadUsageAccumulator) add(e leadEvidence) error {
	if a.seen[e.id] {
		return nil
	}
	a.seen[e.id] = true
	a.totals.Contributions++
	attempt := "session:" + e.session
	if e.run != nil {
		attempt = "run:" + *e.run
	}
	if e.session != "" || e.run != nil {
		a.attempts[attempt] = true
	}
	a.totals.Attempts = len(a.attempts)
	for _, part := range []struct {
		m     *LeadUsageMeasure
		value *string
	}{{&a.totals.Tokens, e.tokens}, {&a.totals.Active, e.active}, {&a.totals.Waiting, e.waiting}} {
		if err := part.m.add(part.value); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) leadUsage(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if len(r.URL.RawQuery) > 512 || !workorders.UUID(r.PathValue("projectId")) {
		return nil, workorders.Fail(400, "invalid lead usage query")
	}
	project := r.PathValue("projectId")
	for _, perm := range []string{"harness.read", "outcome.read"} {
		if err := authz.RequireTx(r.Context(), tx, p, perm, authz.Scope{ProjectID: project}); err != nil {
			return nil, err
		}
	}
	q := r.URL.Query()
	for key, values := range q {
		if (key != "generation" && key != "from" && key != "to") || len(values) != 1 {
			return nil, workorders.Fail(400, "invalid lead usage query")
		}
	}
	from, to, err := parseRange(q.Get("from"), q.Get("to"), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	var generation *int64
	if raw, present := q["generation"]; present {
		n, e := strconv.ParseInt(raw[0], 10, 64)
		if e != nil || n < 1 {
			return nil, workorders.Fail(400, "invalid lead generation")
		}
		generation = &n
	}
	ctx := r.Context()
	// Compatible with the independently owned AEON-734/689 expansions. A
	// missing contract is unavailable, never an empty history of measured zeros.
	var leads, episodes bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass('project_lead_generations') IS NOT NULL,
  to_regclass('ticket_work_contributions') IS NOT NULL AND to_regclass('ticket_work_episodes') IS NOT NULL`).Scan(&leads, &episodes); err != nil {
		return nil, err
	}
	if !leads {
		return nil, workorders.Fail(503, "lead generation usage is unavailable")
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project')`, project).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, workorders.Fail(404, "project not found")
	}
	if generation != nil {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_lead_generations WHERE project_id=$1 AND generation=$2)`, project, generation).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, workorders.Fail(404, "lead generation not found")
		}
	}
	out := LeadUsage{Project: project, Generation: generation, From: from, To: to,
		Basis: "cumulative_for_sessions_and_segments_started_in_range", Gaps: []string{}, Forecast: "estimate_unavailable",
		Held: LeadUsageHeld{State: "withheld", Scope: "attributed_run_reservations_not_exclusive_quota", Windows: []LeadUsageHold{}}}
	// This is a bounded live projection; each source is read once.
	can, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	if !can("harness.read", "") || !can("outcome.read", "") {
		out.gap("source_visibility_incomplete")
	}
	groups := map[string]*leadUsageAccumulator{}
	for _, role := range []string{"lead", "worker", "review", "fix"} {
		groups[role] = newLeadAccumulator()
	}
	total := newLeadAccumulator()
	runs := map[string]bool{}
	add := func(e leadEvidence) error {
		if !can("harness.read", e.project) || !can("outcome.read", e.project) || (e.episodeProject != "" && (!can("harness.read", e.episodeProject) || !can("outcome.read", e.episodeProject))) {
			out.gap("source_project_unreadable")
			return nil
		}
		if e.truncated {
			out.Truncated = true
			out.gap("lead_model_limit")
		}
		if !e.complete {
			out.gap("incomplete_measurement")
		}
		a := groups[e.role]
		if a == nil {
			out.gap("unknown_contribution_role")
			return nil
		}
		if err := a.add(e); err != nil {
			return err
		}
		if err := total.add(e); err != nil {
			return err
		}
		if e.run != nil {
			runs[*e.run] = true
		}
		return nil
	}
	sessions, err := loadLeadSessions(ctx, tx, project, generation, from, to)
	if err != nil {
		return nil, err
	}
	if len(sessions) > leadSessionLimit {
		sessions = sessions[:leadSessionLimit]
		out.Truncated = true
		out.gap("session_limit")
	}
	measuredSessions := map[string]bool{}
	if episodes {
		evidence, truncated, e := loadLeadContributions(ctx, tx, project, generation, from, to)
		if e != nil {
			return nil, e
		}
		if truncated {
			out.Truncated = true
			out.gap("contribution_limit")
		}
		for _, row := range evidence {
			measuredSessions[row.session] = true
			if err = add(row); err != nil {
				return nil, err
			}
		}
	} else {
		out.gap("episodes_unavailable")
	}
	for _, row := range sessions {
		if row.role != "lead" && measuredSessions[row.session] {
			continue
		}
		if err = add(row); err != nil {
			return nil, err
		}
	}
	// Legacy rows have no frozen lineage. Do not guess their original lead from
	// today's mutable parent, run retries, ticket/project or current singleton.
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE project_id=$1 AND usage_lead_session_id IS NULL AND role='worker' AND created_at >= $2 AND created_at < $3)`, project, from, to).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		out.gap("legacy_lineage_unknown")
	}
	out.Total = total.totals
	out.Lead = groups["lead"].totals
	out.Worker = groups["worker"].totals
	out.Review = groups["review"].totals
	out.Fix = groups["fix"].totals
	held, truncated, err := loadLeadHolds(ctx, tx, p, runs)
	if err != nil {
		return nil, err
	}
	out.Held = held
	if truncated {
		out.Truncated = true
		out.gap("reservation_limit")
	}
	return out, nil
}

func loadLeadSessions(ctx context.Context, tx pgx.Tx, project string, generation *int64, from, to time.Time) ([]leadEvidence, error) {
	rows, err := tx.Query(ctx, `WITH selected AS MATERIALIZED (
 SELECT s.*,g.session_id AS lead_session FROM harness_sessions s
 JOIN project_lead_generations g ON g.tenant_id=s.tenant_id AND (g.session_id=s.usage_lead_session_id OR g.session_id=s.id)
 WHERE g.project_id=$1 AND ($2::bigint IS NULL OR g.generation=$2) AND s.created_at >= $3 AND s.created_at < $4
 ORDER BY s.created_at,s.id LIMIT $5
 ) SELECT s.id::text,s.project_id::text,s.run_id::text,s.id=s.lead_session,
  CASE WHEN NOT EXISTS(SELECT 1 FROM harness_sessions other WHERE other.run_id=s.run_id AND other.id<>s.id) THEN u.tokens::text END,CASE WHEN NOT EXISTS(SELECT 1 FROM harness_sessions other WHERE other.run_id=s.run_id AND other.id<>s.id) THEN r.active_ms::text END,
  CASE WHEN NOT EXISTS(SELECT 1 FROM harness_sessions other WHERE other.run_id=s.run_id AND other.id<>s.id) THEN r.waiting_ms::text END,
  coalesce(u.complete,false),coalesce(u.truncated,false)
 FROM selected s
 LEFT JOIN agent_runs r ON r.tenant_id=s.tenant_id AND r.id=s.run_id
 LEFT JOIN LATERAL (
  SELECT CASE WHEN count(*) BETWEEN 1 AND 200 AND bool_and(input_tokens IS NOT NULL AND output_tokens IS NOT NULL) THEN sum(input_tokens::numeric+output_tokens) END AS tokens,
   count(*) BETWEEN 1 AND 200 AND bool_and(NOT provisional) AS complete,count(*)>200 AS truncated
  FROM (SELECT input_tokens,output_tokens,provisional FROM harness_session_usage WHERE tenant_id=s.tenant_id AND session_id=s.id ORDER BY model LIMIT 201) bounded
 ) u ON s.id=s.lead_session
 ORDER BY s.created_at,s.id`, project, generation, from, to, leadSessionLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []leadEvidence{}
	for rows.Next() {
		var e leadEvidence
		var lead, complete, truncated bool
		if err = rows.Scan(&e.session, &e.project, &e.run, &lead, &e.tokens, &e.active, &e.waiting, &complete, &truncated); err != nil {
			return nil, err
		}
		e.truncated = truncated
		e.id = "session:" + e.session
		e.role = "worker"
		if lead {
			e.role = "lead"
			e.complete = complete && e.active != nil && e.waiting != nil && !truncated
		} else {
			e.tokens = nil
			e.active = nil
			e.waiting = nil
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func loadLeadContributions(ctx context.Context, tx pgx.Tx, project string, generation *int64, from, to time.Time) ([]leadEvidence, bool, error) {
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.session_id::text,c.source_project_id::text,ep.source_project_id::text,c.run_id::text,c.role,
 CASE WHEN c.tokens_known THEN c.tokens::text END,CASE WHEN c.timing_known THEN c.active_ms::text END,
 CASE WHEN c.timing_known THEN c.waiting_ms::text END,
 NOT c.gap AND c.tokens_complete AND c.timing_complete AND c.stopped AND ep.coverage_complete
 FROM ticket_work_contributions c JOIN ticket_work_episodes ep ON ep.tenant_id=c.tenant_id AND ep.id=c.episode_id
 JOIN harness_sessions s ON s.tenant_id=c.tenant_id AND s.id=c.session_id
 JOIN project_lead_generations g ON g.tenant_id=s.tenant_id AND g.session_id=s.usage_lead_session_id
 WHERE g.project_id=$1 AND ($2::bigint IS NULL OR g.generation=$2) AND s.id<>g.session_id
 AND c.first_work_at >= $3 AND c.first_work_at < $4
 ORDER BY c.first_work_at,c.id LIMIT $5`, project, generation, from, to, leadContributionLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []leadEvidence{}
	for rows.Next() {
		var e leadEvidence
		if err = rows.Scan(&e.id, &e.session, &e.project, &e.episodeProject, &e.run, &e.role, &e.tokens, &e.active, &e.waiting, &e.complete); err != nil {
			return nil, false, err
		}
		e.id = "contribution:" + e.id
		switch e.role {
		case "built":
			e.role = "worker"
		case "reviewed":
			e.role = "review"
		case "fixed":
			e.role = "fix"
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > leadContributionLimit {
		return out[:leadContributionLimit], true, nil
	}
	return out, false, nil
}

func loadLeadHolds(ctx context.Context, tx pgx.Tx, p tenant.Principal, runs map[string]bool) (LeadUsageHeld, bool, error) {
	out := LeadUsageHeld{State: "withheld", Scope: "attributed_run_reservations_not_exclusive_quota", Windows: []LeadUsageHold{}}
	if p.Kind != tenant.Person {
		return out, false, nil
	}
	if err := authz.RequireTx(ctx, tx, p, "account.read", authz.Scope{}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return out, false, nil
		}
		return out, false, err
	}
	type reservation struct {
		account, window, unit, state string
		reserved                     string
		actual                       *string
	}
	// Stable run identities deduplicate repeated sessions, episode segments and
	// retries' replay. No sums of window.reserved or pooled window.used are read.
	rows, err := tx.Query(ctx, `SELECT w.account_id::text,w.id::text,w.unit,r.state,r.reserved_units::text,r.actual_units::text
 FROM account_reservations r JOIN account_allowance_windows w ON w.tenant_id=r.tenant_id AND w.id=r.window_id
 WHERE r.run_id=ANY($1::uuid[]) ORDER BY r.id LIMIT 1001`, leadIDs(runs))
	if err != nil {
		return out, false, err
	}
	reservations := []reservation{}
	accounts := map[string]bool{}
	for rows.Next() {
		var v reservation
		if err = rows.Scan(&v.account, &v.window, &v.unit, &v.state, &v.reserved, &v.actual); err != nil {
			rows.Close()
			return out, false, err
		}
		reservations = append(reservations, v)
		accounts[v.account] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, false, err
	}
	truncated := len(reservations) > 1000
	if truncated {
		reservations = reservations[:1000]
	}
	// Sharing flags do not widen Q5's owner-only detail boundary. Also retain
	// the existing privacy veto for private siblings of a pooled account.
	owners, err := accountprivacy.LoadControls(ctx, tx, p, leadIDs(accounts))
	if err != nil {
		return out, truncated, err
	}
	privacy, err := accountprivacy.Load(ctx, tx, p, leadIDs(accounts))
	if err != nil {
		return out, truncated, err
	}
	type window struct {
		hold              LeadUsageHold
		reserved, settled LeadUsageMeasure
	}
	windows := map[string]*window{}
	withheld := false
	for _, v := range reservations {
		if !owners[v.account] || !privacy[v.account] {
			withheld = true
			continue
		}
		b := windows[v.window]
		if b == nil {
			b = &window{hold: LeadUsageHold{Window: v.window, Unit: v.unit}}
			windows[v.window] = b
		}
		switch v.state {
		case "active":
			if err = b.reserved.add(&v.reserved); err != nil {
				return out, truncated, err
			}
		case "settled":
			if err = b.settled.add(v.actual); err != nil {
				return out, truncated, err
			}
		}
	}
	// Deterministic output without disclosing hidden account IDs/counts.
	ids := map[string]bool{}
	for id := range windows {
		ids[id] = true
	}
	for _, id := range leadIDs(ids) {
		b := windows[id]
		b.hold.Reserved = "0"
		if b.reserved.Measured != nil {
			b.hold.Reserved = *b.reserved.Measured
		}
		b.hold.Settled = b.settled.Measured
		b.hold.Unknown = b.settled.Unknown
		out.Windows = append(out.Windows, b.hold)
	}
	out.State = "available"
	if withheld {
		out.State = "withheld"
		if len(windows) > 0 {
			out.State = "partial"
		}
	}
	if truncated {
		out.State = "partial"
	}
	return out, truncated, nil
}

func leadIDs(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
