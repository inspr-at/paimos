// SPDX-License-Identifier: AGPL-3.0-only
package journey

import (
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestCurrentTicketStatsIgnoreDeletedMembersAndRestoreThem(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, project, release, prior, live, deleted, historical string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('stats','Stats') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	change := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, fn); err != nil {
			t.Fatal(err)
		}
	}
	change(func(tx pgx.Tx) error {
		node := func(kind, key string, parent any, out *string) error {
			return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,$2,$2,$3 FROM node_kinds WHERE tenant_id=$1 AND slug=$4 RETURNING nodes.id::text`, tid, key, parent, kind).Scan(out)
		}
		if err := node("project", "PRJ-1", nil, &project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, tid, project); err != nil {
			return err
		}
		for i, out := range []*string{&prior, &release} {
			key := "REL-1"
			if i == 1 {
				key = "REL-2"
			}
			if err := node("release", key, project, out); err != nil {
				return err
			}
			state := "released"
			if i == 1 {
				state = "planning"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,$4,$5,CASE WHEN $5='released' THEN now() END)`, tid, *out, project, i+1, state); err != nil {
				return err
			}
		}
		for i, out := range []*string{&live, &deleted, &historical} {
			key := []string{"TKT-1", "TKT-2", "TKT-3"}[i]
			if err := node("ticket", key, project, out); err != nil {
				return err
			}
			r := release
			hours := 2
			if i == 2 {
				r = prior
				hours = 3
			}
			if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source,estimated_hours,scope_revision_required,access_change) VALUES($1,$2,$3,$4,0,'manual',$5,$6,$6)`, tid, *out, project, r, hours, i == 1); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=ANY($1::uuid[])`, []string{deleted, historical})
		return err
	})
	check := func(included, plan int, scope, access, missing bool) {
		t.Helper()
		change(func(tx pgx.Tx) error {
			f := facts{ProjectID: project, Release: &releaseFacts{ID: release}}
			if err := loadTicketStats(ctx, tx, &f); err != nil {
				return err
			}
			if f.IncludedTickets != included || f.OpenReleaseTickets != included || f.PlanCents != int64(plan) || f.SpentCents != 300 || f.ScopeRevision != scope || f.AccessChange != access || f.MissingEstimate != missing {
				t.Errorf("stats: included=%d open=%d plan=%d spent=%d scope=%t access=%t missing=%t", f.IncludedTickets, f.OpenReleaseTickets, f.PlanCents, f.SpentCents, f.ScopeRevision, f.AccessChange, f.MissingEstimate)
			}
			return nil
		})
	}
	change(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE journey_tickets SET estimated_hours=NULL WHERE ticket_node_id=$1`, deleted)
		return err
	})
	check(1, 200, false, false, false)
	change(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, deleted)
		return err
	})
	check(2, 200, true, true, true)
	change(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE journey_tickets SET estimated_hours=20 WHERE ticket_node_id=$1`, deleted)
		return err
	})
	check(2, 2200, true, true, false)
	change(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted)
		return err
	})
	check(1, 200, false, false, false)
}
