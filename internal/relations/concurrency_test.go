// SPDX-License-Identifier: AGPL-3.0-only
package relations

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestRelationDisjointEdgesCannotCloseCycle(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprint("restore=", restore), func(t *testing.T) {
			f := setup(t)
			var d string
			if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'TSK-4',id,'Fourth' FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, f.a.TenantID).Scan(&d); err != nil {
				t.Fatal(err)
			}
			a, b, c := f.nodes[0], f.nodes[1], f.nodes[2]
			create(t, f, b, c, "blocks")
			create(t, f, d, a, "blocks")
			method, path, body := "POST", "/api/relations", fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"blocks"}`, c, d)
			if restore {
				r := create(t, f, c, d, "blocks")
				expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+r.ID, ""), 204)
				var eventID int64
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE tenant_id=$1 AND type='relation.deleted'`, f.a.TenantID).Scan(&eventID); err != nil {
					t.Fatal(err)
				}
				path, body = fmt.Sprintf("/api/events/%d/undo", eventID), ""
			}
			pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool { return strings.Contains(sql, "INSERT INTO node_relations") })
			mux := http.NewServeMux()
			New(pool).Mount(mux)
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", "/api/relations", strings.NewReader(fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"blocks"}`, a, b)))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(ctx, f.a)))
				first <- w
			}()
			pid := barrier.Wait(t, ctx)
			second := make(chan *httptest.ResponseRecorder, 1)
			done := make(chan struct{})
			go func() { second <- request(f.handler, f.a, method, path, body); close(done) }()
			lock := dbtest.BlockedOrDone(t, ctx, f.db.Admin, pid, done)
			barrier.Release()
			expect(t, dbtest.Await(t, ctx, first), 201)
			w := dbtest.Await(t, ctx, second)
			if lock != "advisory" {
				t.Errorf("graph write was not serialized before validation: lock=%q", lock)
			}
			expect(t, w, 409)
		})
	}
}
