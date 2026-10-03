// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

var deliveryTables = []string{"project_delivery", "project_releases", "ships_in", "project_release_note_snapshots", "delivery_adoption_jobs"}

type deliveryFixture struct {
	*visibilityFixture
	releaseA, releaseB, nextRelease, task string
}

func newDeliveryFixture(t *testing.T, d *dbtest.DB, slug string) *deliveryFixture {
	t.Helper()
	f := &deliveryFixture{visibilityFixture: newVisibilityFixture(t, d, slug)}
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		for _, project := range []string{f.projectA, f.projectB} {
			if _, err := tx.Exec(ctx, `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by) VALUES($1,$2,$3)`, f.tenant, project, f.actor); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO delivery_adoption_jobs(tenant_id,project_node_id,instance_id,rollout_artifact_ref,executing_principal_id,authorizing_principal_id,rollout_authorization_ref)
				VALUES($1,$2,'test-instance','artifact:immutable',$3,$3,'authority:local')`, f.tenant, project, f.actor); err != nil {
				return err
			}
		}
		f.releaseA = insertNode(ctx, t, tx, f.visibilityFixture, "release", "RA-1", &f.projectA)
		f.nextRelease = insertNode(ctx, t, tx, f.visibilityFixture, "release", "RA-2", &f.projectA)
		f.releaseB = insertNode(ctx, t, tx, f.visibilityFixture, "release", "RB-1", &f.projectB)
		f.task = insertNode(ctx, t, tx, f.visibilityFixture, "task", "TSK-1", &f.projectA)
		for _, r := range []struct {
			id, project, rank string
			sequence          int
		}{
			{f.releaseA, f.projectA, "V", 1}, {f.nextRelease, f.projectA, "W", 2}, {f.releaseB, f.projectB, "V", 1},
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,release_node_id,project_node_id,sequence,rank) VALUES($1,$2,$3,$4,$5)`, f.tenant, r.id, r.project, r.sequence, r.rank); err != nil {
				return err
			}
		}
		for _, s := range []struct{ id, project, release string }{{f.ticketA, f.projectA, f.releaseA}, {f.ticketB, f.projectB, f.releaseB}} {
			if _, err := tx.Exec(ctx, `INSERT INTO ships_in(tenant_id,item_node_id,project_node_id,release_node_id,rank,source,placed_by) VALUES($1,$2,$3,$4,'V','person',$5)`, f.tenant, s.id, s.project, s.release, f.actor); err != nil {
				return err
			}
		}
		return nil
	})
	return f
}

func (f *deliveryFixture) run(t *testing.T, fn func(context.Context, pgx.Tx) error) {
	t.Helper()
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error { return fn(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}

func (f *deliveryFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	f.run(t, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, args...); return err })
}

// Each failure rolls back its own savepoint, leaving the asserted fixture intact.
func deliveryReject(t *testing.T, ctx context.Context, tx pgx.Tx, code, message, sql string, args ...any) {
	t.Helper()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sp.Exec(ctx, sql, args...)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != code || !strings.Contains(pgerr.Message, message) {
		t.Fatalf("wanted %s containing %q, got %v", code, message, err)
	}
	if err := sp.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryMigrationPreservesJourneyAndLegacyWrites(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var f *visibilityFixture
	var release, before string
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1150_project_delivery.sql" {
			return nil
		}
		f = newVisibilityFixture(t, d, "delivery-legacy")
		ctx := dbtest.Seed(t.Context())
		return db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
			release = insertNode(ctx, t, tx, f, "release", "LEG-1", &f.projectA)
			if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, f.tenant, f.projectA); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,project_node_id,release_node_id,number) VALUES($1,$2,$3,1)`, f.tenant, f.projectA, release); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,project_node_id,ticket_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,7,'manual')`, f.tenant, f.projectA, f.ticketA, release); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT jsonb_build_object('project',to_jsonb(p),'release',to_jsonb(r),'member',to_jsonb(m),'node',to_jsonb(n))::text
				FROM journey_projects p JOIN journey_releases r USING(tenant_id,project_node_id)
				JOIN journey_tickets m USING(tenant_id,project_node_id) JOIN nodes n ON n.tenant_id=m.tenant_id AND n.id=m.ticket_node_id`).Scan(&before)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("pre-expansion fixture hook did not run")
	}
	// The real runner skips every recorded expansion on a subsequent open.
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error { return fmt.Errorf("unexpected reapplied migration %s", name) }); err != nil {
		t.Fatal(err)
	}
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
		var after string
		if err := tx.QueryRow(ctx, `SELECT jsonb_build_object('project',to_jsonb(p),'release',to_jsonb(r),'member',to_jsonb(m),'node',to_jsonb(n))::text
			FROM journey_projects p JOIN journey_releases r USING(tenant_id,project_node_id)
			JOIN journey_tickets m USING(tenant_id,project_node_id) JOIN nodes n ON n.tenant_id=m.tenant_id AND n.id=m.ticket_node_id`).Scan(&after); err != nil {
			return err
		}
		if after != before {
			t.Fatal("expansion changed legacy rows")
		}
		for _, table := range deliveryTables {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("migration populated %s", table)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE journey_tickets SET walker_position=8 WHERE ticket_node_id=$1`, f.ticketA); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET title='Legacy writer still works' WHERE id=$1`, f.ticketA); err != nil {
			return err
		}
		var position int
		var title string
		if err := tx.QueryRow(ctx, `SELECT m.walker_position,n.title FROM journey_tickets m JOIN nodes n ON n.tenant_id=m.tenant_id AND n.id=m.ticket_node_id WHERE m.ticket_node_id=$1`, f.ticketA).Scan(&position, &title); err != nil {
			return err
		}
		if position != 8 || title != "Legacy writer still works" {
			t.Fatal("legacy write lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryTenantAndProjectPolicies(t *testing.T) {
	d := dbtest.Open(t)
	a := newDeliveryFixture(t, d, "delivery-tenant-a")
	b := newDeliveryFixture(t, d, "delivery-tenant-b")
	// Include snapshots in real data isolation checks, not just policy metadata.
	for _, f := range []*deliveryFixture{a, b} {
		f.run(t, func(ctx context.Context, tx pgx.Tx) error {
			for _, r := range []struct{ project, release string }{{f.projectA, f.releaseA}, {f.projectB, f.releaseB}} {
				if _, err := tx.Exec(ctx, `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot)
					VALUES($1::uuid,$2::uuid,$3::uuid,'1.0.0',jsonb_build_object('schema','aeon.release-note-snapshot.v1','tenant_id',$1::text,'project_node_id',$2::text,'release_node_id',$3::text,'version','1.0.0','membership_source','ships_in.release_node_id','field_source','nodes.fields','frozen',true,'tickets','[]'::jsonb))`, f.tenant, r.project, r.release); err != nil {
					return err
				}
			}
			return nil
		})
	}
	ctx := t.Context()
	if err := db.InTenant(db.OnlyProjects(ctx, a.projectA), d.App, a.tenant, func(tx pgx.Tx) error {
		for _, table := range deliveryTables {
			var own, foreign, hidden int
			q := `SELECT count(*) FILTER(WHERE tenant_id=$1 AND project_node_id=$2),count(*) FILTER(WHERE tenant_id=$3),count(*) FILTER(WHERE project_node_id=$4) FROM ` + pgx.Identifier{table}.Sanitize()
			if err := tx.QueryRow(ctx, q, a.tenant, a.projectA, b.tenant, a.projectB).Scan(&own, &foreign, &hidden); err != nil {
				return err
			}
			want := 1
			if table == "project_releases" {
				want = 2
			}
			if own != want || foreign != 0 || hidden != 0 {
				t.Fatalf("%s counts %d/%d/%d", table, own, foreign, hidden)
			}
			var policies int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_policy WHERE polrelid=$1::regclass AND NOT polpermissive AND polqual IS NOT NULL AND polwithcheck IS NOT NULL`, table).Scan(&policies); err != nil {
				return err
			}
			if policies != 1 {
				t.Fatalf("missing restrictive read/write policy: %s", table)
			}
			var forced bool
			if err := tx.QueryRow(ctx, `SELECT relrowsecurity AND relforcerowsecurity FROM pg_class WHERE oid=$1::regclass`, table).Scan(&forced); err != nil {
				return err
			}
			if !forced {
				t.Fatalf("RLS not forced: %s", table)
			}
		}
		deliveryReject(t, ctx, tx, "42501", "row-level security", `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by) VALUES($1,$2,$3)`, b.tenant, b.projectA, b.actor)
		deliveryReject(t, ctx, tx, "42501", "row-level security", `INSERT INTO delivery_adoption_jobs(tenant_id,project_node_id,instance_id,rollout_artifact_ref,executing_principal_id,authorizing_principal_id,rollout_authorization_ref) VALUES($1,$2,'other','artifact',$3,$3,'auth')`, a.tenant, a.projectB, a.actor)
		tag, err := tx.Exec(ctx, `UPDATE delivery_adoption_jobs SET reason_message='hidden edit' WHERE project_node_id=$1`, a.projectB)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("hidden job updated")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, d.App, a.tenant, func(tx pgx.Tx) error {
		for _, table := range deliveryTables {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatalf("unset visibility exposes %s", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reverse tenant direction also sees only that tenant's data.
	if err := db.InTenant(db.OnlyProjects(ctx, b.projectB), d.App, b.tenant, func(tx pgx.Tx) error {
		for _, table := range deliveryTables {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Fatalf("reverse isolation %s: %d", table, n)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryReleaseGuardAndBounds(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-g1")
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		deliveryReject(t, ctx, tx, "P0001", "live release node", `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank) VALUES($1,$2,$3,3,'X')`, f.tenant, f.projectA, f.ticketA)
		deliveryReject(t, ctx, tx, "P0001", "live release node", `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank) VALUES($1,$2,$3,3,'X')`, f.tenant, f.projectA, f.releaseB)
		deliveryReject(t, ctx, tx, "P0001", "numbered order", `UPDATE project_releases SET rank='U' WHERE release_node_id=$1`, f.nextRelease)
		deliveryReject(t, ctx, tx, "P0001", "sequence is immutable", `UPDATE project_releases SET sequence=3 WHERE release_node_id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "P0001", "planned internal release", `UPDATE project_releases SET visibility='internal',sequence=NULL WHERE release_node_id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "P0001", "identity and origin", `UPDATE project_releases SET origin='adopted_released' WHERE release_node_id=$1`, f.releaseA)
		for _, rank := range []string{"", "0", "V0", strings.Repeat("A", 33), "a-"} {
			deliveryReject(t, ctx, tx, "23514", "project_releases_rank_check", `UPDATE project_releases SET rank=$2 WHERE release_node_id=$1`, f.nextRelease, rank)
		}
		for _, tc := range []struct{ assignment, check string }{
			{"revision=0", "project_releases_revision_check"},
			{"creation_key=''", "project_releases_creation_key_check"},
			{"creation_key=repeat('a',129)", "project_releases_creation_key_check"},
			{"build_settings=jsonb_build_object('window',repeat('a',2049))", "project_releases_build_settings_check"},
			{"build_authorized_by='" + f.actor + "',build_authorized_at=now()", "project_releases_build_authorization_state"},
		} {
			deliveryReject(t, ctx, tx, "23514", tc.check, `UPDATE project_releases SET `+tc.assignment+` WHERE release_node_id=$1`, f.releaseA)
		}
		// Reserved v2 origins/sources are accepted by the database, not written by v1 stores.
		internal := insertNode(ctx, t, tx, f.visibilityFixture, "release", "INT-1", &f.projectA)
		if _, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,visibility,rank,origin,creation_key) VALUES($1,$2,$3,'internal','VV','backfill','same-key')`, f.tenant, f.projectA, internal); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "P0001", "numbered order", `UPDATE project_releases SET visibility='published',sequence=3 WHERE release_node_id=$1`, internal)
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET visibility='published',sequence=3,rank='X' WHERE release_node_id=$1`, internal); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "23505", "creation_key", `UPDATE project_releases SET creation_key='same-key' WHERE release_node_id=$1`, f.releaseA)
		// Invalid newly inserted order is guarded too, including abandoned rows.
		bad := insertNode(ctx, t, tx, f.visibilityFixture, "release", "BAD-1", &f.projectA)
		deliveryReject(t, ctx, tx, "P0001", "numbered order", `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank,state,abandoned_at) VALUES($1,$2,$3,4,'T','abandoned',now())`, f.tenant, f.projectA, bad)
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, bad); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "P0001", "live release node", `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank) VALUES($1,$2,$3,4,'Y')`, f.tenant, f.projectA, bad)
		legacy := insertNode(ctx, t, tx, f.visibilityFixture, "release", "HIST-1", &f.projectA)
		if _, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank,state,released_at,origin) VALUES($1,$2,$3,4,'Y','released',now(),'adopted_released')`, f.tenant, f.projectA, legacy); err != nil {
			return err
		}
		planning := insertNode(ctx, t, tx, f.visibilityFixture, "release", "HIST-2", &f.projectA)
		// Only already-released adoption history is exempt from cut/publish proof.
		deliveryReject(t, ctx, tx, "23514", "project_releases_publication_proof", `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank,state,released_at,origin) VALUES($1,$2,$3,5,'Z','released',now(),'adopted_planned')`, f.tenant, f.projectA, planning)
		return nil
	})
}

func TestDeliveryReleaseTransitionGraph(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-transitions")
	edges := map[string][]string{"planned": {"building", "frozen", "abandoned"}, "building": {"planned", "frozen", "abandoned"}, "frozen": {"building", "released", "abandoned"}, "released": {}, "abandoned": {}}
	for _, old := range []string{"planned", "building", "frozen", "released", "abandoned"} {
		for _, next := range []string{"planned", "building", "frozen", "released", "abandoned"} {
			t.Run(old+"-"+next, func(t *testing.T) {
				ctx := dbtest.Seed(t.Context())
				err := db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error {
					id := insertNode(ctx, t, tx, f.visibilityFixture, "release", "GRAPH-1", &f.projectA)
					if _, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,visibility,rank,state,released_at,abandoned_at)
						VALUES($1,$2,$3,'internal','Y',$4,CASE WHEN $4='released' THEN now() END,CASE WHEN $4='abandoned' THEN now() END)`, f.tenant, f.projectA, id, old); err != nil {
						return err
					}
					allowed := old == next
					for _, x := range edges[old] {
						allowed = allowed || next == x
					}
					q := `UPDATE project_releases SET state=$2,released_at=CASE WHEN $2='released' THEN now() END,abandoned_at=CASE WHEN $2='abandoned' THEN now() END WHERE release_node_id=$1`
					if allowed {
						if _, err := tx.Exec(ctx, q, id, next); err != nil {
							return err
						}
					} else {
						deliveryReject(t, ctx, tx, "P0001", "state transition", q, id, next)
					}
					// Roll back this scenario, including its node/key and internal rank.
					return errDeliveryScenarioRollback
				})
				if !errors.Is(err, errDeliveryScenarioRollback) {
					t.Fatalf("scenario failed before rollback: %v", err)
				}
			})
		}
	}
}

var errDeliveryScenarioRollback = errors.New("delivery scenario rollback")

func TestDeliveryCutRequiresTimestamp(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-cut-pair")
	f.exec(t, `UPDATE project_releases SET state='frozen' WHERE release_node_id=$1`, f.releaseA)
	t.Run("update", func(t *testing.T) {
		f.run(t, func(ctx context.Context, tx pgx.Tx) error {
			deliveryReject(t, ctx, tx, "23514", "project_releases_version_cut_pair", `UPDATE project_releases SET version='1.0.0',version_scheme='legacy' WHERE release_node_id=$1`, f.releaseA)
			return nil
		})
	})
	for i, origin := range []string{"planned", "adopted_planned", "backfill"} {
		t.Run("insert/"+origin, func(t *testing.T) {
			f.run(t, func(ctx context.Context, tx pgx.Tx) error {
				id := insertNode(ctx, t, tx, f.visibilityFixture, "release", fmt.Sprintf("CUT-%d", i+1), &f.projectA)
				deliveryReject(t, ctx, tx, "23514", "project_releases_version_cut_pair", `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank,state,origin,version,version_scheme)
					VALUES($1,$2,$3,3,'X','frozen',$4,'1.0.0','legacy')`, f.tenant, f.projectA, id, origin)
				return nil
			})
		})
	}
}

func TestDeliveryAdoptedReleaseMayLackCutTimestamp(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-adopted-cut")
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		id := insertNode(ctx, t, tx, f.visibilityFixture, "release", "HIST-1", &f.projectA)
		_, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank,state,released_at,origin,version,version_scheme)
			VALUES($1,$2,$3,3,'X','released',now(),'adopted_released','1.0.0','legacy')`, f.tenant, f.projectA, id)
		return err
	})
}

func TestDeliveryCutDeadlineAndBuildAuthorization(t *testing.T) {
	f := newDeliveryFixture(t, dbtest.Open(t), "delivery-cut")
	f.exec(t, `UPDATE project_releases SET entry_closes_at=now() WHERE release_node_id=$1`, f.releaseA)
	f.exec(t, `UPDATE project_releases SET state='building' WHERE release_node_id=$1`, f.releaseA)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		deliveryReject(t, ctx, tx, "23514", "project_releases_build_authorization_pair", `UPDATE project_releases SET build_authorized_by=$2 WHERE release_node_id=$1`, f.releaseA, f.actor)
		deliveryReject(t, ctx, tx, "23514", "project_releases_build_authorization_pair", `UPDATE project_releases SET build_authorized_at=now() WHERE release_node_id=$1`, f.releaseA)
		return nil
	})
	f.exec(t, `UPDATE project_releases SET build_authorized_by=$2,build_authorized_at=now() WHERE release_node_id=$1`, f.releaseA, f.actor)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		deliveryReject(t, ctx, tx, "P0001", "entry deadline", `UPDATE project_releases SET entry_closes_at=NULL WHERE release_node_id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "23514", "project_releases_build_authorization_state", `UPDATE project_releases SET state='frozen' WHERE release_node_id=$1`, f.releaseA)
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET state='frozen',build_authorized_by=NULL,build_authorized_at=NULL WHERE release_node_id=$1`, f.releaseA); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "23514", "project_releases_publication_proof", `UPDATE project_releases SET state='released',released_at=now() WHERE release_node_id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "23514", "project_releases_version_pair", `UPDATE project_releases SET version_scheme='legacy' WHERE release_node_id=$1`, f.releaseA)
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET version='1.0.0+old',version_scheme='legacy',cut_at=clock_timestamp() WHERE release_node_id=$1`, f.releaseA); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "P0001", "state transition", `UPDATE project_releases SET state='building' WHERE release_node_id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "P0001", "cut version and scheme", `UPDATE project_releases SET version_scheme='inspr-calendar-v1' WHERE release_node_id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "P0001", "cut version and scheme", `UPDATE project_releases SET cut_at=cut_at+interval '1 second' WHERE release_node_id=$1`, f.releaseA)
		deliveryReject(t, ctx, tx, "P0001", "cut version and scheme", `UPDATE project_releases SET version=NULL,version_scheme=NULL,cut_at=NULL WHERE release_node_id=$1`, f.releaseA)
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET state='released',released_at=clock_timestamp(),reservation_basis='attested',reservation_ref='person assertion',released_by=$2 WHERE release_node_id=$1`, f.releaseA, f.actor); err != nil {
			return err
		}
		// Published versions remain reserved across release states.
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET state='frozen' WHERE release_node_id=$1`, f.nextRelease); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "23505", "project_releases_version_idx", `UPDATE project_releases SET version='1.0.0+old',version_scheme='legacy',cut_at=now() WHERE release_node_id=$1`, f.nextRelease)
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET version='2.0.0',version_scheme='legacy',cut_at=now() WHERE release_node_id=$1`, f.nextRelease); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE project_releases SET state='abandoned',abandoned_at=now() WHERE release_node_id=$1`, f.nextRelease); err != nil {
			return err
		}
		third := insertNode(ctx, t, tx, f.visibilityFixture, "release", "CUT-3", &f.projectA)
		if _, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank,state) VALUES($1,$2,$3,3,'X','frozen')`, f.tenant, f.projectA, third); err != nil {
			return err
		}
		deliveryReject(t, ctx, tx, "23505", "project_releases_version_idx", `UPDATE project_releases SET version='2.0.0',version_scheme='legacy',cut_at=now() WHERE release_node_id=$1`, third)
		return nil
	})
}
