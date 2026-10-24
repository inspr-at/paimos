// SPDX-License-Identifier: AGPL-3.0-only

package outcomes

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

const reviewEventLimit = 50000

type reviewObservation struct {
	Ticket, Key, Title, Kind, Verdict string
	Round                             int
	Rules                             []string
	At                                time.Time
}

type reviewMeasure struct {
	DesignMean   *float64 `json:"design_mean"`
	GateMean     *float64 `json:"gate_mean"`
	Design       int      `json:"design_tickets"`
	Gate         int      `json:"gate_tickets"`
	Tickets      int      `json:"tickets"`
	Open         int      `json:"still_open"`
	WeeksTotal   int      `json:"weeks_total"`
	WeeksCovered int      `json:"weeks_covered"`
}

type reviewWeek struct {
	reviewMeasure
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Week int       `json:"week"`
}

type reviewRuleCount struct {
	Rule     string `json:"rule"`
	Count    int    `json:"count"`
	Previous int    `json:"previous_count"`
}

type reviewTicket struct {
	ID     string            `json:"ticket_node_id"`
	Key    string            `json:"key"`
	Title  string            `json:"title"`
	Design *int              `json:"design_rounds"`
	Gate   *int              `json:"gate_rounds"`
	Open   bool              `json:"open"`
	Latest int               `json:"latest_round"`
	Rules  []reviewRuleCount `json:"rules"`
}

type reviewNumbers struct {
	Project   string            `json:"project_id"`
	Days      int               `json:"window_days"`
	Generated time.Time         `json:"generated_at"`
	Since     *time.Time        `json:"since"`
	Status    string            `json:"status"`
	Current   reviewMeasure     `json:"current"`
	Previous  reviewMeasure     `json:"previous"`
	Weekly    []reviewWeek      `json:"weekly"`
	Most      []reviewTicket    `json:"most_rounds"`
	Rules     []reviewRuleCount `json:"rules_cited_most"`
}

func (m *Module) reviewRoundNumbers(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := r.PathValue("projectId")
	days := 7
	if r.URL.Query().Has("window") {
		var err error
		days, err = strconv.Atoi(r.URL.Query().Get("window"))
		if err != nil {
			writeErr(w, invalid("invalid review round window"))
			return
		}
	}
	if !validUUID(project) || days != 7 && days != 30 && days != 90 && days != 180 && days != 365 {
		writeErr(w, invalid("invalid project or review round window"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out reviewNumbers
	err := db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		var visible string
		if err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL`, project).Scan(&visible); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "outcome.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		// Lifetime history is needed to find the first ok, even when earlier
		// rounds lie outside both windows. Bound it before allocating or computing.
		rows, err := tx.Query(ctx, `SELECT o.ticket_node_id::text,n.key,n.title,o.kind,o.round,o.verdict,o.cited_rules,o.at
 FROM outcome_review_rounds o JOIN nodes n ON n.tenant_id=o.tenant_id AND n.id=o.ticket_node_id AND n.project_id=o.project_id
 WHERE o.project_id=$1 AND n.deleted_at IS NULL AND o.at<=$2 ORDER BY o.at,o.id LIMIT $3`, project, now, reviewEventLimit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		all := []reviewObservation{}
		for rows.Next() {
			var row reviewObservation
			if err := rows.Scan(&row.Ticket, &row.Key, &row.Title, &row.Kind, &row.Round, &row.Verdict, &row.Rules, &row.At); err != nil {
				return err
			}
			all = append(all, row)
			if len(all) > reviewEventLimit {
				return &fail{status: 503, msg: "review round history exceeds the 50000-event budget; no partial figures returned"}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = aggregateReviewNumbers(project, days, now.UTC(), all)
		return nil
	})
	if err != nil {
		reviewReadError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}

func monday(at time.Time) time.Time {
	at = at.UTC().Truncate(24 * time.Hour)
	return at.AddDate(0, 0, -(int(at.Weekday())+6)%7)
}

func reviewWeeks(from, to time.Time, since *time.Time) (total, covered int) {
	for start := monday(from); start.Before(to); start = start.AddDate(0, 0, 7) {
		total++
		if since != nil && since.Before(to) && since.Before(start.AddDate(0, 0, 7)) {
			covered++
		}
	}
	return total, covered
}

type reviewKindState struct {
	firstOK *reviewObservation
	latest  reviewObservation
}

// A null mean really means no completed observations, including open-only
// histories. Ticket creation, merge rounds and absent kinds contribute nothing.
func reviewWindow(all []reviewObservation, from, to time.Time, since *time.Time) (reviewMeasure, []reviewTicket) {
	type ticketState struct {
		key, title string
		kinds      map[string]*reviewKindState
		rules      map[string]int
	}
	tickets := map[string]*ticketState{}
	for _, row := range all {
		if !row.At.Before(to) {
			continue
		}
		ticket := tickets[row.Ticket]
		if ticket == nil {
			ticket = &ticketState{key: row.Key, title: row.Title, kinds: map[string]*reviewKindState{}, rules: map[string]int{}}
			tickets[row.Ticket] = ticket
		}
		kind := ticket.kinds[row.Kind]
		if kind == nil {
			kind = &reviewKindState{}
			ticket.kinds[row.Kind] = kind
		}
		if kind.latest.Round < row.Round || kind.latest.Round == row.Round && row.At.After(kind.latest.At) {
			kind.latest = row
		}
		if row.Verdict == "ok" && (kind.firstOK == nil || row.Round < kind.firstOK.Round) {
			copy := row
			kind.firstOK = &copy
		}
		if !row.At.Before(from) {
			for _, rule := range row.Rules {
				ticket.rules[rule]++
			}
		}
	}
	result := reviewMeasure{}
	result.WeeksTotal, result.WeeksCovered = reviewWeeks(from, to, since)
	most := []reviewTicket{}
	designSum, gateSum := 0, 0
	for id, ticket := range tickets {
		row := reviewTicket{ID: id, Key: ticket.key, Title: ticket.title, Rules: topReviewRules(ticket.rules, nil, 3)}
		selected := false
		openRound := 0
		for name, kind := range ticket.kinds {
			at := kind.latest.At
			round := kind.latest.Round
			if kind.firstOK != nil {
				at, round = kind.firstOK.At, kind.firstOK.Round
			}
			if at.Before(from) || !at.Before(to) {
				continue
			}
			selected = true
			if round > row.Latest {
				row.Latest = round
			}
			value := round
			if name == "design" {
				row.Design = &value
			} else {
				row.Gate = &value
			}
			if kind.firstOK == nil {
				row.Open = true
				if round > openRound {
					openRound = round
				}
				continue
			}
			if name == "design" {
				result.Design++
				designSum += round
			} else {
				result.Gate++
				gateSum += round
			}
		}
		if !selected {
			continue
		}
		result.Tickets++
		if row.Open {
			result.Open++
			row.Latest = openRound
		}
		if row.Open || roundsValue(row.Design) > 1 || roundsValue(row.Gate) > 1 {
			most = append(most, row)
		}
	}
	if result.Design > 0 {
		v := float64(designSum) / float64(result.Design)
		result.DesignMean = &v
	}
	if result.Gate > 0 {
		v := float64(gateSum) / float64(result.Gate)
		result.GateMean = &v
	}
	sort.Slice(most, func(i, j int) bool {
		a, b := most[i], most[j]
		if roundsValue(a.Design) != roundsValue(b.Design) {
			return roundsValue(a.Design) > roundsValue(b.Design)
		}
		if roundsValue(a.Gate) != roundsValue(b.Gate) {
			return roundsValue(a.Gate) > roundsValue(b.Gate)
		}
		return a.Key < b.Key
	})
	if len(most) > 10 {
		most = most[:10]
	}
	return result, most
}

func roundsValue(round *int) int {
	if round == nil {
		return 0
	}
	return *round
}

func topReviewRules(counts, previous map[string]int, limit int) []reviewRuleCount {
	rows := []reviewRuleCount{}
	for rule, count := range counts {
		rows = append(rows, reviewRuleCount{Rule: rule, Count: count, Previous: previous[rule]})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Rule < rows[j].Rule
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func aggregateReviewNumbers(project string, days int, now time.Time, all []reviewObservation) reviewNumbers {
	from := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1-days)
	previousFrom := from.AddDate(0, 0, -days)
	out := reviewNumbers{Project: project, Days: days, Generated: now, Status: "no_data", Weekly: []reviewWeek{}}
	for _, row := range all {
		if !row.At.After(now) && (out.Since == nil || row.At.Before(*out.Since)) {
			at := row.At
			out.Since = &at
		}
	}
	out.Current, out.Most = reviewWindow(all, from, now, out.Since)
	out.Previous, _ = reviewWindow(all, previousFrom, from, out.Since)
	if out.Since != nil {
		out.Status = "ok"
		if out.Since.After(from) {
			out.Status = "partial"
		}
	}
	for start := from; start.Before(now); {
		end := monday(start).AddDate(0, 0, 7)
		if end.After(now) {
			end = now
		}
		measure, _ := reviewWindow(all, start, end, out.Since)
		_, week := start.ISOWeek()
		out.Weekly = append(out.Weekly, reviewWeek{reviewMeasure: measure, From: start, To: end, Week: week})
		start = end
	}
	current, previous := map[string]int{}, map[string]int{}
	for _, row := range all {
		if !row.At.Before(now) || row.At.Before(previousFrom) {
			continue
		}
		counts := current
		if row.At.Before(from) {
			counts = previous
		}
		for _, rule := range row.Rules {
			counts[rule]++
		}
	}
	out.Rules = topReviewRules(current, previous, 10)
	return out
}
