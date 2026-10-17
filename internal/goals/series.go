// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/jackc/pgx/v5"
)

const maxSeriesEvents = 20000
const maxSeriesWork = 20000000

type SeriesPoint struct {
	T             time.Time `json:"t"`
	Pct           int       `json:"pct"`
	Basis         string    `json:"basis"`
	Delta         int       `json:"delta"`
	Keys          []string  `json:"keys"`
	ChangedKeys   []string  `json:"changed_keys"`
	KeysTruncated bool      `json:"keys_truncated"`
}
type SeriesResult struct {
	Range     string        `json:"range"`
	Expansion string        `json:"expansion"`
	Points    []SeriesPoint `json:"points,omitempty"`
	Watermark
}
type sourceEvent struct {
	ID           int64
	At           time.Time
	Node, Family string
	After        json.RawMessage
	Bucket       *string
}
type historicalNode struct {
	ID, Parent, Key, Kind, Bucket string
	Work, Present                 bool
	Hours                         float64
}
type historicalSession struct {
	Ticket  string
	Working bool
}
type reconstruction struct {
	events   []sourceEvent
	metadata map[string]historicalNode
	kinds    map[string]bool
	created  time.Time
	bytes    int
	goal     string
}

func planSeries(now time.Time, span string) ([]time.Time, time.Duration, error) {
	var duration, grain time.Duration
	switch span {
	case "24h":
		duration, grain = 24*time.Hour, time.Hour
	case "7d":
		duration, grain = 7*24*time.Hour, 6*time.Hour
	case "30d":
		duration, grain = 30*24*time.Hour, 24*time.Hour
	default:
		return nil, 0, invalid("range", "must be 24h, 7d or 30d")
	}
	points, width := boundedSeriesBuckets(now, duration, grain)
	return points, width, nil
}
func boundedSeriesBuckets(now time.Time, span, grain time.Duration) ([]time.Time, time.Duration) {
	for (span+grain-1)/grain > 200 {
		switch grain {
		case time.Hour:
			grain = 6 * time.Hour
		case 6 * time.Hour:
			grain = 24 * time.Hour
		case 24 * time.Hour:
			grain = 7 * 24 * time.Hour
		default:
			grain *= 2
		}
	}
	count := int((span + grain - 1) / grain)
	end := now.UTC().Truncate(grain)
	points := make([]time.Time, count)
	for i := range points {
		points[i] = end.Add(time.Duration(i-count+1) * grain)
	}
	return points, grain
}
func (h *reconstruction) add(e sourceEvent) error {
	h.bytes += len(e.After)
	if len(e.After) == 0 || len(e.After) > 1<<20 || h.bytes > 32<<20 || len(h.events) >= maxSeriesEvents {
		return fail(413, "series event bound exceeded")
	}
	h.events = append(h.events, e)
	return nil
}

func (m *module) series(w http.ResponseWriter, r *http.Request) {
	if err := boundedGoalQuery(r); err != nil {
		respond(w, nil, err)
		return
	}
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	if !nodeIdentity(r.PathValue("goalId")) {
		respond(w, nil, invalid("goalId", "must be a goal key or UUID"))
		return
	}
	span := r.URL.Query().Get("range")
	if span == "" {
		span = "24h"
	}
	if _, _, err := planSeries(time.Time{}, span); err != nil {
		respond(w, nil, err)
		return
	}
	out := SeriesResult{Range: span, Expansion: "full"}
	err := m.readSnapshot(r, p, func(ctx context.Context, tx pgx.Tx, w Watermark, now time.Time) error {
		n, err := resolveNode(ctx, tx, r.PathValue("goalId"), false)
		if err != nil {
			return err
		}
		if !IsGoalKind(n.Slug) {
			return pgx.ErrNoRows
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: n.Project}); err != nil {
			return err
		}
		out.Watermark = w
		nested, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		err = validateSeriesScope(ctx, nested, n.ID)
		var history reconstruction
		if err == nil {
			history, err = loadReconstruction(ctx, nested, n.ID, now, span)
		}
		if err == nil {
			out.Points, _, err = history.project(ctx, now, span)
		}
		if err != nil {
			if rollback := nested.Rollback(ctx); rollback != nil {
				return rollback
			}
			if tooLarge(err) {
				out.Expansion = "too_large"
				out.Points = nil
				return nil
			}
			return err
		}
		return nested.Commit(ctx)
	})
	respond(w, out, err)
}

// Scan the selected range and its bounded, indexed prefix separately. The
// prefix supplies recorded baselines and the exact last percentage-change
// time; current rows cannot substitute for those recorded facts.
// history_nodes supplies (goal,id), and page supplies the goal identities.
func nodeHistoryQuery(nowArg, startArg string) string {
	parts := []string{}
	for i, period := range []string{"e.at<" + startArg, "e.at>=" + startArg + " AND e.at<=" + nowArg} {
		parts = append(parts, `(SELECT e.id,e.at,CASE WHEN e.type='node.project_moved' THEN e.after->'node' ELSE e.after END AS after
 FROM events e WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.node_id=h.id
 AND `+period+` AND (e.type LIKE 'node.%' OR e.type LIKE 'import.%' OR e.type LIKE 'status_autopilot.%')
 ORDER BY e.at,e.id LIMIT 20001)`)
		fromArg, untilArg := "'-infinity'", startArg
		if i == 1 {
			fromArg, untilArg = startArg, nowArg
		}
		parts = append(parts, `(SELECT e.id,e.at,s.item AS after
 FROM aeon_goal_history_refs(h.id,'node.bulk_changed',`+fromArg+`,`+untilArg+`) ref
 CROSS JOIN LATERAL (SELECT e.id,e.at,e.after FROM events e
 WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.id=ref.event_id OFFSET 0) e
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.after->'items')='array' THEN e.after->'items' ELSE '[]'::jsonb END) s(item)
 WHERE s.item->>'id'=h.id::text)`)
	}
	return `(SELECT h.goal,e.id,e.at,h.id AS node_id,e.after,'node'::text AS family,
 CASE WHEN e.after ? 'state' THEN aeon_work_status_category(e.after->>'state',k.field_schema) END AS bucket
 FROM history_nodes h JOIN nodes n ON n.id=h.id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 CROSS JOIN LATERAL (` + strings.Join(parts, " UNION ALL ") + `) e
 WHERE e.after ? 'state' OR e.after ? 'fields' OR e.after ? 'parent_id' OR e.after ? 'deleted_at'
 ORDER BY e.at,e.id,h.id LIMIT 20001)`
}

func sessionHistoryQuery(nowArg, startArg string) string {
	parts := []string{}
	for i, period := range []string{"e.at<" + startArg, "e.at>=" + startArg + " AND e.at<=" + nowArg} {
		parts = append(parts, `(SELECT g.id AS goal,e.id,e.at,g.id AS node_id,e.after,'goal'::text AS family,NULL::text AS bucket
 FROM page g CROSS JOIN LATERAL (SELECT e.id,e.at,e.after FROM events e
 WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.node_id=g.id AND e.type='goal.updated'
 AND `+period+` ORDER BY e.at,e.id LIMIT 20001) e)`)
		fromArg, untilArg := "'-infinity'", startArg
		if i == 1 {
			fromArg, untilArg = startArg, nowArg
		}
		parts = append(parts, `(SELECT h.goal,e.id,e.at,e.node_id,e.after,'session'::text AS family,NULL::text AS bucket
 FROM history_nodes h CROSS JOIN LATERAL aeon_goal_history_refs(h.id,'harness.%',`+fromArg+`,`+untilArg+`) ref
 CROSS JOIN LATERAL (SELECT e.id,e.at,e.node_id,e.after,e.before FROM events e
 WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.id=ref.event_id OFFSET 0) e
 WHERE (e.after->>'ticket_node_id'=h.id::text OR e.before->>'ticket_node_id'=h.id::text)
 AND e.after ? 'id' AND e.after ? 'phase')`)
	}
	// Before and after can name the same ticket. Replay a session event only
	// once per goal, while retaining both ends of a ticket rebind.
	return `(SELECT * FROM (` + strings.Join(parts, " UNION ") + `) e ORDER BY e.at,e.id LIMIT 20001)`
}

func loadReconstruction(ctx context.Context, tx pgx.Tx, goal string, now time.Time, spans ...string) (reconstruction, error) {
	out := reconstruction{goal: goal, metadata: map[string]historicalNode{}, kinds: map[string]bool{}}
	span := "24h"
	if len(spans) > 0 {
		span = spans[0]
	}
	starts, _, err := planSeries(now, span)
	if err != nil {
		return out, err
	}
	start := starts[0]
	if err := tx.QueryRow(ctx, `SELECT created_at FROM nodes WHERE id=$1`, goal).Scan(&out.created); err != nil {
		return out, err
	}
	// Discovery uses immutable outcome subjects, including visible moved-away
	// outcomes. Values still come only from their RLS-filtered event snapshots.
	rows, err := tx.Query(ctx, `WITH subjects AS MATERIALIZED (
 SELECT DISTINCT e.node_id FROM aeon_goal_history_refs($1::uuid,'goal.items_changed','-infinity',$2) ref
 CROSS JOIN LATERAL (SELECT e.node_id,e.before,e.after FROM events e
 WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.id=ref.event_id OFFSET 0) e
 WHERE e.node_id::text=coalesce(e.after->>'outcome_node_id',e.before->>'outcome_node_id')
 AND (e.before->>'goal_node_id'=$1::uuid::text OR e.after->>'goal_node_id'=$1::uuid::text)
 ORDER BY e.node_id LIMIT 4097)
 SELECT n.id::text FROM subjects s JOIN nodes n ON n.id=s.node_id
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug='outcome'
 ORDER BY n.id`, goal, now)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(ids) > 4096 {
		return out, fail(413, "series outcome bound exceeded")
	}
	rootSet := map[string]bool{}
	rows, err = tx.Query(ctx, `SELECT e.id,e.at,s.id::text,e.after FROM unnest($1::uuid[]) s(id)
 CROSS JOIN LATERAL (SELECT e.id,e.at,e.after FROM events e
 WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.node_id=s.id AND e.type='goal.items_changed'
 AND e.at<=$2 ORDER BY e.at,e.id LIMIT 20001) e ORDER BY e.at,e.id LIMIT 20001`, ids, now)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		e := sourceEvent{Family: "outcome"}
		if err := rows.Scan(&e.ID, &e.At, &e.Node, &e.After); err != nil {
			rows.Close()
			return out, err
		}
		if err := out.add(e); err != nil {
			rows.Close()
			return out, err
		}
		var n storedNode
		if err := json.Unmarshal(e.After, &n); err != nil {
			rows.Close()
			return out, err
		}
		for _, id := range n.Targets {
			rootSet[id] = true
			if len(rootSet) > 4096 {
				rows.Close()
				return out, fail(413, "series root bound exceeded")
			}
		}

	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.events) > maxSeriesEvents {
		return out, fail(413, "series event bound exceeded")
	}
	roots := []string{}
	for id := range rootSet {
		roots = append(roots, id)
	}
	sort.Strings(roots)
	if len(roots) > 4096 {
		return out, fail(413, "series root bound exceeded")
	}
	nodeIDs := []string{}
	if len(roots) > 0 {
		// A union of recorded parent edges and current identity edges discovers the
		// bounded candidate graph. Current state, estimates and outcome membership
		// are never sampled. Event snapshots choose the actual tree at each instant.
		rows, err = tx.Query(ctx, `WITH RECURSIVE walk(root,id) AS (SELECT n.id,n.id FROM nodes n WHERE n.id=ANY($1::uuid[]) UNION SELECT w.root,e.id FROM walk w CROSS JOIN LATERAL (
 (SELECT n.id FROM nodes n WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.parent_id=w.id OFFSET 0)
 UNION SELECT h.id FROM aeon_goal_history_children(w.id,$3,$2) h
 ) e),
 bounded AS MATERIALIZED (SELECT root,id FROM walk LIMIT 50001)
 SELECT b.root::text,n.id::text,n.key,n.kind_id::text,k.slug FROM bounded b JOIN nodes n ON n.id=b.id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id`, roots, now, start)
		if err != nil {
			return out, err
		}
		pairs := 0
		for rows.Next() {
			pairs++
			var root, id, key, kind, slug string
			if err := rows.Scan(&root, &id, &key, &kind, &slug); err != nil {
				rows.Close()
				return out, err
			}
			out.metadata[id] = historicalNode{ID: id, Key: key, Kind: kind, Work: isWorkKind(slug)}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if pairs > 50000 {
			return out, fail(413, "series work scope budget exceeded")
		}
		for id := range out.metadata {
			nodeIDs = append(nodeIDs, id)
		}
		sort.Strings(nodeIDs)
		rows, err = tx.Query(ctx, `SELECT id::text,slug FROM node_kinds ORDER BY id LIMIT 1001`)
		if err != nil {
			return out, err
		}
		kindCount := 0
		for rows.Next() {
			var id, slug string
			if err := rows.Scan(&id, &slug); err != nil {
				rows.Close()
				return out, err
			}
			out.kinds[id] = isWorkKind(slug)
			kindCount++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if kindCount > 1000 {
			return out, fail(413, "series kind bound exceeded")
		}
		rows, err = tx.Query(ctx, `WITH history_nodes AS MATERIALIZED (SELECT id,id AS goal FROM unnest($1::uuid[]) s(id))
 SELECT e.id,e.at,e.node_id::text,e.after,e.bucket FROM `+nodeHistoryQuery("$2", "$3")+` e`, nodeIDs, now, start)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			e := sourceEvent{Family: "node"}
			if err := rows.Scan(&e.ID, &e.At, &e.Node, &e.After, &e.Bucket); err != nil {
				rows.Close()
				return out, err
			}
			if err := out.add(e); err != nil {
				rows.Close()
				return out, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}

	}
	rows, err = tx.Query(ctx, `WITH page AS MATERIALIZED (SELECT $1::uuid AS id),
 history_nodes AS MATERIALIZED (SELECT id,$1::uuid AS goal FROM unnest($3::uuid[]) s(id))
 SELECT e.id,e.at,coalesce(e.node_id::text,''),e.after,e.family FROM `+sessionHistoryQuery("$2", "$4")+` e`, goal, now, nodeIDs, start)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		e := sourceEvent{}
		if err := rows.Scan(&e.ID, &e.At, &e.Node, &e.After, &e.Family); err != nil {
			rows.Close()
			return out, err
		}
		if err := out.add(e); err != nil {
			rows.Close()
			return out, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.events) > maxSeriesEvents {
		return out, fail(413, "series event bound exceeded")
	}
	sort.Slice(out.events, func(i, j int) bool {
		if out.events[i].At.Equal(out.events[j].At) {
			if out.events[i].ID == out.events[j].ID {
				return out.events[i].Node < out.events[j].Node
			}
			return out.events[i].ID < out.events[j].ID
		}
		return out.events[i].At.Before(out.events[j].At)
	})
	return out, nil
}
func isWorkKind(slug string) bool {
	return slug == "work" || slug == "ticket" || slug == "task" || slug == "epic"
}

type historicalProjection struct {
	goal     string
	outcomes map[string]storedNode
	nodes    map[string]historicalNode
	sessions map[string]historicalSession
	kinds    map[string]bool
	override *int
	work     int
}

func (h reconstruction) projection() *historicalProjection {
	return &historicalProjection{goal: h.goal, outcomes: map[string]storedNode{}, nodes: map[string]historicalNode{}, sessions: map[string]historicalSession{}, kinds: h.kinds}
}
func (p *historicalProjection) apply(e sourceEvent, metadata map[string]historicalNode) error {
	switch e.Family {
	case "outcome":
		var n storedNode
		if err := json.Unmarshal(e.After, &n); err != nil {
			return err
		}
		p.outcomes[e.Node] = n
	case "goal":
		var fields map[string]any
		if err := json.Unmarshal(e.After, &fields); err != nil {
			return err
		}
		p.override = nil
		if pct, ok := fields["progress_override_pct"].(float64); ok {
			value := int(pct)
			p.override = &value
		}
	case "session":
		var s struct {
			ID       string     `json:"id"`
			Ticket   string     `json:"ticket_node_id"`
			Phase    string     `json:"phase"`
			Role     string     `json:"role"`
			Stopped  *time.Time `json:"stopped_at"`
			Archived *time.Time `json:"archived_at"`
		}
		if err := json.Unmarshal(e.After, &s); err != nil {
			return err
		}
		p.sessions[s.ID] = historicalSession{Ticket: s.Ticket, Working: s.Phase == "working" && s.Stopped == nil && s.Archived == nil && (s.Role == "worker" || s.Role == "coordinator")}
	case "node":
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(e.After, &raw); err != nil {
			return err
		}
		n, exists := p.nodes[e.Node]
		if !exists {
			n = metadata[e.Node]
			n.Present = true
		}
		if value, ok := raw["parent_id"]; ok {
			if string(value) == "null" {
				n.Parent = ""
			} else if err := json.Unmarshal(value, &n.Parent); err != nil {
				return err
			}
		}
		if value, ok := raw["kind_id"]; ok {
			if err := json.Unmarshal(value, &n.Kind); err != nil {
				return err
			}
			n.Work = p.kinds[n.Kind]
		}
		if value, ok := raw["deleted_at"]; ok {
			n.Present = string(value) == "null"
		}
		if value, ok := raw["key"]; ok {
			if err := json.Unmarshal(value, &n.Key); err != nil {
				return err
			}
		}
		if e.Bucket != nil {
			n.Bucket = *e.Bucket
		}
		if value, ok := raw["fields"]; ok {
			var fields map[string]any
			if err := json.Unmarshal(value, &fields); err != nil {
				return err
			}
			n.Hours = 0
			if hours, ok := fields["estimate_hours"].(float64); ok && hours > 0 && hours <= 200 {
				n.Hours = hours
			}
		}
		p.nodes[e.Node] = n
	}
	return nil
}
func (p *historicalProjection) progress(ctx context.Context) (int, string, error) {
	pct, basis, _, err := p.values(ctx)
	return pct, basis, err
}

type historicalState struct{ Key, State string }

func (p *historicalProjection) values(ctx context.Context) (int, string, map[string]historicalState, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", nil, err
	}
	p.work += len(p.nodes) + len(p.sessions) + len(p.outcomes)
	if p.work > maxSeriesWork {
		return 0, "", nil, fail(413, "series projection budget exceeded")
	}

	children := map[string][]string{}
	workParents := map[string]bool{}
	working := map[string]bool{}
	for _, s := range p.sessions {
		if s.Working {
			working[s.Ticket] = true
		}
	}
	for id, n := range p.nodes {
		if !n.Present {
			continue
		}
		children[n.Parent] = append(children[n.Parent], id)
		if n.Work {
			workParents[n.Parent] = true
		}
	}
	inputs := []ProgressOutcome{}
	states := map[string]historicalState{}
	outcomeIDs := []string{}
	for id := range p.outcomes {
		outcomeIDs = append(outcomeIDs, id)
	}
	sort.Strings(outcomeIDs)
	for _, outcomeID := range outcomeIDs {
		outcome := p.outcomes[outcomeID]
		if outcome.Deleted || outcome.Parent != p.goal {
			continue
		}
		facts := StateFacts{HandedOver: fieldString(outcome, "handed_over_at") != "", StateSet: State(fieldString(outcome, "state_set"))}
		leaves := []LeafEstimate{}
		seen := map[string]bool{}
		for _, root := range outcome.Targets {
			if n, ok := p.nodes[root]; !ok || !n.Present || !n.Work {
				continue
			}
			queue := []string{root}
			for len(queue) > 0 {
				if err := ctx.Err(); err != nil {
					return 0, "", nil, err
				}
				id := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				if seen[id] {
					continue
				}
				seen[id] = true
				p.work++
				if p.work > maxSeriesWork {
					return 0, "", nil, fail(413, "series projection budget exceeded")
				}
				n := p.nodes[id]
				if !n.Present {
					continue
				}
				queue = append(queue, children[id]...)
				if !n.Work || workParents[id] || n.Bucket == "archived" || n.Bucket == "cancelled" {
					continue
				}
				states[id] = historicalState{Key: n.Key, State: n.Bucket}
				if working[id] {
					states[id] = historicalState{Key: n.Key, State: string(Moving)}
				}
				facts.TotalLeaves++
				if n.Bucket == "done" || n.Bucket == "delivered" || n.Bucket == "accepted" {
					facts.DoneLeaves++
				}
				if working[id] {
					facts.SessionIDs = append(facts.SessionIDs, id)
				}
				leaves = append(leaves, LeafEstimate{ID: id, Hours: n.Hours})
			}
		}
		sort.Slice(leaves, func(i, j int) bool { return leaves[i].ID < leaves[j].ID })
		state := DeriveState(facts).State
		states[outcomeID] = historicalState{Key: outcome.Key, State: string(state)}
		inputs = append(inputs, ProgressOutcome{State: state, Leaves: leaves})
	}
	rollup := ComputeRollup(inputs)
	if p.override != nil {
		return *p.override, rollup.Basis, states, nil
	}
	return rollup.Pct, rollup.Basis, states, nil
}
func (h reconstruction) project(ctx context.Context, now time.Time, span string) ([]SeriesPoint, time.Time, error) {
	starts, grain, err := planSeries(now, span)
	if err != nil {
		return nil, time.Time{}, err
	}
	p := h.projection()
	index := 0
	since := h.created
	previous := 0
	previousStates := map[string]historicalState{}
	// Replay the prefix to establish the preceding bucket's value. It also
	// preserves the true stall time when the last change predates this range.
	advance := func(keys map[string]bool) error {
		event := h.events[index]
		// A bulk event is one atomic transition. Comparing percentages after
		// each item would invent intermediate changes and reset stall.since
		// even when the batch leaves the final percentage unchanged.
		for index < len(h.events) && h.events[index].ID == event.ID && h.events[index].At.Equal(event.At) {
			if err := p.apply(h.events[index], h.metadata); err != nil {
				return err
			}
			index++
		}
		pct, _, states, err := p.values(ctx)
		if err != nil {
			return err
		}
		if pct != previous {
			since = event.At
		}
		if keys != nil {
			for id, state := range states {
				if old, ok := previousStates[id]; !ok || old.State != state.State {
					keys[state.Key] = true
				}
			}
			for id, state := range previousStates {
				if _, ok := states[id]; !ok {
					keys[state.Key] = true
				}
			}
		}
		previous, previousStates = pct, states
		return nil
	}
	for index < len(h.events) && !h.events[index].At.After(starts[0]) {
		if err := advance(nil); err != nil {
			return nil, since, err
		}
	}
	points := []SeriesPoint{}
	for _, start := range starts {
		end := start.Add(grain)
		if end.After(now) {
			end = now
		}
		baseline := previous
		keys := map[string]bool{}
		for index < len(h.events) && !h.events[index].At.After(end) {
			if err := advance(keys); err != nil {
				return nil, since, err
			}
		}
		pct, basis, err := p.progress(ctx)
		if err != nil {
			return nil, since, err
		}
		keyList := []string{}
		for key := range keys {
			if key != "" {
				keyList = append(keyList, key)
			}
		}
		sort.Strings(keyList)
		truncated := len(keyList) > 200
		if truncated {
			keyList = keyList[:200]
		}
		points = append(points, SeriesPoint{T: start, Pct: pct, Basis: basis, Delta: pct - baseline, Keys: keyList, ChangedKeys: keyList, KeysTruncated: truncated})
	}
	return points, since, nil
}

func validateSeriesScope(ctx context.Context, tx pgx.Tx, goal string) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT n.id FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.parent_id=$1 AND k.slug='outcome' AND n.deleted_at IS NULL LIMIT 201) s`, goal).Scan(&count); err != nil {
		return err
	}
	if count > 200 {
		return fail(413, "series outcome bound exceeded")
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT r.target_node_id::text FROM nodes o JOIN node_kinds k ON k.tenant_id=o.tenant_id AND k.id=o.kind_id AND k.slug='outcome' JOIN node_relations r ON r.source_node_id=o.id AND r.type='serves' JOIN nodes n ON n.id=r.target_node_id JOIN node_kinds nk ON nk.tenant_id=n.tenant_id AND nk.id=n.kind_id AND nk.slug IN ('work','epic','ticket','task') WHERE o.parent_id=$1 AND o.deleted_at IS NULL ORDER BY r.target_node_id::text LIMIT 4097`, goal)
	if err != nil {
		return err
	}
	roots := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		roots = append(roots, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(roots) > 4096 {
		return fail(413, "series root bound exceeded")
	}
	return tx.QueryRow(ctx, `SELECT count(*) FROM aeon_work_scope($1::uuid[])`, roots).Scan(&count)
}
