// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"testing"
)

func TestAdoptedProjectRefusesWalkerTicketOptionsAndMembershipWrites(t *testing.T) {
	f := ticketSetup(t)
	ticket := f.existing("ticket", f.project, "Work", "open")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by) VALUES($1,$2,$3)`, f.person.TenantID, f.project, f.person.ID)
		return err
	})
	for _, tc := range []struct{ method, suffix, body string }{{"GET", "/walker", ""}, {"GET", "/ticket-options", ""}, {"PUT", "/plan", `{"expected_revision":1,"ordered_ticket_ids":[],"included_ticket_ids":[]}`}, {"POST", "/membership", `{"expected_revision":1,"ticket_node_ids":["` + ticket + `"]}`}} {
		path := "/api/projects/" + f.project + "/releases/" + f.release + tc.suffix
		w := f.request(f.person, tc.method, path, tc.body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "This project plans with releases") {
			t.Fatalf("%s %s: %d %s", tc.method, path, w.Code, w.Body.String())
		}
	}
}
