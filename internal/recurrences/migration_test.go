// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestUIRetirementMigrationBackfillsUnderTenantRLS(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	// Equal recurrence IDs in different tenants prove the backfill cannot borrow
	// a deletion event from another tenant. A merely paused row must stay live.
	const sharedID = "10000000-0000-4000-8000-000000000099"
	const pausedID = "10000000-0000-4000-8000-000000000088"
	fixtures := []*fixture{}
	tags := []string{}
	var deletionAt time.Time
	var deletionSnapshot []byte
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1122_recurrence_event_visibility.sql" {
			return nil
		}
		for i := 0; i < 2; i++ {
			f := &fixture{t: t, d: d, now: timestamp(t, "2026-10-02T12:00:00Z"), p: tenant.Principal{Kind: tenant.Person, Name: "Owner"}}
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, fmt.Sprintf("recurrence-upgrade-%d", i)).Scan(&f.p.TenantID); err != nil {
				return err
			}
			f.tx(func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, f.p.TenantID).Scan(&f.p.ID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'tag','Tag','TAG','tag') ON CONFLICT DO NOTHING`, f.p.TenantID)
				return err
			})
			dbtest.BindRole(t, d, f.p.TenantID, f.p.ID, "owner")
			f.project = f.node("project", nil, "Project")
			f.parent = f.node("epic", &f.project, "Code health")
			tag := f.node("tag", nil, "Workspace health")
			in := f.input()
			in.Template.Tags = []string{tag}
			if err := in.normalize(f.now); err != nil {
				return err
			}
			template, _ := json.Marshal(in.Template)
			trigger, _ := json.Marshal(in.Trigger)
			f.tx(func(tx pgx.Tx) error {
				for _, id := range []string{sharedID, pausedID} {
					r, err := scanRecurrence(tx.QueryRow(t.Context(), `INSERT INTO recurrences(tenant_id,id,project_id,parent_id,template,trigger,paused,next_at,created_by_principal_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+recurrenceColumns, f.p.TenantID, id, f.project, f.parent, template, trigger, id == pausedID, f.now.Add(7*24*time.Hour), f.p.ID))
					if err != nil {
						return err
					}
					if err := record(t.Context(), tx, f.p, f.project, "recurrence.created", nil, r); err != nil {
						return err
					}
					if i == 0 && id == sharedID {
						before := r
						r.Paused = true
						if _, err := tx.Exec(t.Context(), `UPDATE recurrences SET paused=true WHERE id=$1`, id); err != nil {
							return err
						}
						if err := record(t.Context(), tx, f.p, f.project, "recurrence.deleted", before, r); err != nil {
							return err
						}
						if err := tx.QueryRow(t.Context(), `SELECT at,after FROM events WHERE type='recurrence.deleted'`).Scan(&deletionAt, &deletionSnapshot); err != nil {
							return err
						}
					}
				}
				var taggedEvents int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE $1=ANY(node_refs)`, tag).Scan(&taggedEvents); err != nil {
					return err
				}
				if taggedEvents < 2 {
					return fmt.Errorf("pre-migration fixture lost workspace-tag event references")
				}
				return nil
			})
			fixtures = append(fixtures, f)
			tags = append(tags, tag)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 2 {
		t.Fatal("migration hook did not seed both tenants")
	}
	for i, f := range fixtures {
		f.install(New(d.App))
		f.tx(func(tx pgx.Tx) error {
			var correct bool
			if err := tx.QueryRow(t.Context(), `SELECT
 (SELECT retired_at IS NOT DISTINCT FROM $2::timestamptz FROM recurrences WHERE id=$1)
 AND (SELECT retired_at IS NULL AND paused FROM recurrences WHERE id=$3)
 AND NOT EXISTS(SELECT 1 FROM events WHERE $4=ANY(node_refs))
 AND NOT EXISTS(SELECT 1 FROM events WHERE type LIKE 'recurrence.%' AND after#>'{template,tags}'<>jsonb_build_array($4::text))`, sharedID, func() *time.Time {
				if i == 0 {
					return &deletionAt
				}
				return nil
			}(), pausedID, tags[i]).Scan(&correct); err != nil {
				return err
			}
			if !correct {
				return fmt.Errorf("tenant %d lost retirement state, live pause or audit tags", i)
			}
			if i == 0 {
				var snapshot []byte
				if err := tx.QueryRow(t.Context(), `SELECT after FROM events WHERE type='recurrence.deleted'`).Scan(&snapshot); err != nil {
					return err
				}
				if string(snapshot) != string(deletionSnapshot) {
					return fmt.Errorf("migration changed the deletion snapshot")
				}
			}
			return nil
		})
		if i == 0 {
			f.call(f.p, "GET", "/api/recurrences/"+sharedID, nil, 404)
		} else {
			f.call(f.p, "GET", "/api/recurrences/"+sharedID, nil, 200)
		}
		f.call(f.p, "GET", "/api/recurrences/"+pausedID, nil, 200)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error { return fmt.Errorf("replayed migration %s", name) }); err != nil {
		t.Fatal(err)
	}
}
