// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"

	"github.com/inspr-at/paimos/internal/ticketbenefits"
	"github.com/jackc/pgx/v5"
)

// Resolve both states with the same tenant catalog used by parent derivation.
// Call inside the write transaction; category overrides apply to historical
// completion as well as the requested state.
func benefitTransition(ctx context.Context, tx pgx.Tx, kindID, slug, before, after string, fields json.RawMessage) ([]string, error) {
	if slug != "ticket" && slug != "work" {
		return nil, nil
	}
	var priorCategory, nextCategory string
	if err := tx.QueryRow(ctx, `SELECT aeon_work_status_category($2,field_schema),aeon_work_status_category($3,field_schema) FROM node_kinds WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND id=$1`, kindID, before, after).Scan(&priorCategory, &nextCategory); err != nil {
		return nil, err
	}
	return ticketbenefits.Transition(slug, priorCategory, nextCategory, fields), nil
}
