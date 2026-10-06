// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestRunMeasurementUpgradeFromRelease122PreservesEvidence(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	type legacy struct{ tenant, run, baseline, outcome string }
	var before []legacy
	release123Reached := false
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name == "1243_run_waiting_measurement.sql" {
			var latest string
			var waitingExists bool
			if err := d.App.QueryRow(t.Context(), `SELECT max(version) FROM schema_migrations`).Scan(&latest); err != nil {
				return err
			}
			if latest != "1240_work_account_pins.sql" {
				return fmt.Errorf("expected release-123 schema before waiting measurement, got %s", latest)
			}
			if err := d.App.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='agent_runs' AND column_name='waiting_ms')`).Scan(&waitingExists); err != nil {
				return err
			}
			if waitingExists {
				return fmt.Errorf("waiting measurement applied before the release-123 schema")
			}
			release123Reached = true
			return nil
		}
		// Seed the release-122 evidence before main's one-work-kind upgrade;
		// the waiting column must still be absent through release 123.
		if name != "1215_one_work_kind.sql" {
			return nil
		}
		var latest string
		if err := d.App.QueryRow(t.Context(), `SELECT max(version) FROM schema_migrations`).Scan(&latest); err != nil {
			return err
		}
		if latest != "1209_theme_unsaved_selection.sql" {
			return fmt.Errorf("expected release-122 schema, got %s", latest)
		}
		for i := range 2 {
			var l legacy
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Timing upgrade') RETURNING id::text`, fmt.Sprintf("timing-upgrade-%d", i)).Scan(&l.tenant); err != nil {
				return err
			}
			err := db.InTenant(dbtest.Seed(t.Context()), d.App, l.tenant, func(tx pgx.Tx) error {
				var principal, project, ticket, order string
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','timing') RETURNING id::text`, l.tenant).Scan(&principal); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'UPG-1','Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, l.tenant).Scan(&project); err != nil {
					return err
				}
				for _, item := range []struct {
					kind, key string
					out       *string
				}{{"ticket", "UPG-2", &ticket}, {"work_order", "UPG-3", &order}} {
					if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,$2,$2,$3 FROM node_kinds WHERE slug=$4 RETURNING id::text`, l.tenant, item.key, project, item.kind).Scan(item.out); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, l.tenant, order, principal); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,active_ms,started_at,ended_at)
 VALUES($1,$2,$3,'completed',1234,'2026-09-01T00:00:00Z','2026-09-01T00:00:10Z') RETURNING id::text`, l.tenant, order, principal).Scan(&l.run); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO ticket_estimate_snapshots(tenant_id,ticket_node_id,source_project_id,snapshot,started_at)
 VALUES($1,$2,$3,'{"estimate_hours":9,"source":"status"}','2026-09-01T00:00:00Z') RETURNING row_to_json(ticket_estimate_snapshots)::text`, l.tenant, ticket, project).Scan(&l.baseline); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `INSERT INTO outcome_events(tenant_id,kind,project_id,ticket_node_id,idempotency_key,actor_principal_id,source,payload,request_digest)
 VALUES($1,'ticket_done',$2,$3,'legacy-completion',$4,'automatic','{"elapsed_seconds":10}',decode(md5('legacy'),'hex')) RETURNING row_to_json(outcome_events)::text`, l.tenant, project, ticket, principal).Scan(&l.outcome)
			})
			if err != nil {
				return err
			}
			before = append(before, l)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !release123Reached {
		t.Fatal("release-123 waiting measurement upgrade hook not reached")
	}
	if len(before) != 2 {
		t.Fatal("upgrade hook not reached")
	}
	for _, l := range before {
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, l.tenant, func(tx pgx.Tx) error {
			var active int64
			var waiting *int64
			var snap, outcome string
			if err := tx.QueryRow(t.Context(), `SELECT active_ms,waiting_ms FROM agent_runs WHERE id=$1`, l.run).Scan(&active, &waiting); err != nil {
				return err
			}
			if active != 1234 || waiting != nil {
				return fmt.Errorf("legacy timing rewritten: active=%d waiting=%v", active, waiting)
			}
			if err := tx.QueryRow(t.Context(), `SELECT row_to_json(ticket_estimate_snapshots)::text FROM ticket_estimate_snapshots`).Scan(&snap); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `SELECT row_to_json(outcome_events)::text FROM outcome_events`).Scan(&outcome); err != nil {
				return err
			}
			if snap != l.baseline || outcome != l.outcome {
				return fmt.Errorf("historical snapshot/outcome changed")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error { return fmt.Errorf("unexpected reapply %s", name) }); err != nil {
		t.Fatal(err)
	}
}
