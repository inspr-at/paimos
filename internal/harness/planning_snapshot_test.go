// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/nodes"
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

func TestQAAndBlockedSessionRegistrationPreserveWorkStartBaseline(t *testing.T) {
	f := fixture(t)
	nodes.New(f.db.App, nodes.SQLWriter{}).Mount(f.mux)
	path := "/api/nodes/" + f.ticket
	expect(t, f.call(f.person, "PATCH", path, map[string]any{"state": "in_progress", "fields": map[string]any{"estimate_hours": 2}}, ""), 200)
	var first string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, f.ticket).Scan(&first)
	})
	for _, state := range []string{"qa", "blocked", "open", "active", "inprogress", "in_progress"} {
		t.Run(state, func(t *testing.T) {
			expect(t, f.call(f.person, "PATCH", path, map[string]any{"state": state, "fields": map[string]any{"estimate_hours": 4}}, ""), 200)
			// A new generation binds after every transition, as in the real workflow.
			f.registerSession(t, f.agent.ID, "worker", f.ticket, "episode-ref-"+uid(), "episode-lease-"+uid())
			var count, open int
			var id string
			var hours float64
			f.tx(t, f.person, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE closed_at IS NULL),min(id::text),min((snapshot->>'estimate_hours')::float8)
                    FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, f.ticket).Scan(&count, &open, &id, &hours)
			})
			if count != 1 || open != 1 || id != first || hours != 2 {
				t.Fatalf("%s/session changed the work-start baseline: rows=%d open=%d id=%s hours=%v", state, count, open, id, hours)
			}
		})
	}
}
