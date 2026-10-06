// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestAuditNewestWindowBeyondCap(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	p := tenant.Principal{Kind: tenant.Person}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('audit-recency','Audit recency') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	p.TenantID = tid
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Audit owner') RETURNING id::text`, tid).Scan(&p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='owner'`, tid, p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,after)
			SELECT $1::uuid,$2::uuid,CASE WHEN n=2051 THEN 'agent_key.revoked' ELSE 'binding.set' END,'{}'::jsonb FROM generate_series(1,2051) n ORDER BY n`, tid, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	type page struct {
		Items  []auditItem `json:"items"`
		After  *int64      `json:"next_after"`
		Before *int64      `json:"next_before"`
	}
	get := func(query string) page {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/audit?category=access"+query, nil)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("audit status %d: %s", w.Code, w.Body.String())
		}
		var result page
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	newest := get("&order=desc")
	if len(newest.Items) != auditPage || newest.Items[0].Type != "agent_key.revoked" || newest.Before == nil || newest.After != nil {
		t.Fatalf("newest page omitted the recent revocation or cursor: %+v", newest)
	}
	last := newest.Items[0].ID + 1
	count := 0
	current := newest
	for i := 0; i < 40; i++ {
		for _, item := range current.Items {
			if item.ID != last-1 {
				t.Fatalf("descending page repeated or skipped its cursor: %d after %d", item.ID, last)
			}
			last = item.ID
			count++
		}
		if i < 39 {
			current = get("&order=desc&before=" + strconv.FormatInt(*current.Before, 10))
		}
	}
	if count != 2000 || current.Before == nil {
		t.Fatalf("bounded window count %d, older cursor %v", count, current.Before)
	}
	oldest := get("")
	if oldest.After == nil || oldest.Before != nil || oldest.Items[0].Type != "binding.set" {
		t.Fatalf("legacy pagination changed: %+v", oldest)
	}
	second := get("&after=" + strconv.FormatInt(*oldest.After, 10))
	if second.Items[0].ID <= *oldest.After {
		t.Fatal("legacy cursor repeated an event")
	}
	for _, query := range []string{"&order=wrong", "&order=desc&after=1", "&before=1", "&order=desc&before=-1", "&order=desc&before=bad"} {
		r := httptest.NewRequest("GET", "/api/audit?category=access"+query, nil)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Errorf("invalid cursor %q accepted: %d", query, w.Code)
		}
	}
}
