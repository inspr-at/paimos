// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/lanedispatch"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

const candidateLimit = 200

type Skipped struct {
	TicketNodeID string `json:"ticket_node_id"`
	Reason       string `json:"reason"`
}
type ScheduleResult struct {
	Dispatch  *lanedispatch.Request `json:"dispatch"`
	Skipped   []Skipped             `json:"skipped"`
	Truncated bool                  `json:"truncated"`
	Reason    string                `json:"reason"`
}
type candidate struct {
	id, kind, state, title, body, intentLane string
	revision                                 time.Time
	raw                                      []byte
	fields                                   map[string]any
	run, release                             *string
	queued, intent, routed, busy, blocked    bool
}

func (m *Module) schedule(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	var in actionInput
	if err := decode(w, r, &in); err != nil {
		respond(w, 200, nil, err)
		return
	}
	if in.ExpectedRevision < 1 {
		respond(w, 200, nil, fail(400, "lane revision required"))
		return
	}
	out, err := m.Schedule(r.Context(), p, r.PathValue("laneId"), in.ExpectedRevision)
	respond(w, 200, out, err)
}

// Schedule performs one bounded recovery/event tick. It may be called by an
// internal worker carrying the lane's person principal, or the explicit HTTP
// action. The worker must not manufacture an agent/admin principal. No network,
// model call, account admission, order/run creation or launch occurs here.
func (m *Module) Schedule(ctx context.Context, p tenant.Principal, laneID string, revision int64) (ScheduleResult, error) {
	out := ScheduleResult{Skipped: []Skipped{}}
	if p.Kind != tenant.Person {
		return out, authz.ErrForbidden
	}
	if !uuid.MatchString(laneID) || revision < 1 {
		return out, fail(400, "lane id and revision required")
	}
	r := (&http.Request{}).WithContext(ctx)
	err := m.transaction(r, p, true, func(ctx context.Context, tx pgx.Tx) error {
		l, err := load(ctx, tx, laneID, true)
		if err != nil {
			return err
		}
		if err = permission(ctx, tx, p, "autopilot.manage", l.ProjectID); err != nil {
			return err
		}
		if err = executionAuthority(ctx, tx, p, l); err != nil {
			return err
		}
		if l.Revision != revision {
			return fail(409, "lane changed; reload")
		}
		now := m.now().UTC()
		if !l.Enabled {
			out.Reason = "lane_disabled"
			return nil
		}
		if l.Paused {
			out.Reason = "lane_paused"
			return nil
		}
		window := nextWindow(l.Policy.Window, now)
		if window == nil || now.Before(window.StartsAt) {
			out.Reason = "outside_work_window"
			return nil
		}
		if l.Scope.Kind == "release" {
			var building bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_releases WHERE release_node_id=$1 AND project_node_id=$2 AND state='building')`, l.Scope.ReleaseNodeID, l.ProjectID).Scan(&building); err != nil {
				return err
			}
			if !building {
				out.Reason = "release_not_building"
				return nil
			}
		}
		items, truncated, err := candidates(ctx, tx, l, true)
		if err != nil {
			return err
		}
		out.Truncated = truncated
		if err = dependencyFacts(ctx, tx, items); err != nil {
			return err
		}
		lanes, err := overlapLanes(ctx, tx, l.ProjectID)
		if err != nil {
			return err
		}
		for _, item := range items {
			reason := ""
			switch {
			case len(item.raw) > 65536:
				reason = "ticket_fields_too_large"
			case !matches(l.Policy, item.kind, item.fields):
				reason = "outside_lane_filters"
			case !workqueue.Check(item.kind, item.state, item.title, item.body, item.fields, item.blocked).Queueable:
				reason = "ticket_not_queueable"
			case item.blocked || workqueue.State(item.state) == "blocked":
				reason = "unresolved_blocker"
			case item.busy || item.routed:
				reason = "existing_work"
			case workqueue.Hours(item.fields) > l.Policy.MaxTicketEstimateHours:
				reason = "estimate_above_lane_limit"
			}
			if reason == "" {
				winner := overlapWinner(lanes, item, now)
				if winner != l.ID {
					reason = "owned_by_other_lane"
				}
			}
			if reason != "" {
				out.Skipped = append(out.Skipped, Skipped{item.id, reason})
				continue
			}
			// The tenant/tree fence protects all facts read above; lock ticket before
			// workflow rows. Lane requests never mutate or route the referenced run.
			var live time.Time
			if err = tx.QueryRow(ctx, `SELECT updated_at FROM nodes WHERE id=$1 FOR NO KEY UPDATE`, item.id).Scan(&live); err != nil {
				return err
			}
			if !live.Equal(item.revision) {
				return fail(409, "ticket changed; retry")
			}
			phase := "requested"
			if preparation(item.fields) != "none" {
				phase = "preparation_requested"
			}
			request, err := lanedispatch.Claim(ctx, tx, p, lanedispatch.Request{ProjectID: l.ProjectID, LaneID: &l.ID, TicketNodeID: item.id, TicketRevision: item.revision, LaneRevision: l.Revision, RunID: item.run, Phase: phase})
			if err != nil {
				return err
			}
			if request == nil {
				out.Skipped = append(out.Skipped, Skipped{item.id, "dispatch_already_owned"})
				continue
			}
			out.Dispatch = request
			out.Reason = "dispatch_requested_execution_pending"
			// Event counter LAST. Return immediately; never acquire another lock.
			_, err = events.Append(ctx, tx, p, events.Change{NodeID: &l.ID, Type: "node.autopilot_dispatch_requested", After: request})
			return err
		}
		out.Reason = "no_eligible_ticket"
		if out.Truncated {
			out.Reason = "candidate_scan_limit"
		}
		return nil
	})
	return out, err
}
func candidates(ctx context.Context, tx pgx.Tx, l Lane, unowned bool) ([]candidate, bool, error) {
	// Rank the shared queue before project filtering. Preparation intents are
	// explicit person intake, not autonomous backlog pulls. A release uses its
	// persisted walker order, regardless of queue priority/manual ranks.
	rows, err := tx.Query(ctx, workqueue.CTE+`SELECT n.id::text,k.slug,n.state,left(n.title,512),left(n.body,2048),n.updated_at,left(n.fields::text,65537),q.id::text,j.release_node_id::text,
 coalesce(p.kind='person',false),pr.id IS NOT NULL,coalesce(q.queue_routed_at IS NOT NULL OR q.queue_target_agent_id IS NOT NULL,false),
 EXISTS(SELECT 1 FROM agent_runs a JOIN nodes o ON o.tenant_id=a.tenant_id AND o.id=a.work_order_id WHERE (a.queue_node_id=n.id OR o.parent_id=n.id) AND (a.status IN ('starting','running','waiting','ownership_lost') OR (a.status='queued' AND a.id IS DISTINCT FROM q.id)))
 OR EXISTS(SELECT 1 FROM harness_sessions h WHERE h.ticket_node_id=n.id AND h.stopped_at IS NULL)
 OR EXISTS(SELECT 1 FROM work_orders w JOIN nodes o ON o.tenant_id=w.tenant_id AND o.id=w.node_id WHERE o.parent_id=n.id AND o.deleted_at IS NULL AND w.status='running')
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 LEFT JOIN queue_heads q ON q.queue_node_id=n.id
 LEFT JOIN principals p ON p.tenant_id=q.tenant_id AND p.id=q.queue_by_principal_id
 LEFT JOIN journey_tickets j ON j.tenant_id=n.tenant_id AND j.ticket_node_id=n.id
 LEFT JOIN LATERAL (SELECT a.id,a.created_at FROM autopilot_preparation_requests a JOIN principals requester ON requester.tenant_id=a.tenant_id AND requester.id=a.requested_by_principal_id AND requester.kind='person'
  WHERE a.lane_id=$2 AND a.ticket_node_id=n.id AND a.ticket_revision=n.updated_at AND a.lane_revision=$3 ORDER BY a.created_at,a.id LIMIT 1) pr ON true
 WHERE n.project_id=$1 AND n.deleted_at IS NULL AND k.slug IN ('ticket','task')
 AND (NOT $7::bool OR (NOT EXISTS(SELECT 1 FROM lane_dispatches rejected WHERE rejected.ticket_node_id=n.id AND rejected.lane_id=$2 AND rejected.phase='cancelled' AND rejected.ticket_revision=n.updated_at AND rejected.lane_revision=$3) AND NOT EXISTS(SELECT 1 FROM lane_dispatches d WHERE d.ticket_node_id=n.id AND d.phase NOT IN ('completed','cancelled') AND (d.lane_id IS NOT NULL OR EXISTS(SELECT 1 FROM agent_runs owned_run WHERE owned_run.id=d.run_id AND owned_run.status NOT IN ('completed','failed','cancelled'))))))
 AND (($4='queued_tickets' AND (p.kind='person' OR pr.id IS NOT NULL)) OR ($4='release' AND j.release_node_id=$5))
 ORDER BY CASE WHEN $4='release' THEN j.walker_position END,
 CASE WHEN $4='queued_tickets' THEN coalesce(q.queue_target_agent_id::text,'') END,
 CASE WHEN $4='queued_tickets' THEN q.queue_position END NULLS LAST,pr.created_at,n.id LIMIT $6`, l.ProjectID, l.ID, l.Revision, l.Scope.Kind, l.Scope.ReleaseNodeID, candidateLimit+1, unowned)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.kind, &c.state, &c.title, &c.body, &c.revision, &c.raw, &c.run, &c.release, &c.queued, &c.intent, &c.routed, &c.busy); err != nil {
			return nil, false, err
		}
		if c.intent {
			c.intentLane = l.ID
		}
		if len(c.raw) <= 65536 {
			c.fields = workqueue.Fields(c.raw)
		}
		items = append(items, c)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(items) > candidateLimit
	if truncated {
		items = items[:candidateLimit]
	}
	return items, truncated, nil
}
func dependencyFacts(ctx context.Context, tx pgx.Tx, items []candidate) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].id
	}
	// Internal content-free blocker facts include dependencies in projects the
	// person cannot read. Tenant RLS remains on; restore visibility before any
	// externally visible effect. Hidden blockers must never become permission to
	// launch, and their identity/title is not returned.
	step, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer step.Rollback(ctx)
	var prior string
	if err = step.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&prior); err != nil {
		return err
	}
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return err
	}
	rows, err := step.Query(ctx, `SELECT n.id::text,EXISTS(SELECT 1 FROM node_relations r JOIN nodes b ON b.tenant_id=r.tenant_id AND b.id=r.source_node_id WHERE r.target_node_id=n.id AND r.type='blocks' AND b.deleted_at IS NULL AND replace(replace(lower(btrim(b.state)),' ','_'),'-','_') NOT IN ('done','delivered','accepted','cancelled','archived')) FROM nodes n WHERE n.id=ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	blocked := map[string]bool{}
	for rows.Next() {
		var id string
		var b bool
		if err = rows.Scan(&id, &b); err != nil {
			rows.Close()
			return err
		}
		blocked[id] = b
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range items {
		items[i].blocked = blocked[items[i].id]
	}
	// At most 200 x 200 candidate edges; no dependency traversal can widen intake.
	edges, err := step.Query(ctx, `SELECT r.source_node_id::text,r.target_node_id::text FROM node_relations r JOIN nodes b ON b.tenant_id=r.tenant_id AND b.id=r.source_node_id WHERE r.type='blocks' AND r.source_node_id=ANY($1::uuid[]) AND r.target_node_id=ANY($1::uuid[]) AND b.deleted_at IS NULL AND replace(replace(lower(btrim(b.state)),' ','_'),'-','_') NOT IN ('done','delivered','accepted','cancelled','archived') ORDER BY r.source_node_id,r.target_node_id LIMIT 40001`, ids)
	if err != nil {
		return err
	}
	links := map[string][]string{}
	count := 0
	for edges.Next() {
		var source, target string
		if err = edges.Scan(&source, &target); err != nil {
			edges.Close()
			return err
		}
		links[target] = append(links[target], source)
		count++
	}
	edges.Close()
	if err = edges.Err(); err != nil {
		return err
	}
	if count > 40000 {
		return fail(409, "dependency graph exceeds selection bound")
	}
	dependencyOrder(items, links)
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, prior); err != nil {
		return err
	}
	return step.Commit(ctx)
}
func overlapLanes(ctx context.Context, tx pgx.Tx, projectID string) ([]Lane, error) {
	rows, err := tx.Query(ctx, `SELECT `+columns+source+` AND l.project_id=$1 AND l.enabled AND NOT l.paused AND (l.scope_kind='queued_tickets' OR EXISTS(SELECT 1 FROM journey_releases r WHERE r.release_node_id=l.scope_node_id AND r.state='building')) ORDER BY l.priority,l.node_id LIMIT 101`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lanes := []Lane{}
	for rows.Next() {
		l, err := scan(rows)
		if err != nil {
			return nil, err
		}
		lanes = append(lanes, l)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(lanes) > 100 {
		return nil, fail(409, "overlap selection exceeds 100 lanes")
	}
	return lanes, nil
}
func overlapWinner(lanes []Lane, c candidate, now time.Time) string {
	pin, _ := c.fields["autopilot_lane_id"].(string)
	for _, l := range lanes {
		if pin != "" && l.ID != pin {
			continue
		}
		w := nextWindow(l.Policy.Window, now)
		if w == nil || now.Before(w.StartsAt) || !matches(l.Policy, c.kind, c.fields) || workqueue.Hours(c.fields) > l.Policy.MaxTicketEstimateHours {
			continue
		}
		inScope := l.Scope.Kind == "queued_tickets" && c.queued
		if c.intent && c.intentLane == l.ID {
			inScope = true
		}
		if l.Scope.Kind == "release" && c.release != nil && l.Scope.ReleaseNodeID != nil && *c.release == *l.Scope.ReleaseNodeID {
			inScope = true
		}
		if inScope {
			return l.ID
		}
	}
	return ""
}

// used by preparation rechecks as well as scheduling; no background intake.
func candidateInScope(ctx context.Context, tx pgx.Tx, l Lane, id string) error {
	items, _, err := candidates(ctx, tx, l, false)
	if err != nil {
		return err
	}
	for _, c := range items {
		if c.id == id {
			return nil
		}
	}
	return fail(409, "ticket no longer in the approved lane intake")
}
func ownerAuthority(ctx context.Context, tx pgx.Tx, p tenant.Principal, l Lane) error {
	owner := tenant.Principal{ID: l.OwnerPrincipalID, TenantID: p.TenantID, Kind: tenant.Person}
	err := executionAuthority(ctx, tx, owner, l)
	if errors.Is(err, authz.ErrForbidden) {
		return authz.ErrForbidden
	}
	return err
}

// dependencyOrder is a stable dependency-first walk of the bounded intake.
// Cycles stay blocked by live facts; visiting guards termination. Independent
// tickets retain their shared queue/release order.
func dependencyOrder(items []candidate, links map[string][]string) {
	byID := map[string]candidate{}
	rank := map[string]int{}
	for i, c := range items {
		byID[c.id] = c
		rank[c.id] = i
	}
	for target, sources := range links {
		slices.SortFunc(sources, func(a, b string) int { return rank[a] - rank[b] })
		links[target] = sources
	}
	seen := map[string]bool{}
	ordered := make([]candidate, 0, len(items))
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, source := range links[id] {
			visit(source)
		}
		if c, ok := byID[id]; ok {
			ordered = append(ordered, c)
		}
	}
	for _, c := range items {
		visit(c.id)
	}
	copy(items, ordered)
}
