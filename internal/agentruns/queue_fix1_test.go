// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pause the review after its pairing lock. The queue mutation must wait at
// the earlier tenant fence without holding a conflicting tree/pairing lock.
type reviewLockPause struct {
	first  atomic.Bool
	locked chan uint32
	resume chan struct{}
}
type reviewPauseKey struct{}

func (p *reviewLockPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "pg_advisory_xact_lock") && strings.Contains(q.SQL, "aeon-pairing:") && p.first.CompareAndSwap(false, true) {
		return context.WithValue(ctx, reviewPauseKey{}, true)
	}
	return ctx
}
func (p *reviewLockPause) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(reviewPauseKey{}) == true && q.Err == nil {
		p.locked <- conn.PgConn().PID()
		select {
		case <-p.resume:
		case <-ctx.Done():
		}
	}
}

func TestTicketQueueMutationAndReviewCreationDoNotDeadlock(t *testing.T) {
	f := setup(t)
	queueTicket := f.ticket(t, "open", "high", nil)
	reviewTicket := f.ticket(t, "open", "high", nil)
	pause := &reviewLockPause{locked: make(chan uint32, 1), resume: make(chan struct{})}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = pause
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// The real review handler runs read-only model routing inside the test DB;
	// no reviewer process or external model is dispatched.
	crossreview.New(pool, nil).Mount(f.mux)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	defer func() {
		select {
		case <-pause.resume:
		default:
			close(pause.resume)
		}
	}()
	reviewDone := make(chan int, 1)
	go func() {
		body := `{"request_id":"` + uuid() + `","repository":"inspr-at/paimos","base_sha":"` + strings.Repeat("a", 40) + `","head_sha":"` + strings.Repeat("b", 40) + `","author_family":"openai"}`
		r := httpRequest(ctx, f.person, "POST", "/api/nodes/"+reviewTicket+"/reviews", body)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		reviewDone <- w.Code
	}()
	var pid uint32
	select {
	case pid = <-pause.locked:
	case <-ctx.Done():
		t.Fatal("review did not acquire pairing lock")
	}
	queueDone := make(chan int, 1)
	go func() {
		queueDone <- f.request(f.person, "POST", "/api/queue", `{"node_id":"`+queueTicket+`"}`, "").Code
	}()
	for {
		var waiting bool
		err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("queue did not wait for the tenant/pairing fence")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(pause.resume)
	for _, result := range []struct {
		name string
		code <-chan int
		want int
	}{{"review", reviewDone, 201}, {"queue", queueDone, 200}} {
		select {
		case code := <-result.code:
			if code != result.want {
				t.Fatalf("%s failed during concurrent mutation: %d", result.name, code)
			}
		case <-ctx.Done():
			t.Fatalf("%s deadlocked", result.name)
		}
	}
}

func TestTicketQueueTerminalStateCancelsRunAndReservation(t *testing.T) {
	for _, state := range []string{"done", "cancelled", "archived", "delivered", "accepted", "deleted"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			nodes.New(f.d.App, nil).Mount(f.mux)
			f.queueAccount(t, 1000000)
			id := f.ticket(t, "open", "high", map[string]any{"estimate_hours": 2, "acceptance_criteria": "tests pass", "pill_en": "Queue lifecycle", "pill_de": "Warteschlange aufräumen", "benefit_en": "Finished work leaves the queue.", "benefit_de": "Erledigte Arbeit verlässt die Warteschlange."})
			e := f.addQueue(t, id, map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile})
			f.reserve(t, e.Run)
			if state == "deleted" {
				f.call(t, f.person, "DELETE", "/api/nodes/"+id, nil, 204, nil)
			} else {
				f.call(t, f.person, "PATCH", "/api/nodes/"+id, map[string]string{"state": state}, 200, nil)
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='cancelled'`, e.Run.ID); n != 1 {
				t.Fatal("terminal ticket retained its queued run")
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, e.Run.ID); n != 0 {
				t.Fatal("terminal ticket retained capacity holds")
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM account_allowance_windows WHERE reserved<>0`); n != 0 {
				t.Fatal("terminal ticket retained reserved allowance")
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM work_orders WHERE node_id=$1 AND status='cancelled'`, e.Run.OrderID); n != 1 {
				t.Fatal("terminal ticket retained its ready work order")
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.removed'`, id); n != 1 {
				t.Fatal("queue.removed not recorded")
			}
			if state != "deleted" {
				f.call(t, f.person, "PATCH", "/api/nodes/"+id, map[string]string{"state": "open"}, 200, nil)
				if f.queuePage(t).Count != 0 {
					t.Fatal("reopening silently resurrected queued work")
				}
				fresh := f.addQueue(t, id, nil)
				if fresh.Run.ID == e.Run.ID {
					t.Fatal("requeue reused cancelled run")
				}
			}
		})
	}
}

func TestTicketQueueWaitReasonUsesEffectiveAutopilot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace bool
		project   string
		off       bool
	}{{"default", true, "inherit", false}, {"workspace-off", false, "inherit", true}, {"project-off", true, "off", true}, {"project-on", false, "on", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			id := f.ticket(t, "open", "high", nil)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				rules, _ := json.Marshal(statusautopilot.Defaults().Rules)
				if _, err := tx.Exec(t.Context(), `INSERT INTO status_autopilot_settings(tenant_id,enabled,rules) VALUES($1,$2,$3)`, f.person.TenantID, tc.workspace, rules); err != nil {
					return err
				}
				var project string
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Project','open' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO status_autopilot_projects(tenant_id,project_id,mode) VALUES($1,$2,$3)`, f.person.TenantID, project, tc.project)
				return err
			})
			e := f.addQueue(t, id, nil)
			if off := strings.Contains(e.Queued.WaitReason, "Autopilot is off"); off != tc.off {
				t.Fatalf("effective setting mismatch: %s", e.Queued.WaitReason)
			}
		})
	}
}

func TestTicketQueueTwoAgentsClaimOneRunExactlyOnce(t *testing.T) {
	f := setup(t)
	f.queueAccount(t, 1000000)
	id := f.ticket(t, "open", "high", nil)
	e := f.addQueue(t, id, map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile})
	body, _ := json.Marshal(claimBody(f.reserve(t, e.Run)))
	otherKey := f.key(t, f.other, []string{"run.claim"})
	start := make(chan struct{})
	codes := make(chan int, 2)
	for _, p := range []tenant.Principal{f.agent, f.other} {
		go func() {
			<-start
			token := otherKey
			if p.ID == f.agent.ID {
				token = f.token
			}
			codes <- f.request(p, "POST", "/api/runs/"+e.Run.ID+"/claim", string(body), token).Code
		}()
	}
	close(start)
	got := []int{<-codes, <-codes}
	slices.Sort(got)
	if !slices.Equal(got, []int{200, 403}) {
		t.Fatalf("two claimants: %v", got)
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.picked_up'`, id); n != 1 {
		t.Fatalf("pickup events=%d", n)
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1 AND status='starting' AND agent_principal_id=$2`, id, f.agent.ID); n != 1 {
		t.Fatal("run was not owned exactly once")
	}
}

func TestTicketQueueReadsPollAndTelemetryDoNotTakeTreeLock(t *testing.T) {
	f := setup(t)
	run := f.claim(t, f.run(t, f.order(t, nil)))
	ctx := tenant.WithPrincipal(t.Context(), f.person)
	err := db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0))`); err != nil {
			return err
		}
		for _, tc := range []struct {
			p            tenant.Principal
			method, path string
			body         any
		}{
			{f.person, "GET", "/api/queue", nil},
			{f.person, "GET", "/api/runs", nil},
			{f.agent, "GET", "/api/runs/queued", nil},
			{f.agent, "POST", "/api/runs/" + run.ID + "/telemetry", agentruns.Telemetry{Sequence: 1, Kind: "heartbeat"}},
		} {
			requestCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			body, _ := json.Marshal(tc.body)
			r := httpRequest(requestCtx, tc.p, tc.method, tc.path, string(body))
			r.Header.Set(agentruns.DaemonHeader, "daemon-test")
			r.Header.Set(agentruns.GenerationHeader, "generation-1")
			if tc.p.ID == f.agent.ID {
				r.Header.Set("Authorization", "Bearer "+f.token)
			}
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)
			cancel()
			if w.Code != 200 {
				t.Errorf("%s %s waited for tree lock: %d", tc.method, tc.path, w.Code)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type rejectQueueCloseEvent struct{}

func (rejectQueueCloseEvent) WriteEvent(context.Context, pgx.Tx, nodes.Event) error {
	return errors.New("test node event failure")
}

func TestTicketQueueTerminalCancellationRollsBackWithTicket(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, rejectQueueCloseEvent{}).Mount(f.mux)
	f.queueAccount(t, 1000000)
	id := f.ticket(t, "open", "high", nil)
	e := f.addQueue(t, id, map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile})
	f.reserve(t, e.Run)
	f.call(t, f.person, "PATCH", "/api/nodes/"+id, map[string]string{"state": "cancelled"}, 500, nil)
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='queued'`, e.Run.ID); n != 1 {
		t.Fatal("failed patch cancelled work")
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, e.Run.ID); n != 1 {
		t.Fatal("failed patch released capacity")
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.removed'`, id); n != 0 {
		t.Fatal("failed patch retained removal audit")
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM nodes WHERE id=$1 AND state='open'`, id); n != 1 {
		t.Fatal("failed patch changed ticket")
	}
	f.call(t, f.person, "GET", "/api/nodes/"+e.Run.OrderID, nil, 200, nil)
}

func TestTicketQueueBulkArchiveCancelsOnlyQueuedWork(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	f.queueAccount(t, 1000000)
	queued := f.ticket(t, "open", "high", nil)
	active := f.ticket(t, "open", "high", nil)
	q := f.addQueue(t, queued, map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile})
	f.reserve(t, q.Run)
	a := f.addQueue(t, active, map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile})
	f.claim(t, a.Run)
	f.call(t, f.person, "POST", "/api/nodes/bulk", map[string]any{"ids": []string{queued, active}, "state": "archived"}, 200, nil)
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='cancelled'`, q.Run.ID); n != 1 {
		t.Fatal("bulk archive kept queued work")
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='starting'`, a.Run.ID); n != 1 {
		t.Fatal("bulk archive cancelled started work")
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=ANY($1::uuid[]) AND type='queue.removed'`, []string{queued, active}); n != 1 {
		t.Fatal("bulk removal audit was not exactly once")
	}
}

// Use a bounded context for the lock-order test's review request.
func httpRequest(ctx context.Context, p tenant.Principal, method, path, body string) *http.Request {
	r, _ := http.NewRequestWithContext(tenant.WithPrincipal(ctx, p), method, path, strings.NewReader(body))
	return r
}
