// SPDX-License-Identifier: AGPL-3.0-only
package journey

import (
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestLiveReleaseFactsExcludeFormerAndDeletedLeavesInBothProjections(t *testing.T) {
	d := dbtest.Open(t)
	ctx := dbtest.Seed(t.Context())
	var tenantID string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('release-leaf-facts','Release leaf facts') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
		node := func(kind, title string, parent any) (string, error) {
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
 SELECT $1,id,aeon_next_node_key($1,short_prefix),$3,$4 FROM node_kinds WHERE tenant_id=$1 AND slug=$2 RETURNING nodes.id::text`, tenantID, kind, title, parent).Scan(&id)
			return id, err
		}
		project, err := node("project", "Project", nil)
		if err != nil {
			return err
		}
		release, err := node("release", "Release", project)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id,current_release_node_id) VALUES($1,$2,$3)`, tenantID, project, release); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,project_node_id,release_node_id,number) VALUES($1,$2,$3,1)`, tenantID, project, release); err != nil {
			return err
		}
		parent, err := node("work", "Former leaf", project)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,project_node_id,ticket_node_id,release_node_id,source,walker_position) VALUES($1,$2,$3,$4,'manual',0)`, tenantID, project, parent, release); err != nil {
			return err
		}
		leaf, err := node("work", "Current leaf", parent)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET state='done' WHERE id=$1`, leaf); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE journey_tickets SET estimated_hours=2,scope_revision_required=false WHERE ticket_node_id=$1`, leaf); err != nil {
			return err
		}
		deleted, err := node("work", "Deleted leaf", parent)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE journey_tickets SET estimated_hours=NULL,scope_revision_required=true,access_change=true WHERE ticket_node_id=$1`, deleted); err != nil {
			return err
		}
		// Old frozen scope can still contain a former leaf. Its estimate, open
		// state and access/scope flags must not affect any live projection.
		if _, err := tx.Exec(ctx, `UPDATE journey_releases SET state='building' WHERE release_node_id=$1`, release); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,project_node_id,ticket_node_id,release_node_id,source,walker_position,estimated_hours,scope_revision_required,access_change)
 VALUES($1,$2,$3,$4,'manual',0,100,true,true) ON CONFLICT(tenant_id,ticket_node_id) DO UPDATE SET estimated_hours=100,scope_revision_required=true,access_change=true`, tenantID, project, parent, release); err != nil {
			return err
		}
		check := func(step string, included, open int, plan int64, flagged, missing bool) error {
			want := facts{ProjectID: project, Release: &releaseFacts{ID: release}}
			if err := loadTicketStats(ctx, tx, &want); err != nil {
				return err
			}
			snapshots, err := loadActionSnapshots(ctx, tx, []string{project})
			if err != nil {
				return err
			}
			if len(snapshots) != 1 {
				t.Fatalf("missing batch snapshot: %+v", snapshots)
			}
			for label, f := range map[string]facts{"journey": want, "next actions": snapshots[0].Facts} {
				if f.IncludedTickets != included || f.OpenReleaseTickets != open || f.PlanCents != plan || f.MissingEstimate != missing || f.ScopeRevision != flagged || f.AccessChange != flagged {
					t.Errorf("%s/%s live membership: included=%d open=%d plan=%d missing=%v scope=%v access=%v; want included=%d open=%d plan=%d flagged=%v missing=%v", step, label, f.IncludedTickets, f.OpenReleaseTickets, f.PlanCents, f.MissingEstimate, f.ScopeRevision, f.AccessChange, included, open, plan, flagged, missing)
				}
			}
			return nil
		}
		if err := check("live missing estimate", 2, 1, 200, true, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted); err != nil {
			return err
		}
		if err := check("deleted", 1, 0, 200, false, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, deleted); err != nil {
			return err
		}
		if err := check("restored missing estimate", 2, 1, 200, true, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE journey_tickets SET estimated_hours=20 WHERE ticket_node_id=$1`, deleted); err != nil {
			return err
		}
		if err := check("restored estimate", 2, 1, 2200, true, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted); err != nil {
			return err
		}
		if err := check("deleted estimate", 1, 0, 200, false, false); err != nil {
			return err
		}
		var retained int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM journey_tickets WHERE ticket_node_id=ANY($1::uuid[])`, []string{parent, deleted}).Scan(&retained); err != nil {
			return err
		}
		if retained != 2 {
			t.Fatal("fixture lost the frozen parent or deleted membership")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
