// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sharedReadKey struct{}
type sharedListenerTrace struct {
	replay, releaseReplay       chan struct{}
	reconnect, releaseReconnect chan struct{}
	pids                        chan uint32
	reads                       sync.Once
	listens                     atomic.Int32
}

func (tr *sharedListenerTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == "LISTEN aeon_events" {
		tr.pids <- conn.PgConn().PID()
		if tr.listens.Add(1) == 2 && tr.reconnect != nil {
			close(tr.reconnect)
			select {
			case <-tr.releaseReconnect:
			case <-ctx.Done():
			}
		}
	}
	if strings.Contains(data.SQL, "FROM events WHERE tenant_id=$1 AND id>$2") {
		return context.WithValue(ctx, sharedReadKey{}, true)
	}
	return ctx
}
func (tr *sharedListenerTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if read, _ := ctx.Value(sharedReadKey{}).(bool); read && data.Err == nil {
		tr.reads.Do(func() {
			close(tr.replay)
			select {
			case <-tr.releaseReplay:
			case <-ctx.Done():
			}
		})
	}
}

// Risk: subscribe/replay gaps and listener reconnect gaps silently lose commits;
// overflow must rescan from the delivered cursor while preserving tenant RLS.
// The initial replay and reconnect LISTEN are held at query barriers. A fast
// observer establishes that every overflow hint passed the shared fan-out.
func TestSharedSSEReplaySeamReconnectOverflowAndIsolation(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		name := "reconnect"
		if overflow {
			name = "overflow"
		}
		t.Run(name, func(t *testing.T) {
			d, a, b := fixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			tr := &sharedListenerTrace{replay: make(chan struct{}), releaseReplay: make(chan struct{}), reconnect: make(chan struct{}), releaseReconnect: make(chan struct{}), pids: make(chan uint32, 4)}
			var releaseOnce, reconnectOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(tr.releaseReplay) }) }
			reconnect := func() { reconnectOnce.Do(func() { close(tr.releaseReconnect) }) }
			defer release()
			defer reconnect()
			cfg := d.App.Config()
			cfg.ConnConfig.Tracer = tr
			if _, err := db.ConfigurePool(cfg); err != nil {
				t.Fatal(err)
			}
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			defer db.CloseNotificationConnections(pool)
			mux := http.NewServeMux()
			New(pool).Mount(mux)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), a)))
			}))
			defer srv.Close()
			// Helper opens asynchronously because replay is intentionally held.
			type opening struct {
				response *http.Response
				err      error
			}
			opened := make(chan opening, 1)
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/stream?after=latest", nil)
			go func() { res, err := srv.Client().Do(req); opened <- opening{res, err} }()
			select {
			case <-tr.replay:
			case <-ctx.Done():
				t.Fatal("initial replay barrier missing")
			}
			oldPID := <-tr.pids
			observer, err := db.SubscribeNotifications(ctx, pool, "aeon_events", a.TenantID)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close(context.Background())
			count := 1
			if overflow {
				count = 32
			}
			var seam []Event
			for range count {
				seam = append(seam, appendEvents(t, d, a, 1)[0])
				n, err := observer.WaitForNotification(ctx)
				if err != nil || db.IsResync(n) {
					t.Fatalf("fast observer failed: %v %v", n, err)
				}
			}
			appendEvents(t, d, b, 1)
			release()
			var initial opening
			select {
			case initial = <-opened:
			case <-ctx.Done():
				t.Fatal("stream opening hung")
			}
			if initial.err != nil {
				t.Fatal(initial.err)
			}
			defer initial.response.Body.Close()
			if initial.response.StatusCode != 200 {
				t.Fatal(initial.response.StatusCode)
			}
			scanner := bufio.NewScanner(initial.response.Body)
			scanner.Buffer(make([]byte, 4096), 1<<20)
			ready := false
			for scanner.Scan() {
				if scanner.Text() == "event: stream.ready" {
					if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "data: ") || !scanner.Scan() || scanner.Text() != "" {
						t.Fatal("invalid ready frame")
					}
					ready = true
					break
				}
			}
			if !ready {
				t.Fatal("ready frame missing")
			}

			for _, want := range seam {
				got := nextEvent(t, scanner)
				if got.ID != want.ID || got.ActorPrincipalID != a.ID {
					t.Fatalf("seam/overflow delivered %+v want %d for tenant actor %s", got, want.ID, a.ID)
				}
			}
			if overflow {
				return
			}
			var terminated bool
			if err := d.Admin.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, oldPID).Scan(&terminated); err != nil || !terminated {
				t.Fatalf("listener termination: %t %v", terminated, err)
			}
			select {
			case <-tr.reconnect:
			case <-ctx.Done():
				t.Fatal("reconnect LISTEN barrier missing")
			}
			gap := appendEvents(t, d, a, 2)
			appendEvents(t, d, b, 1)
			reconnect()
			newPID := <-tr.pids
			if newPID == oldPID {
				t.Fatal("backend was not replaced")
			}
			for _, want := range gap {
				got := nextEvent(t, scanner)
				if got.ID != want.ID || got.ActorPrincipalID != a.ID {
					t.Fatalf("reconnect rescan delivered %+v want %d", got, want.ID)
				}
			}
		})
	}
}
