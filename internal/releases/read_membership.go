// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

type nativeMembership struct {
	LeafIDs         []string        `json:"leaf_node_ids"`
	TicketID        string          `json:"ticket_node_id"`
	ReleaseID       *string         `json:"release_node_id"`
	ReleaseTitle    *string         `json:"release_title"`
	ReleaseState    *string         `json:"release_state"`
	IsParent        bool            `json:"is_parent"`
	ReleaseCount    int             `json:"release_count"`
	Releases        []nativeRelease `json:"releases"`
	InheritanceNote *string         `json:"inheritance_note"`
}

type nativeRelease struct {
	ReleaseID string `json:"release_node_id"`
	Title     string `json:"release_title"`
	State     string `json:"release_state"`
}

type nativeMemberships struct {
	Tickets []nativeMembership `json:"tickets"`
}

// readMemberships projects the existing journey relation without changing or
// interpreting imported node fields. It is bounded by the displayed ticket IDs
// and also works before a project has initialized its journey.
func (m *module) readMemberships(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := projectPrincipal(w, r, false)
	if !ok {
		return
	}
	ids := r.URL.Query()["ticket_node_id"]
	if len(ids) < 1 || len(ids) > 100 {
		respond(w, nil, fail(400, "1..100 ticket_node_id values required"))
		return
	}
	seen := make(map[string]bool, len(ids))
	for i, id := range ids {
		id = strings.ToLower(id)
		if !uuid.MatchString(id) || seen[id] {
			respond(w, nil, fail(400, "ticket_node_id values must be unique UUIDs"))
			return
		}
		seen[id], ids[i] = true, id
	}
	project := strings.ToLower(r.PathValue("projectId"))
	out := nativeMemberships{Tickets: []nativeMembership{}}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "releases.read", authz.Scope{ProjectID: project}) != nil {
			return fail(403, "project access required")
		}
		var one int
		if err := tx.QueryRow(r.Context(), `SELECT 1 FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project'`, project).Scan(&one); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `WITH scope AS MATERIALIZED (
 SELECT * FROM aeon_work_release_scope($2::uuid[]) WHERE project_id=$1
) SELECT n.id::text,NOT root.is_leaf,n.fields->>'release_inheritance_note',
 ARRAY(SELECT s.id::text FROM scope s WHERE s.root=n.id AND s.is_leaf ORDER BY s.id LIMIT 1001),
 coalesce((SELECT jsonb_agg(to_jsonb(live) ORDER BY live.release_node_id) FROM (
  SELECT DISTINCT r.release_node_id::text,rn.title AS release_title,r.state AS release_state
  FROM scope s JOIN journey_tickets t ON t.ticket_node_id=s.id AND t.tenant_id=n.tenant_id
   AND t.project_node_id=$1
  JOIN journey_releases r ON r.tenant_id=t.tenant_id AND r.release_node_id=t.release_node_id AND r.project_node_id=$1
  JOIN nodes rn ON rn.tenant_id=r.tenant_id AND rn.id=r.release_node_id AND rn.deleted_at IS NULL
  WHERE s.root=n.id AND s.is_leaf
 ) live),'[]'::jsonb)
 FROM unnest($2::uuid[]) WITH ORDINALITY AS requested(id,ordinal)
 JOIN nodes n ON n.id=requested.id AND n.project_id=$1 AND n.deleted_at IS NULL
 JOIN scope root ON root.root=n.id AND root.id=n.id AND root.kind_slug IN ('work','ticket','task')
 ORDER BY requested.ordinal`, project, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item nativeMembership
			var raw []byte
			if err := rows.Scan(&item.TicketID, &item.IsParent, &item.InheritanceNote, &item.LeafIDs, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &item.Releases); err != nil {
				return err
			}
			if len(item.LeafIDs) > 1000 {
				return fail(409, "release summary exceeds 1000 leaves")
			}
			item.ReleaseCount = len(item.Releases)
			if item.ReleaseCount > 1000 {
				return fail(409, "release summary exceeds 1000 releases")
			}
			if item.ReleaseCount == 1 {
				one := item.Releases[0]
				item.ReleaseID = &one.ReleaseID
				item.ReleaseTitle = &one.Title
				item.ReleaseState = &one.State
			}
			out.Tickets = append(out.Tickets, item)
		}
		return rows.Err()
	})
	respond(w, out, err)
}
