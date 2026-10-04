// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Pause the shipped pairing fence after tree acquisition, before tenant. Every
// real writer must wait without owning tenant, so the pairing caller can finish.
// PostgreSQL's wait graph proves overlap; deadlines only guard against hangs.
func TestPortalWritesSerializeWithPairingTreeBeforeTenant(t *testing.T) {
	for _, name := range []string{
		"portal-settings", "product-settings", "moderation", "market",
		"node-create", "node-update", "node-terminal", "node-delete", "node-move", "node-bulk", "node-convert",
		"wish-legacy", "wish-product", "vote-legacy", "vote-product", "correction-legacy", "correction-product",
	} {
		t.Run(name, func(t *testing.T) {
			f := productFixture(t)
			tid := makeTenant(t, f.d, "lock-order", "Lock order")
			admin := makePerson(t, f.d, tid, "Admin", "admin")
			product := insertNode(t, f.d, tid, "PPR-1", "portal_product", "Product", "summary", "published", "", "{}")
			configureFixtureProduct(t, f.d, tid, product, "first", "legacy", true)
			wish := insertNode(t, f.d, tid, "PWS-1", "portal_wish", "Wish", "summary", "published", product, "{}")
			ticket := insertNode(t, f.d, tid, "TKT-1", "ticket", "Ticket", "", "open", "", "{}")
			project := insertNode(t, f.d, tid, "PRJ-1", "project", "Project", "", "open", "", "{}")
			setPortal(t, f.d, tid, true)
			var kind string
			if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO portal_competitors(tenant_id,product_id,name,published,position) VALUES($1,$2,'Northwind',true,1)`, tid, product); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE slug='ticket'`).Scan(&kind)
			}); err != nil {
				t.Fatal(err)
			}

			method, path, body, event, effect, status := "", "", "", "", "", http.StatusOK
			person := true
			base := "/api/public/portal/lock-order"
			if strings.HasSuffix(name, "-product") {
				base += "/products/first"
			}
			switch name {
			case "portal-settings":
				method, path, body = "PATCH", "/api/portal/settings", `{"enabled":false}`
				event, effect = "portal.settings_updated", `SELECT count(*) FROM portal_settings WHERE NOT enabled`
			case "product-settings":
				method, path, body = "PUT", "/api/portal/products/"+product+"/settings", `{"revision":1,"slug":"first","published":true,"participation_policy":"disabled"}`
				event, effect = "portal.product_settings_updated", `SELECT count(*) FROM portal_products WHERE participation_policy='disabled' AND revision=2`
			case "moderation":
				method, path, body = "PATCH", "/api/portal/products/"+product, `{"summary":"Updated summary"}`
				event, effect = "portal.product_updated", `SELECT count(*) FROM nodes WHERE body='Updated summary'`
			case "market":
				method, path, body = "POST", "/api/portal/competitors", `{"name":"New competitor"}`
				event, effect = "portal.competitor_saved", `SELECT count(*) FROM portal_competitors WHERE name='New competitor'`
			case "node-create":
				method, path, body, status = "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Created ticket"}`, kind), http.StatusCreated
				event, effect = "node.created", `SELECT count(*) FROM nodes WHERE title='Created ticket'`
			case "node-update":
				method, path, body = "PATCH", "/api/nodes/"+ticket, `{"title":"Updated ticket"}`
				event, effect = "node.updated", `SELECT count(*) FROM nodes WHERE title='Updated ticket'`
			case "node-terminal":
				method, path, body = "PATCH", "/api/nodes/"+ticket, `{"state":"cancelled"}`
				event, effect = "node.updated", `SELECT count(*) FROM nodes WHERE state='cancelled'`
			case "node-delete":
				method, path, status = "DELETE", "/api/nodes/"+ticket, http.StatusNoContent
				event, effect = "node.deleted", `SELECT count(*) FROM nodes WHERE deleted_at IS NOT NULL`
			case "node-move":
				method, path, body = "POST", "/api/nodes/"+ticket+"/move", fmt.Sprintf(`{"parent_id":%q}`, project)
				event, effect = "node.moved", `SELECT count(*) FROM nodes WHERE key='TKT-1' AND parent_id IS NOT NULL`
			case "node-bulk":
				method, path, body = "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q],"priority":"high"}`, ticket)
				event, effect = "node.bulk_changed", `SELECT count(*) FROM nodes WHERE fields->>'priority'='high'`
			case "node-convert":
				method, path, body = "POST", "/api/nodes/"+ticket+"/convert", `{"to_kind":"task"}`
				event, effect = "node.kind_changed", `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='task'`
			case "wish-legacy", "wish-product":
				person = false
				method, path, body, status = "POST", base+"/wishes", `{"title":"New wish","summary":"New summary"}`, http.StatusCreated
				event, effect = "portal.wish_submitted", `SELECT count(*) FROM nodes WHERE title='New wish' AND state='pending'`
			case "vote-legacy", "vote-product":
				person = false
				method, path, body, status = "POST", base+"/wishes/PWS-1/votes", "{}", http.StatusCreated
				event, effect = "portal.vote_cast", `SELECT count(*) FROM portal_votes WHERE wish_id='`+wish+`'::uuid`
			case "correction-legacy", "correction-product":
				person = false
				method, path, body, status = "POST", base+"/corrections", `{"competitor":"Northwind","aspect":"One","statement":"New correction","source_url":"https://example.com"}`, http.StatusCreated
				event, effect = "portal.correction_submitted", `SELECT count(*) FROM portal_corrections WHERE statement='New correction'`
			}

			pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool {
				return strings.Contains(sql, "pg_advisory_xact_lock") && !strings.Contains(sql, "aeon-pairing:")
			})
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			paired := make(chan error, 1)
			pairedDone := make(chan struct{})
			go func() {
				defer close(pairedDone)
				paired <- db.InTenant(tenant.WithPrincipal(ctx, admin), pool, tid, func(tx pgx.Tx) error {
					if err := agentpairing.LockMutation(ctx, tx); err != nil {
						return err
					}
					return authz.RequireTx(ctx, tx, admin, "settings.manage", authz.Scope{})
				})
			}()
			pid := barrier.Wait(t, ctx)
			mux := http.NewServeMux()
			f.m.Mount(mux)
			nodes.New(f.d.App, nil).Mount(mux)
			req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "203.0.113.190:1"
			if person {
				req = req.WithContext(tenant.WithPrincipal(ctx, admin))
			}
			response, done := httptest.NewRecorder(), make(chan struct{})
			go func() { defer close(done); mux.ServeHTTP(response, req) }()
			t.Cleanup(func() {
				cancel()
				barrier.Release()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				dbtest.Await(t, cleanup, done)
				dbtest.Await(t, cleanup, pairedDone)
			})
			if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done); lock != "advisory" {
				t.Fatalf("writer must wait on the pairing/tree fence: lock=%q", lock)
			}
			probe, err := f.d.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE NOWAIT`, tid)
			_ = probe.Rollback(ctx)
			if err != nil {
				t.Fatalf("tree waiter must leave tenant free: %v", err)
			}
			barrier.Release()
			if err := dbtest.Await(t, ctx, paired); err != nil {
				t.Fatalf("pairing fence failed: %v", err)
			}
			dbtest.Await(t, ctx, done)
			if response.Code != status {
				t.Fatalf("writer: %d %s, want %d", response.Code, response.Body.String(), status)
			}
			if err := db.InTenant(dbtest.Seed(ctx), f.d.App, tid, func(tx pgx.Tx) error {
				var count int
				if err := tx.QueryRow(ctx, effect).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					return fmt.Errorf("durable effect count=%d, want 1", count)
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type=$1`, event).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					return fmt.Errorf("%s audit count=%d, want 1", event, count)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
