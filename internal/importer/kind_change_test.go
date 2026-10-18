// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestImportRefusesKindChange(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	if err := db.EnsureTenant(ctx, d.Admin, "kind-import", "Kind import"); err != nil {
		t.Fatal(err)
	}
	snap := func(typ, title, otherTitle string, parent bool) Snapshot {
		row := Record{"id": json.Number("11"), "issue_key": "KI-11", "type": typ, "title": title, "status": "open"}
		if parent {
			row["parent_id"] = json.Number("12")
		}
		return Snapshot{SourceID: "kind-import-source", Details: map[int64]Details{}, Projects: []Project{{
			Record: Record{"id": json.Number("1"), "key": "KI", "name": "Kind", "status": "active"},
			Issues: []Record{
				row,
				{"id": json.Number("12"), "issue_key": "KI-12", "type": "ticket", "title": otherTitle, "status": "open"},
			},
		}}}
	}
	writer := PostgresWriter{Pool: d.App}
	if _, err := writer.Write(ctx, snap("ticket", "Stay", "Other", false), "kind-import"); err != nil {
		t.Fatal(err)
	}
	var tid string
	if err := d.Admin.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug='kind-import'`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	read := func(key string) (kind, title, parent string) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT k.slug, n.title, coalesce(n.parent_id::text,'') FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND n.key=$2`, tid, key).Scan(&kind, &title, &parent)
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if kind, title, parent := read("KI-11"); kind != "work" || title != "Stay" || parent == "" {
		t.Fatalf("seed kind %s title %s parent %q", kind, title, parent)
	}
	beforeKind, beforeTitle, beforeParent := read("KI-11")
	report, err := writer.Write(ctx, snap("memory", "Changed", "Updated", true), "kind-import")
	if err != nil {
		t.Fatal(err)
	}
	if report.Updated != 1 || len(report.Conflicts) != 1 {
		t.Fatalf("report updated %d conflicts %#v", report.Updated, report.Conflicts)
	}
	conflict := report.Conflicts[0]
	if conflict.Reason != "kind_change_not_allowed" || conflict.Key != "KI-11" || conflict.CurrentKind != "work" || conflict.RequestedKind != "memory" || conflict.ClassicID != 11 {
		t.Fatalf("conflict %#v", conflict)
	}
	kind, title, parent := read("KI-11")
	if kind != beforeKind || title != beforeTitle || parent != beforeParent {
		t.Fatalf("conflicted row changed from %s %s %s to %s %s %s", beforeKind, beforeTitle, beforeParent, kind, title, parent)
	}
	if kind, title, _ := read("KI-12"); kind != "work" || title != "Updated" {
		t.Fatalf("other row %s %s", kind, title)
	}
}

func TestImportKindConflictSkipsRelation(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	if err := db.EnsureTenant(ctx, d.Admin, "kind-import-rel", "Kind import relations"); err != nil {
		t.Fatal(err)
	}
	base := func(typ, other string) Snapshot {
		return Snapshot{
			SourceID: "kind-import-rel-source",
			Details:  map[int64]Details{},
			Projects: []Project{{
				Record: Record{"id": json.Number("1"), "key": "KR", "name": "Kind", "status": "active"},
				Issues: []Record{
					{"id": json.Number("11"), "issue_key": "KR-11", "type": typ, "title": "Stay", "status": "open"},
					{"id": json.Number("12"), "issue_key": "KR-12", "type": "ticket", "title": other, "status": "open"},
					{"id": json.Number("13"), "issue_key": "KR-13", "type": "ticket", "title": "Third", "status": "open"},
				},
			}},
		}
	}
	writer := PostgresWriter{Pool: d.App}
	if _, err := writer.Write(ctx, base("ticket", "Other"), "kind-import-rel"); err != nil {
		t.Fatal(err)
	}
	var tid string
	if err := d.Admin.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug='kind-import-rel'`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	read := func(key string) (kind, title, parent string) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT k.slug, n.title, coalesce(n.parent_id::text,'') FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND n.key=$2`, tid, key).Scan(&kind, &title, &parent)
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	relations := func(key string) int {
		t.Helper()
		var n int
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM node_relations nr WHERE nr.tenant_id=$1 AND (nr.source_node_id IN (SELECT id FROM nodes WHERE tenant_id=$1 AND key=$2) OR nr.target_node_id IN (SELECT id FROM nodes WHERE tenant_id=$1 AND key=$2))`, tid, key).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	beforeKind, beforeTitle, beforeParent := read("KR-11")
	if beforeKind != "work" || beforeTitle != "Stay" || beforeParent == "" || relations("KR-11") != 0 {
		t.Fatalf("seed %s %s parent %q relations %d", beforeKind, beforeTitle, beforeParent, relations("KR-11"))
	}
	second := base("memory", "Updated")
	second.Details = map[int64]Details{12: {Relations: []Record{
		{"id": json.Number("90"), "source_id": json.Number("12"), "target_id": json.Number("11"), "type": "parent"},
		{"id": json.Number("91"), "source_id": json.Number("12"), "target_id": json.Number("11"), "type": "blocks"},
		{"id": json.Number("92"), "source_id": json.Number("12"), "target_id": json.Number("13"), "type": "blocks"},
	}}}
	report, err := writer.Write(ctx, second, "kind-import-rel")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 1 {
		t.Fatalf("conflicts %#v", report.Conflicts)
	}
	conflict := report.Conflicts[0]
	if conflict.Reason != "kind_change_not_allowed" || conflict.Key != "KR-11" || conflict.CurrentKind != "work" || conflict.RequestedKind != "memory" || conflict.ClassicID != 11 {
		t.Fatalf("conflict %#v", conflict)
	}
	kind, title, parent := read("KR-11")
	if kind != beforeKind || title != beforeTitle || parent != beforeParent || relations("KR-11") != 0 {
		t.Fatalf("conflicted row changed from %s %s %s to %s %s %s relations %d", beforeKind, beforeTitle, beforeParent, kind, title, parent, relations("KR-11"))
	}
	if kind, title, _ := read("KR-12"); kind != "work" || title != "Updated" {
		t.Fatalf("other row %s %s", kind, title)
	}
	var kept int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM node_relations nr JOIN nodes s ON s.tenant_id=nr.tenant_id AND s.id=nr.source_node_id JOIN nodes t ON t.tenant_id=nr.tenant_id AND t.id=nr.target_node_id WHERE nr.tenant_id=$1 AND nr.type='blocks' AND s.key='KR-12' AND t.key='KR-13'`, tid).Scan(&kept)
	}); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("unrelated relation writes %d", kept)
	}
}
