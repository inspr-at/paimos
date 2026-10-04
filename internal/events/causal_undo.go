// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const derivedStatusEvent = "status_autopilot.derived"

type causalNode struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}
type causalPreview struct {
	EventID      int64        `json:"event_id"`
	CauseEventID int64        `json:"cause_event_id"`
	Nodes        []causalNode `json:"affected_nodes"`
	Message      string       `json:"message"`
	ChangeType   string       `json:"change_type"`
}

// causalChange reads both events under caller RLS. Never raise visibility for
// preview or Undo: a parent event cannot grant access to its hidden cause.
func (m *module) causalChange(ctx context.Context, tx pgx.Tx, p tenant.Principal, parent Event) (Event, UndoFunc, causalPreview, error) {
	var out causalPreview
	if parent.Type != derivedStatusEvent || parent.NodeID == nil || parent.UndoOf != nil {
		return Event{}, nil, out, ErrConflict
	}
	// Historical derivations remain undoable while the feature is disabled.
	// Fence access changes before reading grants, independently of its flag.
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return Event{}, nil, out, err
	}
	var id *int64
	if err := tx.QueryRow(ctx, `SELECT (metadata->>'cause_event_id')::bigint FROM events WHERE tenant_id=$1 AND id=$2`, p.TenantID, parent.ID).Scan(&id); err != nil {
		return Event{}, nil, out, err
	}
	if id == nil {
		return Event{}, nil, out, ErrConflict
	}
	e, err := scanEvent(tx.QueryRow(ctx, `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at,undo_of FROM events WHERE tenant_id=$1 AND id=$2`, p.TenantID, *id))
	if err != nil {
		return Event{}, nil, out, err
	}
	fn := m.causalUndo[e.Type]
	if fn == nil {
		fn = m.undo[e.Type]
	}
	if fn == nil || e.UndoOf != nil {
		return Event{}, nil, out, ErrConflict
	}
	var undone bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE tenant_id=$1 AND undo_of=ANY($2::bigint[]))`, p.TenantID, []int64{parent.ID, e.ID}).Scan(&undone); err != nil {
		return Event{}, nil, out, err
	}
	if undone {
		return Event{}, nil, out, ErrConflict
	}
	if len(e.Before)+len(e.After) > 4<<20 {
		return Event{}, nil, out, ErrConflict
	}
	changes := summarizeNodeChanges(e)
	if len(changes) == 0 || len(changes) > 200 {
		return Event{}, nil, out, ErrConflict
	}
	before, after := causalSnapshots(e)
	out = causalPreview{EventID: parent.ID, CauseEventID: e.ID, ChangeType: e.Type, Nodes: []causalNode{}}
	for _, c := range changes {
		var key, state, slug string
		var rev time.Time
		var project *string
		if err := tx.QueryRow(ctx, `SELECT n.key,n.state,n.updated_at,n.project_id::text,k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND n.id=$2`, p.TenantID, c.ID).Scan(&key, &state, &rev, &project, &slug); err != nil {
			return Event{}, nil, out, err
		}
		if slug != "work" {
			return Event{}, nil, out, ErrConflict
		}
		scope := authz.Scope{}
		if project != nil {
			scope.ProjectID = *project
		}
		if authz.RequireTx(ctx, tx, p, "events.undo", scope) != nil || (parent.ActorPrincipalID != p.ID && authz.RequireTx(ctx, tx, p, "events.undo_other", scope) != nil) || (e.ActorPrincipalID != p.ID && authz.RequireTx(ctx, tx, p, "events.undo_other", scope) != nil) {
			return Event{}, nil, out, ErrForbidden
		}
		expected := stringOf(after[c.ID]["updated_at"])
		expectedState := stringOf(after[c.ID]["state"])
		if expected == nil || expectedState == nil {
			return Event{}, nil, out, ErrConflict
		}
		parsed, err := time.Parse(time.RFC3339Nano, *expected)
		if err != nil || !parsed.Equal(rev) || *expectedState != state {
			return Event{}, nil, out, ErrConflict
		}
		to := stringOf(before[c.ID]["state"])
		target := "removed"
		if to != nil {
			target = *to
		}
		out.Nodes = append(out.Nodes, causalNode{ID: c.ID, Key: key, From: state, To: target})
	}
	out.Message = fmt.Sprintf("Revert the change to %s.", out.Nodes[0].Key)
	switch e.Type {
	case "node.created", "import.node_created":
		out.Message = fmt.Sprintf("Remove the work child %s.", out.Nodes[0].Key)
	case "node.deleted":
		out.Message = fmt.Sprintf("Restore the work child %s.", out.Nodes[0].Key)
	case "node.moved", "node.project_moved":
		out.Message = fmt.Sprintf("Move %s back to its previous parent.", out.Nodes[0].Key)
	}
	if len(out.Nodes) > 1 {
		out.Message = fmt.Sprintf("Revert the change to %d work children.", len(out.Nodes))
	}
	return e, fn, out, nil
}
func causalSnapshots(e Event) (map[string]snapshotObject, map[string]snapshotObject) {
	collect := func(raw json.RawMessage) map[string]snapshotObject {
		out := map[string]snapshotObject{}
		obj := objectOf(raw)
		if e.Type == "node.project_moved" {
			obj = objectOf(obj["node"])
		}
		if e.Type == "node.bulk_changed" {
			var batch struct {
				Items []snapshotObject `json:"items"`
			}
			_ = json.Unmarshal(raw, &batch)
			for _, item := range batch.Items {
				if id := stringOf(item["id"]); id != nil {
					out[*id] = item
				}
			}
		} else if id := stringOf(obj["id"]); id != nil {
			out[*id] = obj
		}
		return out
	}
	return collect(e.Before), collect(e.After)
}
func (m *module) handleUndoPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := parseID(r.PathValue("eventId"))
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_request", "invalid event ID")
		return
	}
	var out causalPreview
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		e, err := scanEvent(tx.QueryRow(r.Context(), `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at,undo_of FROM events WHERE tenant_id=$1 AND id=$2`, p.TenantID, id))
		if err != nil {
			return err
		}
		cause, fn, preview, err := m.causalChange(r.Context(), tx, p, e)
		if err != nil {
			return err
		}
		// Every UndoFunc is transaction-only. Exercise its full mutation guard in
		// a rolled-back savepoint so preview and confirmation use the same rules.
		check, err := tx.Begin(r.Context())
		if err != nil {
			return err
		}
		_, err = fn(r.Context(), check, p, cause)
		rollbackErr := check.Rollback(r.Context())
		if err != nil {
			return err
		}
		if rollbackErr != nil {
			return rollbackErr
		}
		out = preview
		return nil
	})
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// Derivation is the safe public envelope. Cause IDs are included only when
// the original cause event is itself visible to the reader; no hidden child
// identity, count or private metadata crosses this boundary.
type Derivation struct {
	CauseEventID  *int64   `json:"cause_event_id,omitempty"`
	RuleVersion   int      `json:"rule_version"`
	Reason        string   `json:"reason"`
	AffectedNodes []string `json:"affected_nodes"`
}

func attachDerivation(ctx context.Context, tx pgx.Tx, tenantID string, items []Event) error {
	ids := []int64{}
	byID := map[int64]int{}
	for i, e := range items {
		if e.Type == derivedStatusEvent || e.Type == "status_autopilot.retained" || e.Type == "status_autopilot.causal_undo" {
			ids = append(ids, e.ID)
			byID[e.ID] = i
			// Old append-only causal audits stored this private reference in
			// their snapshot. Strip it at read time, without rewriting history.
			if e.Type == "status_autopilot.causal_undo" {
				var after map[string]json.RawMessage
				if err := json.Unmarshal(e.After, &after); err != nil {
					return err
				}
				delete(after, "cause_event_id")
				raw, err := json.Marshal(after)
				if err != nil {
					return err
				}
				items[i].After = raw
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT e.id,coalesce((e.metadata->>'rule_version')::integer,1),coalesce(e.metadata->>'reason',''),
 (SELECT c.id FROM events c WHERE c.tenant_id=e.tenant_id AND c.id=coalesce(e.metadata->>'cause_event_id',
 CASE WHEN e.type='status_autopilot.causal_undo' THEN e.after->>'cause_event_id' END)::bigint)
 FROM events e WHERE e.tenant_id=$1 AND e.id=ANY($2::bigint[])`, tenantID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var d Derivation
		if err := rows.Scan(&id, &d.RuleVersion, &d.Reason, &d.CauseEventID); err != nil {
			return err
		}
		i := byID[id]
		if items[i].NodeID != nil {
			d.AffectedNodes = []string{*items[i].NodeID}
		} else {
			d.AffectedNodes = []string{}
		}
		items[i].Derivation = &d
	}
	return rows.Err()
}
