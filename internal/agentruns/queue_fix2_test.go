// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Create through the node API; lifecycle tests must not hide work-order children
// or bypass the real tree guard with fixture SQL.
func (f *fixture) apiQueueTicket(t *testing.T, parent *string) string {
	t.Helper()
	var kinds struct{ Items []struct{ ID, Slug string } }
	f.call(t, f.person, "GET", "/api/kinds", nil, 200, &kinds)
	for _, kind := range kinds.Items {
		if kind.Slug != "ticket" {
			continue
		}
		var node struct{ ID string }
		f.call(t, f.person, "POST", "/api/nodes", map[string]any{
			"kind_id": kind.ID, "title": "Work", "state": "open", "parent_id": parent,
			"fields": map[string]any{"estimate_hours": 2, "acceptance_criteria": "tests pass", "priority": "high"},
		}, 201, &node)
		return node.ID
	}
	t.Fatal("ticket kind missing")
	return ""
}

func TestTicketQueueDeleteCommitsWithGeneratedChild(t *testing.T) {
	for _, removeFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue-removed-first=%t", removeFirst), func(t *testing.T) {
			f := setup(t)
			nodes.New(f.d.App, nil).Mount(f.mux)
			f.queueAccount(t, 1000000)
			id := f.apiQueueTicket(t, nil)
			e := f.addQueue(t, id, map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile})
			f.reserve(t, e.Run)
			f.call(t, f.person, "GET", "/api/nodes/"+e.Run.OrderID, nil, 200, nil)
			if removeFirst {
				f.call(t, f.person, "DELETE", "/api/queue/"+id, nil, 200, nil)
				f.call(t, f.person, "GET", "/api/nodes/"+id, nil, 200, nil)
			}
			f.call(t, f.person, "DELETE", "/api/nodes/"+id, nil, 204, nil)
			f.call(t, f.person, "GET", "/api/nodes/"+id, nil, 404, nil)
			f.call(t, f.person, "GET", "/api/nodes/"+e.Run.OrderID, nil, 404, nil)
			if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='cancelled'`, e.Run.ID); n != 1 {
				t.Fatal("deleted ticket retained queued run")
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.removed'`, id); n != 1 {
				t.Fatal("queue removal audit must commit exactly once")
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, e.Run.ID); n != 0 {
				t.Fatal("deleted ticket retained capacity holds")
			}
			if f.queuePage(t).Count != 0 {
				t.Fatal("deleted ticket retained queue projection")
			}
		})
	}
}

func TestTicketQueueDeleteWithOtherLiveChildRollsBack(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	id := f.apiQueueTicket(t, nil)
	child := f.apiQueueTicket(t, &id)
	e := f.addQueue(t, id, nil)
	f.call(t, f.person, "DELETE", "/api/nodes/"+id, nil, 409, nil)
	for _, live := range []string{id, child, e.Run.OrderID} {
		f.call(t, f.person, "GET", "/api/nodes/"+live, nil, 200, nil)
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='queued'`, e.Run.ID); n != 1 {
		t.Fatal("failed deletion cancelled queued work")
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.removed'`, id); n != 0 {
		t.Fatal("failed deletion retained removal audit")
	}
	f.call(t, f.person, "DELETE", "/api/nodes/"+child, nil, 204, nil)
	f.call(t, f.person, "DELETE", "/api/nodes/"+id, nil, 204, nil)
}

// Pause a real PATCH after it has locked the ticket row. The tree lock must
// already be held, and a concurrent queue add must finish after PATCH resumes.
type ticketPatchPause struct {
	first  atomic.Bool
	locked chan uint32
	resume chan struct{}
}
type ticketPatchPauseKey struct{}

func (p *ticketPatchPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, " FROM nodes n WHERE n.id =") && strings.HasSuffix(q.SQL, " FOR UPDATE") && p.first.CompareAndSwap(false, true) {
		return context.WithValue(ctx, ticketPatchPauseKey{}, true)
	}
	return ctx
}

func (p *ticketPatchPause) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(ticketPatchPauseKey{}) == true && q.Err == nil {
		p.locked <- conn.PgConn().PID()
		select {
		case <-p.resume:
		case <-ctx.Done():
		}
	}
}

func TestTicketQueueConcurrentPatchAndAddFinish(t *testing.T) {
	f := setup(t)
	pause := &ticketPatchPause{locked: make(chan uint32, 1), resume: make(chan struct{})}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = pause
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	nodes.New(pool, nil).Mount(f.mux)
	id := f.apiQueueTicket(t, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	defer func() {
		select {
		case <-pause.resume:
		default:
			close(pause.resume)
		}
	}()
	patchDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, httpRequest(ctx, f.person, "PATCH", "/api/nodes/"+id, `{"title":"Patched work"}`))
		patchDone <- w
	}()
	var pid uint32
	select {
	case pid = <-pause.locked:
	case <-ctx.Done():
		t.Fatal("PATCH did not lock ticket")
	}
	var hasTreeLock bool
	if err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted)`, pid).Scan(&hasTreeLock); err != nil {
		t.Fatal(err)
	}
	if !hasTreeLock {
		t.Error("PATCH locked ticket row before tenant tree")
	}
	queueDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, httpRequest(ctx, f.person, "POST", "/api/queue", `{"node_id":"`+id+`"}`))
		queueDone <- w
	}()
	for {
		var waiting bool
		if err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("queue add did not overlap PATCH")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(pause.resume)
	for _, result := range []struct {
		name string
		done <-chan *httptest.ResponseRecorder
	}{{"PATCH", patchDone}, {"queue add", queueDone}} {
		select {
		case w := <-result.done:
			if w.Code != 200 {
				t.Fatalf("%s failed: %d %s", result.name, w.Code, w.Body.String())
			}
		case <-ctx.Done():
			t.Fatalf("%s did not finish", result.name)
		}
	}
	var node struct{ Title string }
	f.call(t, f.person, "GET", "/api/nodes/"+id, nil, 200, &node)
	if node.Title != "Patched work" || f.queuePage(t).Count != 1 {
		t.Fatal("concurrent edits did not both commit")
	}
}

type ticketDeadlockEvent struct{}

func (ticketDeadlockEvent) WriteEvent(_ context.Context, _ pgx.Tx, e nodes.Event) error {
	return fmt.Errorf("write %s: %w", e.Type, &pgconn.PgError{Code: "40P01", Message: "deadlock detected"})
}

func TestTicketQueuePatchDeadlockReturnsRetryableConflict(t *testing.T) {
	f := setup(t)
	id := f.ticket(t, "open", "high", nil)
	nodes.New(f.d.App, ticketDeadlockEvent{}).Mount(f.mux)
	var response struct{ Error, Code string }
	f.call(t, f.person, "PATCH", "/api/nodes/"+id, map[string]string{"title": "Should roll back"}, 409, &response)
	if response.Code != "retryable_conflict" || !strings.Contains(response.Error, "retry") {
		t.Fatalf("deadlock lacks retry advice: %+v", response)
	}
	var node struct{ Title string }
	f.call(t, f.person, "GET", "/api/nodes/"+id, nil, 200, &node)
	if node.Title != "Work" {
		t.Fatal("deadlocked patch committed")
	}
}
