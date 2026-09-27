// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

type nativeMembership struct {
	TicketID     string  `json:"ticket_node_id"`
	ReleaseID    *string `json:"release_node_id"`
	ReleaseTitle *string `json:"release_title"`
	ReleaseState *string `json:"release_state"`
}

type nativeMemberships struct {
	Tickets []nativeMembership `json:"tickets"`
}

// readMemberships projects the existing journey relation without changing or
// interpreting imported node fields. It is bounded by the displayed ticket IDs
// and also works before a project has initialized its journey.
func (m *module) readMemberships(w http.ResponseWriter, r *http.Request) {
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
		rows, err := tx.Query(r.Context(), `SELECT n.id::text,live.release_node_id::text,live.title,live.state
 FROM unnest($2::uuid[]) WITH ORDINALITY AS requested(id,ordinal)
 JOIN nodes n ON n.id=requested.id AND n.project_id=$1 AND n.deleted_at IS NULL
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug IN ('ticket','task')
 LEFT JOIN journey_tickets t ON t.tenant_id=n.tenant_id AND t.project_node_id=n.project_id AND t.ticket_node_id=n.id
 LEFT JOIN (
   SELECT r.tenant_id,r.project_node_id,r.release_node_id,r.state,rn.title
   FROM journey_releases r JOIN nodes rn ON rn.tenant_id=r.tenant_id AND rn.id=r.release_node_id AND rn.deleted_at IS NULL
 ) live ON live.tenant_id=t.tenant_id AND live.project_node_id=t.project_node_id AND live.release_node_id=t.release_node_id
 ORDER BY requested.ordinal`, project, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item nativeMembership
			if err := rows.Scan(&item.TicketID, &item.ReleaseID, &item.ReleaseTitle, &item.ReleaseState); err != nil {
				return err
			}
			out.Tickets = append(out.Tickets, item)
		}
		return rows.Err()
	})
	respond(w, out, err)
}
