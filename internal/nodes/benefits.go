// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/inspr-at/paimos/internal/ticketbenefits"
	"github.com/jackc/pgx/v5"
)

func noReleaseNeeded(fields json.RawMessage) bool {
	var flags struct {
		NoRelease bool `json:"no_release_needed"`
	}
	_ = json.Unmarshal(fields, &flags)
	return flags.NoRelease
}

// Resolve both states with the same tenant catalog used by parent derivation.
// Call inside the write transaction; category overrides apply to historical
// completion as well as the requested state.
func benefitTransition(ctx context.Context, tx pgx.Tx, kindID, slug, before, after string, priorFields, fields json.RawMessage) ([]string, error) {
	if slug != "ticket" && slug != "work" {
		return nil, nil
	}
	var priorCategory, nextCategory string
	if err := tx.QueryRow(ctx, `SELECT aeon_work_status_category($2,field_schema),aeon_work_status_category($3,field_schema) FROM node_kinds WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND id=$1`, kindID, before, after).Scan(&priorCategory, &nextCategory); err != nil {
		return nil, err
	}
	// Cancellation and archival must remain available for legacy invalid pills,
	// including deferred lifecycle actions. New or changed pills still validate.
	if nextCategory == "cancelled" || nextCategory == "archived" {
		var prior, next map[string]json.RawMessage
		if json.Unmarshal(priorFields, &prior) == nil && json.Unmarshal(fields, &next) == nil &&
			sameJSON(prior["pill_en"], next["pill_en"]) && sameJSON(prior["pill_de"], next["pill_de"]) {
			return nil, nil
		}
	}
	issues := ticketbenefits.Transition(slug, priorCategory, nextCategory, fields)
	if len(issues) > 0 && (ticketbenefits.Completed(priorCategory) || !ticketbenefits.Completed(nextCategory)) {
		return nil, unprocessable(strings.Join(issues, "; "))
	}
	return issues, nil
}
