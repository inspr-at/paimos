// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestAEON587DeltaWaitsForNativeEditBeforeCheckingBaseline(t *testing.T) {
	d := dbtest.Open(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := db.EnsureTenant(ctx, d.Admin, "delta-lock", "Delta lock"); err != nil {
		t.Fatal(err)
	}
	snapshot := relationSnapshot("delta-lock-source", 3, []Record{issue(11, 3, "ticket", "DL-11", "Imported")}, nil)
	writer := PostgresWriter{Pool: d.App}
	if _, err := writer.Write(ctx, snapshot, "delta-lock"); err != nil {
		t.Fatal(err)
	}
	tid := tenantBySlug(t, d, "delta-lock")
	locked := make(chan int, 1)
	release := make(chan struct{})
	nativeDone := make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		nativeDone <- db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0))`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET title='Native winner' WHERE key='DL-11'`); err != nil {
				return err
			}
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			locked <- pid
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	var pid int
	select {
	case pid = <-locked:
	case err := <-nativeDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	snapshot.Projects[0].Issues[0]["title"] = "Newer source"
	type result struct {
		report Report
		err    error
	}
	done := make(chan result, 1)
	go func() { report, err := writer.Write(ctx, snapshot, "delta-lock"); done <- result{report, err} }()
	// Observe an actual database lock barrier. Before the fix, UPDATE waits
	// after inspecting an unlocked baseline; after the fix the tree read waits.
	for {
		var waiting bool
		if err := d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE $1=ANY(pg_blocking_pids(a.pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("import did not wait: %+v", result)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
	}
	close(release)
	if err := <-nativeDone; err != nil {
		t.Fatal(err)
	}
	outcome := <-done
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if len(outcome.report.Conflicts) != 1 || !strings.Contains(outcome.report.Conflicts[0].Reason, "changed since last import") {
		t.Fatalf("missing native conflict: %+v", outcome.report)
	}
	var title string
	if err := d.Admin.QueryRow(ctx, `SELECT title FROM nodes WHERE key='DL-11'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Native winner" {
		t.Fatalf("native edit overwritten: %q", title)
	}
}

func TestAEON587RelationBackfillKeepsSourceNamespaces(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	if err := db.EnsureTenant(ctx, d.Admin, "source-isolation", "Source isolation"); err != nil {
		t.Fatal(err)
	}
	writer := PostgresWriter{Pool: d.App}
	for index, source := range []string{"source-a", "source-b"} {
		project := int64(index + 1)
		prefix := strings.ToUpper(source)
		snapshot := relationSnapshot(source, project, []Record{issue(11, project, "ticket", prefix+"-11", "First"), issue(12, project, "ticket", prefix+"-12", "Second")}, []Record{{"source_id": 11, "target_id": 12, "type": "blocks"}})
		if _, err := writer.Write(ctx, snapshot, "source-isolation"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Admin.Exec(ctx, `DELETE FROM node_relations`); err != nil {
		t.Fatal(err)
	}
	report, err := BackfillRelations(ctx, d.App, tenantBySlug(t, d, "source-isolation"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Writes != 2 {
		t.Fatalf("source relations collapsed: %+v", report)
	}
	var crossSource int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM node_relations r JOIN nodes a ON a.id=r.source_node_id JOIN nodes b ON b.id=r.target_node_id WHERE a.fields->'classic'->>'source_id'<>b.fields->'classic'->>'source_id'`).Scan(&crossSource); err != nil {
		t.Fatal(err)
	}
	if crossSource != 0 {
		t.Fatal("relation crossed import source namespaces")
	}
	// An old unqualified event cannot guess between overlapping source IDs.
	tid := tenantBySlug(t, d, "source-isolation")
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		issues, err := loadImportedIssues(ctx, tx, tid)
		if err != nil {
			return err
		}
		_, _, err = classicRelationFromEvent([]byte(`{"record":{"source_id":11,"target_id":12,"type":"blocks"}}`), issues)
		if err == nil {
			t.Error("ambiguous historical relation was resolved")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
