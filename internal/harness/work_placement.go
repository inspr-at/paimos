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
	if ticket == nil {
		return nil, nil
	}
	if run != nil {
		var raw []byte
		var person, runTicket *string
		// Match the nearest ticket/task used by DispatchPlacement. A session can
		// retain its run after rebinding, but the run's decision belongs only to
		// that ticket, never to the new binding or the person editing it.
		if err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (
 SELECT n.id,n.parent_id,k.slug,0 AS depth FROM agent_runs r
 JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id AND n.deleted_at IS NULL
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE r.id=$1::uuid
 UNION ALL SELECT n.id,n.parent_id,k.slug,up.depth+1 FROM up
 JOIN nodes n ON n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=up.parent_id
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE up.slug NOT IN ('ticket','task') AND up.depth<32 AND n.deleted_at IS NULL)
 SELECT trace,prefs_person_id::text,
 (SELECT id::text FROM up WHERE slug IN ('ticket','task') ORDER BY depth LIMIT 1)
 FROM agent_runs WHERE id=$1::uuid`, *run).Scan(&raw, &person, &runTicket); err != nil {
			return nil, err
		}
		if same(ticket, runTicket) {
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
			// A matching legacy run still belongs to its historical starter,
			// including an operator key with no You slice.
			p = tenant.Principal{}
			placement, err := modelregistry.PlacementFor(ctx, tx, p, modelregistry.WorkQuery{TicketID: *ticket, PersonID: person}, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			return placement.JSON()
		}
	}
	placement, err := modelregistry.PlacementFor(ctx, tx, p, modelregistry.WorkQuery{TicketID: *ticket}, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return placement.JSON()
}
