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
	if kind, title, parent := read("KI-11"); kind != "ticket" || title != "Stay" || parent == "" {
		t.Fatalf("seed kind %s title %s parent %q", kind, title, parent)
	}
	beforeKind, beforeTitle, beforeParent := read("KI-11")
	report, err := writer.Write(ctx, snap("epic", "Changed", "Updated", true), "kind-import")
	if err != nil {
		t.Fatal(err)
	}
	if report.Updated != 1 || len(report.Conflicts) != 1 {
		t.Fatalf("report updated %d conflicts %#v", report.Updated, report.Conflicts)
	}
	conflict := report.Conflicts[0]
	if conflict.Reason != "kind_change_not_allowed" || conflict.Key != "KI-11" || conflict.CurrentKind != "ticket" || conflict.RequestedKind != "epic" || conflict.ClassicID != 11 {
		t.Fatalf("conflict %#v", conflict)
	}
	kind, title, parent := read("KI-11")
	if kind != beforeKind || title != beforeTitle || parent != beforeParent {
		t.Fatalf("conflicted row changed from %s %s %s to %s %s %s", beforeKind, beforeTitle, beforeParent, kind, title, parent)
	}
	if kind, title, _ := read("KI-12"); kind != "ticket" || title != "Updated" {
		t.Fatalf("other row %s %s", kind, title)
	}
}
