// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func registrationPlacement(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, run *string) (json.RawMessage, error) {
	if run != nil {
		var raw []byte
		var person *string
		if err := tx.QueryRow(ctx, `SELECT trace,prefs_person_id::text FROM agent_runs WHERE id=$1::uuid`, *run).Scan(&raw, &person); err != nil {
			return nil, err
		}
		var trace struct {
			Placement json.RawMessage `json:"work_placement"`
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &trace); err != nil {
				return nil, err
			}
		}
		if len(trace.Placement) > 0 && string(trace.Placement) != "null" {
			return trace.Placement, nil
		}
		// A legacy run still belongs to its historical starter, including an
		// operator key with no You slice; never use the daemon's key creator.
		p = tenant.Principal{}
		if ticket != nil {
			placement, err := modelregistry.PlacementFor(ctx, tx, p, modelregistry.WorkQuery{TicketID: *ticket, PersonID: person}, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			return placement.JSON()
		}
	}
	if ticket == nil {
		return nil, nil
	}
	placement, err := modelregistry.PlacementFor(ctx, tx, p, modelregistry.WorkQuery{TicketID: *ticket}, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return placement.JSON()
}
