// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestStateNormalizationAndStoredUserBackfill(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	if _, err := tenantbootstrap.Create(ctx, d.App, "states", "States"); err != nil {
		t.Fatal(err)
	}
	tid, err := tenantbootstrap.ResolveSlug(ctx, d.App, "states")
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{SourceID: "source", Users: []Record{{"id": 7, "username": "mba", "display_name": "Markus Barta", "role": "member"}}, Projects: []Project{{Record: Record{"id": 1, "key": "ST", "name": "States", "status": "active"}}}}
	variants := []string{"in-progress", "in progress", "in_progress", "inprogress", "canceled", "cancelled", " DONE ", "custom-state"}
	want := []string{"in_progress", "in_progress", "in_progress", "in_progress", "cancelled", "cancelled", "done", "custom-state"}
	for i, state := range variants {
		s.Projects[0].Issues = append(s.Projects[0].Issues, Record{"id": i + 1, "issue_key": fmt.Sprintf("ST-%d", i+1), "title": "Ticket", "type": "ticket", "status": state, "assignee_id": 7, "created_by": 7})
	}
	w := PostgresWriter{Pool: d.App}
	if _, err := w.Write(ctx, s, "states"); err != nil {
		t.Fatal(err)
	}
	txdo := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, fn); err != nil {
			t.Fatal(err)
		}
	}
	var principal string
	var initialEvents int
	txdo(func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT p.id::text FROM principals p JOIN identities i ON i.id=p.identity_id WHERE p.tenant_id=$1 AND i.subject='source:7' AND p.name='mba'`, tid).Scan(&principal); err != nil {
			return err
		}
		for i, state := range want {
			var actual, original, assignee string
			if err := tx.QueryRow(ctx, `SELECT state,fields->'classic'->>'status',fields->>'assignee' FROM nodes WHERE tenant_id=$1 AND key=$2`, tid, fmt.Sprintf("ST-%d", i+1)).Scan(&actual, &original, &assignee); err != nil {
				return err
			}
			if actual != state || original != variants[i] || assignee != principal {
				return fmt.Errorf("state/assignment: %q %q %q", actual, original, assignee)
			}
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tid).Scan(&initialEvents)
	})
	if r, err := w.Write(ctx, s, "states"); err != nil || r.Updated != 0 {
		t.Fatalf("replay %+v %v", r, err)
	}
	txdo(func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tid).Scan(&n); err != nil {
			return err
		}
		if n != initialEvents {
			return fmt.Errorf("replay events %d != %d", n, initialEvents)
		}
		return nil
	})
	// Simulate the old importer leaving only classic references and full names.
	txdo(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE principals SET name='Markus Barta' WHERE tenant_id=$1 AND id=$2`, tid, principal); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=fields-'assignee' WHERE tenant_id=$1 AND key LIKE 'ST-%'`, tid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE nodes SET fields=jsonb_set(fields,'{assignee}','null'::jsonb) WHERE tenant_id=$1 AND key='ST-8'`, tid)
		return err
	})
	r, err := BackfillPrincipals(ctx, d.App, tid)
	if err != nil || r.Counts["assignees"] != 7 || r.Counts["names"] != 1 {
		t.Fatalf("backfill %+v %v", r, err)
	}
	txdo(func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type IN ('node.updated','principal.updated')`, tid).Scan(&n); err != nil {
			return err
		}
		if n != 8 {
			return fmt.Errorf("expected 8 change events, got %d", n)
		}
		return nil
	})
	if r, err := BackfillPrincipals(ctx, d.App, tid); err != nil || r.Writes != 0 {
		t.Fatalf("backfill replay %+v %v", r, err)
	}
	// A filtered snapshot may omit users; durable source mapping still applies.
	s.Users = nil
	s.Projects[0].Issues = s.Projects[0].Issues[:1]
	if _, err := w.Write(ctx, s, "states"); err != nil {
		t.Fatal(err)
	}
	txdo(func(tx pgx.Tx) error {
		var assigned string
		if err := tx.QueryRow(ctx, `SELECT fields->>'assignee' FROM nodes WHERE tenant_id=$1 AND key='ST-1'`, tid).Scan(&assigned); err != nil {
			return err
		}
		if assigned != principal {
			return fmt.Errorf("lost stored user mapping")
		}
		return nil
	})
	// Username-only snapshots keep the classic username.
	s.Users = []Record{{"id": 7, "username": "mba", "role": "member"}}
	if _, err := w.Write(ctx, s, "states"); err != nil {
		t.Fatal(err)
	}
	txdo(func(tx pgx.Tx) error {
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM principals WHERE tenant_id=$1 AND id=$2`, tid, principal).Scan(&name); err != nil {
			return err
		}
		if name != "mba" {
			return fmt.Errorf("display name regressed: %s", name)
		}
		return nil
	})

}

func TestNormalizeStatesMigrationEventsAndRollback(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	migration, err := os.ReadFile("../db/migrations/0531_normalize_states.sql")
	if err != nil {
		t.Fatal(err)
	}
	tenants := []string{"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"}
	for i, tid := range tenants {
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Migration')`, tid, fmt.Sprintf("migration-%d", i)); err != nil {
				return err
			}
			for j, state := range []string{"in-progress", "in progress", "canceled", " QA ", "done", "custom-state"} {
				if _, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,state,fields) SELECT $1,$2,id,'Ticket',$3,jsonb_build_object('classic',jsonb_build_object('status',$3::text)) FROM node_kinds WHERE tenant_id=$1 AND slug='ticket'`, tid, fmt.Sprintf("ST-%d", j+1), state); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Execute exactly the migration SQL, first in a transaction that must roll back.
	var baseline int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tenants[0]).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	sentinel := fmt.Errorf("rollback probe")
	if err := db.InTenant(dbtest.Seed(ctx), d.Admin, tenants[0], func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, string(migration)); err != nil {
			return err
		}
		return sentinel
	}); err != sentinel {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenants[0], func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tenants[0]).Scan(&count); err != nil {
			return err
		}
		if count != baseline {
			return fmt.Errorf("rolled back migration retained %d events", count)
		}
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM nodes WHERE tenant_id=$1 AND key='ST-1'`, tenants[0]).Scan(&state); err != nil {
			return err
		}
		if state != "in-progress" {
			return fmt.Errorf("rollback state %s", state)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.InTenant(dbtest.Seed(ctx), d.Admin, tenants[0], func(tx pgx.Tx) error { _, err := tx.Exec(ctx, string(migration)); return err }); err != nil {
			t.Fatal(err)
		}
	}
	for _, tid := range tenants {
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT e.before,e.after,p.name,p.roles FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE e.tenant_id=$1 AND e.type='node.state_normalized' ORDER BY e.id`, tid)
			if err != nil {
				return err
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var before, after []byte
				var name string
				var roles []string
				if err := rows.Scan(&before, &after, &name, &roles); err != nil {
					return err
				}
				var b, a Record
				if err := json.Unmarshal(before, &b); err != nil {
					return err
				}
				if err := json.Unmarshal(after, &a); err != nil {
					return err
				}
				if name != "System" || len(roles) != 1 || roles[0] != "system" || a["state"] != canonicalState(b["state"].(string)) || a["id"] != b["id"] {
					return fmt.Errorf("invalid migration event")
				}
				count++
			}
			if count != 4 {
				return fmt.Errorf("migration events = %d", count)
			}
			return rows.Err()
		}); err != nil {
			t.Fatal(err)
		}
	}
}
