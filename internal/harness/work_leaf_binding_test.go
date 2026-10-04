// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestWorkLeafRegistrationBindingAndGracefulDeadline(t *testing.T) {
	f := fixtureWithKind(t, nil, "work")
	base := "/api/projects/" + f.project + "/harness-sessions"
	reg := pauseRegistration(f)
	reg["advertised_capabilities"] = []string{"inbox", "pause"}
	reg["worker_lease"] = "work-leaf-lease-00000000000000000001"
	reply := f.call(f.agent, "POST", base, reg, "")
	expect(t, reply, 201)
	session := decode(t, reply)["id"].(string)
	// Deadline expiry is deterministic fixture data. It may expire the request;
	// it cannot promote this lifecycle handover to interrupt or process stop.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
			return err
		}
		flush, err := harness.PrepareWorkHandover(t.Context(), tx, f.person, []string{f.ticket}, "deadline-test")
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET pause_record=jsonb_set(pause_record,'{deadline_at}','"2000-01-01T00:00:00Z"'::jsonb) WHERE id=$1`, session); err != nil {
			return err
		}
		return flush()
	})
	if err != nil {
		t.Fatal(err)
	}
	reply = f.call(f.agent, "POST", base+"/"+session+"/heartbeat", map[string]any{"activity_sequence": 1, "phase": "working", "activity": "busy"}, "work-leaf-lease-00000000000000000001")
	expect(t, reply, 200)
	var signals int
	var stopped bool
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_controls WHERE session_id=$1 AND (kind='interrupt' OR request_payload->>'stop_now'='true')`, session).Scan(&signals); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT stopped_at IS NOT NULL FROM harness_sessions WHERE id=$1`, session).Scan(&stopped)
	})
	if signals != 0 || stopped {
		t.Fatal("expired handover gained force-stop authority")
	}
	// Stop the generation without rebinding history, then add a work child.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=$2,stop_reason='completed' WHERE id=$1`, session, time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
		return err
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,'WORK-3',id,'Child',$2 FROM node_kinds WHERE slug='work'`, f.person.TenantID, f.ticket)
		return err
	})
	reg["harness_session_ref"] = "work-leaf-reference-0002"
	reg["worker_lease"] = "work-leaf-lease-00000000000000000002"
	reply = f.call(f.agent, "POST", base, reg, "")
	expect(t, reply, 409)
	if decode(t, reply)["code"] != "work_leaf_required" {
		t.Fatal("registration failed for the wrong reason")
	}
	var original string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT ticket_node_id::text FROM harness_sessions WHERE id=$1`, session).Scan(&original)
	})
	if original != f.ticket {
		t.Fatal("historical session identity changed")
	}
	// A pre-guard lost-contact history may already sit on a parent. Allowing
	// same-generation recovery must still preserve leaf-only admission.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stop_reason='heartbeat_lost' WHERE id=$1`, session)
		return err
	})
	reply = f.call(f.agent, "POST", base+"/"+session+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 2}, "work-leaf-lease-00000000000000000001")
	expect(t, reply, 409)
	if decode(t, reply)["code"] != "work_leaf_required" {
		t.Fatal("historical parent revival failed for wrong reason")
	}
}

// Wait for the database's actual blocker graph; the timeout only guards hangs.
func waitWorkFence(t *testing.T, ctx context.Context, f *harnessFixture, pid uint32, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	for {
		var blocked bool
		if err := f.db.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case w := <-done:
			t.Fatalf("generation request bypassed barrier: %d %s", w.Code, w.Body.String())
		default:
		}
		runtime.Gosched()
	}
}
func generationRequest(t *testing.T, ctx context.Context, f *harnessFixture, p tenant.Principal, path string, body any, lease string) <-chan *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(ctx, p))
	if p.Kind == tenant.Agent {
		r.Header.Set("Authorization", "Bearer "+f.key)
	}
	if lease != "" {
		r.Header.Set("X-Aeon-Worker-Lease", lease)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); f.mux.ServeHTTP(w, r); done <- w }()
	return done
}

func TestWorkLeafRecoveryFencesBeforeResourceLocks(t *testing.T) {
	for _, admission := range []string{"revive", "resume", "restore", "confirm-exit"} {
		t.Run(admission, func(t *testing.T) {
			f := fixtureWithKind(t, nil, "work")
			// Rollout explicitly OFF: its status triggers must not mask missing locks.
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `CREATE TABLE IF NOT EXISTS features(tenant_id uuid NOT NULL,key text NOT NULL,project_id uuid,enabled boolean NOT NULL); INSERT INTO features VALUES(current_setting('aeon.tenant_id')::uuid,'work-parent-status',NULL,false)`)
				return err
			})
			reg := pauseRegistration(f)
			path, lease, control := pauseSession(t, f, reg)
			sid := strings.TrimPrefix(path, "/api/projects/"+f.project+"/harness-sessions/")
			who := f.agent
			body := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}
			endpoint := path + "/heartbeat"
			if admission == "revive" {
				age(t, f, sid, "16 minutes", false)
				if n := sweep(t, f); n != 1 {
					t.Fatalf("sweep=%d", n)
				}
			} else if admission == "confirm-exit" {
				expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "ownership_lost"}, lease), 200)
				endpoint = path + "/confirm-exit"
				body = map[string]any{"reason": "process_exited"}
			} else if admission == "restore" {
				events.New(f.db.App, events.WithUndoHandlers(harness.UndoHandlers())).Mount(f.mux)
				w := f.call(f.person, "POST", path+"/remove", map[string]any{"reason": "Remove lost registration"}, "")
				expect(t, w, 200)
				endpoint = fmt.Sprintf("/api/events/%.0f/undo", decode(t, w)["event_id"].(float64))
				who = f.person
				lease = ""
				body = map[string]any{}
			} else {
				finishPaused(t, f, path, lease, control)
				who = f.person
				lease = ""
				endpoint = path + "/resume"
				body = map[string]any{"registration": map[string]any{"harness_session_ref": "fence-ref-" + uid(), "worker_lease": "fence-lease-" + uid()}}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			held, err := f.db.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Rollback(context.Background())
			// Only the tree key: a missing admission fence can reach the session first.
			if _, err = held.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, f.person.TenantID); err != nil {
				t.Fatal(err)
			}
			done := generationRequest(t, ctx, f, who, endpoint, body, lease)
			waitWorkFence(t, ctx, f, held.Conn().PgConn().PID(), done)
			if _, err = held.Exec(ctx, `SELECT id FROM harness_sessions WHERE id=$1 FOR UPDATE NOWAIT`, sid); err != nil {
				t.Fatalf("%s locked session before tree fence: %v", admission, err)
			}
			if _, err = held.Exec(ctx, `SELECT last_id FROM event_counters WHERE tenant_id=$1 FOR UPDATE NOWAIT`, f.person.TenantID); err != nil {
				t.Fatalf("%s locked event counter before admission: %v", admission, err)
			}
			if err = held.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				expected := 200
				if admission == "restore" {
					expected = 201
				}
				expect(t, w, expected)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestWorkLeafResumeDefersEventsUntilSuccessorLocks(t *testing.T) {
	f := fixtureWithKind(t, nil, "work")
	path, lease, control := pauseSession(t, f, pauseRegistration(f))
	finishPaused(t, f, path, lease, control)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// An AFTER INSERT barrier makes successor admission overlap a counter probe.
	// Each fixture has its own database; the trigger affects this test only.
	if _, err := f.db.Admin.Exec(ctx, `CREATE FUNCTION test_successor_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(652002); RETURN NEW; END $$;
 CREATE TRIGGER test_successor_barrier AFTER INSERT ON harness_sessions FOR EACH ROW EXECUTE FUNCTION test_successor_barrier()`); err != nil {
		t.Fatal(err)
	}
	held, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(context.Background())
	if _, err = held.Exec(ctx, `SELECT pg_advisory_xact_lock(652002)`); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"registration": map[string]any{"harness_session_ref": "events-ref-" + uid(), "worker_lease": "events-lease-" + uid()}}
	done := generationRequest(t, ctx, f, f.person, path+"/resume", body, "")
	waitWorkFence(t, ctx, f, held.Conn().PgConn().PID(), done)
	if _, err = held.Exec(ctx, `SELECT last_id FROM event_counters WHERE tenant_id=$1 FOR UPDATE NOWAIT`, f.person.TenantID); err != nil {
		t.Fatalf("resume appended an event before successor insert finished: %v", err)
	}
	if err = held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		expect(t, w, 200)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestWorkLeafPendingLostContactCanReviveAndStop(t *testing.T) {
	f := fixtureWithKind(t, nil, "work")
	reg := pauseRegistration(f)
	path, lease, control := pauseSession(t, f, reg)
	sid := strings.TrimPrefix(path, "/api/projects/"+f.project+"/harness-sessions/")
	action := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO work_lifecycle_actions(tenant_id,id,node_id,requested_by,kind,targets) VALUES($1,$2,$3::uuid,$4,'split',jsonb_build_array(jsonb_build_object('id',$3::text,'updated_at','2000-01-01T00:00:00Z')))`, f.person.TenantID, action, f.ticket, f.person.ID)
		return err
	})
	age(t, f, sid, "16 minutes", false)
	if n := sweep(t, f); n != 1 {
		t.Fatalf("sweep=%d", n)
	}
	w := f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, lease)
	expect(t, w, 200)
	if got := decode(t, w); got["stopped_at"] != nil {
		t.Fatal("same generation did not revive")
	}
	// Existing same-binding progress remains admissible; a new generation is fenced.
	reg["harness_session_ref"] = fmt.Sprint("new-ref-", uid())
	reg["worker_lease"] = fmt.Sprint("new-lease-", uid())
	w = f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", reg, "")
	expect(t, w, 409)
	if decode(t, w)["code"] != "work_handover_pending" {
		t.Fatal("admission failed for wrong reason")
	}
	finishPaused(t, f, path, lease, control)
}
