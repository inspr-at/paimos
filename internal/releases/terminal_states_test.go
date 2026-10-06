// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestWalkerClosedStatesDoNotCountAsOpen(t *testing.T) {
	f := ticketSetup(t)
	for i, state := range []string{"accepted", "delivered", "done", "cancelled", "canceled", "archived", "closed", "open", "in_progress", "custom-active"} {
		for _, included := range []bool{false, true} {
			id := f.existing("ticket", f.feature, fmt.Sprint(state, included), state)
			f.tx(func(tx pgx.Tx) error {
				var release any
				if included {
					release = f.release
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,feature_node_id,walker_position,source) VALUES($1,$2,$3,$4,$5,$6,'manual')`, f.person.TenantID, id, f.project, release, f.feature, i)
				return err
			})
		}
	}
	w := f.request(f.person, "GET", "/api/projects/"+f.project+"/releases/"+f.release+"/walker", "")
	if w.Code != 200 {
		t.Fatalf("walker %d: %s", w.Code, w.Body.String())
	}
	var out Walker
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Features) != 1 || out.Features[0].OpenCount != 6 || out.Features[0].IncludedCount != 3 || out.Features[0].Selection != "some" {
		t.Fatalf("terminal states inflated counts: %+v", out.Features)
	}
}
