// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

const workMigration = "1215_one_work_kind.sql"

var beforeWork = errors.New("stop before work-kind migration")

func workOldDatabase(t *testing.T) *dbtest.DB {
	t.Helper()
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name == workMigration {
			return beforeWork
		}
		return nil
	})
	if !errors.Is(err, beforeWork) {
		t.Fatalf("old schema: %v", err)
	}
	return d
}

func workSeed(t *testing.T, d *dbtest.DB, slug string) string {
	t.Helper()
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		stmts := []string{
			`INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT current_setting('aeon.tenant_id')::uuid,id,'PRJ-1','Project' FROM node_kinds WHERE slug='project'`,
			`INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields,parent_id) SELECT current_setting('aeon.tenant_id')::uuid,k.id,'KEEP-1','Parent','blocked','{"estimate_hours":40,"custom":"keep","hide_from_release_notes":true}',p.id FROM node_kinds k,nodes p WHERE k.slug='epic' AND p.key='PRJ-1'`,
			`INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields,parent_id) SELECT current_setting('aeon.tenant_id')::uuid,k.id,'KEEP-2','Leaf','accepted','{"estimate_hours":3,"custom":"precise","classic":{"type":"task","source_id":"fixture"}}',p.id FROM node_kinds k,nodes p WHERE k.slug='task' AND p.key='KEEP-1'`,
			`INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT current_setting('aeon.tenant_id')::uuid,k.id,'KEEP-3','Past leaf',p.id FROM node_kinds k,nodes p WHERE k.slug='ticket' AND p.key='PRJ-1'`,
			`INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT current_setting('aeon.tenant_id')::uuid,k.id,'KEEP-4','Deleted parent',p.id FROM node_kinds k,nodes p WHERE k.slug='ticket' AND p.key='PRJ-1'`,
			`INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT current_setting('aeon.tenant_id')::uuid,k.id,'KEEP-5','Deleted leaf',p.id FROM node_kinds k,nodes p WHERE k.slug='task' AND p.key='KEEP-4'`,
			`UPDATE nodes SET deleted_at='2026-10-01T00:00:00Z' WHERE key='KEEP-5'`,
			`UPDATE nodes SET deleted_at='2026-10-01T00:00:00Z' WHERE key='KEEP-4'`,
			`INSERT INTO principals(tenant_id,kind,name) VALUES(current_setting('aeon.tenant_id')::uuid,'agent','Historical agent')`,
			`INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,stopped_at)
    SELECT p.tenant_id,p.id,a.id,n.id,'codex','fixture','unmanaged','worker','ship',sha256('fixture-ref'::bytea),sha256('fixture-lease'::bytea),'stopped',clock_timestamp() FROM nodes p,nodes n,principals a WHERE p.key='PRJ-1' AND n.key='KEEP-3' AND a.name='Historical agent'`,
			`INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) SELECT n.tenant_id,p.id,n.id,'import.node_created',to_jsonb(n) FROM nodes n,principals p WHERE n.key='KEEP-2' AND p.name='Historical agent'`,
			`UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{properties}',coalesce(field_schema->'properties','{}')||'{"custom":{"type":"string"}}') WHERE slug IN ('epic','ticket','task')`,
			`UPDATE node_kinds SET allowed_child_kinds=ARRAY['ticket','task','guideline','question'] WHERE slug='project'`,
			`UPDATE node_kinds SET allowed_child_kinds=ARRAY['ticket','task','guideline'] WHERE slug='epic'`,
			`UPDATE node_kinds SET allowed_child_kinds=ARRAY[]::text[] WHERE slug IN ('ticket','task')`,
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(t.Context(), stmt); err != nil {
				return err
			}
		}
		// Desk identity uses the dedicated service gate, and never shares a work ID.
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.desk_write','on',true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,'QUE-1','Historical question' FROM node_kinds k WHERE k.slug='question'`, tid); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=(SELECT id FROM nodes WHERE key='PRJ-1') WHERE key='QUE-1'`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO desk_questions(tenant_id,project_id,node_id,input,suggested_outcome,suggestion_reason) SELECT $1,p.id,q.id,'{}','once','agent_suggestion' FROM nodes p,nodes q WHERE p.key='PRJ-1' AND q.key='QUE-1'`, tid); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO desk_askers(tenant_id,project_id,question_id,principal_id,request_id,request_digest,session_id,ticket_id,comment_node_id,input) SELECT $1,p.id,q.id,a.id,gen_random_uuid(),'fixture',s.id,n.id,n.id,'{}' FROM nodes p,nodes q,nodes n,principals a,harness_sessions s WHERE p.key='PRJ-1' AND q.key='QUE-1' AND n.key='KEEP-3' AND a.name='Historical agent'`, tid); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=ARRAY['task'] WHERE slug='question'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tid
}

func workSnapshot(t *testing.T, d *dbtest.DB, tid, sql string) []byte {
	t.Helper()
	var raw []byte
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), sql).Scan(&raw) }); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWorkNodesMigrationPreservesRowsHistoryAndIdentities(t *testing.T) {
	d := workOldDatabase(t)
	tid := workSeed(t, d, "work-upgrade")
	tid2 := workSeed(t, d, "work-upgrade-two")
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=ARRAY['guideline'] WHERE slug='ticket'`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,'GUI-1','Non-work child',n.id FROM node_kinds k,nodes n WHERE k.slug='guideline' AND n.key='KEEP-3'`, tid); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='working',stopped_at=NULL`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	nodesSQL := `SELECT jsonb_agg(to_jsonb(n)-ARRAY['kind_id','benefit_generation'] ORDER BY key) FROM nodes n`
	sessionSQL := `SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM harness_sessions s`
	deskSQL := `SELECT jsonb_agg(to_jsonb(n) ORDER BY key) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='question'`
	before := workSnapshot(t, d, tid, nodesSQL)
	sessions := workSnapshot(t, d, tid, sessionSQL)
	historySQL := `SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e WHERE type='import.node_created'`
	history := workSnapshot(t, d, tid, historySQL)
	desk := workSnapshot(t, d, tid, deskSQL)
	deskProjectionSQL := `SELECT jsonb_build_object('questions',(SELECT jsonb_agg(to_jsonb(q) ORDER BY node_id) FROM desk_questions q),'askers',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM desk_askers a))`
	deskProjections := workSnapshot(t, d, tid, deskProjectionSQL)
	var oldEvents int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='import.node_created'`).Scan(&oldEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, workSnapshot(t, d, tid, nodesSQL)) {
		t.Fatal("migration altered node content or identities")
	}
	if !reflect.DeepEqual(history, workSnapshot(t, d, tid, historySQL)) {
		t.Fatal("historical event payloads changed")
	}
	if !reflect.DeepEqual(sessions, workSnapshot(t, d, tid, sessionSQL)) {
		t.Fatal("historical session changed")
	}
	if !reflect.DeepEqual(deskProjections, workSnapshot(t, d, tid, deskProjectionSQL)) {
		t.Fatal("Decision Desk question/asker identity or work/session reference changed")
	}
	if !reflect.DeepEqual(desk, workSnapshot(t, d, tid, deskSQL)) {
		t.Fatal("Decision Desk node changed")
	}
	for _, id := range []string{tid, tid2} {
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			var old, work, moves int
			var properties []byte
			var children []string
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE slug IN ('epic','ticket','task')`).Scan(&old); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='work'`).Scan(&work); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='node.work_kind_migrated' AND before->>'kind_id'<>after->>'kind_id' AND after->>'legacy_kind_slug' IN ('epic','ticket','task')`).Scan(&moves); err != nil {
				return err
			}
			if old != 0 || work != 5 || moves != 5 {
				t.Fatalf("old=%d work=%d events=%d", old, work, moves)
			}
			if err := tx.QueryRow(t.Context(), `SELECT field_schema->'properties',allowed_child_kinds FROM node_kinds WHERE slug='work'`).Scan(&properties, &children); err != nil {
				return err
			}
			var props map[string]any
			if err := json.Unmarshal(properties, &props); err != nil {
				return err
			}
			if props["custom"] == nil || props["human_check_completed"] == nil || props["pill_en"] == nil || !reflect.DeepEqual(children, []string{"guideline", "work"}) {
				t.Fatalf("schema or allowed children lost: %s %v", properties, children)
			}
			var refs int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE allowed_child_kinds && ARRAY['epic','ticket','task']`).Scan(&refs); err != nil {
				return err
			}
			if refs != 0 {
				t.Fatal("legacy allowed-child reference remains")
			}
			// Restoration and ordinary nested work are valid after the tree guard returns.
			if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,'KEEP-6','New nested leaf',n.id FROM node_kinds k,nodes n WHERE k.slug='work' AND n.key='KEEP-2'`, id); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var afterEvents int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='import.node_created'`).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterEvents != oldEvents {
		t.Fatal("old events lost")
	}
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error { t.Fatalf("unexpected replay %s", name); return nil }); err != nil {
		t.Fatal(err)
	}
	var newTenant string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('new-work','New work') RETURNING id::text`).Scan(&newTenant); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, newTenant, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE slug IN ('work','epic','ticket','task')`).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("new tenant work kinds = %d", count)
		}
		var slug string
		if err := tx.QueryRow(t.Context(), `SELECT slug FROM node_kinds WHERE slug IN ('work','epic','ticket','task')`).Scan(&slug); err != nil {
			return err
		}
		if slug != "work" {
			t.Fatalf("new tenant seeded %s", slug)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkNodesMigrationBusyGuardAndAtomicRetry(t *testing.T) {
	for _, reason := range []string{"session", "claim", "work_order", "schema", "reserved", "invalid_fields"} {
		t.Run(reason, func(t *testing.T) {
			d := workOldDatabase(t)
			tid := workSeed(t, d, "blocked-work")
			healthy := workSeed(t, d, "healthy-work")
			if tid < healthy {
				tid, healthy = healthy, tid
			}
			healthyBefore := workSnapshot(t, d, healthy, `SELECT jsonb_agg(to_jsonb(n) ORDER BY key) FROM nodes n`)
			err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
				switch reason {
				case "session":
					_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET ticket_node_id=(SELECT id FROM nodes WHERE key='KEEP-1'),phase='working',stopped_at=NULL`)
					return err
				case "schema":
					_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{properties,custom}','{"type":"number"}') WHERE slug='task'`)
					return err
				case "invalid_fields":
					_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{additionalProperties}','false') WHERE slug='task'`)
					return err
				case "reserved":
					_, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'work','Existing custom','WRK','custom')`, tid)
					return err
				default:
					if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET allowed_child_kinds=array_append(allowed_child_kinds,'work_order') WHERE slug='epic'`); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,'WOR-1','Bound work order',n.id FROM node_kinds k,nodes n WHERE k.slug='work_order' AND n.key='KEEP-1'`, tid); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) SELECT $1,n.id,p.id,'running' FROM nodes n,principals p WHERE n.key='WOR-1' AND p.name='Historical agent'`, tid); err != nil {
						return err
					}
					if reason == "work_order" {
						return nil
					}
					if _, err := tx.Exec(t.Context(), `UPDATE work_orders SET status='ready'`); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,queue_node_id,queue_by_principal_id,queue_at,queue_security_review_required) SELECT $1,w.node_id,p.id,'queued',n.id,p.id,clock_timestamp(),false FROM work_orders w,principals p,nodes n WHERE n.key='KEEP-1' AND p.name='Historical agent'`, tid)
					return err
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			before := workSnapshot(t, d, tid, `SELECT jsonb_agg(to_jsonb(n) ORDER BY key) FROM nodes n`)
			var reported []db.BusyWorkParent
			preflightErr := db.CheckWorkMigration(t.Context(), d.App, func(p db.BusyWorkParent) error {
				reported = append(reported, p)
				return nil
			})
			busy := reason == "session" || reason == "claim" || reason == "work_order"
			var blocked *db.BusyWorkParentsError
			if busy {
				if !errors.As(preflightErr, &blocked) || len(reported) != 1 || reported[0].Key != "KEEP-1" || reported[0].TenantID != tid {
					t.Fatalf("preflight missed exact busy parent for %s: %v, %+v", reason, preflightErr, reported)
				}
			} else if preflightErr != nil || len(reported) != 0 {
				t.Fatalf("non-busy preflight: %v, %+v", preflightErr, reported)
			}
			if !reflect.DeepEqual(before, workSnapshot(t, d, tid, `SELECT jsonb_agg(to_jsonb(n) ORDER BY key) FROM nodes n`)) {
				t.Fatal("preflight changed node content")
			}
			err = db.MigrateWithHook(t.Context(), d.App, nil)
			want := "busy work parents require graceful handover"
			if reason == "schema" {
				want = "incompatible property"
			}
			if reason == "invalid_fields" {
				want = "fields do not satisfy the merged work schema"
			}
			if reason == "reserved" {
				want = "work kind already exists"
			}
			if err == nil || !strings.Contains(err.Error(), want) || ((reason == "session" || reason == "claim" || reason == "work_order") && !strings.Contains(err.Error(), "KEEP-1")) {
				t.Fatalf("guard failed for %s: %v", reason, err)
			}
			if !reflect.DeepEqual(before, workSnapshot(t, d, tid, `SELECT jsonb_agg(to_jsonb(n) ORDER BY key) FROM nodes n`)) {
				t.Fatal("failed migration changed rows")
			}
			if !reflect.DeepEqual(healthyBefore, workSnapshot(t, d, healthy, `SELECT jsonb_agg(to_jsonb(n) ORDER BY key) FROM nodes n`)) {
				t.Fatal("healthy earlier tenant was not rolled back")
			}
			var applied int
			if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations WHERE version=$1`, workMigration).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied != 0 {
				t.Fatal("failed migration recorded")
			}
			var guard string
			if err := d.Admin.QueryRow(t.Context(), `SELECT tgenabled::text FROM pg_trigger WHERE tgname='nodes_tree_guard'`).Scan(&guard); err != nil {
				t.Fatal(err)
			}
			if guard != "O" {
				t.Fatal("tree guard was not restored on rollback")
			}
			if reason == "session" {
				if _, err := d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp()`); err != nil {
					t.Fatal(err)
				}
				if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
					t.Fatalf("retry after handover: %v", err)
				}
			}
		})
	}
}

func TestWorkMigrationPreflightPagesAllTenantsWithoutWrites(t *testing.T) {
	d := workOldDatabase(t)
	tid := workSeed(t, d, "paged-preflight")
	other := workSeed(t, d, "other-preflight")
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		statements := []string{
			`UPDATE node_kinds SET allowed_child_kinds=ARRAY['task'] WHERE tenant_id=$1 AND slug='ticket'`,
			`INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
 SELECT $1,k.id,'PAGE-'||i,'Parent',p.id FROM generate_series(1,105) i,node_kinds k,nodes p WHERE k.slug='ticket' AND p.key='PRJ-1'`,
			`INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
 SELECT $1,k.id,'CHILD-'||p.key,'Child',p.id FROM node_kinds k,nodes p WHERE k.slug='task' AND p.key LIKE 'PAGE-%'`,
			`INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase)
 SELECT $1,n.project_id,a.id,n.id,'codex','fixture','unmanaged','worker','ship',sha256(n.id::text::bytea),sha256(n.key::bytea),'working'
 FROM nodes n,principals a WHERE n.key LIKE 'PAGE-%' AND a.name='Historical agent'`,
		}
		for _, sql := range statements {
			if _, err := tx.Exec(t.Context(), sql, tid); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET ticket_node_id=(SELECT id FROM nodes WHERE key='KEEP-1'),stopped_at=NULL,phase='working'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var ledgerBefore int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&ledgerBefore); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	err := db.CheckWorkMigration(t.Context(), d.App, func(p db.BusyWorkParent) error {
		if p.Reason != "bound session" || (p.TenantID != tid && p.TenantID != other) {
			return fmt.Errorf("wrong blocker: %+v", p)
		}
		if seen[p.TenantID+":"+p.Key] {
			return errors.New("duplicate preflight parent")
		}
		seen[p.TenantID+":"+p.Key] = true
		return nil
	})
	var blocked *db.BusyWorkParentsError
	if !errors.As(err, &blocked) || !blocked.Truncated || len(blocked.Parents) != 100 || len(seen) != 106 || !seen[other+":KEEP-1"] {
		t.Fatalf("incomplete cross-tenant preflight: %v, emitted %d", err, len(seen))
	}
	for i := 1; i <= 105; i++ {
		if !seen[fmt.Sprintf("%s:PAGE-%d", tid, i)] {
			t.Fatalf("missing parent %d", i)
		}
	}
	var ledgerAfter int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&ledgerAfter); err != nil || ledgerAfter != ledgerBefore {
		t.Fatalf("preflight changed ledger: %d to %d: %v", ledgerBefore, ledgerAfter, err)
	}
	stop := errors.New("output failed")
	if err := db.CheckWorkMigration(t.Context(), d.App, func(db.BusyWorkParent) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("output failure reported clean: %v", err)
	}
}

func TestWorkNodesMigrationExactNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, constraint, value string
		valid                   bool
	}{
		{"large_integer_above_exclusive_min", `{"type":"integer","exclusiveMinimum":9007199254740992}`, `9007199254740993`, true},
		{"large_integer_below_exclusive_max", `{"type":"integer","exclusiveMaximum":9007199254740993}`, `9007199254740992`, true},
		{"decimal_above_exclusive_min", `{"type":"number","exclusiveMinimum":0.10000000000000000001}`, `0.10000000000000000002`, true},
		{"decimal_below_exclusive_max", `{"type":"number","exclusiveMaximum":0.10000000000000000002}`, `0.10000000000000000001`, true},
		{"large_integer_below_min", `{"type":"integer","minimum":9007199254740993}`, `9007199254740992`, false},
		{"large_integer_above_max", `{"type":"integer","maximum":9007199254740992}`, `9007199254740993`, false},
		{"decimal_below_min", `{"type":"number","minimum":0.10000000000000000002}`, `0.10000000000000000001`, false},
		{"decimal_above_max", `{"type":"number","maximum":0.10000000000000000001}`, `0.10000000000000000002`, false},
		{"fraction_is_not_integer", `{"type":"integer"}`, `1.00000000000000000001`, false},
		{"distinct_large_integer_const", `{"const":9007199254740992}`, `9007199254740993`, false},
		{"distinct_decimal_enum", `{"enum":[0.10000000000000000001]}`, `0.10000000000000000002`, false},
		{"exclusive_min_equality", `{"exclusiveMinimum":9007199254740993}`, `9007199254740993`, false},
		{"exclusive_max_equality", `{"exclusiveMaximum":0.10000000000000000002}`, `0.10000000000000000002`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := workOldDatabase(t)
			tid := workSeed(t, d, "numeric-work")
			if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{properties,precise}',$1::jsonb) WHERE slug IN ('epic','ticket','task')`, tc.constraint); err != nil {
					return err
				}
				// Exercise nested items as well as an ordinary numeric property.
				if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{properties,nested}',jsonb_build_object('type','array','items',jsonb_build_object('type','object','properties',jsonb_build_object('precise',$1::jsonb)))) WHERE slug IN ('epic','ticket','task')`, tc.constraint); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||jsonb_build_object('precise',$1::jsonb,'nested',jsonb_build_array(jsonb_build_object('precise',$1::jsonb))) WHERE key='KEEP-2'`, tc.value)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			nodesSQL := `SELECT jsonb_agg(to_jsonb(n)-ARRAY['kind_id','benefit_generation'] ORDER BY key) FROM nodes n`
			before := workSnapshot(t, d, tid, nodesSQL)
			kindsSQL := `SELECT jsonb_agg(to_jsonb(k) ORDER BY slug) FROM node_kinds k`
			kinds := workSnapshot(t, d, tid, kindsSQL)
			eventsSQL := `SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e`
			events := workSnapshot(t, d, tid, eventsSQL)
			err := db.MigrateWithHook(t.Context(), d.App, nil)
			if tc.valid {
				if err != nil {
					t.Fatalf("valid exact number blocked migration: %v", err)
				}
				var kind string
				if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.key='KEEP-2'`).Scan(&kind)
				}); err != nil {
					t.Fatal(err)
				}
				if kind != "work" {
					t.Fatalf("numeric node kind = %s, want work", kind)
				}
				var retained bool
				if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT field_schema->'properties'->'precise'=$1::jsonb AND field_schema->'properties'->'nested'->'items'->'properties'->'precise'=$1::jsonb FROM node_kinds WHERE slug='work'`, tc.constraint).Scan(&retained)
				}); err != nil {
					t.Fatal(err)
				}
				if !retained {
					t.Fatal("migration changed exact schema constraints")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "KEEP-2 fields do not satisfy the merged work schema") {
					t.Fatalf("invalid exact number: got %v, want KEEP-2 schema validation failure", err)
				}
				if !reflect.DeepEqual(kinds, workSnapshot(t, d, tid, kindsSQL)) || !reflect.DeepEqual(events, workSnapshot(t, d, tid, eventsSQL)) {
					t.Fatal("rejected numeric migration changed kinds or events")
				}
			}
			if !reflect.DeepEqual(before, workSnapshot(t, d, tid, nodesSQL)) {
				t.Fatal("numeric migration changed historical fields or node identities")
			}
		})
	}
}

func TestWorkNodesVerifiedBackupRestoreDrill(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("local restore drill requires Docker and aeon-dev-db")
	}
	d := workOldDatabase(t)
	tid := workSeed(t, d, "restore-work")
	// Compare every durable public table, not just the migrated nodes. The backup
	// is a synthetic fixture with no live credentials or production data.
	digest := func(d *dbtest.DB) map[string]string {
		t.Helper()
		out := map[string]string{}
		rows, err := d.Admin.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			tables = append(tables, table)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range tables {
			var hash string
			sql := `SELECT md5(coalesce(jsonb_agg(row ORDER BY row)::text,'[]')) FROM (SELECT to_jsonb(t) row FROM ` + pgx.Identifier{table}.Sanitize() + ` t) x`
			if err := d.Admin.QueryRow(t.Context(), sql).Scan(&hash); err != nil {
				t.Fatalf("digest %s: %v", table, err)
			}
			out[table] = hash
		}
		for _, function := range []string{"aeon_seed_node_kinds", "aeon_validate_node_tree"} {
			var hash string
			if err := d.Admin.QueryRow(t.Context(), `SELECT md5(prosrc) FROM pg_proc WHERE proname=$1`, function).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			out[function] = hash
		}
		return out
	}
	before := digest(d)
	dir := filepath.Join("..", "..", "tmp", "aeon-649-wn")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	archive, err := os.CreateTemp(dir, "pre-work-*.dump")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := archive.Name()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	dump := exec.CommandContext(ctx, "docker", "exec", "aeon-dev-db", "pg_dump", "-U", "aeon", "-d", d.Name, "--format=custom", "--no-owner", "--no-acl", "--exclude-extension=vector")
	dump.Stdout = archive
	if err := dump.Run(); err != nil {
		_ = archive.Close()
		t.Fatalf("local pg_dump: %v", err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 5 || string(raw[:5]) != "PGDMP" {
		t.Fatal("backup is not a PostgreSQL custom archive")
	}
	backupHash := fmt.Sprintf("%x", sha256.Sum256(raw))
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, digest(d)) {
		t.Fatal("drill never applied migration")
	}
	restored, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	})
	input, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	restore := exec.CommandContext(ctx, "docker", "exec", "-i", "aeon-dev-db", "pg_restore", "-U", "aeon", "-d", restored.Name, "--no-owner", "--no-acl", "--exit-on-error", "--role="+restored.Role)
	restore.Stdin = input
	if err := restore.Run(); err != nil {
		t.Fatalf("local pg_restore: %v", err)
	}
	after := digest(restored)
	for table, expected := range before {
		if after[table] != expected {
			t.Fatalf("restore digest mismatch: %s", table)
		}
	}
	if len(before) != len(after) {
		t.Fatal("restore table/function inventory differs")
	}
	// Prove the restored old schema can run the same migration under ordinary RLS.
	if err := db.MigrateWithHook(t.Context(), restored.App, nil); err != nil {
		t.Fatal(err)
	}
	if got := workSnapshot(t, restored, tid, `SELECT jsonb_build_object('legacy',(SELECT count(*) FROM node_kinds WHERE slug IN ('epic','ticket','task')),'work',(SELECT count(*) FROM node_kinds WHERE slug='work'))`); string(got) != `{"work": 1, "legacy": 0}` {
		t.Fatalf("restored migration %s", got)
	}
	evidence := map[string]any{"ticket": "AEON-649", "scope": "synthetic local fixture; no production access", "archive": filepath.ToSlash(filepath.Join("tmp", "aeon-649-wn", filepath.Base(archivePath))), "sha256": backupHash, "verified_tables_and_functions": len(before), "pre_migration_backup": true, "restored_digests_equal": true, "restored_migration_under_force_rls": true}
	report, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore-evidence.json"), append(report, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified local backup %s SHA256 %s; %d table/function digests restored", archivePath, backupHash, len(before))
}

//go:embed testdata/work_release_122.json
var workRelease122 []byte

// Mask files absent from the pinned release while constructing its exact SQL
// schema, then remove those temporary ledger entries before the real upgrade.
// This uses the runner's staged/concurrent paths and ordinary FORCE-RLS role.
func TestWorkNodesUpgradeFromRelease122(t *testing.T) {
	var release struct {
		Sequence   int               `json:"release_sequence"`
		Version    string            `json:"version"`
		Commit     string            `json:"source_commit"`
		Migrations map[string]string `json:"migrations"`
	}
	if err := json.Unmarshal(workRelease122, &release); err != nil {
		t.Fatal(err)
	}
	if release.Sequence != 122 || release.Version != "261003095616.0.0" || release.Commit != "068611ab87b80319ed1f0843bbad79ca897b7dcb" || len(release.Migrations) != 231 {
		t.Fatal("release 122 fixture pin changed")
	}
	for name, digest := range release.Migrations {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(body)) != digest {
			t.Fatalf("release 122 SQL changed: %s", name)
		}
	}
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.App.Exec(t.Context(), `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	var masked []string
	for _, name := range migrationNames(t) {
		if _, published := release.Migrations[name]; !published {
			masked = append(masked, name)
			if _, err := d.App.Exec(t.Context(), `INSERT INTO schema_migrations(version) VALUES($1)`, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if _, published := release.Migrations[name]; !published {
			return fmt.Errorf("unexpected release 122 file: %s", name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.App.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version=ANY($1::text[])`, masked); err != nil {
		t.Fatal(err)
	}
	var applied []string
	if err := d.App.QueryRow(t.Context(), `SELECT array_agg(version ORDER BY version) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(release.Migrations) {
		t.Fatalf("release 122 schema has %d migrations", len(applied))
	}
	for _, name := range applied {
		if _, published := release.Migrations[name]; !published {
			t.Fatalf("non-release migration: %s", name)
		}
	}
	var absent bool
	if err := d.App.QueryRow(t.Context(), `SELECT to_regclass('themes') IS NULL AND to_regprocedure('aeon_work_status_begin()') IS NULL AND to_regprocedure('aeon_work_aggregates(uuid[])') IS NULL`).Scan(&absent); err != nil {
		t.Fatal(err)
	}
	if !absent {
		t.Fatal("fixture already includes later schema")
	}
	tid := workSeed(t, d, "release-122-upgrade")
	// 1117 adds desk_answers after release 122; compare every pre-existing column.
	preserved := `SELECT jsonb_build_object('nodes',(SELECT jsonb_agg(to_jsonb(n)-ARRAY['kind_id','benefit_generation'] ORDER BY key) FROM nodes n),'sessions',(SELECT jsonb_agg(to_jsonb(s)-'desk_answers' ORDER BY id) FROM harness_sessions s),'questions',(SELECT jsonb_agg(to_jsonb(q) ORDER BY node_id) FROM desk_questions q))`
	before := workSnapshot(t, d, tid, preserved)
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	if after := workSnapshot(t, d, tid, preserved); !reflect.DeepEqual(before, after) {
		t.Fatalf("release 122 upgrade changed nodes, sessions or Decision Desk identities: %s", snapshotDifferences(before, after))
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		var legacy, work, migrated int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM node_kinds WHERE slug IN ('epic','ticket','task')),(SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='work'),(SELECT count(*) FROM events WHERE type='node.work_kind_migrated')`).Scan(&legacy, &work, &migrated); err != nil {
			return err
		}
		if legacy != 0 || work != 5 || migrated != 5 {
			return fmt.Errorf("legacy=%d work=%d migration events=%d", legacy, work, migrated)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error { return fmt.Errorf("unexpected reapplication: %s", name) }); err != nil {
		t.Fatal(err)
	}
	t.Logf("release 122 (%s): %d exact SQL files, upgrade and reapply under FORCE RLS", release.Commit, len(applied))
}

// Keep upgrade failures readable without dumping entire row snapshots.
func snapshotDifferences(before, after []byte) []string {
	var a, b map[string][]map[string]any
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
		return []string{"invalid snapshot"}
	}
	var changed []string
	for table, rows := range a {
		if len(rows) != len(b[table]) {
			changed = append(changed, table+": row count")
			continue
		}
		for i, row := range rows {
			for field, value := range row {
				if !reflect.DeepEqual(value, b[table][i][field]) {
					changed = append(changed, fmt.Sprintf("%s[%v].%s: %v -> %v", table, row["key"], field, value, b[table][i][field]))
				}
			}
			for field := range b[table][i] {
				if _, ok := row[field]; !ok {
					changed = append(changed, table+": added column "+field)
				}
			}
		}
	}
	return changed
}
