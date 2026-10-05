// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// A live flag and a pending proposal share a bounded, RLS-filtered projection.
// Events are the durable identity; the node timestamp binds each action to the
// revision shown. Existing receipts suppress dismissed episodes.
const attentionCTE = `WITH pending AS (SELECT DISTINCT ON (node_id) * FROM status_autopilot_proposals WHERE status='pending' ORDER BY node_id,event_id DESC), attention AS (
 SELECT q.event_id,n.id node_id,n.key,n.title,n.project_id,coalesce(n.fields->'assignee'->>'id',n.fields->>'assignee',n.fields->>'assignee_id') assignee_id,n.updated_at revision,
 'proposed' kind,e.before->>'state' source,
 coalesce(nullif(q.decision->>'Flag',''),q.decision->>'To') target,
 q.decision->>'Reason' reason,e.at,
 n.updated_at<>coalesce((SELECT u.after->>'updated_at' FROM events u WHERE u.metadata->>'original'=q.event_id::text AND u.type='status_autopilot.attention_undone' ORDER BY u.id DESC LIMIT 1),e.before->>'updated_at')::timestamptz OR n.state<>e.before->>'state' stale
 FROM pending q JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id
 JOIN events e ON e.tenant_id=q.tenant_id AND e.id=q.event_id
 WHERE q.status='pending' AND n.deleted_at IS NULL
 UNION ALL
 SELECT e.id,n.id,n.key,n.title,n.project_id,coalesce(n.fields->'assignee'->>'id',n.fields->>'assignee',n.fields->>'assignee_id'),n.updated_at,
 CASE mark.key WHEN 'triage_list' THEN 'triage' WHEN 'cancel_suggested' THEN 'cancel'
 WHEN 'blocked_reminder' THEN 'blocked' ELSE 'missed' END,n.state,mark.key,e.metadata->>'reason',e.at,false
 FROM nodes n CROSS JOIN LATERAL jsonb_each(n.status_autopilot) mark
 JOIN LATERAL (SELECT e.* FROM events e WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id
 AND e.type='status_autopilot.changed' AND e.metadata->>'flag'=mark.key AND e.after->>'state'=n.state
 AND NOT EXISTS(SELECT 1 FROM events u WHERE u.tenant_id=e.tenant_id AND u.undo_of=e.id)
 ORDER BY e.id DESC LIMIT 1) e ON true
 WHERE n.deleted_at IS NULL AND mark.value='true'::jsonb AND NOT EXISTS(SELECT 1 FROM pending q WHERE q.node_id=n.id)
 AND mark.key IN ('triage_list','cancel_suggested','blocked_reminder','missed_release')
), filtered AS (SELECT * FROM attention WHERE ($1='' OR kind=$1)
 AND ($2='' OR project_id=nullif($2,'')::uuid)
 AND ($3='' OR ($3='none' AND assignee_id IS NULL) OR assignee_id=nullif(nullif($3,''),'none'))
 AND ($4='' OR strpos(lower(key||' '||title),lower($4))>0)) `

type attentionInput struct {
	EventID         int64     `json:"event_id"`
	NodeID          string    `json:"node_id"`
	Revision        time.Time `json:"revision"`
	Resolution      int64     `json:"resolution_event_id,omitempty"`
	ReleaseID       string    `json:"release_id,omitempty"`
	ReleaseRevision int64     `json:"release_revision,omitempty"`
}
type attentionItem struct {
	attentionInput
	Key          string    `json:"key"`
	Title        string    `json:"title"`
	ProjectID    string    `json:"project_id"`
	Kind         string    `json:"kind"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	Reason       string    `json:"reason"`
	At           time.Time `json:"at"`
	Editable     bool      `json:"editable"`
	Applicable   bool      `json:"applicable"`
	Unavailable  string    `json:"unavailable_reason,omitempty"`
	ReleaseTitle string    `json:"release_title,omitempty"`
	stale        bool
}
type attentionFacet struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type attentionPage struct {
	Items  []attentionItem `json:"items"`
	Total  int             `json:"total"`
	Counts map[string]int  `json:"counts"`
	Next   *string         `json:"next_cursor"`
	Facets struct {
		Projects  []attentionFacet `json:"projects"`
		Assignees []attentionFacet `json:"assignees"`
	} `json:"facets"`
	Truncated bool `json:"facets_truncated"`
}

func attentionKind(k string) bool {
	return k == "" || k == "proposed" || k == "triage" || k == "cancel" || k == "blocked" || k == "missed"
}
func attentionCursor(raw string) (string, int64, error) {
	if raw == "" {
		return "", 0, nil
	}
	if len(raw) > 120 {
		return "", 0, fmt.Errorf("cursor too long")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", 0, err
	}
	parts := strings.Split(string(b), "/")
	if len(parts) != 2 || !uuid.MatchString(parts[0]) {
		return "", 0, fmt.Errorf("invalid cursor")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id < 1 {
		return "", 0, fmt.Errorf("invalid event")
	}
	return parts[0], id, nil
}
func (m *Module) attention(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind, project, assignee, search := q.Get("kind"), q.Get("project_id"), q.Get("assignee"), q.Get("q")
	nodeID, eventID, err := attentionCursor(q.Get("after"))
	if err != nil || !attentionKind(kind) || len(search) > 200 || project != "" && !uuid.MatchString(project) || assignee != "" && assignee != "none" && !uuid.MatchString(assignee) {
		httpapi.WriteError(w, 400, "invalid filter or cursor")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := attentionPage{Items: []attentionItem{}, Counts: map[string]int{}}
	out.Facets.Projects = []attentionFacet{}
	out.Facets.Assignees = []attentionFacet{}
	args := []any{kind, project, assignee, search}
	err = db.InTenant(db.WithReadStatementTimeout(ctx, 8*time.Second), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, attentionCTE+`SELECT kind,count(*) FROM attention WHERE ($2='' OR project_id=nullif($2,'')::uuid)
 AND ($3='' OR ($3='none' AND assignee_id IS NULL) OR assignee_id=nullif(nullif($3,''),'none'))
 AND ($4='' OR strpos(lower(key||' '||title),lower($4))>0) GROUP BY kind`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			var n int
			if err = rows.Scan(&k, &n); err != nil {
				rows.Close()
				return err
			}
			out.Counts[k] = n
			if kind == "" || kind == k {
				out.Total += n
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, attentionCTE+`SELECT event_id,node_id::text,key,title,coalesce(project_id::text,''),revision,kind,source,target,reason,at,stale
 FROM filtered WHERE ($5='' OR (node_id,event_id)>(nullif($5,'')::uuid,$6)) ORDER BY node_id,event_id LIMIT 51`, append(args, nodeID, eventID)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item attentionItem
			if err = rows.Scan(&item.EventID, &item.NodeID, &item.Key, &item.Title, &item.ProjectID, &item.Revision, &item.Kind, &item.From, &item.To, &item.Reason, &item.At, &item.stale); err != nil {
				rows.Close()
				return err
			}
			out.Items = append(out.Items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(out.Items) > 50 {
			out.Items = out.Items[:50]
			last := out.Items[49]
			cursor := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s/%d", last.NodeID, last.EventID)))
			out.Next = &cursor
		}
		for i := range out.Items {
			if err = m.prepareAttention(ctx, tx, p, &out.Items[i]); err != nil {
				return err
			}
		}
		for _, facet := range []string{"projects", "assignees"} {
			query := `SELECT DISTINCT n.id::text,n.key||' '||n.title FROM attention a JOIN nodes n ON n.id=a.project_id ORDER BY 2,1 LIMIT 101`
			if facet == "assignees" {
				query = `SELECT DISTINCT p.id::text,p.name FROM attention a JOIN principals p ON p.id::text=a.assignee_id ORDER BY 2,1 LIMIT 101`
			}
			rows, err = tx.Query(ctx, attentionCTE+query, args...)
			if err != nil {
				return err
			}
			items := []attentionFacet{}
			for rows.Next() {
				var f attentionFacet
				if err = rows.Scan(&f.ID, &f.Label); err != nil {
					rows.Close()
					return err
				}
				items = append(items, f)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(items) > 100 {
				out.Truncated = true
				items = items[:100]
			}
			if facet == "projects" {
				out.Facets.Projects = items
			} else {
				out.Facets.Assignees = items
			}
		}
		return nil
	})
	respond(w, out, err)
}
func (m *Module) prepareAttention(ctx context.Context, tx pgx.Tx, p tenant.Principal, item *attentionItem) error {
	scope := authz.Scope{ProjectID: item.ProjectID}
	item.Editable = authz.RequireTx(ctx, tx, p, "nodes.write", scope) == nil
	item.Applicable = item.Editable && !item.stale
	if item.stale {
		item.Unavailable = "Ticket changed since the proposal. Dismiss it and review the ticket."
	}
	var parent bool
	var err error
	parent, err = db.WorkStatusParentTx(ctx, tx, item.NodeID)
	if err != nil {
		return err
	}
	if parent {
		item.Applicable = false
		item.Unavailable = "Parent statuses follow their children."
	}
	switch item.To {
	case "triage_list":
		item.To = "backlog"
	case "cancel_suggested":
		item.To = "cancelled"
	case "blocked_reminder":
		item.To = "open"
	case "missed_release":
		item.To = "release"
		err = tx.QueryRow(ctx, `SELECT r.release_node_id::text,n.title,r.revision FROM journey_projects j
 JOIN journey_releases r ON r.tenant_id=j.tenant_id AND r.release_node_id=j.current_release_node_id
 JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id
 WHERE j.project_node_id=$1 AND r.state='planning' AND n.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM journey_tickets t WHERE t.ticket_node_id=$2 AND t.release_node_id IS NOT NULL)`, item.ProjectID, item.NodeID).Scan(&item.ReleaseID, &item.ReleaseTitle, &item.ReleaseRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			item.Applicable = false
			item.Unavailable = "Choose a planning release in the ticket’s project first."
		} else if err != nil {
			return err
		}
		if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "releases.write", scope) != nil {
			item.Applicable = false
			item.Unavailable = "Adding to a release needs permission to manage releases."
		}
	}
	if item.Kind == "proposed" {
		s, err := Load(ctx, tx)
		if err != nil {
			return err
		}
		if s.ServerMode == "off" {
			item.Applicable = false
			item.Unavailable = "The server operator has paused status autopilot."
		}
		// Exact eligibility is checked again under the mutation fence.
	}
	return nil
}

func decodeAttention(w http.ResponseWriter, r *http.Request, out any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("one object required")
	}
	return nil
}

type attentionResult struct {
	EventID    int64      `json:"event_id"`
	OK         bool       `json:"ok"`
	Resolution int64      `json:"resolution_event_id,omitempty"`
	Revision   *time.Time `json:"revision,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func (m *Module) attentionActions(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Action string           `json:"action"`
		Items  []attentionInput `json:"items"`
	}
	if decodeAttention(w, r, &in) != nil || in.Action != "apply" && in.Action != "dismiss" && in.Action != "undo" || len(in.Items) < 1 || len(in.Items) > 100 {
		httpapi.WriteError(w, 400, "action and 1 to 100 items required")
		return
	}
	seen := map[int64]bool{}
	for _, item := range in.Items {
		if item.EventID < 1 || !uuid.MatchString(item.NodeID) || item.Revision.IsZero() || seen[item.EventID] || in.Action == "undo" && item.Resolution < 1 || item.ReleaseID != "" && !uuid.MatchString(item.ReleaseID) {
			httpapi.WriteError(w, 400, "invalid or duplicate item")
			return
		}
		seen[item.EventID] = true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out := struct {
		Items []attentionResult `json:"items"`
	}{Items: []attentionResult{}}
	for _, item := range in.Items {
		result := attentionResult{EventID: item.EventID}
		// Independent transactions make failures precise and bound each lock hold.
		err := db.InTenant(db.WithAdditionalTenantGuard(ctx, func(ctx context.Context, tx pgx.Tx, tid string) error {
			var id string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, tid).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aeon-pairing:"+tid); err != nil {
				return err
			}
			return lock(ctx, tx)
		}), m.pool, p.TenantID, func(tx pgx.Tx) error {
			if err := db.SetLocalStatementTimeout(ctx, tx, 8*time.Second); err != nil {
				return err
			}
			return m.resolveAttention(ctx, tx, p, in.Action, item, &result)
		})
		if err != nil {
			result.Error = "The suggestion could not be changed. Try again."
			if errors.Is(err, events.ErrConflict) {
				result.Error = "The ticket or suggestion changed. Reload before trying again."
			}
			if errors.Is(err, authz.ErrForbidden) || errors.Is(err, events.ErrForbidden) {
				result.Error = "You may no longer edit this ticket."
			}
			if errors.Is(err, pgx.ErrNoRows) {
				result.Error = "The ticket or suggestion is no longer available."
			}
		} else {
			result.OK = true
		}
		out.Items = append(out.Items, result)
	}
	respond(w, out, nil)
}

type attentionResolution struct {
	Original   int64  `json:"original"`
	Proposal   bool   `json:"proposal"`
	Action     string `json:"action"`
	Rule       string `json:"rule"`
	Anchor     string `json:"anchor"`
	Membership int64  `json:"membership_event_id,omitempty"`
}

func (m *Module) resolveAttention(ctx context.Context, tx pgx.Tx, p tenant.Principal, action string, in attentionInput, out *attentionResult) error {
	c, before, err := loadCandidate(ctx, tx, in.NodeID, time.Now().UTC())
	if err != nil {
		return err
	}
	scope := authz.Scope{}
	if c.Node.ProjectID != nil {
		scope.ProjectID = *c.Node.ProjectID
	}
	if err = authz.RequireTx(ctx, tx, p, "nodes.write", scope); err != nil {
		return err
	}
	if !c.Node.Updated.Equal(in.Revision) {
		return events.ErrConflict
	}
	if action == "undo" {
		return m.undoAttention(ctx, tx, p, in, c, before, out)
	}
	var item attentionItem
	err = tx.QueryRow(ctx, attentionCTE+`SELECT event_id,node_id::text,key,title,coalesce(project_id::text,''),revision,kind,source,target,reason,at,stale FROM filtered WHERE event_id=$5 AND node_id=$6`, "", "", "", "", in.EventID, in.NodeID).Scan(&item.EventID, &item.NodeID, &item.Key, &item.Title, &item.ProjectID, &item.Revision, &item.Kind, &item.From, &item.To, &item.Reason, &item.At, &item.stale)
	if err != nil {
		return err
	}
	originalTarget := item.To
	if err = m.prepareAttention(ctx, tx, p, &item); err != nil {
		return err
	}
	if action == "apply" && !item.Applicable {
		return events.ErrConflict
	}
	resolution := attentionResolution{Original: in.EventID, Proposal: item.Kind == "proposed", Action: action}
	var membershipChange *events.Change
	var d decision
	if resolution.Proposal {
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT rule,anchor,decision FROM status_autopilot_proposals WHERE event_id=$1 AND status='pending' FOR UPDATE`, in.EventID).Scan(&resolution.Rule, &resolution.Anchor, &raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			return err
		}
		if action == "apply" {
			s, err := Load(ctx, tx)
			if err != nil {
				return err
			}
			if c.Node.ProjectID != nil {
				o, err := Project(ctx, tx, *c.Node.ProjectID, s.Enabled)
				if err != nil {
					return err
				}
				if o.Mode == "off" {
					return events.ErrConflict
				}
				s.Enabled = o.Effective
			}
			if err = proposalEligible(ctx, tx, c, s, d, time.Now().UTC()); err != nil {
				return err
			}
		}
	} else {
		if err = tx.QueryRow(ctx, `SELECT rule,anchor FROM status_autopilot_receipts WHERE node_id=$1 AND event_id=$2`, in.NodeID, in.EventID).Scan(&resolution.Rule, &resolution.Anchor); err != nil {
			return err
		}
	}
	if action == "apply" {
		parent, err := db.WorkStatusParentTx(ctx, tx, in.NodeID)
		if err != nil {
			return err
		}
		if parent {
			return events.ErrConflict
		}
		if item.To == "release" {
			if in.ReleaseID != item.ReleaseID || in.ReleaseRevision != item.ReleaseRevision {
				return events.ErrConflict
			}
			// Acquire membership's pairing fence before node/resource locks in this path
			// (the tenant fence serializes pairing admission as well).
			if c.Node.ProjectID == nil {
				return events.ErrConflict
			}
			if resolution.Proposal {
				if _, err = tx.Exec(ctx, `UPDATE nodes SET status_autopilot=jsonb_set(status_autopilot,'{missed_release}','true'::jsonb) WHERE id=$1`, in.NodeID); err != nil {
					return err
				}
			}
			change, err := releases.AddMissedReleaseTx(ctx, tx, p, *c.Node.ProjectID, item.ReleaseID, in.NodeID, item.ReleaseRevision)
			if err != nil {
				return err
			}
			membershipChange = &change
		} else {
			if item.To == "delivered" || item.To == "accepted" {
				if pending(c.Node) {
					return events.ErrConflict
				}
			}
			c.Node.State = item.To
			c.Node.Marks = map[string]bool{}
		}
	}
	if c.Node.Marks == nil {
		c.Node.Marks = map[string]bool{}
	}
	delete(c.Node.Marks, originalTarget)
	marks, err := json.Marshal(c.Node.Marks)
	if err != nil {
		return err
	}
	var after []byte
	var revision time.Time
	if err = tx.QueryRow(ctx, `UPDATE nodes SET state=$2,status_autopilot=$3,updated_at=clock_timestamp() WHERE id=$1 RETURNING to_jsonb(nodes),updated_at`, in.NodeID, c.Node.State, marks).Scan(&after, &revision); err != nil {
		return err
	}
	if resolution.Proposal {
		status := "dismissed"
		if action == "apply" {
			status = "applied"
		}
		if _, err = tx.Exec(ctx, `UPDATE status_autopilot_proposals SET status=$2 WHERE event_id=$1`, in.EventID, status); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO status_autopilot_receipts(tenant_id,node_id,rule,anchor,event_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, p.TenantID, in.NodeID, resolution.Rule, resolution.Anchor, in.EventID); err != nil {
			return err
		}
	}
	// Membership, node, proposal and receipt writes precede the event counter.
	if membershipChange != nil {
		e, err := events.Append(ctx, tx, p, *membershipChange)
		if err != nil {
			return err
		}
		resolution.Membership = e.ID
	}
	meta, err := json.Marshal(map[string]any{"job": Job, "reason": item.Reason, "rule": resolution.Rule, "resolution": resolution})
	if err != nil {
		return err
	}
	e, err := events.Append(ctx, tx, p, events.Change{NodeID: &in.NodeID, Type: "status_autopilot.attention_" + action, Before: json.RawMessage(before), After: json.RawMessage(after), Metadata: meta})
	if err != nil {
		return err
	}
	out.Resolution = e.ID
	out.Revision = &revision
	return nil
}
func (m *Module) undoAttention(ctx context.Context, tx pgx.Tx, p tenant.Principal, in attentionInput, c candidate, current []byte, out *attentionResult) error {
	var e events.Event
	var meta []byte
	var nodeID string
	var used bool
	err := tx.QueryRow(ctx, `SELECT id,node_id::text,type,before,after,metadata,EXISTS(SELECT 1 FROM events u WHERE u.tenant_id=e.tenant_id AND u.undo_of=e.id)
 FROM events e WHERE id=$1 AND node_id=$2 AND type IN ('status_autopilot.attention_apply','status_autopilot.attention_dismiss')`, in.Resolution, in.NodeID).Scan(&e.ID, &nodeID, &e.Type, &e.Before, &e.After, &meta, &used)
	if err != nil {
		return err
	}
	e.NodeID = &nodeID
	var metadata struct {
		Resolution attentionResolution `json:"resolution"`
	}
	var before, after node
	if used || json.Unmarshal(meta, &metadata) != nil || metadata.Resolution.Original != in.EventID || json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil || !c.Node.Updated.Equal(after.Updated) || c.Node.State != after.State {
		return events.ErrConflict
	}
	parent, err := db.WorkStatusParentTx(ctx, tx, in.NodeID)
	if err != nil {
		return err
	}
	if parent {
		return events.ErrConflict
	}
	res := metadata.Resolution
	var membershipUndo *events.Change
	if res.Membership != 0 {
		var member events.Event
		if err = tx.QueryRow(ctx, `SELECT id,before,after,node_id::text FROM events WHERE id=$1 AND type='journey.release_membership_changed'`, res.Membership).Scan(&member.ID, &member.Before, &member.After, &member.NodeID); err != nil {
			return err
		}
		member.Type = "journey.release_membership_changed"
		change, err := releases.UndoHandlers()[member.Type](ctx, tx, p, member)
		if err != nil {
			return err
		}
		change.UndoOf = &member.ID
		membershipUndo = &change
	}
	marks, err := json.Marshal(before.Marks)
	if err != nil {
		return err
	}
	var restored []byte
	var revision time.Time
	if err = tx.QueryRow(ctx, `UPDATE nodes SET state=$2,status_autopilot=$3,updated_at=clock_timestamp() WHERE id=$1 RETURNING to_jsonb(nodes),updated_at`, in.NodeID, before.State, marks).Scan(&restored, &revision); err != nil {
		return err
	}
	if res.Proposal {
		if _, err = tx.Exec(ctx, `UPDATE status_autopilot_proposals SET status='pending' WHERE event_id=$1`, in.EventID); err != nil {
			return err
		}
		// Restored proposals get a fresh authoritative baseline via the Undo event;
		// the original event remains immutable. Listing/eligibility consult this below.
		if _, err = tx.Exec(ctx, `DELETE FROM status_autopilot_receipts WHERE node_id=$1 AND rule=$2 AND anchor=$3 AND event_id=$4`, in.NodeID, res.Rule, res.Anchor, in.EventID); err != nil {
			return err
		}
	}
	// All resource and membership locks precede the event counter.
	if membershipUndo != nil {
		if _, err = events.Append(ctx, tx, p, *membershipUndo); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(map[string]any{"job": Job, "reason": "Undo Needs attention resolution", "original": in.EventID})
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &in.NodeID, Type: "status_autopilot.attention_undone", Before: json.RawMessage(current), After: json.RawMessage(restored), Metadata: raw, UndoOf: &e.ID})
	if err != nil {
		return err
	}
	out.Revision = &revision
	return nil
}
