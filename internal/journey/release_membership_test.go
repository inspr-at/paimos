// SPDX-License-Identifier: AGPL-3.0-only

package journey_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestOpenFirstReleaseWithFiveExistingTicketsAtomic(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-227", "Membership project")
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,decision,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256) VALUES($1,$2,now(),'go',1,1,$3)`, f.tenant, project, digest)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 5)
	for i := range ids {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,aeon_next_node_key($1,short_prefix),id,$3,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING nodes.id::text`, f.tenant, project, fmt.Sprintf("Existing %d", i+1)).Scan(&ids[i])
		}); err != nil {
			t.Fatal(err)
		}
	}
	view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.NextAction.Key != "open_first_release" {
		t.Fatalf("next action %+v", view.NextAction)
	}
	payload := map[string]any{"action": "open_first_release", "expected_revision": view.Revision, "idempotency_key": "bulk-new", "ticket_node_ids": ids}
	body, _ := json.Marshal(payload)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state='accepted' WHERE id=$1`, ids[4]); err != nil {
		t.Fatal(err)
	}
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", string(body)); w.Code != 409 {
		t.Fatalf("closed ticket %d %s", w.Code, w.Body.String())
	}
	if f.releaseCount(t, project) != 0 {
		t.Fatal("rejected ticket left a release")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state='open' WHERE id=$1`, ids[4]); err != nil {
		t.Fatal(err)
	}
	opened := f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", string(body))
	if opened.CurrentReleaseID == nil || opened.Stage != "requirements" {
		t.Fatalf("opened %+v", opened)
	}
	var count, scope int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE scope_revision_required) FROM journey_tickets WHERE project_node_id=$1 AND release_node_id=$2`, project, *opened.CurrentReleaseID).Scan(&count, &scope); err != nil {
		t.Fatal(err)
	}
	if count != 5 || scope != 5 {
		t.Fatalf("selected=%d scope=%d", count, scope)
	}
	replay := f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", string(body))
	if replay.CurrentReleaseID == nil || *replay.CurrentReleaseID != *opened.CurrentReleaseID || f.releaseCount(t, project) != 1 {
		t.Fatal("replay created another release")
	}
}

// Two application connections establish an actual database lock barrier. The
// direct request holds tenant/pairing locks; the atomic action must wait there before
// taking the journey row. NOWAIT detects the old inverse order deterministically
// instead of relying on a race or waiting for PostgreSQL's deadlock detector.
func TestAtomicReleaseMembershipLockOrder(t *testing.T) {
	for _, direct := range []string{"membership", "plan"} {
		t.Run(direct, func(t *testing.T) {
			f := newFixture(t)
			releases.New(f.db.App).Mount(f.mux)
			project := f.node(t, "project", "PRJ-229", "Concurrent release")
			prior := f.node(t, "release", "REL-229", "Release 1")
			ids := make([]string, 5)
			if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,decision,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256,current_release_node_id) VALUES($1,$2,now(),'go',1,1,$4,$3)`, f.tenant, project, prior, digest); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,1,'released',now())`, f.tenant, prior, project); err != nil {
					return err
				}
				for i := range ids {
					state := "open"
					if i == 4 {
						state = "done"
					}
					if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,state) SELECT $1,aeon_next_node_key($1,short_prefix),id,$3,$2,$4 FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING nodes.id::text`, f.tenant, project, fmt.Sprintf("Concurrent %d", i+1), state).Scan(&ids[i]); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
			payload, _ := json.Marshal(map[string]any{"action": "plan_next_release", "expected_revision": view.Revision, "idempotency_key": "barrier-five", "release_id": prior, "ticket_node_ids": ids})
			path := "/api/projects/" + project + "/journey/actions"
			baseline := f.eventCount(t)
			result := make(chan *httptest.ResponseRecorder, 1)
			ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), f.person), 10*time.Second)
			defer cancel()
			err := db.InTransaction(ctx, f.db.App, func(held context.Context) error {
				if err := db.InTenant(held, f.db.App, f.tenant, func(tx pgx.Tx) error {
					return db.LockWorkTreeTx(held, tx)
				}); err != nil {
					return err
				}
				go func() {
					req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload))).WithContext(ctx)
					w := httptest.NewRecorder()
					f.mux.ServeHTTP(w, req)
					result <- w
				}()
				if err := db.InTenant(held, f.db.App, f.tenant, func(tx pgx.Tx) error {
					var holder int
					if err := tx.QueryRow(held, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
						return err
					}
					for {
						var waiting bool
						// Match the blocked tenant fence to this connection;
						// another tenant or a runner timing delay cannot satisfy it.
						if err := f.db.Admin.QueryRow(held, `SELECT EXISTS(
 SELECT 1 FROM pg_stat_activity waiter
 WHERE waiter.wait_event_type='Lock' AND $1::int=ANY(pg_blocking_pids(waiter.pid))
 AND strpos(waiter.query,'FROM tenants')>0 AND strpos(waiter.query,'FOR NO KEY UPDATE')>0)`, holder).Scan(&waiting); err != nil {
							return err
						}
						if waiting {
							break
						}
						select {
						case early := <-result:
							return fmt.Errorf("action finished before lock barrier: %d %s", early.Code, early.Body.String())
						case <-held.Done():
							return held.Err()
						case <-time.After(10 * time.Millisecond):
						}
					}
					var locked string
					return tx.QueryRow(held, `SELECT project_node_id::text FROM journey_projects WHERE project_node_id=$1 FOR UPDATE NOWAIT`, project).Scan(&locked)
				}); err != nil {
					return fmt.Errorf("journey row was taken before tenant/pairing fence: %w", err)
				}
				method := http.MethodPost
				body := fmt.Sprintf(`{"expected_revision":1,"ticket_node_ids":[%q]}`, ids[0])
				if direct == "plan" {
					method, body = http.MethodPut, `{"expected_revision":1,"ordered_ticket_ids":[],"included_ticket_ids":[]}`
				}
				req := httptest.NewRequest(method, "/api/projects/"+project+"/releases/"+prior+"/"+direct, strings.NewReader(body)).WithContext(held)
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, req)
				if w.Code != 409 || !strings.Contains(w.Body.String(), "current planning release") {
					return fmt.Errorf("direct validation: %d %s", w.Code, w.Body.String())
				}
				return nil // Releases tenant/journey locks and lets the action proceed.
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-result:
				if w.Code != 409 || !strings.Contains(w.Body.String(), "closed tickets") {
					t.Fatalf("atomic rejection %d %s", w.Code, w.Body.String())
				}
			case <-ctx.Done():
				t.Fatal("atomic action did not complete after barrier")
			}
			var tickets, receipts int
			var revision int64
			var current string
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT revision,current_release_node_id::text,(SELECT count(*) FROM journey_tickets WHERE project_node_id=$1),(SELECT count(*) FROM journey_action_receipts WHERE project_node_id=$1) FROM journey_projects WHERE project_node_id=$1`, project).Scan(&revision, &current, &tickets, &receipts); err != nil {
				t.Fatal(err)
			}
			if f.releaseCount(t, project) != 1 || f.eventCount(t) != baseline || tickets != 0 || receipts != 0 || revision != view.Revision || current != prior {
				t.Fatalf("rejected batch left writes: tickets=%d receipts=%d revision=%d current=%s", tickets, receipts, revision, current)
			}
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state='open' WHERE id=$1`, ids[4]); err != nil {
				t.Fatal(err)
			}
			retried := f.journey(t, f.person, http.MethodPost, path, string(payload))
			if retried.CurrentReleaseID == nil || *retried.CurrentReleaseID == prior {
				t.Fatalf("retry %+v", retried)
			}
			replay := f.journey(t, f.person, http.MethodPost, path, string(payload))
			if replay.CurrentReleaseID == nil || *replay.CurrentReleaseID != *retried.CurrentReleaseID || f.releaseCount(t, project) != 2 {
				t.Fatal("replay duplicated release")
			}
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE release_node_id=$1 AND scope_revision_required`, *retried.CurrentReleaseID).Scan(&tickets); err != nil || tickets != 5 {
				t.Fatalf("atomic selected count=%d err=%v", tickets, err)
			}
		})
	}
}

func TestPlanNextReleaseWithExistingTickets(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-228", "Next release project")
	prior := f.node(t, "release", "REL-228", "Release 1")
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,decision,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256,current_release_node_id) VALUES($1,$2,now(),'go',1,1,$4,$3)`, f.tenant, project, prior, digest); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,1,'released',now())`, f.tenant, prior, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 5)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		for i := range ids {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,aeon_next_node_key($1,short_prefix),id,$3,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING nodes.id::text`, f.tenant, project, fmt.Sprintf("Next %d", i+1)).Scan(&ids[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.NextAction.Key != "plan_next_release" {
		t.Fatalf("next action %+v", view.NextAction)
	}
	payload := map[string]any{"action": "plan_next_release", "expected_revision": view.Revision, "idempotency_key": "next-with-five", "release_id": prior, "ticket_node_ids": ids}
	body, _ := json.Marshal(payload)
	next := f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", string(body))
	if next.CurrentReleaseID == nil || *next.CurrentReleaseID == prior {
		t.Fatalf("next release %+v", next)
	}
	var number, count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.number,(SELECT count(*) FROM journey_tickets WHERE release_node_id=r.release_node_id) FROM journey_releases r WHERE r.release_node_id=$1`, *next.CurrentReleaseID).Scan(&number, &count); err != nil {
		t.Fatal(err)
	}
	if number != 2 || count != 5 {
		t.Fatalf("number=%d selected=%d", number, count)
	}
}
