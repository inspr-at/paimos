// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Risk: worker-only ETA writes change displayed fallback values between jobs.
// Both heartbeat and direct coordinator reporting must record A→B→A at commit.
func TestGoalETAHeartbeatAndDirectWriteTransitions(t *testing.T) {
	f := fixture(t)
	goal, outcome := uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, spec := range [][3]string{{"goal", "Goal", "GOAL"}, {"outcome", "Outcome", "OUT"}} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,$2,$3,$4,'goal') ON CONFLICT DO NOTHING`, f.person.TenantID, spec[0], spec[1], spec[2]); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,parent_id) SELECT $1,$2,id,'GOAL-1','Goal',$3 FROM node_kinds WHERE tenant_id=$1 AND slug='goal'`, f.person.TenantID, goal, f.project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,parent_id,fields) SELECT $1,$2,id,'OUT-1','Outcome',$3,'{"outcome_ref":"test"}' FROM node_kinds WHERE tenant_id=$1 AND slug='outcome'`, f.person.TenantID, outcome, goal); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, f.person.TenantID, outcome, f.ticket)
		return err
	})
	lease := "worker-goal-eta-lease-01234567890123456789"
	session := f.registerSession(t, f.agent.ID, "worker", f.ticket, "goal-eta-worker", lease)
	a, b := time.Now().Add(time.Hour).UTC().Truncate(time.Second), time.Now().Add(2*time.Hour).UTC().Truncate(time.Second)
	for i, at := range []time.Time{a, b, a} {
		f.beat(t, session, lease, i+1, map[string]any{"eta_ready_at": at.Format(time.RFC3339)})
	}
	check := func(want []string) {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			rows, err := tx.Query(t.Context(), `SELECT after->>'at' FROM events WHERE node_id=$1 AND type='goal.eta_changed' ORDER BY id`, outcome)
			if err != nil {
				return err
			}
			values := []string{}
			for rows.Next() {
				var value string
				if err := rows.Scan(&value); err != nil {
					return err
				}
				values = append(values, value)
			}
			err = rows.Err()
			rows.Close()
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("ETA transitions %v want %v", values, want)
			}
			return err
		})
	}
	check([]string{a.Format(time.RFC3339), b.Format(time.RFC3339), a.Format(time.RFC3339)})
	coordinatorLease := "coordinator-goal-lease-01234567890123456789"
	f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "goal-eta-coordinator", coordinatorLease)
	for _, at := range []time.Time{b, a} {
		expect(t, f.call(f.agent, "PUT", "/api/nodes/"+f.ticket+"/live-eta", map[string]any{"eta_live_at": at.Format(time.RFC3339)}, coordinatorLease), 200)
	}
	check([]string{a.Format(time.RFC3339), b.Format(time.RFC3339), a.Format(time.RFC3339), b.Format(time.RFC3339), a.Format(time.RFC3339)})
}
