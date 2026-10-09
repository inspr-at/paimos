// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: deleting a lead erases a running generation, bypasses current authority,
// or commits without audit. Legacy coordinator registrations are unrelated.
func TestProjectLeadRemovalAuditsOnlyNeverStartedIntent(t *testing.T) {
	f := projectLeadFixture(t, readyLeadChecks)
	path := "/api/projects/" + f.project + "/lead"
	leadCandidate(t, f)
	l := startLead(t, f, 0)
	w := f.call(f.person, "POST", path+"/pause", map[string]any{"expected_revision": l["revision"], "generation": 0}, "")
	expect(t, w, 200)
	l = decode(t, w)
	expect(t, f.call(f.person, "DELETE", path, map[string]any{"expected_revision": 1}, ""), 409)
	w = f.call(f.person, "DELETE", path, map[string]any{"expected_revision": l["revision"]}, "")
	expect(t, w, 200)
	next := decode(t, w)
	if next["state"] != "none" || next["revision"] != float64(0) || next["session_id"] != nil {
		t.Fatalf("removed projection=%v", next)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var rows, generations, audits int
		if err := tx.QueryRow(t.Context(), `SELECT
		 (SELECT count(*) FROM project_leads WHERE project_id=$1),
		 (SELECT count(*) FROM project_lead_generations WHERE project_id=$1),
		 (SELECT count(*) FROM events WHERE node_id=$1 AND type='lead.removed'
		 AND actor_principal_id=$2 AND "before"->>'state'='paused'
		 AND ("before"->>'revision')::bigint=$3 AND "after"->>'state'='none')`, f.project, f.person.ID, l["revision"]).Scan(&rows, &generations, &audits); err != nil {
			return err
		}
		if rows != 0 || generations != 0 || audits != 1 {
			t.Fatalf("rows=%d generations=%d audits=%d", rows, generations, audits)
		}
		return nil
	})
	expect(t, f.call(f.person, "DELETE", path, map[string]any{"expected_revision": l["revision"]}, ""), 409)
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if decode(t, w)["state"] != "none" {
		t.Fatal("read resurrected the removed lead")
	}
	recreated := startLead(t, f, 0)
	if recreated["revision"].(float64) <= l["revision"].(float64) {
		t.Fatal("recreated intent reused a removed revision")
	}
	w = f.call(f.person, "DELETE", path, map[string]any{"expected_revision": l["revision"]}, "")
	expect(t, w, 409)
	if decode(t, w)["error"] != "lead revision conflict" {
		t.Fatal("stale confirmation crossed a remove/recreate boundary")
	}
}

func TestProjectLeadRemovalRejectsStartedAndHistoricalGenerations(t *testing.T) {
	for _, mode := range []string{"running", "stopped", "generation_only", "session_only", "history_only"} {
		t.Run(mode, func(t *testing.T) {
			f := projectLeadFixture(t, readyLeadChecks)
			session, lease, _ := leadCandidate(t, f)
			l := startLead(t, f, 0)
			l = claimLead(t, f, session, lease, l["revision"])
			if mode == "stopped" {
				expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/stop", map[string]any{"reason": "process_exited"}, lease), 200)
			}
			if mode == "generation_only" || mode == "session_only" || mode == "history_only" {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE project_leads SET generation=CASE WHEN $2 THEN generation ELSE 0 END,session_id=CASE WHEN $3 THEN session_id ELSE NULL END WHERE project_id=$1`, f.project, mode == "generation_only", mode == "session_only")
					return err
				})
			}
			w := f.call(f.person, "DELETE", "/api/projects/"+f.project+"/lead", map[string]any{"expected_revision": l["revision"]}, "")
			expect(t, w, 409)
			if decode(t, w)["error"] != "only a never-started lead can be removed" {
				t.Fatal("started lead refused for the wrong reason")
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var rows, generations, audits int
				if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM project_leads WHERE project_id=$1),(SELECT count(*) FROM project_lead_generations WHERE project_id=$1),(SELECT count(*) FROM events WHERE node_id=$1 AND type='lead.removed')`, f.project).Scan(&rows, &generations, &audits); err != nil {
					return err
				}
				if rows != 1 || generations != 1 || audits != 0 {
					t.Fatal("refusal changed the lead or its audit/history")
				}
				return nil
			})
		})
	}
}

func TestProjectLeadRemovalRequiresPersonAndCurrentControl(t *testing.T) {
	f := projectLeadFixture(t, readyLeadChecks)
	l := startLead(t, f, 0)
	path := "/api/projects/" + f.project + "/lead"
	body := map[string]any{"expected_revision": l["revision"]}
	agent := f.agent
	agent.Scopes = []string{"harness.control"}
	expect(t, f.call(agent, "DELETE", path, body, ""), 403)
	expect(t, f.call(f.foreign, "DELETE", path, body, ""), 403)
	for _, bad := range []any{map[string]any{}, map[string]any{"expected_revision": 0}, map[string]any{"expected_revision": 1, "force": true}} {
		expect(t, f.call(f.person, "DELETE", path, bad, ""), 400)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
		return err
	})
	expect(t, f.call(f.person, "DELETE", path, body, ""), 403)
}

// Barriers establish overlap at the project fence; the timeout only guards a hang.
func TestProjectLeadRemovalSerializesClaimAndRevocation(t *testing.T) {
	for _, mode := range []string{"claim", "revocation"} {
		t.Run(mode, func(t *testing.T) {
			f := projectLeadFixture(t, readyLeadChecks)
			session, lease, _ := leadCandidate(t, f)
			l := startLead(t, f, 0)
			barrier := &leadBarrier{locked: make(chan uint32, 1), resume: make(chan struct{})}
			cfg := f.db.App.Config()
			cfg.ConnConfig.Tracer = barrier
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			f.mux = http.NewServeMux()
			harness.NewWithLeadAdmission(pool, readyLeadChecks).Mount(f.mux)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			defer func() {
				select {
				case <-barrier.resume:
				default:
					close(barrier.resume)
				}
			}()
			first := make(chan error, 1)
			go func() {
				if mode == "claim" {
					w := f.call(f.agent, "POST", "/api/projects/"+f.project+"/lead/claim", map[string]any{"expected_revision": l["revision"], "session_id": session}, lease)
					if w.Code != 200 {
						first <- context.Canceled
						return
					}
					first <- nil
					return
				}
				first <- db.InTenant(dbtest.Seed(ctx), pool, f.person.TenantID, func(tx pgx.Tx) error {
					if err := db.LockWorkTreeTx(ctx, tx); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
					return err
				})
			}()
			var pid uint32
			select {
			case pid = <-barrier.locked:
			case <-ctx.Done():
				t.Fatal("first writer did not enter fence")
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.call(f.person, "DELETE", "/api/projects/"+f.project+"/lead", map[string]any{"expected_revision": l["revision"]}, "")
			}()
			for {
				var waiting bool
				if err := f.db.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
			}
			close(barrier.resume)
			select {
			case err := <-first:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("first writer hung")
			}
			var w *httptest.ResponseRecorder
			select {
			case w = <-done:
			case <-ctx.Done():
				t.Fatal("remove hung")
			}
			if mode == "claim" {
				expect(t, w, 409)
				if decode(t, w)["error"] != "lead revision conflict" {
					t.Fatal("claim conflict refused for wrong reason")
				}
			} else {
				expect(t, w, 403)
			}
		})
	}
}
