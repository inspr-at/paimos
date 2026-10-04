// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type exitBodyBarrier struct {
	*io.PipeReader
	started chan struct{}
	once    sync.Once
}

func (b *exitBodyBarrier) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.PipeReader.Read(p)
}

func TestWorkLeafExitBodyReadBeforeLocks(t *testing.T) {
	for _, revoke := range []string{"none", "key", "permission"} {
		t.Run(revoke, func(t *testing.T) {
			f := fixture(t)
			base := "/api/projects/" + f.project + "/harness-sessions"
			reg := pauseRegistration(f)
			w := f.call(f.person, "POST", base, reg, "")
			expect(t, w, 201)
			id := decode(t, w)["id"].(string)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped',stop_reason='heartbeat_lost' WHERE id=$1`, id)
				return err
			})

			// No clock threshold establishes overlap: the reader signals entry,
			// then cannot finish until this test supplies the body below.
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			reader, writer := io.Pipe()
			body := &exitBodyBarrier{PipeReader: reader, started: make(chan struct{})}
			r := httptest.NewRequest("POST", base+"/"+id+"/confirm-exit", body).WithContext(tenant.WithPrincipal(ctx, f.agent))
			r.Header.Set("Authorization", "Bearer "+f.key)
			r.Header.Set("X-Aeon-Worker-Lease", reg["worker_lease"].(string))
			w = httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				f.mux.ServeHTTP(w, r)
			}()
			defer func() {
				_ = writer.CloseWithError(context.Canceled)
				_ = reader.Close()
				select {
				case <-done:
				case <-ctx.Done():
					t.Error("body-reading handler did not finish")
				}
			}()
			select {
			case <-body.started:
			case <-done:
				t.Fatalf("handler returned before reading body: %d %s", w.Code, w.Body.String())
			case <-ctx.Done():
				t.Fatal("handler never entered body read")
			}

			probe, err := f.db.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = probe.Rollback(context.Background()) }()
			for _, key := range []string{"aeon-pairing:" + f.person.TenantID, f.person.TenantID} {
				var acquired bool
				if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, key).Scan(&acquired); err != nil {
					t.Fatal(err)
				}
				if !acquired {
					t.Fatal("stalled request body holds the pairing/tree fence")
				}
			}
			if _, err := probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, f.person.TenantID); err != nil {
				t.Fatalf("stalled request body holds tenant row: %v", err)
			}
			if err := probe.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if n := f.db.App.Stat().AcquiredConns(); n != 0 {
				t.Fatalf("stalled request body retains %d database connections", n)
			}

			// Revocation completes while the body is still unread. The final
			// mutation must recheck both key scope and target permission.
			if revoke != "none" {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					if err := db.LockWorkTreeTx(ctx, tx); err != nil {
						return err
					}
					query := `UPDATE agent_keys SET scopes=ARRAY[]::text[] WHERE principal_id=$1`
					if revoke == "permission" {
						query = `DELETE FROM role_bindings WHERE principal_id=$1`
					}
					_, err := tx.Exec(ctx, query, f.agent.ID)
					return err
				})
			}
			if _, err := io.WriteString(writer, `{"reason":"process_exited"}`); err != nil {
				t.Fatal(err)
			}
			_ = writer.Close()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("confirmation did not finish after body read")
			}
			wantReason, wantStatus := "process_exited", http.StatusOK
			if revoke != "none" {
				wantReason, wantStatus = "heartbeat_lost", http.StatusForbidden
			}
			expect(t, w, wantStatus)
			if revoke == "key" && !strings.Contains(w.Body.String(), "key scope required: harness.worker") {
				t.Fatalf("key revocation failed for wrong reason: %s", w.Body.String())
			}
			if revoke == "permission" && !strings.Contains(w.Body.String(), "forbidden") {
				t.Fatalf("permission revocation failed for wrong reason: %s", w.Body.String())
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var reason string
				if err := tx.QueryRow(ctx, `SELECT stop_reason FROM harness_sessions WHERE id=$1`, id).Scan(&reason); err != nil {
					return err
				}
				if reason != wantReason {
					t.Fatalf("stored stop reason=%s, want %s", reason, wantReason)
				}
				return nil
			})
		})
	}
}

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
