// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (m *Module) modelReports(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := worker(r.Context(), tx, r, p)
	if err != nil {
		return nil, err
	}
	var in []modelregistry.Observation
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := modelregistry.ReportInSession(r.Context(), tx, p, s.Harness, s.ID, in); err != nil {
		return nil, workorders.Fail(400, "invalid model evidence")
	}
	return map[string]bool{"accepted": true}, nil
}
