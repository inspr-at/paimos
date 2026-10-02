// SPDX-License-Identifier: AGPL-3.0-only
package intake

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestSingleDraftDoesNotMaterializeProjectHistory(t *testing.T) {
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	var wanted string
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantA, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO intake_drafts(tenant_id,project_node_id,kind,requirement_kind,title,body,base_event_id,idempotency_key,proposed_by_principal_id)
   VALUES($1,$2,'requirement','functional','wanted','wanted',0,'wanted',$3) RETURNING id::text`, fx.tenantA, fx.projectA, fx.agent.ID).Scan(&wanted); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO intake_drafts(tenant_id,project_node_id,kind,requirement_kind,title,body,base_event_id,idempotency_key,proposed_by_principal_id)
   SELECT $1,$2,'requirement','functional','unrelated',repeat('x',65536),0,'history-'||g,$3 FROM generate_series(1,500) g`, fx.tenantA, fx.projectA, fx.agent.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	work := dbtest.ReadWork{}
	err = db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantA, func(tx pgx.Tx) error {
		got, err := findDraftByID(t.Context(), dbtest.CountReads(tx, &work), fx.projectA, wanted)
		if err == nil && (got.ID != wanted || got.Body != "wanted") {
			t.Fatal("wrong draft")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if work.Rows != 1 || work.Statements != 3 {
		t.Fatalf("single draft materialized unrelated history: %+v", work)
	}
}
