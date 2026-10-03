// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/jackc/pgx/v5"
)

func TestKeyScopeUsageRegistrationVersusMoveLockOrder(t *testing.T) {
	for _, first := range []string{"registration", "move"} {
		t.Run(first, func(t *testing.T) {
			f := fixture(t)
			destination := uid()
			f.agent.Scopes = []string{"harness.worker", "nodes.move"}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id)
				 SELECT $1,$2,'HTS-3',id,'Destination epic',$3 FROM node_kinds WHERE slug='epic'`, f.person.TenantID, destination, f.project); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1 RETURNING id::text`, f.agent.ID, f.agent.Scopes).Scan(&f.agent.KeyID)
			})
			// The registration holds its usage grant before CapturePlanningStart
			// locks the ticket; the move holds that ticket before RequireInProjects.
			pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool {
				if first == "registration" {
					return strings.Contains(sql, "INSERT INTO agent_key_scope_usage")
				}
				return strings.Contains(sql, "FROM nodes n WHERE n.id") && strings.Contains(sql, "FOR UPDATE")
			})
			f.mux = http.NewServeMux()
			if first == "registration" {
				harness.New(pool, nodes.CapturePlanningStart).Mount(f.mux)
				nodes.New(f.db.App, nodes.SQLWriter{}).Mount(f.mux)
			} else {
				harness.New(f.db.App, nodes.CapturePlanningStart).Mount(f.mux)
				nodes.New(pool, nodes.SQLWriter{}).Mount(f.mux)
			}
			registration := func() *httptest.ResponseRecorder {
				return f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{
					"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker",
					"harness_session_ref": "lock-order-ref-" + uid(), "worker_lease": "lock-order-lease-" + uid(), "ticket_node_id": f.ticket, "work_shape": "ship",
				}, "")
			}
			move := func() *httptest.ResponseRecorder {
				return f.call(f.agent, "POST", "/api/nodes/"+f.ticket+"/move", map[string]any{"parent_id": destination}, "")
			}
			a, b := registration, move
			statusA, statusB := http.StatusCreated, http.StatusOK
			if first == "move" {
				a, b = move, registration
				statusA, statusB = statusB, statusA
			}
			firstDone, secondDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			go func() { firstDone <- a() }()
			holder := barrier.Wait(t, ctx)
			finished := make(chan struct{})
			go func() { secondDone <- b(); close(finished) }()
			lock := dbtest.BlockedOrDone(t, ctx, f.db.Admin, holder, finished)
			// Waiting on the tenant row at entry means the second operation has
			// acquired neither the ticket nor a conflicting usage lock yet.
			if lock != "transactionid" {
				t.Errorf("%s first: competing operation waited on %q, want tenant transactionid", first, lock)
			}
			barrier.Release()
			expect(t, dbtest.Await(t, ctx, firstDone), statusA)
			expect(t, dbtest.Await(t, ctx, secondDone), statusB)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var moved, registered, used bool
				err := tx.QueryRow(t.Context(), `SELECT
				 (SELECT parent_id=$3 FROM nodes WHERE id=$1),
				 EXISTS(SELECT 1 FROM harness_sessions WHERE ticket_node_id=$1),
				 (SELECT count(*)=2 FROM agent_key_scope_usage WHERE key_id=$2 AND scope IN ('harness.worker','nodes.move'))`, f.ticket, f.agent.KeyID, destination).Scan(&moved, &registered, &used)
				if err == nil && (!moved || !registered || !used) {
					t.Error("registration, move or usage evidence did not commit")
				}
				return err
			})
		})
	}
}
