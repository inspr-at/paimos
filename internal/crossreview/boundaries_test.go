// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This boundary fixture uses the real API middleware and the same deferred usage
// and completion collaborators as production. No injected authenticated caller.
func (f *fixture) boundaryHandler(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	am, err := auth.New(auth.Config{Env: "dev", PublicURL: "https://review.test", SessionKey: []byte(strings.Repeat("f", 32))}, pool)
	if err != nil {
		t.Fatal(err)
	}
	review := New(pool, nil)
	usage := func(ctx context.Context, tx pgx.Tx, p tenant.Principal, v agentruns.Run, _ agentruns.Telemetry, pending *[]events.Change) error {
		return agentaccounts.SettleDeferred(ctx, tx, p, v.ID, pending)
	}
	s := &httpapi.Server{Pool: pool, Modules: []httpapi.Module{am, review, authz.New(pool), agentaccounts.New(pool), agentpairing.New(pool, "https://review.test", "review-fixture"), modelregistry.New(pool), workorders.New(pool), agentruns.NewWithReviews(pool, usage, review.RequestForRun, review.PrepareForRun)}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	return s.Handler()
}

func (f *fixture) boundaryCookie(t *testing.T) *http.Cookie {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	var identity string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,email) VALUES('https://review.test',$1,'review@example.test') RETURNING id::text`, testID()).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO sessions(id,identity_id,tenant_id,principal_id,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 day')`, hex.EncodeToString(sum[:]), identity, f.person.TenantID, f.person.ID); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "aeon_session", Value: hex.EncodeToString(raw)}
}

func boundaryRequest(ctx context.Context, path string, body any, token string, cookie *http.Cookie) *http.Request {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(raw))).WithContext(ctx)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	r.Header.Set(agentruns.DaemonHeader, "review-daemon")
	r.Header.Set(agentruns.GenerationHeader, "review-generation")
	r.Header.Set(reviewgate.PolicyHeader, reviewgate.Policy)
	return r
}

func serveBoundary(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func (f *fixture) completion(t *testing.T, profileless ...bool) (string, agentruns.Telemetry) {
	t.Helper()
	var order workorders.Order
	f.call(t, f.agent, "POST", "/api/work-orders", map[string]any{"title": "Boundary build", "parent_id": f.ticket, "assignee_principal_id": f.agent.ID, "max_cost_micros": int64(1), "criteria": []string{"Pass isolation"}}, 201, &order)
	var run string
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE work_orders SET status='running' WHERE node_id=$1`, order.NodeID); err != nil {
			return err
		}
		var err error
		if len(profileless) > 0 && profileless[0] {
			err = tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,status,account_id,daemon_id,daemon_generation,started_at) VALUES($1,$2,$3,'running',$4,'review-daemon','review-generation',clock_timestamp()) RETURNING id::text`, f.person.TenantID, order.NodeID, f.agent.ID, f.account).Scan(&run)
		} else {
			err = tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,status,account_id,daemon_id,daemon_generation,started_at)
   SELECT $1,$2,$3,p.id,p.model,'running',$4,'review-daemon','review-generation',clock_timestamp() FROM model_profiles p WHERE p.family='openai' AND p.version='test-version' RETURNING id::text`, f.person.TenantID, order.NodeID, f.agent.ID, f.account).Scan(&run)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units,state)
   SELECT $1,$2,id,10,'active' FROM account_allowance_windows WHERE account_id=$3`, f.person.TenantID, run, f.account)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE account_allowance_windows SET reserved=reserved+10 WHERE account_id=$1`, f.account)
		return err
	})
	return run, agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: "completed", Input: 20, Cost: 1, ReviewRange: &reviewgate.CommitRange{Repository: "example/review-fixture", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}}
}

func (f *fixture) completionState(t *testing.T) string {
	t.Helper()
	var state string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT jsonb_build_object(
   'nodes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY id) FROM nodes n),
   'orders',(SELECT jsonb_agg(to_jsonb(w) ORDER BY node_id) FROM work_orders w),
   'criteria',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM work_criteria c),
   'reviews',(SELECT jsonb_agg(to_jsonb(w) ORDER BY work_order_id) FROM work_order_reviews w),
   'runs',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM agent_runs r),
   'telemetry',(SELECT jsonb_agg(to_jsonb(t) ORDER BY run_id,sequence) FROM run_telemetry t),
   'accounts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM agent_accounts a),
   'windows',(SELECT jsonb_agg(to_jsonb(w) ORDER BY id) FROM account_allowance_windows w),
   'reservations',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM account_reservations r),
   'enrollments',(SELECT jsonb_agg(to_jsonb(e) ORDER BY computer_id,account_id) FROM agent_pairing_enrollments e),
   'learning',(SELECT jsonb_agg(to_jsonb(l) ORDER BY account_id) FROM account_capacity_learning l),
   'readings',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM account_capacity_readings l),
   'computers',(SELECT jsonb_agg(jsonb_build_object('id',id,'state',state,'revision',revision) ORDER BY id) FROM agent_pairing_computers),
   'pairing_requests',(SELECT jsonb_agg(jsonb_build_object('id',id,'state',state) ORDER BY id) FROM agent_pairing_requests),
   'keys',(SELECT jsonb_agg(jsonb_build_object('id',id,'revoked_at',revoked_at) ORDER BY id) FROM agent_keys),
   'links',(SELECT jsonb_agg(jsonb_build_object('id',id,'state',state,'person_id',person_id) ORDER BY id) FROM account_person_link_requests),
   'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e WHERE type<>'model.registry_seeded'))::text`).Scan(&state)
	})
	return state
}

func (f *fixture) eventTypes(t *testing.T) []string {
	t.Helper()
	var out []string
	f.tx(t, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT type FROM events WHERE type IN ('account.settled','work_order.budget_exhausted','node.created','work_order.created','run.created','review.requested','review.unavailable','run.telemetry') ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind string
			if err := rows.Scan(&kind); err != nil {
				return err
			}
			out = append(out, kind)
		}
		return rows.Err()
	})
	return out
}

// PostgreSQL sequences survive transaction rollback. This probe proves the
// addressed failure trigger actually fired, so a generic HTTP database refusal
// cannot make a rollback test pass for an unrelated earlier failure.
func (f *fixture) armFailureProbe(t *testing.T) {
	t.Helper()
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE SEQUENCE aeon633_failure_hits`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `GRANT USAGE ON SEQUENCE aeon633_failure_hits TO `+pgx.Identifier{f.d.Role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) assertFailureProbe(t *testing.T) {
	t.Helper()
	var called bool
	var hits int64
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT is_called,last_value FROM aeon633_failure_hits`).Scan(&called, &hits); err != nil {
		t.Fatal(err)
	}
	if !called || hits != 1 {
		t.Fatalf("intended failure trigger did not fire exactly once: called=%v hits=%d", called, hits)
	}
}

// Pause before the initiating preparation's tenant fence, after middleware and
// bounded decoding. A real revoker can commit before preparation reauthorizes.
type beforePreparation struct {
	used   atomic.Bool
	ready  chan struct{}
	resume chan struct{}
	once   sync.Once
}

func (b *beforePreparation) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "FROM tenants WHERE") && strings.Contains(q.SQL, "FOR NO KEY UPDATE") && b.used.CompareAndSwap(false, true) {
		close(b.ready)
		select {
		case <-b.resume:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*beforePreparation) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (b *beforePreparation) release()                                                      { b.once.Do(func() { close(b.resume) }) }
func (f *fixture) preparationBarrier(t *testing.T) (*pgxpool.Pool, *beforePreparation, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	b := &beforePreparation{ready: make(chan struct{}), resume: make(chan struct{})}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = b
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.release(); cancel(); pool.Close() })
	return pool, b, ctx
}

func TestCompletionBoundarySettlesBudgetReviewsAndReplay(t *testing.T) {
	f := newCatalogFixture(t, false) // required additions absent; author profile retained
	run, report := f.completion(t)
	h := f.boundaryHandler(t, f.d.App)
	before := len(f.eventTypes(t))
	out := serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 200 {
		t.Fatalf("completion status %d: %s", out.Code, out.Body.String())
	}
	want := []string{"account.settled", "work_order.budget_exhausted", "node.created", "work_order.created", "run.created", "review.requested", "run.telemetry"}
	got := f.eventTypes(t)[before:]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("completion event order %v", got)
	}
	state := f.completionState(t)
	var profiles, seed int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &seed)
	})
	if profiles <= 3 || seed != 1 {
		t.Fatal("incomplete catalog was not prepared once")
	}
	out = serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 200 || f.completionState(t) != state {
		t.Fatal("identical completion replay changed state")
	}
	divergent := report
	divergent.Cost++
	out = serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", divergent, f.token, nil))
	if out.Code != 409 || !strings.Contains(out.Body.String(), "divergent telemetry replay") || f.completionState(t) != state {
		t.Fatal("divergent replay was not refused atomically")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var current int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='model.registry_seeded'`).Scan(&current); err != nil {
			return err
		}
		if current != seed {
			t.Fatal("replay prepared again")
		}
		return nil
	})
}

func TestCompletionEventFailureRollsBackWholeUnitButKeepsAuthorizedSetup(t *testing.T) {
	f := newCatalogFixture(t, false)
	run, report := f.completion(t)
	h := f.boundaryHandler(t, f.d.App)
	before := f.completionState(t)
	f.armFailureProbe(t)
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_final_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='run.telemetry' THEN PERFORM nextval('aeon633_failure_hits'); RAISE EXCEPTION 'injected final event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_final_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_final_event()`); err != nil {
		t.Fatal(err)
	}
	out := serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 400 || !strings.Contains(out.Body.String(), "invalid value or related resource") {
		t.Fatalf("expected injected database failure, got %d %s", out.Code, out.Body.String())
	}
	f.assertFailureProbe(t)
	if f.completionState(t) != before {
		t.Fatal("failed completion retained resource, usage, budget or event changes")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var seed int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='model.registry_seeded'`).Scan(&seed); err != nil {
			return err
		}
		if seed != 1 {
			t.Fatal("authorized standalone setup was lost")
		}
		return nil
	})
	if _, err := f.d.Admin.Exec(t.Context(), `DROP TRIGGER reject_final_event ON events`); err != nil {
		t.Fatal(err)
	}
	out = serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 200 {
		t.Fatalf("retry status %d %s", out.Code, out.Body.String())
	}
}

func TestPreparationExactKeyRevocationBeforeFenceLeavesCatalogUntouched(t *testing.T) {
	f := newCatalogFixture(t, false)
	run, report := f.completion(t)
	pool, barrier, ctx := f.preparationBarrier(t)
	h := f.boundaryHandler(t, pool)
	before := f.completionState(t)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- serveBoundary(h, boundaryRequest(ctx, "/api/runs/"+run+"/telemetry", report, f.token, nil))
	}()
	dbtest.Await(t, ctx, barrier.ready)
	if err := db.InTenant(dbtest.Seed(ctx), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE principal_id=$1`, f.agent.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before = f.completionState(t) // account for the independently committed revocation
	barrier.release()
	out := dbtest.Await(t, ctx, done)
	if out.Code != 403 || !strings.Contains(out.Body.String(), "key scope required: run.telemetry") {
		t.Fatalf("wrong revocation refusal %d %s", out.Code, out.Body.String())
	}
	if f.completionState(t) != before {
		t.Fatal("denied preparation changed completion state")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var profiles, seed int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &seed); err != nil {
			return err
		}
		if profiles != 3 || seed != 0 {
			t.Fatal("denied preparation persisted setup")
		}
		return nil
	})
}

func TestManualPreparationRevocationColdAndIncomplete(t *testing.T) {
	for _, cold := range []bool{true, false} {
		t.Run(map[bool]string{true: "cold", false: "incomplete"}[cold], func(t *testing.T) {
			f := newCatalogFixture(t, false, cold)
			pool, barrier, ctx := f.preparationBarrier(t)
			h := f.boundaryHandler(t, pool)
			cookie := f.boundaryCookie(t)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- serveBoundary(h, boundaryRequest(ctx, "/api/nodes/"+f.ticket+"/reviews", f.input(), "", cookie))
			}()
			dbtest.Await(t, ctx, barrier.ready)
			if err := db.InTenant(dbtest.Seed(ctx), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
				if err := authz.LockProjectMutation(ctx, tx, f.person.TenantID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1`, f.person.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			barrier.release()
			out := dbtest.Await(t, ctx, done)
			// Retained target visibility identifies a current permission refusal.
			if out.Code != 403 || !strings.Contains(out.Body.String(), "missing_role_permission") {
				t.Fatalf("target revocation returned %d %s", out.Code, out.Body.String())
			}
			f.tx(t, func(tx pgx.Tx) error {
				var profiles, seed, reviews int
				if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded'),(SELECT count(*) FROM work_order_reviews)`).Scan(&profiles, &seed, &reviews); err != nil {
					return err
				}
				want := 3
				if cold {
					want = 0
				}
				if profiles != want || seed != 0 || reviews != 0 {
					t.Fatal("revoked manual preparation wrote catalog or review")
				}
				return nil
			})
		})
	}
}

func TestManualPreparationUsesCurrentProjectAfterMove(t *testing.T) {
	for _, cold := range []bool{true, false} {
		t.Run(map[bool]string{true: "cold", false: "incomplete"}[cold], func(t *testing.T) {
			f := newCatalogFixture(t, false, cold)
			a, b := testID(), testID()
			f.tx(t, func(tx pgx.Tx) error {
				for i, id := range []string{a, b} {
					if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,$3,id,'Scope test' FROM node_kinds WHERE slug='project'`, f.person.TenantID, id, []string{"PRJ-1", "PRJ-2"}[i]); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, a); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1`, f.person.ID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='admin'`, f.person.TenantID, f.person.ID, a)
				return err
			})
			// This succeeds on project A without models.manage or workspace
			// review-write authority, and has no catalog side effects.
			if err := db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
				_, err := authorizeReview(t.Context(), tx, f.person, f.ticket, f.input(), false)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			pool, barrier, ctx := f.preparationBarrier(t)
			cookie := f.boundaryCookie(t)
			h := f.boundaryHandler(t, pool)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- serveBoundary(h, boundaryRequest(ctx, "/api/nodes/"+f.ticket+"/reviews", f.input(), "", cookie))
			}()
			dbtest.Await(t, ctx, barrier.ready)
			if err := db.InTenant(dbtest.Seed(ctx), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
				if err := db.LockTree(ctx, tx, f.person.TenantID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, b)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			before := f.completionState(t)
			barrier.release()
			out := dbtest.Await(t, ctx, done)
			if out.Code != 403 || !strings.Contains(out.Body.String(), "missing_role_permission") {
				t.Fatalf("wrong moved-target refusal %d %s", out.Code, out.Body.String())
			}
			if f.completionState(t) != before {
				t.Fatal("moved-target refusal changed review resources or events")
			}
			f.tx(t, func(tx pgx.Tx) error {
				var profiles, seeds int
				if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &seeds); err != nil {
					return err
				}
				want := 3
				if cold {
					want = 0
				}
				if profiles != want || seeds != 0 {
					t.Fatal("preparation used stale project authority")
				}
				return nil
			})
		})
	}
}

// The SQL guard observes resource mutations inside triggers as well as Go SQL.
// Its transaction-local flag resets automatically at each standalone commit.
func (f *fixture) guardEventTail(t *testing.T) {
	t.Helper()
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION event_tail_start() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM set_config('aeon633.events_started','on',true); RETURN NEW; END $$;
  CREATE TRIGGER event_tail_start BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION event_tail_start();
  CREATE FUNCTION resource_after_events() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF current_setting('aeon633.events_started',true)='on' THEN RAISE EXCEPTION 'resource write after event batch'; END IF; IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"nodes", "work_orders", "work_criteria", "work_order_reviews", "agent_runs", "run_telemetry", "agent_accounts", "account_allowance_windows", "account_reservations", "agent_pairing_enrollments", "agent_pairing_computers", "agent_keys", "account_person_link_requests", "model_profiles", "model_role_routes", "account_capacity_learning", "account_capacity_readings", "agent_pairing_requests", "harness_activity_notes", "work_evidence"} {
		if _, err := f.d.Admin.Exec(t.Context(), `CREATE TRIGGER resource_after_events BEFORE INSERT OR UPDATE OR DELETE ON `+table+` FOR EACH ROW EXECUTE FUNCTION resource_after_events()`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManualAndCompletionResourceWritesPrecedeEventBatch(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "finished telemetry"}[automatic], func(t *testing.T) {
			f := newCatalogFixture(t, false)
			var run string
			var report agentruns.Telemetry
			if automatic {
				run, report = f.completion(t)
			}
			cookie := f.boundaryCookie(t)
			f.guardEventTail(t)
			h := f.boundaryHandler(t, f.d.App)
			var request *http.Request
			want := 201
			if automatic {
				request = boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil)
				want = 200
			} else {
				request = boundaryRequest(t.Context(), "/api/nodes/"+f.ticket+"/reviews", f.input(), "", cookie)
			}
			out := serveBoundary(h, request)
			if out.Code != want {
				t.Fatalf("resource acquired/written after events: %d %s", out.Code, out.Body.String())
			}
		})
	}
}

func TestCompletionReviewPermissionLossSkipsSetupAndRecordsClosedGate(t *testing.T) {
	f := newCatalogFixture(t, false)
	run, report := f.completion(t)
	// Keep telemetry authority and exact key valid; remove only the independent
	// review scope. This is the specific existing closed-gate completion branch.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=array_remove(scopes,'run.create') WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	h := f.boundaryHandler(t, f.d.App)
	out := serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 200 {
		t.Fatalf("authorized closed-gate completion %d %s", out.Code, out.Body.String())
	}
	f.tx(t, func(tx pgx.Tx) error {
		var status string
		var profiles, seed, reviews, closed int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT status FROM agent_runs WHERE id=$1),(SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded'),(SELECT count(*) FROM work_order_reviews),(SELECT count(*) FROM events WHERE type='review.unavailable' AND after->>'reason'='Author lacks permission to request an independent review; gate remains closed.')`, run).Scan(&status, &profiles, &seed, &reviews, &closed); err != nil {
			return err
		}
		if status != "completed" || profiles != 3 || seed != 0 || reviews != 0 || closed != 1 {
			t.Fatal("closed gate performed setup, lost telemetry or reported wrong refusal")
		}
		return nil
	})
}

func TestManualFinalEventFailureRollsBackReviewUnit(t *testing.T) {
	f := newCatalogFixture(t, false)
	h := f.boundaryHandler(t, f.d.App)
	cookie := f.boundaryCookie(t)
	before := f.completionState(t)
	f.armFailureProbe(t)
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_review_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='review.requested' THEN PERFORM nextval('aeon633_failure_hits'); RAISE EXCEPTION 'injected review event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_review_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_review_event()`); err != nil {
		t.Fatal(err)
	}
	out := serveBoundary(h, boundaryRequest(t.Context(), "/api/nodes/"+f.ticket+"/reviews", f.input(), "", cookie))
	if out.Code != 400 || !strings.Contains(out.Body.String(), "invalid value or related resource") {
		t.Fatalf("wrong event failure %d %s", out.Code, out.Body.String())
	}
	f.assertFailureProbe(t)
	if f.completionState(t) != before {
		t.Fatal("manual review event failure left partial review/order/node/run resources")
	}
}

func TestModelInitializerAdaptersReauthorizeBeforeColdOrAdditiveSetup(t *testing.T) {
	for _, cold := range []bool{true, false} {
		for _, route := range []struct {
			name, method, path string
			body               any
			scope              string
		}{
			{"model list", "GET", "/api/models", nil, "models.read"},
			{"legacy resolver", "GET", "/api/models/resolve?role=build", nil, "models.read"},
			{"profile management", "POST", "/api/models", map[string]any{"slug": "boundary-extra", "version": "99", "harness": "codex", "family": "openai", "model": "gpt-test", "effort": "high", "tier": "strong"}, "models.manage"},
			{"legacy replacement", "PUT", "/api/models/routes", []any{}, "models.manage"},
		} {
			t.Run(route.name+map[bool]string{true: " cold", false: " additive"}[cold], func(t *testing.T) {
				f := newCatalogFixture(t, false, cold)
				pool, barrier, ctx := f.preparationBarrier(t)
				h := f.boundaryHandler(t, pool)
				cookie := f.boundaryCookie(t)
				request := boundaryRequest(ctx, route.path, route.body, "", cookie)
				request.Method = route.method
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- serveBoundary(h, request) }()
				dbtest.Await(t, ctx, barrier.ready)
				if err := db.InTenant(dbtest.Seed(ctx), f.d.Admin, f.person.TenantID, func(tx pgx.Tx) error {
					if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				barrier.release()
				out := dbtest.Await(t, ctx, done)
				if out.Code != 403 || !strings.Contains(out.Body.String(), "missing_role_permission") {
					t.Fatalf("wrong initializer revocation %d %s", out.Code, out.Body.String())
				}
				f.tx(t, func(tx pgx.Tx) error {
					var profiles, seed int
					if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &seed); err != nil {
						return err
					}
					want := 3
					if cold {
						want = 0
					}
					if profiles != want || seed != 0 {
						t.Fatal("denied initializer persisted catalog setup")
					}
					return nil
				})
			})
		}
	}
}

func TestModelReadKeyCanPrepareWithoutManagementScope(t *testing.T) {
	f := newCatalogFixture(t, false, true)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['models.read'] WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	h := f.boundaryHandler(t, f.d.App)
	r := boundaryRequest(t.Context(), "/api/models", nil, f.token, nil)
	r.Method = "GET"
	out := serveBoundary(h, r)
	if out.Code != 200 {
		t.Fatalf("read-only exact key could not initialize %d %s", out.Code, out.Body.String())
	}
	var profiles []modelregistry.Profile
	if err := json.Unmarshal(out.Body.Bytes(), &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles) <= 3 {
		t.Fatal("cold catalog not initialized by authorized reader")
	}
}

func TestCompletionPreparationWaitsForConcurrentKeyRevoker(t *testing.T) {
	f := newCatalogFixture(t, false)
	run, report := f.completion(t)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.Contains(sql, "INSERT INTO model_profiles") })
	h := f.boundaryHandler(t, pool)
	plain := f.boundaryHandler(t, f.d.App)
	cookie := f.boundaryCookie(t)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- serveBoundary(h, boundaryRequest(ctx, "/api/runs/"+run+"/telemetry", report, f.token, nil))
	}()
	holder := barrier.Wait(t, ctx)
	var keyID string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM agent_keys WHERE principal_id=$1`, f.agent.ID).Scan(&keyID)
	})
	revoked := make(chan *httptest.ResponseRecorder, 1)
	revokerDone := make(chan struct{})
	go func() {
		defer close(revokerDone)
		r := boundaryRequest(ctx, "/api/agent-keys/"+keyID, nil, "", cookie)
		r.Method = "DELETE"
		revoked <- serveBoundary(plain, r)
	}()
	if wait := dbtest.BlockedOrDone(t, ctx, f.d.Admin, holder, revokerDone); wait == "" {
		t.Fatal("revoker did not wait behind authorized preparation")
	}
	barrier.Release()
	revoke := dbtest.Await(t, ctx, revoked)
	if revoke.Code != 204 {
		t.Fatalf("revoker failed %d %s", revoke.Code, revoke.Body.String())
	}
	out := dbtest.Await(t, ctx, done)
	if out.Code != 200 && (out.Code != 403 || !strings.Contains(out.Body.String(), "key scope required: run.telemetry")) {
		t.Fatalf("wrong completion contention outcome %d %s", out.Code, out.Body.String())
	}
	f.tx(t, func(tx pgx.Tx) error {
		var seed int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='model.registry_seeded'`).Scan(&seed); err != nil {
			return err
		}
		if seed != 1 {
			t.Fatal("authorized preparation did not commit before revocation")
		}
		return nil
	})
}

func TestCompletionReviewValidationSavepointDiscardsPartialResources(t *testing.T) {
	f := newCatalogFixture(t, false)
	run, report := f.completion(t)
	// Force a genuine dispatch validation refusal after the order and immutable
	// binding exist. This is a database fixture trigger, not an injected caller.
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION refuse_review_dispatch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='review' THEN NEW.status='blocked'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_review_dispatch BEFORE UPDATE ON work_orders FOR EACH ROW EXECUTE FUNCTION refuse_review_dispatch()`); err != nil {
		t.Fatal(err)
	}
	out := serveBoundary(f.boundaryHandler(t, f.d.App), boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 200 {
		t.Fatalf("review validation refused completion %d %s", out.Code, out.Body.String())
	}
	f.tx(t, func(tx pgx.Tx) error {
		var reviews, nodes, telemetry, unavailable int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM work_order_reviews),(SELECT count(*) FROM work_orders WHERE node_id<>(SELECT work_order_id FROM agent_runs WHERE id=$1)),(SELECT count(*) FROM run_telemetry WHERE run_id=$1),(SELECT count(*) FROM events WHERE type='review.unavailable' AND after->>'reason'='Automatic review could not bind this run and commit range; gate remains closed. Request a review from the ticket.')`, run).Scan(&reviews, &nodes, &telemetry, &unavailable); err != nil {
			return err
		}
		if reviews != 0 || nodes != 0 || telemetry != 1 || unavailable != 1 {
			t.Fatalf("savepoint retained partial review or lost completion: reviews=%d orders=%d telemetry=%d unavailable=%d", reviews, nodes, telemetry, unavailable)
		}
		return nil
	})
	types := f.eventTypes(t)
	for _, typ := range types {
		if typ == "review.requested" || typ == "run.created" {
			t.Fatalf("refused partial review event leaked: %s", typ)
		}
	}
	replay := serveBoundary(f.boundaryHandler(t, f.d.App), boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if replay.Code != 200 {
		t.Fatalf("accepted closed-gate replay failed %d", replay.Code)
	}
	if !reflect.DeepEqual(types, f.eventTypes(t)) {
		t.Fatal("closed-gate replay appended events")
	}
}

func TestCompletionEveryDeferredEventFailureRollsBackUnit(t *testing.T) {
	for _, typ := range []string{"account.settled", "work_order.budget_exhausted", "node.created", "work_order.created", "run.created", "review.requested"} {
		t.Run(typ, func(t *testing.T) {
			f := newCatalogFixture(t, false)
			run, report := f.completion(t)
			before := f.completionState(t)
			f.armFailureProbe(t)
			if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_completion_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='`+typ+`' THEN PERFORM nextval('aeon633_failure_hits'); RAISE EXCEPTION 'injected deferred event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_completion_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_completion_event()`); err != nil {
				t.Fatal(err)
			}
			out := serveBoundary(f.boundaryHandler(t, f.d.App), boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
			if out.Code != 400 || !strings.Contains(out.Body.String(), "invalid value or related resource") {
				t.Fatalf("wrong injected event refusal %d %s", out.Code, out.Body.String())
			}
			f.assertFailureProbe(t)
			if f.completionState(t) != before {
				t.Fatal("failed deferred append retained completion state")
			}
		})
	}
}

// SQL tracing checks explicit row/advisory acquisition; the trigger guard above
// covers resource DML performed indirectly by PostgreSQL triggers as well.
type eventLastTracer struct {
	mu         sync.Mutex
	appended   map[uint32]bool
	violations []string
}

func (g *eventLastTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	g.mu.Lock()
	defer g.mu.Unlock()
	sql := strings.ToUpper(strings.TrimSpace(q.SQL))
	pid := c.PgConn().PID()
	if sql == "BEGIN" || sql == "COMMIT" || sql == "ROLLBACK" {
		delete(g.appended, pid)
	}
	if g.appended[pid] && (strings.Contains(sql, "FOR UPDATE") || strings.Contains(sql, "FOR NO KEY UPDATE") || strings.Contains(sql, "PG_ADVISORY") || strings.HasPrefix(sql, "INSERT INTO") || strings.HasPrefix(sql, "UPDATE ") || strings.HasPrefix(sql, "DELETE FROM")) && !strings.Contains(sql, "INSERT INTO EVENTS") {
		g.violations = append(g.violations, sql)
	}
	if strings.Contains(sql, "INSERT INTO EVENTS") {
		g.appended[pid] = true
	}
	return ctx
}
func (*eventLastTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestCompositeBoundariesAcquireNoResourceAfterEvent(t *testing.T) {
	for _, completion := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "completion"}[completion], func(t *testing.T) {
			f := newCatalogFixture(t, false)
			tracer := &eventLastTracer{appended: map[uint32]bool{}}
			cfg := f.d.App.Config()
			cfg.ConnConfig.Tracer = tracer
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			h := f.boundaryHandler(t, pool)
			var out *httptest.ResponseRecorder
			if completion {
				run, report := f.completion(t)
				out = serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
			} else {
				out = serveBoundary(h, boundaryRequest(t.Context(), "/api/nodes/"+f.ticket+"/reviews", f.input(), "", f.boundaryCookie(t)))
			}
			if out.Code != 200 && out.Code != 201 {
				t.Fatalf("composite request failed %d %s", out.Code, out.Body.String())
			}
			tracer.mu.Lock()
			defer tracer.mu.Unlock()
			if len(tracer.violations) != 0 {
				t.Fatalf("resource acquired after event append: %v", tracer.violations)
			}
		})
	}
}

func (f *fixture) draining(t *testing.T) string {
	t.Helper()
	request, computer := testID(), testID()
	f.tx(t, func(tx pgx.Tx) error {
		hash := strings.Repeat("d", 64)
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,$2,'123456789',$3,$3,$3,'{"platform":"darwin","arch":"arm64"}',$3,'redeemed',$4)`, f.person.TenantID, request, hash, f.person.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,state,setup_state) SELECT $1,$2,$3,$4,k.id,'review-daemon',$5,'draining','connected' FROM agent_keys k WHERE k.principal_id=$4`, f.person.TenantID, computer, request, f.agent.ID, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_pairing_requests SET computer_id=$2 WHERE id=$1`, request, computer); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,state,verification_expires_at,ongoing_approved_at) VALUES($1,$2,$3,$4,$5,'draining',clock_timestamp()+interval '1 hour',clock_timestamp())`, f.person.TenantID, f.account, computer, request, f.profile); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_person_link_requests(tenant_id,account_id,user_code_hash,account_revision) VALUES($1,$2,$3,0)`, f.person.TenantID, f.account, strings.Repeat("e", 64))
		return err
	})
	return computer
}
func TestCompletionDrainResourcesAndCancellationJoinAtomicBatch(t *testing.T) {
	f := newCatalogFixture(t, false)
	run, report := f.completion(t)
	computer := f.draining(t)
	before := f.completionState(t)
	f.armFailureProbe(t)
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_drain_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='account.link_cancelled' THEN PERFORM nextval('aeon633_failure_hits'); RAISE EXCEPTION 'injected drain event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_drain_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_drain_event()`); err != nil {
		t.Fatal(err)
	}
	f.guardEventTail(t)
	h := f.boundaryHandler(t, f.d.App)
	out := serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 400 || !strings.Contains(out.Body.String(), "invalid value or related resource") {
		t.Fatalf("wrong drain failure %d %s", out.Code, out.Body.String())
	}
	f.assertFailureProbe(t)
	if f.completionState(t) != before {
		t.Fatal("failed cancellation append retained usage, drain, key or completion state")
	}
	if _, err := f.d.Admin.Exec(t.Context(), `DROP TRIGGER reject_drain_event ON events`); err != nil {
		t.Fatal(err)
	}
	out = serveBoundary(h, boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 200 {
		t.Fatalf("drained completion failed %d %s", out.Code, out.Body.String())
	}
	f.tx(t, func(tx pgx.Tx) error {
		var computerState, enrollmentState, accountState, linkState string
		var revoked bool
		var cancellation int
		if err := tx.QueryRow(t.Context(), `SELECT c.state,e.state,a.state,k.revoked_at IS NOT NULL,l.state,(SELECT count(*) FROM events WHERE type='account.link_cancelled') FROM agent_pairing_computers c JOIN agent_pairing_enrollments e ON e.computer_id=c.id JOIN agent_accounts a ON a.id=e.account_id JOIN agent_keys k ON k.id=c.key_id JOIN account_person_link_requests l ON l.account_id=a.id WHERE c.id=$1`, computer).Scan(&computerState, &enrollmentState, &accountState, &revoked, &linkState, &cancellation); err != nil {
			return err
		}
		if computerState != "revoked" || enrollmentState != "revoked" || accountState != "unavailable" || !revoked || linkState != "revoked" || cancellation != 1 {
			t.Fatal("drain did not finish exactly once with deferred cancellation")
		}
		return nil
	})
}

func TestCompletionFinalFenceSerializesConnectedWriters(t *testing.T) {
	for _, operation := range []string{"manual_review", "legacy_ladder", "access", "account_lifecycle", "duplicate_completion"} {
		t.Run(operation, func(t *testing.T) {
			f := newCatalogFixture(t, false)
			run, report := f.completion(t)
			pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.Contains(sql, "UPDATE account_allowance_windows") })
			h := f.boundaryHandler(t, pool)
			plain := f.boundaryHandler(t, f.d.App)
			cookie := f.boundaryCookie(t)
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				first <- serveBoundary(h, boundaryRequest(ctx, "/api/runs/"+run+"/telemetry", report, f.token, nil))
			}()
			holder := barrier.Wait(t, ctx)
			var r *http.Request
			want := 200
			switch operation {
			case "manual_review":
				r = boundaryRequest(ctx, "/api/nodes/"+f.ticket+"/reviews", f.input(), "", cookie)
				want = 201
			case "legacy_ladder":
				r = boundaryRequest(ctx, "/api/models/routes", []any{}, "", cookie)
				r.Method = "PUT"
			case "access":
				var role string
				f.tx(t, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT id::text FROM roles WHERE key='viewer'`).Scan(&role)
				})
				r = boundaryRequest(ctx, "/api/members/"+f.agent.ID+"/workspace-role", map[string]any{"role_id": role}, "", cookie)
				r.Method = "PUT"
			case "account_lifecycle":
				r = boundaryRequest(ctx, "/api/agent-accounts/"+f.account, map[string]any{"state": "unavailable"}, "", cookie)
				r.Method = "PATCH"
			case "duplicate_completion":
				r = boundaryRequest(ctx, "/api/runs/"+run+"/telemetry", report, f.token, nil)
			}
			second := make(chan *httptest.ResponseRecorder, 1)
			done := make(chan struct{})
			go func() { defer close(done); second <- serveBoundary(plain, r) }()
			if dbtest.BlockedOrDone(t, ctx, f.d.Admin, holder, done) == "" {
				t.Fatal("connected writer did not overlap completion fence")
			}
			barrier.Release()
			a, b := dbtest.Await(t, ctx, first), dbtest.Await(t, ctx, second)
			if a.Code != 200 || b.Code != want {
				t.Fatalf("connected writer outcomes: completion=%d %s other=%d %s", a.Code, a.Body.String(), b.Code, b.Body.String())
			}
			f.tx(t, func(tx pgx.Tx) error {
				var telemetry, review int
				if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM run_telemetry WHERE run_id=$1),(SELECT count(*) FROM work_order_reviews WHERE author_run_id=$1 AND reviewer_profile_id IS NOT NULL AND run_id IS NOT NULL)`, run).Scan(&telemetry, &review); err != nil {
					return err
				}
				if telemetry != 1 || review != 1 {
					t.Fatalf("completion was duplicated or lost bound review: telemetry=%d reviews=%d", telemetry, review)
				}
				return nil
			})
			if operation == "duplicate_completion" {
				types := f.eventTypes(t)
				for _, typ := range []string{"account.settled", "work_order.budget_exhausted", "review.requested", "run.telemetry"} {
					n := 0
					for _, actual := range types {
						if actual == typ {
							n++
						}
					}
					if n != 1 {
						t.Fatalf("duplicate source event %s=%d", typ, n)
					}
				}
			}
		})
	}
}

func TestCompletionPartialReviewInfrastructureFailureAbortsCompletion(t *testing.T) {
	for _, table := range []string{"work_order_reviews", "agent_runs"} {
		t.Run(table, func(t *testing.T) {
			f := newCatalogFixture(t, false)
			run, report := f.completion(t)
			before := f.completionState(t)
			f.armFailureProbe(t)
			if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_review_resource() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('aeon633_failure_hits'); RAISE EXCEPTION 'injected review resource failure'; END $$; CREATE TRIGGER reject_review_resource BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_review_resource()`); err != nil {
				t.Fatal(err)
			}
			out := serveBoundary(f.boundaryHandler(t, f.d.App), boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
			if out.Code != 400 || !strings.Contains(out.Body.String(), "invalid value or related resource") {
				t.Fatalf("wrong infrastructure refusal %d %s", out.Code, out.Body.String())
			}
			f.assertFailureProbe(t)
			if f.completionState(t) != before {
				t.Fatal("partial review infrastructure failure was swallowed or retained completion state")
			}
		})
	}
}

type beforeFinal struct {
	beforePreparation
	prepared atomic.Bool
	seeded   atomic.Bool
}

func (b *beforeFinal) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "INSERT INTO model_profiles") {
		b.seeded.Store(true)
	}
	if b.prepared.Load() && strings.Contains(q.SQL, "FROM tenants WHERE") && strings.Contains(q.SQL, "FOR NO KEY UPDATE") && b.used.CompareAndSwap(false, true) {
		close(b.ready)
		select {
		case <-b.resume:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (b *beforeFinal) TraceQueryEnd(_ context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	if q.CommandTag.String() == "COMMIT" && q.Err == nil && b.seeded.Load() {
		b.prepared.Store(true)
	}
}
func TestCompletionReviewPermissionRevokedAfterAuthorizedSetup(t *testing.T) {
	f := newCatalogFixture(t, false)
	run, report := f.completion(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	b := &beforeFinal{beforePreparation: beforePreparation{ready: make(chan struct{}), resume: make(chan struct{})}}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = b
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.release(); pool.Close() })
	done := make(chan *httptest.ResponseRecorder, 1)
	h := f.boundaryHandler(t, pool)
	go func() {
		done <- serveBoundary(h, boundaryRequest(ctx, "/api/runs/"+run+"/telemetry", report, f.token, nil))
	}()
	dbtest.Await(t, ctx, b.ready)
	if err := db.InTenant(dbtest.Seed(ctx), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=array_remove(scopes,'run.create') WHERE principal_id=$1`, f.agent.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	b.release()
	out := dbtest.Await(t, ctx, done)
	if out.Code != 200 {
		t.Fatalf("completion did not retain accepted authority %d %s", out.Code, out.Body.String())
	}
	f.tx(t, func(tx pgx.Tx) error {
		var seeds, reviews, closed, telemetry int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM events WHERE type='model.registry_seeded'),(SELECT count(*) FROM work_order_reviews),(SELECT count(*) FROM events WHERE type='review.unavailable' AND after->>'reason'='Author lacks permission to request an independent review; gate remains closed.'),(SELECT count(*) FROM run_telemetry WHERE run_id=$1)`, run).Scan(&seeds, &reviews, &closed, &telemetry); err != nil {
			return err
		}
		if seeds != 1 || reviews != 0 || closed != 1 || telemetry != 1 {
			t.Fatal("final current scope not honored or legitimate setup lost")
		}
		return nil
	})
}

func TestColdProfilelessCompletionKeepsClosedGateWithoutSetup(t *testing.T) {
	f := newCatalogFixture(t, false, true)
	run, report := f.completion(t, true)
	out := serveBoundary(f.boundaryHandler(t, f.d.App), boundaryRequest(t.Context(), "/api/runs/"+run+"/telemetry", report, f.token, nil))
	if out.Code != 200 {
		t.Fatalf("profile-less completion failed %d %s", out.Code, out.Body.String())
	}
	f.tx(t, func(tx pgx.Tx) error {
		var profiles, reviews, telemetry, closed, seeds int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM work_order_reviews),(SELECT count(*) FROM run_telemetry WHERE run_id=$1),(SELECT count(*) FROM events WHERE type='review.unavailable' AND after->>'reason'='Automatic review could not bind this run and commit range; gate remains closed. Request a review from the ticket.'),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`, run).Scan(&profiles, &reviews, &telemetry, &closed, &seeds); err != nil {
			return err
		}
		if profiles != 0 || reviews != 0 || seeds != 0 || telemetry != 1 || closed != 1 {
			t.Fatal("ineligible cold builder seeded or lost its closed-gate completion")
		}
		return nil
	})
}

func TestCompletionCurrentTelemetryAuthorityAtBothBoundaries(t *testing.T) {
	for _, afterSetup := range []bool{false, true} {
		phase := "before-setup"
		if afterSetup {
			phase = "after-setup"
		}
		for _, change := range []struct {
			name, sql, reason string
			status            int
		}{
			{"key-scope", `UPDATE agent_keys SET scopes=array_remove(scopes,'run.telemetry') WHERE principal_id=$1`, "key scope required: run.telemetry", 403},
			{"run-generation", `UPDATE agent_runs SET daemon_generation='replacement-generation' WHERE id=$2`, "daemon generation conflict", 409},
			{"account-owner", `UPDATE agent_accounts SET registered_by_principal_id=$3 WHERE id=$4`, "current daemon account owner required", 403},
		} {
			t.Run(phase+"/"+change.name, func(t *testing.T) {
				f := newCatalogFixture(t, false)
				run, report := f.completion(t)
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cfg := f.d.App.Config()
				b := &beforePreparation{ready: make(chan struct{}), resume: make(chan struct{})}
				cfg.ConnConfig.Tracer = b
				if afterSetup {
					final := &beforeFinal{beforePreparation: beforePreparation{ready: make(chan struct{}), resume: make(chan struct{})}}
					b = &final.beforePreparation
					cfg.ConnConfig.Tracer = final
				}
				pool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { b.release(); pool.Close() })
				h := f.boundaryHandler(t, pool)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- serveBoundary(h, boundaryRequest(ctx, "/api/runs/"+run+"/telemetry", report, f.token, nil))
				}()
				dbtest.Await(t, ctx, b.ready)
				if err := db.InTenant(dbtest.Seed(ctx), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
					if err := agentpairing.Lock(ctx, tx); err != nil {
						return err
					}
					// All four typed parameters are referenced so pgx/Postgres can
					// infer them even when a change addresses only one resource.
					_, err := tx.Exec(ctx, `WITH identifiers AS (SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid) `+change.sql, f.agent.ID, run, f.other.ID, f.account)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				before := f.completionState(t) // exclude the legitimate competing write
				b.release()
				out := dbtest.Await(t, ctx, done)
				if out.Code != change.status || !strings.Contains(out.Body.String(), change.reason) {
					t.Fatalf("wrong current-authority refusal %d %s", out.Code, out.Body.String())
				}
				if f.completionState(t) != before {
					t.Fatal("refused telemetry changed completion resources or events")
				}
				f.tx(t, func(tx pgx.Tx) error {
					var profiles, seeds int
					if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &seeds); err != nil {
						return err
					}
					if afterSetup {
						if profiles <= 3 || seeds != 1 {
							t.Fatal("legitimate independently authorized setup was lost")
						}
					} else if profiles != 3 || seeds != 0 {
						t.Fatal("denied preparation changed the incomplete catalog")
					}
					return nil
				})
			})
		}
	}
}

func TestVendorRetryPollCollectsResourcesBeforeItsEventBatch(t *testing.T) {
	f := newCatalogFixture(t, false)
	run, _ := f.completion(t)
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET harness='codex' WHERE id=$1`, f.account); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET unit='requests',allowance=100,pace_model='unrestricted' WHERE account_id=$1`, f.account); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_orders SET max_cost_micros=NULL,status='ready' WHERE node_id=(SELECT work_order_id FROM agent_runs WHERE id=$1)`, run); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='failed',ended_at=clock_timestamp(),vendor_retry_pending=true,vendor_retry_same_account=true,vendor_retry_at=clock_timestamp()-interval '1 second' WHERE id=$1`, run)
		return err
	})
	tracer := &eventLastTracer{appended: map[uint32]bool{}}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	r := boundaryRequest(t.Context(), "/api/runs/queued", nil, f.token, nil)
	r.Method = "GET"
	out := serveBoundary(f.boundaryHandler(t, pool), r)
	if out.Code != 200 {
		t.Fatalf("vendor poll failed %d %s", out.Code, out.Body.String())
	}
	var queued []agentruns.Run
	if err := json.Unmarshal(out.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].RetryOfRunID == nil || *queued[0].RetryOfRunID != run {
		t.Fatal("fixture did not execute the vendor retry writer")
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if len(tracer.violations) != 0 {
		t.Fatalf("vendor poll resource after event: %v", tracer.violations)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='run.capacity_handoff'`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("vendor retry lost its event")
		}
		return nil
	})
}
