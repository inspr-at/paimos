// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// resolveSessionBinding maps a harness-native session reference to the caller's
// single active generation. The reference is hashed with digest("ref") and is
// never stored or returned. Zero or several matches are the same 404.
func (m *Module) resolveSessionBinding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
		httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in struct {
		Ref string `json:"harness_session_ref"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		workorders.WriteError(w, err)
		return
	}
	if len(in.Ref) < 16 || len(in.Ref) > 4096 || strings.ContainsAny(in.Ref, "\r\n") {
		workorders.WriteError(w, workorders.Fail(400, "invalid harness session reference"))
		return
	}
	var ids []string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id::text FROM harness_sessions WHERE agent_principal_id=$1 AND stopped_at IS NULL AND archived_at IS NULL AND (ref_digest=$2 OR vendor_ref_digest=$2) LIMIT 2`, p.ID, digest("ref", in.Ref))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		workorders.WriteError(w, err)
		return
	}
	if len(ids) != 1 || !workorders.UUID(ids[0]) {
		httpapi.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, struct {
		SessionID string `json:"session_id"`
	}{ids[0]})
}
