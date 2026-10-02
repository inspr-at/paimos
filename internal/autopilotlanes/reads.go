// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

func (m *Module) history(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	limit, err := pageLimit(r)
	if err != nil {
		respond(w, 200, nil, err)
		return
	}
	after := int64(0)
	if s := r.URL.Query().Get("after"); s != "" {
		after, err = strconv.ParseInt(s, 10, 64)
		if err != nil || after < 0 {
			respond(w, 200, nil, fail(400, "invalid event cursor"))
			return
		}
	}
	out := struct {
		Items []events.Event `json:"items"`
		Next  *int64         `json:"next_after"`
	}{Items: []events.Event{}}
	err = m.transaction(r, p, false, func(ctx context.Context, tx pgx.Tx) error {
		l, err := load(ctx, tx, r.PathValue("laneId"), false)
		if err != nil {
			return err
		}
		if err = permission(ctx, tx, p, "autopilot.read", l.ProjectID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at,undo_of FROM events WHERE node_id=$1 AND type LIKE 'node.autopilot_%' AND id>$2 ORDER BY id LIMIT $3`, l.ID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e events.Event
			if err = rows.Scan(&e.ID, &e.ActorPrincipalID, &e.NodeID, &e.Type, &e.Before, &e.After, &e.At, &e.UndoOf); err != nil {
				return err
			}
			out.Items = append(out.Items, e)
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			out.Next = &out.Items[limit-1].ID
		}
		return rows.Err()
	})
	respond(w, 200, out, err)
}

type previewCursor struct {
	Lane     string `json:"lane"`
	Revision int64  `json:"revision"`
	Target   string `json:"target"`
	Position int    `json:"position"`
	Run      string `json:"run"`
}

func parseCursor(raw string, l Lane) (previewCursor, error) {
	c := previewCursor{Lane: l.ID, Revision: l.Revision, Run: "00000000-0000-0000-0000-000000000000"}
	if raw == "" {
		return c, nil
	}
	if len(raw) > 1024 {
		return c, fail(400, "invalid preview cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, fail(400, "invalid preview cursor")
	}
	if err = json.Unmarshal(b, &c); err != nil || !uuid.MatchString(c.Run) || c.Target != "" && !uuid.MatchString(c.Target) || c.Position < 1 {
		return c, fail(400, "invalid preview cursor")
	}
	if c.Lane != l.ID || c.Revision != l.Revision {
		return c, fail(409, "preview policy changed; reload")
	}
	return c, nil
}
func cursorString(c previewCursor) *string {
	b, _ := json.Marshal(c)
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

type Route struct {
	Role      string  `json:"role"`
	Area      string  `json:"area"`
	ProfileID *string `json:"profile_id"`
	Basis     string  `json:"basis"`
}
type PreviewItem struct {
	TicketNodeID   string    `json:"ticket_node_id"`
	TicketRevision time.Time `json:"ticket_revision"`
	RunID          string    `json:"run_id"`
	Key            string    `json:"key"`
	Title          string    `json:"title"`
	Eligible       bool      `json:"eligible"`
	Exclusions     []string  `json:"exclusions"`
	Preparation    string    `json:"preparation"`
	EstimateBasis  string    `json:"estimate_basis"`
	ProposedRoute  Route     `json:"proposed_route"`
}
type Preview struct {
	LaneID             string          `json:"lane_id"`
	Revision           int64           `json:"revision"`
	Items              []PreviewItem   `json:"items"`
	Candidate          *PreviewItem    `json:"candidate"`
	NextCursor         *string         `json:"next_cursor"`
	MissingDecisions   []string        `json:"missing_decisions"`
	BudgetBasis        string          `json:"budget_basis"`
	NextWindow         *WindowInstance `json:"next_window"`
	ExecutionAvailable bool            `json:"execution_available"`
}

func preparation(fields map[string]any) string {
	if len(workqueue.Criteria(fields)) == 0 {
		return "criteria_draft_requires_person"
	}
	if workqueue.Hours(fields) == 0 {
		return "automatic_estimate"
	}
	return "none"
}
func previewEligibility(l Lane, item *PreviewItem, kind, state, body string, fields map[string]any, personQueued, blocked, busy bool) {
	item.Exclusions = []string{}
	item.Preparation = preparation(fields)
	item.EstimateBasis = "uncalibrated ticket estimate; not a finish promise"
	if !personQueued {
		item.Exclusions = append(item.Exclusions, "not_person_queued")
	}
	if !matches(l.Policy, kind, fields) {
		item.Exclusions = append(item.Exclusions, "outside_lane_filters")
	}
	ready := workqueue.Check(kind, state, item.Title, body, fields, blocked)
	if !ready.Queueable {
		item.Exclusions = append(item.Exclusions, "ticket_not_queueable")
	}
	if workqueue.State(state) == "blocked" || blocked {
		item.Exclusions = append(item.Exclusions, "unresolved_blocker")
	}
	if busy {
		item.Exclusions = append(item.Exclusions, "existing_work")
	}
	if workqueue.Hours(fields) > l.Policy.MaxTicketEstimateHours {
		item.Exclusions = append(item.Exclusions, "estimate_above_lane_limit")
	}
	if item.Preparation != "none" {
		item.Exclusions = append(item.Exclusions, "preparation_required")
	}
	item.Eligible = len(item.Exclusions) == 0
}
func (m *Module) preview(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	limit, err := pageLimit(r)
	if err != nil {
		respond(w, 200, nil, err)
		return
	}
	// Bound cursor bytes before opening a transaction or decoding.
	if len(r.URL.Query().Get("cursor")) > 1024 {
		respond(w, 200, nil, fail(400, "invalid preview cursor"))
		return
	}
	out := Preview{Items: []PreviewItem{}, MissingDecisions: []string{"execution_enforcement_pending", "account_readiness_and_locked_policy_recheck_required", "person_working_target_required"}, BudgetBasis: "subscription agent-hours per window: summed owned-process elapsed time; quota reserve retained; usage and holds not measured here"}
	err = m.transaction(r, p, false, func(ctx context.Context, tx pgx.Tx) error {
		// One policy/queue snapshot; tree writers (queue moves included) cannot
		// change ordering midway through the advisory read.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended(current_setting('aeon.tenant_id'),0))`); err != nil {
			return err
		}
		l, err := load(ctx, tx, r.PathValue("laneId"), false)
		if err != nil {
			return err
		}
		if err = permission(ctx, tx, p, "autopilot.read", l.ProjectID); err != nil {
			return err
		}
		c, err := parseCursor(r.URL.Query().Get("cursor"), l)
		if err != nil {
			return err
		}
		now := m.now().UTC()
		out.LaneID = l.ID
		out.Revision = l.Revision
		out.NextWindow = nextWindow(l.Policy.Window, now)
		if !l.Enabled {
			out.MissingDecisions = append(out.MissingDecisions, "lane_disabled")
		}
		if l.Paused {
			out.MissingDecisions = append(out.MissingDecisions, "lane_paused")
		}
		if out.NextWindow == nil || now.Before(out.NextWindow.StartsAt) {
			out.MissingDecisions = append(out.MissingDecisions, "outside_work_window")
		}
		if l.Scope.Kind == "release" {
			out.MissingDecisions = append(out.MissingDecisions, "release_preview_pending")
			return nil
		}
		rows, err := tx.Query(ctx, workqueue.CTE+`SELECT q.queue_node_id::text,n.updated_at,q.id::text,q.key,left(q.title,512),q.state,k.slug,left(n.body,2048),left(q.fields::text,65537),p.kind='person',q.model_profile_id::text,coalesce(q.queue_target_agent_id::text,''),q.queue_position,
   EXISTS(SELECT 1 FROM node_relations r JOIN nodes b ON b.tenant_id=r.tenant_id AND b.id=r.source_node_id WHERE r.target_node_id=n.id AND r.type='blocks' AND b.deleted_at IS NULL AND replace(replace(lower(btrim(b.state)),' ','_'),'-','_') NOT IN ('done','delivered','accepted','cancelled','archived')),
   EXISTS(SELECT 1 FROM agent_runs a WHERE a.tenant_id=q.tenant_id AND (a.queue_node_id=n.id OR a.work_order_id IN (SELECT w.node_id FROM work_orders w WHERE w.tenant_id=q.tenant_id AND w.node_id IN (SELECT child.id FROM nodes child WHERE child.tenant_id=q.tenant_id AND child.parent_id=n.id))) AND a.id<>q.id AND a.status IN ('starting','running','waiting','ownership_lost'))
   FROM queue_heads q JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.queue_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id JOIN principals p ON p.tenant_id=q.tenant_id AND p.id=q.queue_by_principal_id
   WHERE q.project_id=$1 AND (coalesce(q.queue_target_agent_id::text,''),q.queue_position,q.id)>($2::text,$3::int,$4::uuid)
   ORDER BY coalesce(q.queue_target_agent_id::text,''),q.queue_position,q.id LIMIT $5`, l.ProjectID, c.Target, c.Position, c.Run, limit+1)
		if err != nil {
			return err
		}
		type sample struct {
			item                      PreviewItem
			kind, state, body, target string
			fields                    []byte
			person, blocked, busy     bool
			position                  int
		}
		samples := []sample{}
		for rows.Next() {
			var s sample
			err = rows.Scan(&s.item.TicketNodeID, &s.item.TicketRevision, &s.item.RunID, &s.item.Key, &s.item.Title, &s.state, &s.kind, &s.body, &s.fields, &s.person, &s.item.ProposedRoute.ProfileID, &s.target, &s.position, &s.blocked, &s.busy)
			if err != nil {
				rows.Close()
				return err
			}
			samples = append(samples, s)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(samples) > limit {
			samples = samples[:limit]
			last := samples[limit-1]
			out.NextCursor = cursorString(previewCursor{l.ID, l.Revision, last.target, last.position, last.item.RunID})
		}
		routeCache := map[string]*modelregistry.TicketRoute{}
		for _, s := range samples {
			item := s.item
			fields := map[string]any{}
			if len(s.fields) <= 65536 {
				fields = workqueue.Fields(s.fields)
			}
			previewEligibility(l, &item, s.kind, s.state, s.body, fields, s.person, s.blocked, s.busy)
			if len(s.fields) > 65536 {
				item.Exclusions = append(item.Exclusions, "ticket_fields_too_large")
				item.Eligible = false
			}
			role, _ := fields["route_role"].(string)
			area, _ := fields["area"].(string)
			item.ProposedRoute.Role = role
			item.ProposedRoute.Area = area
			item.ProposedRoute.Basis = "existing queue profile; policy/account admission not evaluated"
			if item.ProposedRoute.ProfileID == nil {
				item.ProposedRoute.Basis = "role ladder only; area fit and account admission not evaluated"
				key := role + "\x00" + area
				route, seen := routeCache[key]
				if !seen {
					route, err = modelregistry.ResolveTicketRoute(ctx, tx, role, area, now)
					if err != nil {
						return err
					}
					routeCache[key] = route
				}
				if route != nil {
					id := route.Profile.ID
					item.ProposedRoute.ProfileID = &id
				} else {
					item.ProposedRoute.Basis = "route unresolved; role/area or permitted profile required"
				}
			}
			out.Items = append(out.Items, item)
			if out.Candidate == nil && item.Eligible {
				copy := item
				out.Candidate = &copy
			}
		}
		return nil
	})
	respond(w, 200, out, err)
}
