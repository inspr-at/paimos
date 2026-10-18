// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// AEON-1414 race risk: polling accounts must not hold the tenant/tree write
// fence while a person saves a ticket. This exercises the real PATCH handler.
func TestCapacityReadSnapshot(t *testing.T) {
	d := dbtest.Open(t)
	var person tenant.Principal
	person.Kind = tenant.Person
	var account, node string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('capacity-read','Capacity read') RETURNING id::text`).Scan(&person.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, person.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Owner',ARRAY['admin']) RETURNING id::text`, person.TenantID).Scan(&person.ID); err != nil {
			return err
		}
		var agent string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Runner') RETURNING id::text`, person.TenantID).Scan(&agent); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,label,registered_by_principal_id,owner_person_id) VALUES($1,'main','codex','daemon','Main',$2,$3) RETURNING id::text`, person.TenantID, agent, person.ID).Scan(&account); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'READ-1',id,'Polling must not block edits' FROM node_kinds WHERE slug='work' RETURNING id::text`, person.TenantID).Scan(&node)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, person.TenantID, person.ID)
	request := func(ctx context.Context, mod httpapi.Module, method, path, body string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		mod.Mount(mux)
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.WithPrincipal(ctx, person))
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, r)
		return out
	}
	paths := []string{"/api/agent-accounts", "/api/agent-accounts/capacity", "/api/agent-accounts/groups", "/api/agent-accounts/pins?ticket_id=" + node, "/api/agent-accounts/capacity/schedule", "/api/agent-accounts/" + account + "/readings", "/api/agent-accounts/overview", "/api/agent-accounts/capacity/next?harness=codex"}
	for i, path := range paths {
		t.Run(path, func(t *testing.T) {
			pool, barrier, ctx := dbtest.BarrierPool(t, d.App, func(sql string) bool {
				return strings.Contains(sql, "FROM agent_accounts") || strings.Contains(sql, "FROM account_groups") || strings.Contains(sql, "FROM account_ticket_pins") || strings.Contains(sql, "FROM account_capacity_schedules")
			})
			read := make(chan *httptest.ResponseRecorder, 1)
			go func() { read <- request(ctx, agentaccounts.New(pool), "GET", path, "") }()
			holder := barrier.Wait(t, ctx)
			patch := make(chan *httptest.ResponseRecorder, 1)
			done := make(chan struct{})
			// One second guards a hung PATCH. The barrier and actual lock graph prove
			// the overlap; no sleep or elapsed-time threshold establishes ordering.
			patchCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			go func() {
				patch <- request(patchCtx, nodes.New(d.App, nil), "PATCH", "/api/nodes/"+node, fmt.Sprintf(`{"title":"Saved while reading %d"}`, i))
				close(done)
			}()
			if lock := dbtest.BlockedOrDone(t, patchCtx, d.Admin, holder, done); lock != "" {
				t.Fatalf("account GET blocks node PATCH on %s", lock)
			}
			if got := dbtest.Await(t, ctx, patch); got.Code != http.StatusOK {
				t.Fatalf("node PATCH: %d %s", got.Code, got.Body.String())
			}
			barrier.Release()
			if got := dbtest.Await(t, ctx, read); got.Code != http.StatusOK {
				t.Fatalf("account GET: %d %s", got.Code, got.Body.String())
			}
		})
	}
}
