// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWorkLeafUncertainExitConfirmation(t *testing.T) {
	for _, closure := range []string{"ownership_lost", "heartbeat_lost", "archived", "archived_after_ownership_lost", "removed"} {
		t.Run(closure, func(t *testing.T) {
			f := fixtureWithKind(t, nil, "work")
			base := "/api/projects/" + f.project + "/harness-sessions"
			reg := pauseRegistration(f)
			reg["management_mode"], reg["advertised_capabilities"] = "managed", []string{"stop", "inbox"}
			if closure == "heartbeat_lost" || closure == "archived" {
				reg["management_mode"], reg["advertised_capabilities"] = "unmanaged", []string{"inbox"}
			}
			w := f.call(f.person, "POST", base, reg, "")
			expect(t, w, 201)
			id, lease := decode(t, w)["id"].(string), reg["worker_lease"].(string)
			path := base + "/" + id
			// Real bearer authentication needs the tenant encoded in the prefix.
			// Only confirmation calls use the production middleware; setup keeps
			// its person principal without fabricating an authenticated session.
			parts := strings.Split(f.key, "_")
			prefix := strings.ReplaceAll(f.agent.TenantID, "-", "") + "0123456789abcdef"
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET prefix=$1,scopes=ARRAY['harness.worker'] WHERE principal_id=$2`, prefix, f.agent.ID)
				return err
			})
			f.key = "aeon_" + prefix + "_" + parts[2]
			mod, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, f.db.App)
			if err != nil {
				t.Fatal(err)
			}
			handler := (&httpapi.Server{Mux: f.mux, Pool: f.db.App, Middleware: []func(http.Handler) http.Handler{mod.Middleware}}).Handler()
			confirm := func(body any, proof string) *httptest.ResponseRecorder {
				t.Helper()
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", path+"/confirm-exit", bytes.NewReader(raw))
				r.Header.Set("Authorization", "Bearer "+f.key)
				r.Header.Set("X-Aeon-Worker-Lease", proof)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			body := map[string]any{"reason": "process_exited"}
			expect(t, confirm(body, lease), 409)
			if closure == "heartbeat_lost" {
				age(t, f, id, "20 minutes", false)
				if n := sweep(t, f); n != 1 {
					t.Fatalf("lost-contact sweep closed %d generations", n)
				}
			} else {
				switch closure {
				case "ownership_lost":
					expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "ownership_lost"}, lease), 200)
				case "archived", "archived_after_ownership_lost":
					if closure == "archived_after_ownership_lost" {
						expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "ownership_lost"}, lease), 200)
					}
					v := decode(t, f.call(f.person, "GET", path+"/recovery", nil, ""))
					expect(t, f.call(f.person, "POST", path+"/archive", map[string]any{"expected_revision": v["observed_revision"], "confirmation": v["confirmation"], "request_id": uid(), "reason": "Archive the unconfirmed generation"}, ""), 200)
				case "removed":
					expect(t, f.call(f.person, "POST", path+"/remove", map[string]any{"reason": "Remove the unconfirmed generation"}, ""), 200)
				}
			}
			var beforeStop, beforeArchive, receipt, originalBinding string
			f.tx(t, f.person, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT stopped_at::text,coalesce(archived_at::text,''),coalesce(recovery_request_id::text,''),ticket_node_id::text FROM harness_sessions WHERE id=$1`, id).Scan(&beforeStop, &beforeArchive, &receipt, &originalBinding)
			})
			assertBusy := func(want bool) {
				t.Helper()
				var busy bool
				f.tx(t, f.person, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT aeon_work_busy($1)`, f.ticket).Scan(&busy)
				})
				if busy != want {
					t.Fatalf("work busy=%t, want %t", busy, want)
				}
			}
			assertBusy(true)
			insertChild := func() error {
				return db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,'WORK-3',id,'After confirmed exit',$2 FROM node_kinds WHERE slug='work'`, f.person.TenantID, f.ticket)
					return err
				})
			}
			var pg *pgconn.PgError
			if err := insertChild(); !errors.As(err, &pg) || pg.ConstraintName != "busy_work_leaf" {
				t.Fatalf("uncertain stop did not retain work fence: %v", err)
			}
			expect(t, f.call(f.person, "POST", path+"/confirm-exit", body, lease), 403)
			expect(t, confirm(body, "wrong-generation-lease-00000000000000000"), 403)
			expect(t, confirm(map[string]any{"reason": "ownership_lost"}, lease), 400)
			// Current key authority remains necessary even with the exact lease.
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY[]::text[] WHERE principal_id=$1`, f.agent.ID)
				return err
			})
			expect(t, confirm(body, lease), 403)
			assertBusy(true)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.write'] WHERE principal_id=$1`, f.agent.ID)
				return err
			})
			expect(t, confirm(body, lease), 403)
			assertBusy(true)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.worker'] WHERE principal_id=$1`, f.agent.ID)
				return err
			})
			w = confirm(body, lease)
			expect(t, w, 200)
			confirmed := decode(t, w)
			if confirmed["stop_reason"] != "process_exited" {
				t.Fatalf("confirmation did not record exit: %v", confirmed)
			}
			assertBusy(false)
			expect(t, confirm(body, lease), 200)
			expect(t, confirm(map[string]any{"reason": "process_failed"}, lease), 409)
			// Confirmation cannot revive the historical generation.
			status := 403
			if beforeArchive != "" {
				status = 410
			}
			expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, lease), status)
			if err := insertChild(); err != nil {
				t.Fatalf("confirmed exit still blocked child creation: %v", err)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var stopped, archived, recoveryReceipt, binding string
				if err := tx.QueryRow(t.Context(), `SELECT stopped_at::text,coalesce(archived_at::text,''),coalesce(recovery_request_id::text,''),ticket_node_id::text FROM harness_sessions WHERE id=$1`, id).Scan(&stopped, &archived, &recoveryReceipt, &binding); err != nil {
					return err
				}
				if stopped != beforeStop || archived != beforeArchive || recoveryReceipt != receipt || binding != originalBinding {
					t.Fatal("exit confirmation rewrote generation identity or archival history")
				}
				var n int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.stop_confirmed' AND "after"->>'id'=$1 AND actor_principal_id=$2 AND "before"->>'stop_reason'<>'process_exited'`, id, f.agent.ID).Scan(&n); err != nil {
					return err
				}
				if n != 1 {
					t.Fatalf("authenticated exit audit events=%d, want 1", n)
				}
				return nil
			})
		})
	}
}
