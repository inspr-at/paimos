// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"testing"
)

// Risk: a merged Desk question erases another asker's goal association.
func TestGoalLinksSurviveQuestionMerge(t *testing.T) {
	f := newFixture(t)
	goals := []string{uid(), uid(), uid()}
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix) VALUES($1,'goal','Goal','GOAL')`, f.person.TenantID); err != nil {
			return err
		}
		for i, id := range goals {
			parent := f.project
			if i == 2 {
				parent = f.hidden
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,parent_id) SELECT $1,$2,k.id,aeon_next_node_key($1,k.short_prefix),'Goal',$3 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='goal'`, f.person.TenantID, id, parent); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first := input()
	first.GoalID = goals[0]
	first.TicketID = f.ticket
	q := f.ask(t, first)
	second := first
	second.RequestID = uid()
	second.GoalID = goals[1]
	merged := question(t, request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", second), 201)
	if q.ID != merged.ID {
		t.Fatal("goal association prevented otherwise equivalent asks merging")
	}
	third := first
	third.RequestID = uid()
	f.ask(t, third)
	var count int
	err = db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM node_relations WHERE source_node_id=$1 AND type='asks_for'`, f.ticket).Scan(&count)
	})
	if err != nil || count != 2 {
		t.Fatalf("retained/deduped links %d %v", count, err)
	}
	hidden := first
	hidden.RequestID = uid()
	hidden.GoalID = goals[2]
	rec := request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", hidden)
	if rec.Code != 404 {
		t.Fatalf("hidden goal %d %s", rec.Code, rec.Body)
	}
}
