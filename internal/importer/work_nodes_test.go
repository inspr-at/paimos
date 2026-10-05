// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/jackc/pgx/v5"
)

func TestWorkNodesImporterLegacyMappingAndProvenance(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	tid, err := tenantbootstrap.Create(ctx, d.App, "work-import", "Work import")
	if err != nil {
		t.Fatal(err)
	}
	snap := Snapshot{SourceID: "work-source", Details: map[int64]Details{}, Projects: []Project{{Record: Record{"id": 1, "key": "WI", "name": "Work", "status": "open"}, Issues: []Record{
		{"id": 11, "project_id": 1, "issue_key": "WI-11", "type": "epic", "title": "Parent", "status": "open", "estimate_hours": 40},
		{"id": 12, "project_id": 1, "issue_key": "WI-12", "type": "ticket", "title": "Ticket", "status": "done", "parent_id": 11},
		{"id": 13, "project_id": 1, "issue_key": "WI-13", "type": "task", "title": "Task", "status": "accepted", "parent_id": 11, "hide_from_release_notes": true},
	}}}}
	writer := PostgresWriter{Pool: d.App}
	report, err := writer.Write(ctx, snap, "work-import")
	if err != nil {
		t.Fatal(err)
	}
	if report.Created != 4 || len(report.Conflicts) != 0 {
		t.Fatalf("first import %+v", report)
	}
	report, err = writer.Write(ctx, snap, "work-import")
	if err != nil {
		t.Fatal(err)
	}
	if report.Created != 0 || report.Updated != 0 || len(report.Conflicts) != 0 {
		t.Fatalf("replay %+v", report)
	}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='work' AND n.fields->'classic'->>'type' IN ('epic','ticket','task')`).Scan(&count); err != nil {
			return err
		}
		if count != 3 {
			t.Fatalf("mapped/provenanced %d", count)
		}
		var hidden bool
		if err := tx.QueryRow(ctx, `SELECT (fields->>'hide_from_release_notes')::boolean FROM nodes WHERE key='WI-13'`).Scan(&hidden); err != nil {
			return err
		}
		if !hidden {
			t.Fatal("task release-note setting lost")
		}
		var legacy int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_kinds WHERE slug IN ('epic','ticket','task')`).Scan(&legacy); err != nil {
			return err
		}
		if legacy != 0 {
			t.Fatal("importer resurrected old kinds")
		}
		source, target := reconcileSet{}, reconcileSet{}
		if err := readReconcileTarget(ctx, tx, tid, snap.SourceID, "", map[int64]string{1: "WI"}, attachments.Store{FilesDir: t.TempDir()}, target, nil); err != nil {
			return err
		}
		for _, issue := range snap.Projects[0].Issues {
			if err := addSourceIssue(source, 1, issue); err != nil {
				return err
			}
		}
		for id, hash := range source[1]["tickets"] {
			if hash != target[1]["tickets"][id] {
				t.Fatalf("reconcile mismatch for %s", id)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET kind_id=(SELECT id FROM node_kinds WHERE slug='release') WHERE key='WI-13'`); err != nil {
			return err
		}
		wrong := reconcileSet{}
		if err := readReconcileTarget(ctx, tx, tid, snap.SourceID, "", map[int64]string{1: "WI"}, attachments.Store{FilesDir: t.TempDir()}, wrong, nil); err != nil {
			return err
		}
		if !strings.HasSuffix(wrong[1]["tickets"]["13"], ":kind-projection-mismatch") {
			t.Fatal("reconcile concealed a non-work kind change")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkNodesImporterReplaysMigrationWithoutMaskingPersonEdits(t *testing.T) {
	ctx := t.Context()
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	stop := errors.New("before work migration")
	if err := db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name == "1215_one_work_kind.sql" {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	tid, err := tenantbootstrap.Create(ctx, d.App, "migrated-import", "Migrated import")
	if err != nil {
		t.Fatal(err)
	}
	project := Record{"id": 1, "key": "MI", "name": "Migrated", "status": "open"}
	issue := Record{"id": 2, "issue_key": "MI-2", "type": "task", "title": "Original task", "status": "open"}
	var nodeID string
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var actor, pid string
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Classic Paimos importer') RETURNING id::text`, tid).Scan(&actor); err != nil {
			return err
		}
		pf, _ := json.Marshal(mappedFields(project, nil, "migration-source", true))
		nf, _ := json.Marshal(mappedFields(issue, nil, "migration-source", false))
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields) SELECT $1,id,'PRJ-1','Migrated','open',$2::jsonb FROM node_kinds WHERE slug='project' RETURNING id::text`, tid, string(pf)).Scan(&pid); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields,parent_id) SELECT $1,id,'MI-2','Original task','open',$2::jsonb,$3 FROM node_kinds WHERE slug='task' RETURNING id::text`, tid, string(nf), pid).Scan(&nodeID); err != nil {
			return err
		}
		for _, id := range []string{pid, nodeID} {
			var raw []byte
			if err := tx.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, id).Scan(&raw); err != nil {
				return err
			}
			if _, err := events.Append(ctx, tx, tenant.Principal{TenantID: tid, ID: actor}, events.Change{Type: "import.node_created", NodeID: &id, After: json.RawMessage(raw)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET title='Person edit before migration' WHERE id=$1`, nodeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(ctx, d.App, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		diverged, err := importNodeDiverged(ctx, tx, tid, nodeID)
		if err != nil {
			return err
		}
		if !diverged {
			t.Fatal("migration snapshot masked a pre-existing person edit")
		}
		// Return the synthetic fixture to the original import content, then
		// exercise the independent source-delta/replay path against that baseline.
		_, err = tx.Exec(ctx, `UPDATE nodes SET title='Original task' WHERE id=$1`, nodeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snap := Snapshot{SourceID: "migration-source", Details: map[int64]Details{}, Projects: []Project{{Record: project, Issues: []Record{issue}}}}
	writer := PostgresWriter{Pool: d.App}
	report, err := writer.Write(ctx, snap, "migrated-import")
	if err != nil {
		t.Fatal(err)
	}
	if report.Created != 0 || report.Updated != 0 || len(report.Conflicts) != 0 {
		t.Fatalf("unchanged migrated replay %+v", report)
	}
	issue["title"] = "Updated at source"
	report, err = writer.Write(ctx, snap, "migrated-import")
	if err != nil {
		t.Fatal(err)
	}
	if report.Updated != 1 || len(report.Conflicts) != 0 {
		t.Fatalf("source delta after migration %+v", report)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET title='Person edit' WHERE id=$1`, nodeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue["title"] = "Source conflicts"
	report, err = writer.Write(ctx, snap, "migrated-import")
	if err != nil {
		t.Fatal(err)
	}
	if report.Updated != 0 || len(report.Conflicts) != 1 || report.Conflicts[0].Reason != "Aeon node changed since last import" {
		t.Fatalf("person divergence masked %+v", report)
	}
	var title string
	if err := d.Admin.QueryRow(ctx, `SELECT title FROM nodes WHERE id=$1`, nodeID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Person edit" {
		t.Fatal("person edit overwritten")
	}
}

func TestWorkParentStatusImporterReplayAndChildChange(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	tid, err := tenantbootstrap.Create(ctx, d.App, "parent-import", "Parent import")
	if err != nil {
		t.Fatal(err)
	}
	dbtest.EnableWorkParentStatus(t, d, tid)
	snap := Snapshot{SourceID: "parent-source", Details: map[int64]Details{}, Projects: []Project{{Record: Record{"id": 1, "key": "WI", "name": "Work", "status": "open"}, Issues: []Record{
		{"id": 11, "project_id": 1, "issue_key": "WI-11", "type": "epic", "title": "Parent", "status": "open"},
		{"id": 12, "project_id": 1, "issue_key": "WI-12", "type": "ticket", "title": "Child", "status": "done", "parent_id": 11},
	}}}}
	writer := PostgresWriter{Pool: d.App}
	if _, err = writer.Write(ctx, snap, "parent-import"); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			var state string
			err := tx.QueryRow(ctx, `SELECT state FROM nodes WHERE key='WI-11'`).Scan(&state)
			if err == nil && state != want {
				t.Fatalf("imported parent %s want %s", state, want)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	check("done")
	replay, err := writer.Write(ctx, snap, "parent-import")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range replay.Conflicts {
		if c.Reason != "parent_status_derived" {
			t.Fatalf("derived status appears as person edit %+v", replay)
		}
	}
	snap.Projects[0].Issues[1]["status"] = "in_progress"
	if _, err = writer.Write(ctx, snap, "parent-import"); err != nil {
		t.Fatal(err)
	}
	check("in_progress")
}
