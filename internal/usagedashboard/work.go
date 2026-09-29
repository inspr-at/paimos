// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"context"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AEON-301: what agents got done, how long they worked and where the waste is.
// Every figure here comes from sessions, runs and outcome events that exist for
// every session, so it is known without a usage report. The one reported figure,
// tokens, carries its own coverage count.
const (
	workBasis       = "sessions_started_in_range"
	maxWorkTickets  = 50
	maxWasteRows    = 10
	maxDoneTickets  = 5000
	stuckAfter      = 10 * time.Minute
	lateStopGrace   = 15 * time.Minute
	retriedSessions = 3
)

// Work is the outcome side of the dashboard. Sessions are those that started in
// [from, to), the same set as the usage totals. Done and released count tickets
// whose outcome event was recorded in [from, to) and that an agent worked on.
type Work struct {
	Basis            string       `json:"basis"`
	Sessions         int          `json:"sessions"`
	WorkerSessions   int          `json:"worker_sessions"`
	TimedSessions    int          `json:"timed_sessions"`
	AgentSeconds     int64        `json:"agent_seconds"`
	ReportedSessions int          `json:"usage_reported_sessions"`
	ModelSessions    int          `json:"model_sessions"`
	Done             int          `json:"done"`
	Released         int          `json:"released"`
	DoneAttributed   int          `json:"done_attributed"`
	TicketsWorked    int          `json:"tickets_worked"`
	TicketsDone      int          `json:"tickets_done"`
	Days             []WorkDay    `json:"days"`
	Tickets          []WorkTicket `json:"tickets"`
	ByHarness        []WorkGroup  `json:"by_harness"`
	ByModel          []WorkGroup  `json:"by_model"`
	ByProject        []WorkGroup  `json:"by_project"`
	Waste            Waste        `json:"waste"`
}

// WorkDay is one UTC day of the range. Every day is present; zero is a known zero.
type WorkDay struct {
	Day          string `json:"day"`
	Done         int    `json:"done"`
	Sessions     int    `json:"sessions"`
	AgentSeconds int64  `json:"agent_seconds"`
}

// WorkTicket is one ticket that sessions in the range worked on.
type WorkTicket struct {
	ID               string    `json:"id"`
	Key              string    `json:"key"`
	Title            string    `json:"title"`
	ProjectID        string    `json:"project_id"`
	ProjectKey       string    `json:"project_key"`
	State            string    `json:"state"`
	DoneInRange      bool      `json:"done_in_range"`
	Released         bool      `json:"released"`
	Sessions         int       `json:"sessions"`
	TimedSessions    int       `json:"timed_sessions"`
	AgentSeconds     int64     `json:"agent_seconds"`
	Tokens           *string   `json:"tokens"`
	ReportedSessions int       `json:"usage_reported_sessions"`
	LastActiveAt     time.Time `json:"last_active_at"`
}

// WorkGroup is one harness, model or project.
type WorkGroup struct {
	Key              string  `json:"key"`
	Label            string  `json:"label"`
	Sessions         int     `json:"sessions"`
	TimedSessions    int     `json:"timed_sessions"`
	AgentSeconds     int64   `json:"agent_seconds"`
	Done             int     `json:"done"`
	Tokens           *string `json:"tokens"`
	ReportedSessions int     `json:"usage_reported_sessions"`
}

// Waste counts every wasted session or retried ticket; Rows is the costliest few.
type Waste struct {
	Total    int         `json:"total"`
	Stuck    int         `json:"stuck"`
	Failed   int         `json:"failed"`
	Lost     int         `json:"lost"`
	NoResult int         `json:"no_result"`
	Retried  int         `json:"retried"`
	Rows     []WasteItem `json:"rows"`
}

// WasteItem is one session, or for retried one ticket with all its sessions.
type WasteItem struct {
	Kind         string    `json:"kind"`
	SessionID    string    `json:"session_id"`
	ProjectID    string    `json:"project_id"`
	ProjectKey   string    `json:"project_key"`
	TicketID     *string   `json:"ticket_id"`
	TicketKey    *string   `json:"ticket_key"`
	TicketTitle  *string   `json:"ticket_title"`
	Harness      string    `json:"harness"`
	Model        *string   `json:"model"`
	Label        *string   `json:"label"`
	At           time.Time `json:"at"`
	Sessions     int       `json:"sessions"`
	AgentSeconds *int64    `json:"agent_seconds"`
}

type workRow struct {
	id, projectID, projectKey, projectTitle string
	ticketID, ticketKey, ticketTitle        *string
	ticketState                             *string
	harness                                 string
	model                                   *string
	role, shape, phase, activity            string
	created                                 time.Time
	heartbeat, stopped                      *time.Time
	stopReason                              string
	worktree                                bool
	commits                                 int
	label                                   *string
	runStatus, runOutcome                   *string
	usageRows                               int64
	tokens                                  *string
}

type doneRow struct {
	ticketID, projectID, projectTitle string
	at                                time.Time
	harness, model                    *string
}

func emptyWork() Work {
	return Work{
		Basis: workBasis, Days: []WorkDay{}, Tickets: []WorkTicket{},
		ByHarness: []WorkGroup{}, ByModel: []WorkGroup{}, ByProject: []WorkGroup{},
		Waste: Waste{Rows: []WasteItem{}},
	}
}

func loadWork(ctx context.Context, tx pgx.Tx, from, to, now time.Time, project string) (Work, error) {
	var projectArg any
	if project != "" {
		projectArg = project
	}
	rows, err := loadWorkSessions(ctx, tx, from, to, projectArg)
	if err != nil {
		return Work{}, err
	}
	done, err := loadDone(ctx, tx, from, to, projectArg)
	if err != nil {
		return Work{}, err
	}
	var released int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT ticket_node_id) FROM outcome_events
		 WHERE kind = 'released' AND session_id IS NOT NULL
		   AND recorded_at >= $1 AND recorded_at < $2
		   AND ($3::uuid IS NULL OR project_id = $3)`, from, to, projectArg).Scan(&released); err != nil {
		return Work{}, err
	}
	tickets := map[string]struct{}{}
	for _, row := range rows {
		if row.ticketID != nil {
			tickets[*row.ticketID] = struct{}{}
		}
	}
	releasedTickets, err := loadReleasedTickets(ctx, tx, keys(tickets))
	if err != nil {
		return Work{}, err
	}
	out := buildWork(rows, done, releasedTickets, from, to, now)
	out.Released = released
	return out, nil
}

func loadWorkSessions(ctx context.Context, tx pgx.Tx, from, to time.Time, project any) ([]workRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.project_id::text, p.key, p.title,
		       t.id::text, t.key, t.title, t.state,
		       s.harness, s.model, s.role, s.work_shape, s.phase, s.activity,
		       s.created_at, s.heartbeat_at, s.stopped_at, coalesce(s.stop_reason, ''),
		       s.worktree IS NOT NULL, jsonb_array_length(s.commits), s.display_label,
		       r.status, r.outcome_detail,
		       coalesce(u.rows, 0), u.tokens::text
		  FROM harness_sessions s
		  JOIN nodes p ON p.tenant_id = s.tenant_id AND p.id = s.project_id
		  LEFT JOIN nodes t ON t.tenant_id = s.tenant_id AND t.id = s.ticket_node_id AND t.deleted_at IS NULL
		  LEFT JOIN agent_runs r ON r.tenant_id = s.tenant_id AND r.id = s.run_id
		  LEFT JOIN LATERAL (
		      SELECT count(*) AS rows,
		             sum(coalesce(x.input_tokens, 0) + coalesce(x.output_tokens, 0))
		                 FILTER (WHERE x.input_tokens IS NOT NULL OR x.output_tokens IS NOT NULL) AS tokens
		        FROM harness_session_usage x
		       WHERE x.tenant_id = s.tenant_id AND x.session_id = s.id
		  ) u ON true
		 WHERE s.created_at >= $1 AND s.created_at < $2
		   AND ($3::uuid IS NULL OR s.project_id = $3)
		 ORDER BY s.created_at, s.id
		 LIMIT $4`, from, to, project, maxSessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []workRow{}
	for rows.Next() {
		var r workRow
		if err := rows.Scan(
			&r.id, &r.projectID, &r.projectKey, &r.projectTitle,
			&r.ticketID, &r.ticketKey, &r.ticketTitle, &r.ticketState,
			&r.harness, &r.model, &r.role, &r.shape, &r.phase, &r.activity,
			&r.created, &r.heartbeat, &r.stopped, &r.stopReason,
			&r.worktree, &r.commits, &r.label,
			&r.runStatus, &r.runOutcome,
			&r.usageRows, &r.tokens,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadDone reads tickets finished in the range that an agent worked on, once
// per ticket (its first completion in the range). The harness and model are
// those of the ticket's latest worker session before it was done, so a ticket
// counts once, for the agent that finished it.
func loadDone(ctx context.Context, tx pgx.Tx, from, to time.Time, project any) ([]doneRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (o.ticket_node_id)
		       o.ticket_node_id::text, o.project_id::text, coalesce(p.title, p.key, ''), o.recorded_at, w.harness, w.model
		  FROM outcome_events o
		  LEFT JOIN nodes p ON p.tenant_id = o.tenant_id AND p.id = o.project_id
		  LEFT JOIN LATERAL (
		      SELECT s.harness, s.model FROM harness_sessions s
		       WHERE s.tenant_id = o.tenant_id AND s.ticket_node_id = o.ticket_node_id AND s.created_at <= o.recorded_at
		       ORDER BY (s.role = 'worker') DESC, s.created_at DESC, s.id DESC
		       LIMIT 1
		  ) w ON true
		 WHERE o.kind = 'ticket_done' AND o.session_id IS NOT NULL
		   AND o.recorded_at >= $1 AND o.recorded_at < $2
		   AND ($3::uuid IS NULL OR o.project_id = $3)
		 ORDER BY o.ticket_node_id, o.recorded_at, o.id
		 LIMIT $4`, from, to, project, maxDoneTickets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []doneRow{}
	for rows.Next() {
		var d doneRow
		if err := rows.Scan(&d.ticketID, &d.projectID, &d.projectTitle, &d.at, &d.harness, &d.model); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func loadReleasedTickets(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ticket_node_id::text FROM outcome_events WHERE kind = 'released' AND ticket_node_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func keys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func doneLike(state *string) bool {
	if state == nil {
		return false
	}
	switch *state {
	case "done", "accepted", "delivered":
		return true
	}
	return false
}

// agentSeconds is the time from a session's start to its last sign of life:
// its last heartbeat, or its stop when that came soon after. A session closed
// long after it went silent counts to its last heartbeat. A session with no
// heartbeat that was not stopped soon after registering has no known time.
func agentSeconds(r workRow, to time.Time) (int64, bool) {
	var end time.Time
	switch {
	case r.heartbeat != nil:
		end = *r.heartbeat
		if r.stopped != nil && r.stopped.After(end) && r.stopped.Sub(end) <= lateStopGrace {
			end = *r.stopped
		}
	case r.stopped != nil && r.stopped.Sub(r.created) <= lateStopGrace:
		end = *r.stopped
	default:
		return 0, false
	}
	if end.After(to) {
		end = to
	}
	if end.Before(r.created) {
		return 0, false
	}
	return int64(end.Sub(r.created) / time.Second), true
}

var problemWords = regexp.MustCompile(`(?i)\b(error|errored|failed|failure|blocked|crash|crashed|ownership lost|timeout|timed out)\b`)

// wasteKind is why one session was wasted, or "" when it was not. Stuck is
// about now; the rest are finished worker sessions on a ticket that is not
// done and that left nothing behind.
func wasteKind(r workRow, now time.Time) string {
	if r.stopped == nil {
		if (r.phase == "starting" || r.phase == "working" || r.phase == "stopping") && r.activity != "idle" && r.activity != "throttled" {
			last := r.created
			if r.heartbeat != nil {
				last = *r.heartbeat
			}
			if now.Sub(last) > stuckAfter {
				return "stuck"
			}
		}
		return ""
	}
	if r.role != "worker" || r.ticketID == nil || doneLike(r.ticketState) {
		return ""
	}
	if (r.runStatus != nil && (*r.runStatus == "failed" || *r.runStatus == "ownership_lost")) ||
		(r.stopReason != "heartbeat_lost" && problemWords.MatchString(strings.NewReplacer("_", " ", "-", " ").Replace(r.stopReason))) {
		return "failed"
	}
	if r.commits > 0 {
		return ""
	}
	if r.stopReason == "heartbeat_lost" {
		return "lost"
	}
	if (r.worktree && r.shape == "ship") || (r.runOutcome != nil && *r.runOutcome == "no_commit") {
		return "no_result"
	}
	return ""
}

type tally struct {
	sessions, timed, reported int
	seconds                   int64
	tokens                    *big.Int
}

func (t *tally) add(r workRow, secs int64, timed bool) {
	t.sessions++
	if timed {
		t.timed++
		t.seconds += secs
	}
	if r.usageRows > 0 {
		t.reported++
	}
	if r.tokens != nil {
		n, ok := new(big.Int).SetString(*r.tokens, 10)
		if ok {
			if t.tokens == nil {
				t.tokens = new(big.Int)
			}
			t.tokens.Add(t.tokens, n)
		}
	}
}

func (t *tally) tokenText() *string {
	if t.tokens == nil {
		return nil
	}
	s := t.tokens.String()
	return &s
}

type groupAcc struct {
	key, label string
	tally
	done int
}

func touchGroup(set map[string]*groupAcc, key, label string) *groupAcc {
	if g := set[key]; g != nil {
		return g
	}
	g := &groupAcc{key: key, label: label}
	set[key] = g
	return g
}

func workGroups(set map[string]*groupAcc) []WorkGroup {
	out := make([]WorkGroup, 0, len(set))
	for _, g := range set {
		out = append(out, WorkGroup{
			Key: g.key, Label: g.label, Sessions: g.sessions, TimedSessions: g.timed, AgentSeconds: g.seconds,
			Done: g.done, Tokens: g.tokenText(), ReportedSessions: g.reported,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Done != out[j].Done {
			return out[i].Done > out[j].Done
		}
		if out[i].AgentSeconds != out[j].AgentSeconds {
			return out[i].AgentSeconds > out[j].AgentSeconds
		}
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Label < out[j].Label
	})
	return out
}

type ticketAcc struct {
	WorkTicket
	tally
	workers int
}

func buildWork(rows []workRow, done []doneRow, released map[string]bool, from, to, now time.Time) Work {
	out := emptyWork()
	days := map[string]*WorkDay{}
	for d := from.UTC().Truncate(24 * time.Hour); d.Before(to); d = d.Add(24 * time.Hour) {
		day := d.Format("2006-01-02")
		days[day] = &WorkDay{Day: day}
		out.Days = append(out.Days, WorkDay{Day: day})
	}
	harnesses, models, projects := map[string]*groupAcc{}, map[string]*groupAcc{}, map[string]*groupAcc{}
	tickets := map[string]*ticketAcc{}
	doneTickets := map[string]bool{}
	for _, d := range done {
		doneTickets[d.ticketID] = true
	}
	var total tally
	type wasted struct {
		item WasteItem
		secs int64
		ok   bool
	}
	var perSession []wasted
	for _, r := range rows {
		secs, timed := agentSeconds(r, to)
		total.add(r, secs, timed)
		if r.role == "worker" {
			out.WorkerSessions++
		}
		if day := days[r.created.UTC().Format("2006-01-02")]; day != nil {
			day.Sessions++
			if timed {
				day.AgentSeconds += secs
			}
		}
		touchGroup(harnesses, r.harness, r.harness).add(r, secs, timed)
		if r.model != nil && *r.model != "" {
			out.ModelSessions++
			touchGroup(models, *r.model, *r.model).add(r, secs, timed)
		}
		projectLabel := r.projectTitle
		if projectLabel == "" {
			projectLabel = r.projectKey
		}
		touchGroup(projects, r.projectID, projectLabel).add(r, secs, timed)
		if r.ticketID != nil && r.ticketKey != nil {
			t := tickets[*r.ticketID]
			if t == nil {
				t = &ticketAcc{WorkTicket: WorkTicket{ID: *r.ticketID, Key: *r.ticketKey, ProjectID: r.projectID, ProjectKey: r.projectKey}}
				if r.ticketTitle != nil {
					t.Title = *r.ticketTitle
				}
				if r.ticketState != nil {
					t.State = *r.ticketState
				}
				tickets[*r.ticketID] = t
			}
			t.add(r, secs, timed)
			if r.role == "worker" {
				t.workers++
			}
			last := r.created
			if r.heartbeat != nil && r.heartbeat.After(last) {
				last = *r.heartbeat
			}
			if r.stopped != nil && r.stopped.After(last) {
				last = *r.stopped
			}
			if last.After(t.LastActiveAt) {
				t.LastActiveAt = last.UTC()
			}
		}
		if kind := wasteKind(r, now); kind != "" {
			at := r.created
			if r.heartbeat != nil {
				at = *r.heartbeat
			}
			if kind != "stuck" && r.stopped != nil {
				at = *r.stopped
			}
			item := WasteItem{
				Kind: kind, SessionID: r.id, ProjectID: r.projectID, ProjectKey: r.projectKey,
				TicketID: r.ticketID, TicketKey: r.ticketKey, TicketTitle: r.ticketTitle,
				Harness: r.harness, Model: r.model, Label: r.label, At: at.UTC(), Sessions: 1,
			}
			perSession = append(perSession, wasted{item: item, secs: secs, ok: timed})
		}
	}
	out.Sessions = total.sessions
	out.TimedSessions = total.timed
	out.AgentSeconds = total.seconds
	out.ReportedSessions = total.reported

	for _, d := range done {
		out.Done++
		if day := days[d.at.UTC().Format("2006-01-02")]; day != nil {
			day.Done++
		}
		touchGroup(projects, d.projectID, d.projectTitle).done++
		if d.harness != nil {
			out.DoneAttributed++
			touchGroup(harnesses, *d.harness, *d.harness).done++
			if d.model != nil && *d.model != "" {
				touchGroup(models, *d.model, *d.model).done++
			}
		}
	}
	for i := range out.Days {
		out.Days[i] = *days[out.Days[i].Day]
	}

	// Waste: a ticket that took three or more worker sessions and is still not
	// done is one "retried" row that holds all of them.
	retried := map[string]*wasted{}
	for id, t := range tickets {
		if t.workers >= retriedSessions && !doneLike(&t.State) {
			tid, key, title := id, t.Key, t.Title
			retried[id] = &wasted{item: WasteItem{
				Kind: "retried", ProjectID: t.ProjectID, ProjectKey: t.ProjectKey,
				TicketID: &tid, TicketKey: &key, TicketTitle: &title, At: t.LastActiveAt, Sessions: t.workers,
			}, secs: t.seconds, ok: t.timed > 0}
		}
	}
	var items []wasted
	for _, w := range perSession {
		if w.item.TicketID != nil && retried[*w.item.TicketID] != nil && w.item.Kind != "stuck" {
			continue
		}
		items = append(items, w)
	}
	// Rows run oldest first, so the last worker session seen is the one to open.
	for _, r := range rows {
		if r.ticketID == nil || r.role != "worker" {
			continue
		}
		if w := retried[*r.ticketID]; w != nil {
			w.item.SessionID, w.item.Harness, w.item.Model, w.item.Label = r.id, r.harness, r.model, r.label
		}
	}
	for _, w := range retried {
		items = append(items, *w)
	}
	for _, w := range items {
		out.Waste.Total++
		switch w.item.Kind {
		case "stuck":
			out.Waste.Stuck++
		case "failed":
			out.Waste.Failed++
		case "lost":
			out.Waste.Lost++
		case "no_result":
			out.Waste.NoResult++
		case "retried":
			out.Waste.Retried++
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ok != items[j].ok {
			return items[i].ok
		}
		if items[i].secs != items[j].secs {
			return items[i].secs > items[j].secs
		}
		if !items[i].item.At.Equal(items[j].item.At) {
			return items[i].item.At.After(items[j].item.At)
		}
		return items[i].item.SessionID < items[j].item.SessionID
	})
	for i, w := range items {
		if i == maxWasteRows {
			break
		}
		if w.ok {
			secs := w.secs
			w.item.AgentSeconds = &secs
		}
		out.Waste.Rows = append(out.Waste.Rows, w.item)
	}

	list := make([]WorkTicket, 0, len(tickets))
	for id, t := range tickets {
		t.Sessions, t.TimedSessions, t.AgentSeconds = t.sessions, t.timed, t.seconds
		t.Tokens, t.ReportedSessions = t.tokenText(), t.reported
		t.DoneInRange = doneTickets[id]
		t.Released = released[id]
		if doneLike(&t.State) {
			out.TicketsDone++
		}
		list = append(list, t.WorkTicket)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].AgentSeconds != list[j].AgentSeconds {
			return list[i].AgentSeconds > list[j].AgentSeconds
		}
		if list[i].Sessions != list[j].Sessions {
			return list[i].Sessions > list[j].Sessions
		}
		return list[i].Key < list[j].Key
	})
	out.TicketsWorked = len(list)
	if len(list) > maxWorkTickets {
		list = list[:maxWorkTickets]
	}
	out.Tickets = list
	out.ByHarness = workGroups(harnesses)
	out.ByModel = workGroups(models)
	out.ByProject = workGroups(projects)
	return out
}
