// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"context"
	"encoding/json"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"testing"
)

type contactReadWork struct {
	rows       int64
	statements int
}
type contactReadKey struct{}

func (w *contactReadWork) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, contactReadKey{}, q.SQL)
}
func (w *contactReadWork) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(contactReadKey{}).(string)
	if strings.Contains(sql, "crm_contact_profiles") {
		w.statements++
		w.rows += q.CommandTag.RowsAffected()
	}
}
func TestContactPageBatchesRecordsInsteadOfHydratingEachRow(t *testing.T) {
	f := setup(t)
	rec := jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Volume"})
	expect(t, rec, 201)
	var customer Customer
	if err := json.Unmarshal(rec.Body.Bytes(), &customer); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH created AS (
   INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'BOUND-'||g,$2,'Contact '||lpad(g::text,4,'0') FROM generate_series(1,250) g RETURNING tenant_id,id
   ) INSERT INTO crm_contact_profiles(tenant_id,contact_node_id,organisation_node_id) SELECT tenant_id,id,$3 FROM created`, f.admin.TenantID, f.contactKind, customer.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	work := &contactReadWork{}
	cfg := f.db.App.Config()
	cfg.ConnConfig.Tracer = work
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reg := plugins.NewRegistry()
	plug, err := Plugin()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	f.handler = (&httpapi.Server{Pool: pool, Modules: []httpapi.Module{New(pool, reg)}}).Handler()
	seen := map[string]bool{}
	after := ""
	for page := 0; page < 2; page++ {
		rec = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+customer.ID+"/contacts?limit=3&after_id="+after, "")
		expect(t, rec, 200)
		var rows []ContactRecord
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 || rec.Header().Get("X-Next-Cursor") == "" {
			t.Fatal("contact page not bounded or continuation missing")
		}
		for _, row := range rows {
			if seen[row.ID] {
				t.Fatal("repeated contact")
			}
			seen[row.ID] = true
		}
		after = rec.Header().Get("X-Next-Cursor")
	}
	if work.statements > 3 || work.rows > 9 {
		t.Fatalf("contacts hydrated per-row: %+v", work)
	}
}
