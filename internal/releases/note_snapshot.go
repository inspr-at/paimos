// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/jackc/pgx/v5"
)

// The journey export remains one MVCC statement. Releases-mode selection,
// revision, preflight and pages share the store's read-only repeatable-read
// transaction. Both exports keep tenant and project visibility.
func (m *module) noteSnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	var adopted bool
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	modeErr := db.ReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery WHERE tenant_id=$1 AND project_node_id=$2)`, p.TenantID, r.PathValue("projectId")).Scan(&adopted)
	})
	if modeErr != nil {
		containerResponse(w, nil, modeErr)
		return
	}
	if adopted {
		result, err := m.store.NoteSnapshot(ctx, p, r.PathValue("projectId"), r.PathValue("releaseId"))
		if err != nil {
			containerResponse(w, nil, err)
			return
		}
		if result.Unavailable {
			missing := releasehistory.MissingNotes()
			missing.Gaps = []string{"Not captured before this project adopted releases."}
			containerResponse(w, missing, nil)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Aeon-Notes-Waiting", strconv.Itoa(len(result.Waiting)))
		w.Header().Set("Aeon-Notes-Carried-Forward", strconv.Itoa(len(result.CarriedForward)))
		_, _ = w.Write(result.Raw)
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
