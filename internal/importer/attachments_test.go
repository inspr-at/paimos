// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestImportAttachmentsFixtureIdempotent(t *testing.T) {
	d := dbtest.Open(t)
	var tenantID, actorID, nodeID, kindID string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('importatt','Import') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Importer') RETURNING id::text`, tenantID).Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE tenant_id=$1 AND slug='work'`, tenantID).Scan(&kindID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,fields) VALUES($1,'IMP-11',$2,'Imported','{"classic":{"id":11,"source_id":"fixture"}}') RETURNING id::text`, tenantID, kindID).Scan(&nodeID)
	})
	if err != nil {
		t.Fatal(err)
	}
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/attachments/51" {
			w.WriteHeader(404) // file purged in the classic instance
			return
		}
		if r.Method != "GET" || r.URL.Path != "/api/attachments/50" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("unexpected source request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		gets++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("hello fixture"))
	}))
	defer server.Close()
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, []byte("fixture-key"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewHTTPSource(server.URL, keyPath, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_set(fields,'{classic,source_id}',to_jsonb($2::text)) WHERE tenant_id=$1 AND id=$3`, tenantID, source.InstanceID(), nodeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snap := Snapshot{SourceID: source.InstanceID(), Details: map[int64]Details{11: {Attachments: []Record{{"id": int64(50), "filename": "fixture.txt"}, {"id": int64(51), "filename": "gone.png"}}}}}
	store := attachments.Store{FilesDir: t.TempDir()}
	for i := 0; i < 2; i++ {
		count, err := ImportAttachments(t.Context(), d.App, store, source, snap, tenantID, actorID)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1-i {
			t.Fatalf("run %d created %d", i, count)
		}
	}
	if gets != 1 {
		t.Fatalf("download count %d", gets)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		var rows, eventsCount int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM attachments WHERE tenant_id=$1 AND node_id=$2`, tenantID, nodeID).Scan(&rows); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='attachment.added'`, tenantID).Scan(&eventsCount); err != nil {
			return err
		}
		if rows != 1 || eventsCount != 1 {
			t.Fatalf("rows=%d events=%d", rows, eventsCount)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
