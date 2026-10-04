// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

func TestWorkLeafRegistrationBindingAndGracefulDeadline(t *testing.T) {
	f := fixtureWithKind(t, nil, "work")
	base := "/api/projects/" + f.project + "/harness-sessions"
	reg := pauseRegistration(f)
	reg["advertised_capabilities"] = []string{"inbox", "pause"}
	reg["worker_lease"] = "work-leaf-lease-00000000000000000001"
	reply := f.call(f.agent, "POST", base, reg, "")
	expect(t, reply, 201)
	session := decode(t, reply)["id"].(string)
	// Deadline expiry is deterministic fixture data. It may expire the request;
	// it cannot promote this lifecycle handover to interrupt or process stop.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
			return err
		}
		flush, err := harness.PrepareWorkHandover(t.Context(), tx, f.person, []string{f.ticket}, "deadline-test")
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET pause_record=jsonb_set(pause_record,'{deadline_at}','"2000-01-01T00:00:00Z"'::jsonb) WHERE id=$1`, session); err != nil {
			return err
		}
		return flush()
	})
	if err != nil {
		t.Fatal(err)
	}
	reply = f.call(f.agent, "POST", base+"/"+session+"/heartbeat", map[string]any{"activity_sequence": 1, "phase": "working", "activity": "busy"}, "work-leaf-lease-00000000000000000001")
	expect(t, reply, 200)
	var signals int
	var stopped bool
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_controls WHERE session_id=$1 AND (kind='interrupt' OR request_payload->>'stop_now'='true')`, session).Scan(&signals); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT stopped_at IS NOT NULL FROM harness_sessions WHERE id=$1`, session).Scan(&stopped)
	})
	if signals != 0 || stopped {
		t.Fatal("expired handover gained force-stop authority")
	}
	// Stop the generation without rebinding history, then add a work child.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=$2,stop_reason='completed' WHERE id=$1`, session, time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
		return err
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,'WORK-3',id,'Child',$2 FROM node_kinds WHERE slug='work'`, f.person.TenantID, f.ticket)
		return err
	})
	reg["harness_session_ref"] = "work-leaf-reference-0002"
	reg["worker_lease"] = "work-leaf-lease-00000000000000000002"
	reply = f.call(f.agent, "POST", base, reg, "")
	expect(t, reply, 409)
	if decode(t, reply)["code"] != "work_leaf_required" {
		t.Fatal("registration failed for the wrong reason")
	}
	var original string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT ticket_node_id::text FROM harness_sessions WHERE id=$1`, session).Scan(&original)
	})
	if original != f.ticket {
		t.Fatal("historical session identity changed")
	}
}
