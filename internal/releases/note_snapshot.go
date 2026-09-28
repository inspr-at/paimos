// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"net/http"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/jackc/pgx/v5"
)

// One SQL statement gives membership, revision and fields the same MVCC
// snapshot without blocking editors or depending on multiple paginated reads.
// RLS scopes both the project and tenant; every join also binds tenant_id.
func (m *module) noteSnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	var out releasehistory.NoteSnapshot
	var historical *releasehistory.Notes
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var state string
		var raw []byte
		// The stored row wins. Only an unpublished release falls through to a
		// live preview. A published release with no row was published before
		// snapshots existed: CASE yields NULL and does not read current tickets.
		err := tx.QueryRow(r.Context(), `SELECT r.state, COALESCE(
   (SELECT s.snapshot FROM journey_release_note_snapshots s
     WHERE s.tenant_id=r.tenant_id AND s.release_node_id=r.release_node_id),
   CASE WHEN r.state IN ('released', 'superseded') THEN NULL
        ELSE aeon_release_note_snapshot(r.project_node_id, r.release_node_id) END)
   FROM journey_releases r
   JOIN nodes rn ON rn.tenant_id=r.tenant_id AND rn.id=r.release_node_id AND rn.deleted_at IS NULL
   JOIN nodes pn ON pn.tenant_id=r.tenant_id AND pn.id=r.project_node_id AND pn.deleted_at IS NULL
   WHERE r.tenant_id=$1 AND r.project_node_id=$2 AND r.release_node_id=$3`, p.TenantID, r.PathValue("projectId"), r.PathValue("releaseId")).Scan(&state, &raw)
		if err != nil {
			return err
		}
		if raw == nil {
			if state == "released" || state == "superseded" {
				historical = releasehistory.MissingNotes()
				return nil
			}
			return pgx.ErrNoRows
		}
		return json.Unmarshal(raw, &out)
	})
	if historical != nil {
		respond(w, historical, err)
		return
	}
	respond(w, out, err)
}
