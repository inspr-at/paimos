// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"context"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRulesDeadlineBoundsSaturatedPool(t *testing.T) {
	w := newBatchWorld(t, "rules-acquire-bound")
	p := w.principal(tenant.Person, "Owner", "owner")
	cfg := w.d.App.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	held, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			held.Release()
		}
	}()
	saved := txTimeout
	txTimeout = -time.Millisecond
	defer func() { txTimeout = saved }()
	reached := false
	handler := (&Module{pool: pool}).endpoint("rules.read", "", func(_ *http.Request, _ pgx.Tx, _ tenant.Principal) (any, error) { reached = true; return nil, nil })
	req := httptest.NewRequest("GET", "/api/rules/layers", nil).WithContext(tenant.WithPrincipal(context.Background(), p))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler(rec, req) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		held.Release()
		released = true
		<-done
		t.Fatal("expired request waited for pool capacity")
	}
	if rec.Code != 503 || reached {
		t.Fatalf("expired acquisition returned %d, endpoint reached=%t", rec.Code, reached)
	}
}
