// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestRegistrationAndBindingCaptureOneEstimateBaseline(t *testing.T) {
	f := fixture(t)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields || '{"estimate_hours":2}'::jsonb WHERE id=$1`, f.ticket)
		return err
	})
	base := "/api/projects/" + f.project + "/harness-sessions"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "snapshot-ref-" + uid(), "worker_lease": "snapshot-lease-" + uid(), "ticket_node_id": f.ticket, "work_shape": "ship"}
	expect(t, f.call(f.agent, "POST", base, registration, ""), 201)
	expect(t, f.call(f.agent, "POST", base, registration, ""), 201)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields || '{"estimate_hours":4}'::jsonb WHERE id=$1`, f.ticket)
		return err
	})
	// Another initially unbound generation joins the same work episode.
	path, _, _ := usageSession(t, f, "unmanaged")
	current := decode(t, f.call(f.person, "GET", path, nil, ""))
	expect(t, f.call(f.agent, "PATCH", path+"/binding", map[string]any{"expected_revision": current["revision"], "ticket_node_id": f.ticket, "work_shape": "ship"}, ""), 200)
	var count int
	var hours float64
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*),min((snapshot->>'estimate_hours')::float8) FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, f.ticket).Scan(&count, &hours)
	})
	if count != 1 || hours != 2 {
		t.Fatalf("binding replaced baseline: %d rows, %vh", count, hours)
	}
}
