// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Proposal struct {
	EventID      int64     `json:"event_id"`
	NodeID       string    `json:"node_id"`
	Key          string    `json:"key"`
	Title        string    `json:"title"`
	Rule         string    `json:"rule"`
	Reason       string    `json:"reason"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	At           time.Time `json:"at"`
	ChangedSince bool      `json:"changed_since"`
	Applicable   bool      `json:"applicable"`
}

// Suggest records the decision and the exact ticket revision without changing
// the ticket, its flags, timestamps or comments. The episode key deduplicates
// restarts; dismissed episodes also retain a receipt so On cannot replay them.
func enact(ctx context.Context, tx pgx.Tx, p tenant.Principal, n node, d decision, mode string) error {
	if mode == "off" {
		return nil
	}
	if mode != "suggest" {
		return apply(ctx, tx, p, n, d)
	}
	if d.Skip {
		return nil
	}
	var seen bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM status_autopilot_receipts WHERE node_id=$1 AND rule=$2 AND anchor=$3)
 OR EXISTS(SELECT 1 FROM status_autopilot_proposals WHERE node_id=$1 AND rule=$2 AND anchor=$3)`, n.ID, d.Rule, d.Anchor).Scan(&seen)
	if err != nil || seen {
		return err
	}
	var before json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, n.ID).Scan(&before); err != nil {
		return err
	}
	meta, err := json.Marshal(map[string]any{"job": Job, "rule": d.Rule, "reason": d.Reason, "flag": d.Flag})
	if err != nil {
		return err
	}
	e, err := events.Append(ctx, tx, p, events.Change{NodeID: &n.ID, Type: "status_autopilot.proposed", Before: before, After: before, Metadata: meta})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO status_autopilot_proposals(tenant_id,event_id,node_id,rule,anchor,decision) VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, e.ID, n.ID, d.Rule, d.Anchor, raw)
	return err
}

func (m *Module) proposals(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	out := []Proposal{}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		s, err := Load(r.Context(), tx)
		if err != nil {
			return err
		}
		allowed := admin(r.Context(), tx, p) == nil && s.ServerMode != "off"
		rows, err := tx.Query(r.Context(), `SELECT q.event_id,n.id::text,n.key,n.title,q.rule,q.decision->>'Reason',e.before->>'state',
 coalesce(nullif(q.decision->>'Flag',''),q.decision->>'To'),e.at,
 n.updated_at<>(e.before->>'updated_at')::timestamptz OR n.state<>e.before->>'state',n.project_id::text
 FROM status_autopilot_proposals q JOIN events e ON e.tenant_id=q.tenant_id AND e.id=q.event_id
 JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id
 WHERE q.status='pending' AND n.deleted_at IS NULL ORDER BY e.at DESC,e.id DESC`)
		if err != nil {
			return err
		}
		projects := []*string{}
		for rows.Next() {
			var item Proposal
			var project *string
			if err = rows.Scan(&item.EventID, &item.NodeID, &item.Key, &item.Title, &item.Rule, &item.Reason, &item.From, &item.To, &item.At, &item.ChangedSince, &project); err != nil {
				rows.Close()
				return err
			}
			out = append(out, item)
			projects = append(projects, project)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for i, project := range projects {
			scope := authz.Scope{}
			if project != nil {
				scope.ProjectID = *project
			}
			out[i].Applicable = allowed && !out[i].ChangedSince && authz.RequireTx(r.Context(), tx, p, "nodes.write", scope) == nil
		}
		return nil
	})
	respond(w, struct {
		Items []Proposal `json:"items"`
	}{out}, err)
}

func (m *Module) resolveProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("eventId"), 10, 64)
	var in struct {
		Action string `json:"action"`
	}
	if err != nil || id < 1 || decode(w, r, &in) != nil || in.Action != "apply" && in.Action != "dismiss" {
		httpapi.WriteError(w, 400, "proposal and action required")
		return
	}
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := admin(ctx, tx, p); err != nil {
			return err
		}
		if err := lock(ctx, tx); err != nil {
			return err
		}
		var nodeID, status string
		var raw, baseline []byte
		if err := tx.QueryRow(ctx, `SELECT q.node_id::text,q.status,q.decision,e.before FROM status_autopilot_proposals q
 JOIN events e ON e.tenant_id=q.tenant_id AND e.id=q.event_id WHERE q.event_id=$1 FOR UPDATE OF q`, id).Scan(&nodeID, &status, &raw, &baseline); err != nil {
			return err
		}
		if status != "pending" {
			return events.ErrConflict
		}
		var d decision
		var before node
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		if err := json.Unmarshal(baseline, &before); err != nil {
			return err
		}
		now := time.Now().UTC()
		c, _, err := loadCandidate(ctx, tx, nodeID, now)
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
		if in.Action == "apply" {
			if !c.Node.Updated.Equal(before.Updated) || c.Node.State != before.State {
				return events.ErrConflict
			}
			s, err := Load(ctx, tx)
			if err != nil {
				return err
			}
			if s.ServerMode == "off" {
				return authz.ErrForbidden
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
			if err = proposalEligible(ctx, tx, c, s, d, now); err != nil {
				return err
			}
			actor, err := systemactor.Ensure(ctx, tx, p.TenantID)
			if err != nil {
				return err
			}
			if err = apply(ctx, tx, actor, c.Node, d); err != nil {
				return err
			}
			status = "applied"
		} else {
			status = "dismissed"
			if _, err = tx.Exec(ctx, `INSERT INTO status_autopilot_receipts(tenant_id,node_id,rule,anchor,event_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, p.TenantID, nodeID, d.Rule, d.Anchor, id); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE status_autopilot_proposals SET status=$2 WHERE event_id=$1`, id, status); err != nil {
			return err
		}
		// Resolution is an autopilot audit event, not fresh work/activity evidence.
		// In particular, Dismiss must not create a new Backlog inactivity anchor.
		meta, err := json.Marshal(map[string]any{"job": Job, "rule": d.Rule, "reason": d.Reason})
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &nodeID, Type: "status_autopilot.proposal_" + status, After: map[string]any{"proposal_event_id": id, "action": in.Action}, Metadata: meta})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteError(w, 404, "proposal not found")
		return
	}
	respond(w, struct{}{}, err)
}

func proposalEligible(ctx context.Context, tx pgx.Tx, c candidate, s Settings, d decision, now time.Time) error {
	if d.Rule != "publish" {
		current := evaluate(c, s, now)
		if current == nil || current.Skip || *current != d {
			return events.ErrConflict
		}
		return nil
	}
	if !s.Enabled || !s.Rules["publish"].Enabled || pending(c.Node) || normaliseState(c.Node.State) != "done" || c.Node.ProjectID == nil {
		return events.ErrConflict
	}
	var title, version string
	var published time.Time
	err := tx.QueryRow(ctx, `SELECT n.title,coalesce(r.version,''),r.released_at FROM journey_releases r
 JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id
 WHERE r.release_node_id=$1 AND r.project_node_id=$2 AND r.state IN ('released','superseded') AND n.deleted_at IS NULL`, d.Anchor, *c.Node.ProjectID).Scan(&title, &version, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return events.ErrConflict
	}
	if err != nil {
		return err
	}
	if c.Since.After(published) || deliveryDecision(c.Node, d.Anchor, title, version) != d {
		return events.ErrConflict
	}
	return nil
}
