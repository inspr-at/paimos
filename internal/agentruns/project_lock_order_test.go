// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/jackc/pgx/v5"
)

// Exercise both project authorization helpers through their HTTP writers while
// a real pairing caller owns tenant but has not yet acquired pairing/tree.
// The project waiter must leave tree free so the resumed caller can finish.
func TestProjectWritesSerializeWithPairingTenantBeforeTree(t *testing.T) {
	for _, pairedWrite := range []string{"queue", "terminal-node"} {
		for _, projectWrite := range []string{"membership", "attachment"} {
			t.Run(pairedWrite+"/"+projectWrite, func(t *testing.T) {
				f := setup(t)
				ticket := f.ticket(t, "open", "high", nil)
				var project, role, attachment string
				f.tx(t, f.person, func(tx pgx.Tx) error {
					ctx := t.Context()
					if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title)
 SELECT $1,id,'LOCK-1','Lock order' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
						return err
					}
					if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1 AND key='viewer'`, f.person.TenantID).Scan(&role); err != nil {
						return err
					}
					return tx.QueryRow(ctx, `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by)
 VALUES($1,$2,repeat('ab',32),'fixture.txt','text/plain',7,$3) RETURNING id::text`, f.person.TenantID, ticket, f.person.ID).Scan(&attachment)
				})
				pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(query string) bool {
					return strings.Contains(query, "FROM tenants") && strings.Contains(query, "FOR NO KEY UPDATE")
				})
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				mux := http.NewServeMux()
				agentruns.New(pool).Mount(mux)
				nodes.New(pool, nil).Mount(mux)
				authz.New(f.d.App).Mount(mux)
				attachments.New(f.d.App, attachments.Store{FilesDir: t.TempDir(), MaxSize: 1 << 20}).Mount(mux)
				start := func(method, path, body string) (*httptest.ResponseRecorder, <-chan struct{}) {
					w, done := httptest.NewRecorder(), make(chan struct{})
					r := httpRequest(ctx, f.person, method, path, body)
					go func() { defer close(done); mux.ServeHTTP(w, r) }()
					t.Cleanup(func() {
						cancel()
						barrier.Release()
						cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
						defer stop()
						dbtest.Await(t, cleanup, done)
					})
					return w, done
				}
				method, path, body := "POST", "/api/queue", `{"node_id":"`+ticket+`"}`
				if pairedWrite == "terminal-node" {
					method, path, body = "PATCH", "/api/nodes/"+ticket, `{"state":"cancelled"}`
				}
				paired, pairedDone := start(method, path, body)
				pairedPID := barrier.Wait(t, ctx)
				method, path, body = "PUT", "/api/projects/"+project+"/members/"+f.other.ID, `{"role_id":"`+role+`"}`
				if projectWrite == "attachment" {
					method, path, body = "PATCH", "/api/attachments/"+attachment, `{"caption":"updated"}`
				}
				projectResponse, projectDone := start(method, path, body)
				if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pairedPID, projectDone); lock != "transactionid" {
					t.Fatalf("project writer did not wait on the pairing caller's tenant fence: lock=%q", lock)
				}
				probe, err := f.d.Admin.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer probe.Rollback(context.Background())
				var treeFree bool
				if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, f.person.TenantID).Scan(&treeFree); err != nil {
					t.Fatal(err)
				}
				if !treeFree {
					t.Fatal("tenant waiter must leave tree free")
				}
				if err := probe.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				barrier.Release()
				dbtest.Await(t, ctx, pairedDone)
				dbtest.Await(t, ctx, projectDone)
				for name, response := range map[string]*httptest.ResponseRecorder{"paired": paired, "project": projectResponse} {
					if response.Code != http.StatusOK {
						t.Fatalf("%s writer failed: %d %s", name, response.Code, response.Body.String())
					}
				}
				// Check durable effects and exactly one audit from each real path.
				if pairedWrite == "queue" {
					if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1 AND status='queued'`, ticket); n != 1 {
						t.Fatalf("queued tickets=%d", n)
					}
				} else if n := f.count(t, f.person, `SELECT count(*) FROM nodes WHERE id=$1 AND state='cancelled'`, ticket); n != 1 {
					t.Fatalf("terminal tickets=%d", n)
				}
				event := map[string]string{"queue": "queue.added", "terminal-node": "node.updated"}[pairedWrite]
				if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type=$2`, ticket, event); n != 1 {
					t.Fatalf("pairing caller audit count=%d", n)
				}
				eventNode, event := project, "binding.set"
				if projectWrite == "membership" {
					if n := f.count(t, f.person, `SELECT count(*) FROM role_bindings WHERE principal_id=$1 AND scope_type='project' AND scope_id=$2 AND role_id=$3`, f.other.ID, project, role); n != 1 {
						t.Fatalf("project bindings=%d", n)
					}
				} else {
					eventNode, event = ticket, "attachment.updated"
					if n := f.count(t, f.person, `SELECT count(*) FROM attachments WHERE id=$1 AND caption='updated' AND deleted_at IS NULL`, attachment); n != 1 {
						t.Fatalf("updated attachments=%d", n)
					}
				}
				if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type=$2`, eventNode, event); n != 1 {
					t.Fatalf("project writer audit count=%d", n)
				}
			})
		}
	}
}
