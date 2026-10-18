// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"github.com/jackc/pgx/v5"
	"testing"
)

func bindOrderToWorkLeaf(t *testing.T, f *fixture, order string) string {
	t.Helper()
	var leaf string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'WORK-1',id,'Work leaf' FROM node_kinds WHERE slug='work' RETURNING id::text`, f.person.TenantID).Scan(&leaf); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, order, leaf)
		return err
	})
	return leaf
}
func addHistoricalWorkChild(t *testing.T, f *fixture, leaf string) {
	t.Helper()
	// Retain a deliberately invalid legacy queue binding to prove that pickup
	// itself rejects it. This bypass exists only in this fixture transaction.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `ALTER TABLE nodes DISABLE TRIGGER nodes_busy_work_child`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,'WORK-2',id,'Historical child',$2 FROM node_kinds WHERE slug='work'`, f.person.TenantID, leaf); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `ALTER TABLE nodes ENABLE TRIGGER nodes_busy_work_child`)
		return err
	})
}
func TestWorkLeafDispatchAndClaimRejectParents(t *testing.T) {
	t.Run("dispatch", func(t *testing.T) {
		f := setup(t)
		order := f.order(t, nil)
		leaf := bindOrderToWorkLeaf(t, f, order.NodeID)
		addHistoricalWorkChild(t, f, leaf)
		var response map[string]any
		f.call(t, f.person, "POST", "/api/work-orders/"+order.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 409, &response)
		if response["code"] != "work_leaf_required" {
			t.Fatalf("wrong dispatch rejection: %v", response)
		}
		if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE work_order_id=$1`, order.NodeID) != 0 {
			t.Fatal("rejected dispatch left a run")
		}
	})
	t.Run("claim", func(t *testing.T) {
		f := setup(t)
		order := f.order(t, nil)
		leaf := bindOrderToWorkLeaf(t, f, order.NodeID)
		run := f.run(t, order)
		reservations := f.reserve(t, run)
		addHistoricalWorkChild(t, f, leaf)
		var response map[string]any
		f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(reservations), 409, &response)
		if response["code"] != "work_leaf_required" {
			t.Fatalf("wrong claim rejection: %v", response)
		}
		if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='queued'`, run.ID) != 1 {
			t.Fatal("rejected claim changed the historic queued run")
		}
	})
}
