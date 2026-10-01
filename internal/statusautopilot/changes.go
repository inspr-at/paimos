// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Change struct {
	EventID      int64     `json:"event_id"`
	NodeID       string    `json:"node_id"`
	Key          string    `json:"key"`
	Title        string    `json:"title"`
	Actor        string    `json:"actor"`
	Rule         string    `json:"rule"`
	Reason       string    `json:"reason"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	At           time.Time `json:"at"`
	Undone       bool      `json:"undone"`
	ChangedSince bool      `json:"changed_since"`
	Undoable     bool      `json:"undoable"`
}

// ChangesTx reads only events and tickets already visible under the caller's
// RLS context. NodeID narrows a ticket's Activity; zero limit returns its full
// reversible event map (Activity's own cursor decides which rows to show).
// Suggestions reads all current marks, independently of that history limit.
func ChangesTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID string, suggestions bool, limit int) ([]Change, error) {
	from := `FROM events e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id
 WHERE e.type=$1 AND n.deleted_at IS NULL AND ($2='' OR n.id=nullif($2,'')::uuid)
 ORDER BY e.at DESC,e.id DESC LIMIT nullif($3,0)`
	if suggestions {
		limit = 0
		// Start from live ticket marks, not the latest 50 audit rows. A mark remains
		// listed after unrelated edits, until status changes or a guarded Undo clears it.
		from = `FROM nodes n CROSS JOIN LATERAL jsonb_each(n.status_autopilot) mark
 JOIN LATERAL (SELECT e.* FROM events e WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id
 AND e.type=$1 AND e.metadata->>'flag'=mark.key AND e.after->>'state'=n.state
 AND NOT EXISTS(SELECT 1 FROM events u WHERE u.tenant_id=e.tenant_id AND u.undo_of=e.id)
 ORDER BY e.at DESC,e.id DESC LIMIT 1) e ON true
 WHERE n.deleted_at IS NULL AND n.status_autopilot<>'{}'::jsonb AND ($2='' OR n.id=nullif($2,'')::uuid)
 AND mark.value='true'::jsonb AND mark.key IN ('triage_list','cancel_suggested','blocked_reminder','missed_release')
 ORDER BY mark.key,n.key,n.id LIMIT nullif($3,0)`
	}
	rows, err := tx.Query(ctx, `SELECT e.id,n.id::text,n.key,n.title,e.metadata->>'rule',e.metadata->>'reason',e.before->>'state',
 coalesce(nullif(e.metadata->>'flag',''),e.after->>'state'),e.at,
 EXISTS(SELECT 1 FROM events u WHERE u.tenant_id=e.tenant_id AND u.undo_of=e.id),
 n.updated_at=(e.after->>'updated_at')::timestamptz AND n.state=e.after->>'state',n.project_id::text
 `+from, Changed, nodeID, limit)
	if err != nil {
		return nil, err
	}
	out := []Change{}
	type scopeRow struct {
		project *string
		matches bool
	}
	scopes := []scopeRow{}
	for rows.Next() {
		var c Change
		var s scopeRow
		c.Actor = "Status autopilot"
		if err = rows.Scan(&c.EventID, &c.NodeID, &c.Key, &c.Title, &c.Rule, &c.Reason, &c.From, &c.To, &c.At, &c.Undone, &s.matches, &s.project); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
		scopes = append(scopes, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i, s := range scopes {
		out[i].ChangedSince = !s.matches
		if !s.matches || out[i].Undone {
			continue
		}
		scope := authz.Scope{}
		if s.project != nil {
			scope.ProjectID = *s.project
		}
		out[i].Undoable = authz.RequireTx(ctx, tx, p, "events.undo_other", scope) == nil && authz.RequireTx(ctx, tx, p, "nodes.write", scope) == nil
	}
	return out, nil
}
func (m *Module) changes(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("node_id")
	if id != "" && !uuid.MatchString(id) {
		httpapi.WriteError(w, 400, "invalid node id")
		return
	}
	var out []Change
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = ChangesTx(r.Context(), tx, p, id, r.URL.Query().Get("suggestions") == "true", 50)
		return err
	})
	respond(w, struct {
		Items []Change `json:"items"`
	}{out}, err)
}
