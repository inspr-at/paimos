// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"net/http"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type watchSecurity struct {
	ConsentMode string `json:"consent_mode"`
}

func watchConsentMode(ctx context.Context, tx pgx.Tx, owner string) (string, error) {
	var mode string
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT consent_mode FROM person_watch_security WHERE person_id=$1),'aeon')`, owner).Scan(&mode)
	return mode, err
}
func (m *Module) watchSecurity(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var out watchSecurity
	if r.Method == "PUT" && (decode(w, r, &out) != nil || !attachwatch.ConsentModeValid(out.ConsentMode)) {
		WriteError(w, fail(400, "invalid_request", "choose aeon or local_auth consent"))
		return
	}
	// Same transaction lock as approval: a setting change cannot race the pin.
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if r.Method == "PUT" {
			_, err := tx.Exec(r.Context(), `INSERT INTO person_watch_security(tenant_id,person_id,consent_mode) VALUES($1,$2,$3) ON CONFLICT(tenant_id,person_id) DO UPDATE SET consent_mode=excluded.consent_mode`, p.TenantID, p.ID, out.ConsentMode)
			return err
		}
		var err error
		out.ConsentMode, err = watchConsentMode(r.Context(), tx, p.ID)
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}
