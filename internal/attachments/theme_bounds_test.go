// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"net/http"
	"testing"
)

func TestMetadataLookupHonorsNodeVisibilityAndDeletion(t *testing.T) {
	d, p, node := setup(t)
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by) VALUES($1,$2,repeat('a',64),'file','text/plain',4,$3) RETURNING id::text`, p.TenantID, node, p.ID).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App, Store{}).Mount(mux)
	w := request(t, mux, p, "GET", "/api/attachments/"+id, "", nil)
	if w.Code != 200 {
		t.Fatalf("metadata status %d: %s", w.Code, w.Body.String())
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, node)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/content"} {
		w = request(t, mux, p, "GET", "/api/attachments/"+id+suffix, "", nil)
		if w.Code != 404 {
			t.Fatalf("deleted-node attachment leaked through %q: %d", suffix, w.Code)
		}
	}
}
